package datasource

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cwbudde/watercolormap/internal/safe"
)

// Cache defaults. They are the values the `cache:` config block documents, kept
// here so a caller constructing a CacheConfig in Go gets the same behaviour as
// one configuring it in YAML.
const (
	// DefaultCacheDir matches the `cache/` directory that is already gitignored
	// and bind-mounted into the Docker image (see the Justfile).
	DefaultCacheDir = "cache/overpass"
	// DefaultCacheTTL is one week. OSM data for a tile changes slowly; a week
	// is short enough that a re-render eventually picks edits up and long
	// enough to cover a multi-day batch run.
	DefaultCacheTTL = 7 * 24 * time.Hour
	// DefaultCacheMaxBytes is the on-disk budget, enforced by evicting the
	// oldest-written entries first. Not LRU: Get deliberately leaves mtime
	// alone, because mtime is also what the TTL reads and refreshing it on
	// access would make a hot entry immortal.
	DefaultCacheMaxBytes int64 = 5 << 30 // 5 GB
)

// cacheKeyPrefix versions the *storage format*, not the query. Bumping it
// invalidates every existing entry, which is what you want when the on-disk
// encoding changes; it must not be bumped for query-builder changes, because
// those already change the query text that goes into the key.
const cacheKeyPrefix = "wcm-overpass-cache/1\n"

// maxCacheEntryBytes caps how much a single entry may decompress to. It mirrors
// DefaultMaxResponseBytes: an entry larger than the response cap could never
// have been produced by a live fetch, so it is corrupt or hostile either way.
const maxCacheEntryBytes = DefaultMaxResponseBytes

// sweepMinInterval and sweepWriteFraction bound how often eviction runs: at
// most once per interval, and only after enough new bytes have accumulated to
// make a sweep worth its directory walk.
const (
	sweepMinInterval   = 5 * time.Minute
	sweepWriteFraction = 10 // sweep after budget/10 bytes written
)

// CacheConfig configures a ResponseCache.
//
// Fields are ordered for struct alignment, not for reading order.
type CacheConfig struct {
	// Logger receives the debug lines about hits, misses and discarded
	// entries. Nil falls back to slog.Default().
	Logger *slog.Logger
	// Dir is the cache root. Every entry lives underneath it.
	Dir string
	// TTL is how long an entry stays usable, measured from its file mtime.
	// Zero or less means entries never expire by age.
	TTL time.Duration
	// MaxBytes is the on-disk budget. Zero or less disables eviction.
	MaxBytes int64
	// StoreEmpty allows caching responses with zero elements. Off by default:
	// a 200 with no data is how a silent Overpass failure looks, and freezing
	// that shape for a week would bake the failure into every later run.
	StoreEmpty bool
}

// ResponseCache is an on-disk store of verbatim Overpass response bodies.
//
// Entries are the raw upstream bytes, gzipped, at
// <dir>/<endpointHash8>/<key[0:2]>/<key>.json.gz. Storing the response verbatim
// rather than a parsed structure is deliberate: bytes in equal bytes out, so a
// hit and a miss feed the go-overpass decoder exactly the same input and there
// is no second serialization format to keep in sync with the first.
//
// # What the cache does and does not guarantee
//
// It guarantees byte-identical Overpass *input*. It does not guarantee
// byte-identical PNGs, because feature order is already nondeterministic
// upstream of the cache: ExtractFeaturesFromOverpassResult ranges over the
// map[int64]*Way / map[int64]*Relation of an overpass.Result without sorting,
// so two decodes of the same bytes can emit features in different orders. The
// cache neither causes nor worsens that — caching the raw JSON is precisely the
// choice that leaves the existing behaviour alone, whereas caching an extracted
// types.FeatureCollection would have frozen one arbitrary order into every
// future render. Fixing the ordering is a separate change; it will move the
// pipeline goldens.
//
// All read-path failures are non-fatal. A cache that cannot be read is a cache
// miss, never a failed render.
type ResponseCache struct {
	log      *slog.Logger
	dir      string
	ttl      time.Duration
	maxBytes int64

	// sinceSweep counts bytes written since the last eviction sweep;
	// lastSweep holds its Unix-nano timestamp. sweeping guards against
	// starting a second background sweep while one is running.
	sinceSweep atomic.Int64
	lastSweep  atomic.Int64
	sweeping   atomic.Bool

	storeEmpty bool
}

// NewResponseCache prepares the cache directory and runs one eviction sweep.
//
// The sweep at open is synchronous because it happens once, at startup, before
// any tile is rendered. Every later sweep runs in the background: eviction must
// never sit on a request path.
func NewResponseCache(cfg CacheConfig) (*ResponseCache, error) {
	if cfg.Dir == "" {
		cfg.Dir = DefaultCacheDir
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("create overpass cache directory %q: %w", cfg.Dir, err)
	}

	c := &ResponseCache{
		log:        cfg.Logger,
		dir:        cfg.Dir,
		ttl:        cfg.TTL,
		maxBytes:   cfg.MaxBytes,
		storeEmpty: cfg.StoreEmpty,
	}
	c.lastSweep.Store(time.Now().UnixNano())
	c.sweep()

	return c, nil
}

// Dir returns the cache root.
func (c *ResponseCache) Dir() string { return c.dir }

// TTL returns the configured entry lifetime.
func (c *ResponseCache) TTL() time.Duration { return c.ttl }

// MaxBytes returns the configured on-disk budget.
func (c *ResponseCache) MaxBytes() int64 { return c.maxBytes }

// CacheKey derives the storage key for one Overpass request.
//
// The key covers the endpoint and the query text and nothing else. In
// particular it contains no tile identity: bounds and zoom reach it only
// through the query that buildTileQuery emits, which is what actually
// determines the response. Two different tiles that ask the same question get
// the same answer, which is the same rule the rest of the renderer follows —
// output depends on world position, not on tile identity.
func CacheKey(endpoint, query string) string {
	sum := sha256.Sum256([]byte(cacheKeyPrefix + endpoint + "\n" + query))
	return hex.EncodeToString(sum[:])
}

// entryPath returns the file backing one key, sharded by endpoint and by the
// first key byte so no single directory collects every entry.
func (c *ResponseCache) entryPath(endpoint, key string) string {
	epSum := sha256.Sum256([]byte(endpoint))
	return filepath.Join(c.dir, hex.EncodeToString(epSum[:])[:8], key[:2], key+".json.gz")
}

// Get returns the cached body for a query, if a live entry exists.
//
// Any failure — missing file, unreadable file, bad gzip header, CRC mismatch,
// short read, oversized payload, expired mtime — is reported as a miss, logged
// at debug level, and the offending entry is unlinked so the next run does not
// pay for it again.
func (c *ResponseCache) Get(endpoint, query string) ([]byte, bool) {
	if c == nil {
		return nil, false
	}

	key := CacheKey(endpoint, query)
	path := c.entryPath(endpoint, key)

	info, err := os.Stat(path)
	switch {
	case err != nil:
		if !errors.Is(err, fs.ErrNotExist) {
			c.discard(path, "stat failed", err)
		}
		return nil, false
	case info.IsDir():
		c.discard(path, "entry is a directory", nil)
		return nil, false
	case c.expired(info.ModTime()):
		c.discard(path, "entry expired", nil)
		return nil, false
	}

	body, err := readGzipEntry(path)
	if err != nil {
		c.discard(path, "unreadable entry", err)
		return nil, false
	}

	c.log.Debug("overpass cache hit", "key", key, "bytes", len(body))
	return body, true
}

// readGzipEntry decompresses one entry, enforcing the size cap.
//
// gzip verifies the trailing CRC32 and length on the final read, so a
// truncated or corrupted file surfaces here as an error rather than as
// plausible-looking partial JSON.
func readGzipEntry(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // read-only

	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("gzip header: %w", err)
	}
	defer gz.Close() //nolint:errcheck // read-only

	body, err := io.ReadAll(io.LimitReader(gz, maxCacheEntryBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read entry: %w", err)
	}
	if int64(len(body)) > maxCacheEntryBytes {
		return nil, fmt.Errorf("%w: entry over %d bytes", ErrResponseTooLarge, maxCacheEntryBytes)
	}
	return body, nil
}

// Put stores a response body. Failures are logged and otherwise ignored: a
// cache that cannot be written is a slow renderer, not a broken one.
func (c *ResponseCache) Put(endpoint, query string, body []byte) {
	if c == nil || !c.storable(body) {
		return
	}

	key := CacheKey(endpoint, query)
	path := c.entryPath(endpoint, key)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		c.log.Debug("overpass cache write skipped", "key", key, "err", err)
		return
	}

	written, err := writeGzipEntryAtomic(path, endpoint+"\n"+query, body)
	if err != nil {
		c.log.Debug("overpass cache write failed", "key", key, "err", err)
		return
	}

	c.log.Debug("overpass cache stored", "key", key, "bytes", len(body), "on_disk", written)
	c.noteWrite(written)
}

// storable reports whether a body is worth caching.
//
// The governing rule is that the cache must never turn a transient failure into
// a persistent one. Overpass signals failure in three shapes, and only the first
// is caught before this point:
//
//   - a non-200 status or a non-JSON body — filtered by the transport;
//   - a 200 carrying a "remark", which is how Overpass reports a query timeout
//     or a runtime error while still returning whatever it managed to collect;
//   - a 200 with an empty "elements" array, which is what a silently failing
//     instance looks like (the same case validateFeatureResponse guards, which
//     cannot be reused here because it needs the zoom level).
//
// The remark case is the dangerous one, because go-overpass does not decode
// "remark" at all: a timed-out query is indistinguishable from a successful one
// with fewer features, so without this check a partial result would be replayed
// as authoritative for the whole TTL and every tile in that area would render
// with features missing.
//
// storeEmpty relaxes only the empty-elements rule. It is not an escape hatch
// from structural validation: a body must still parse and still carry an
// "elements" array, so a JSON error object can never be cached.
func (c *ResponseCache) storable(body []byte) bool {
	if c.maxBytes > 0 && int64(len(body)) > c.maxBytes {
		return false
	}

	var probe struct {
		Elements *[]json.RawMessage `json:"elements"`
		Remark   string             `json:"remark"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	if probe.Remark != "" {
		c.log.Debug("overpass cache skipped a response carrying a remark", "remark", probe.Remark)
		return false
	}
	if probe.Elements == nil {
		c.log.Debug("overpass cache skipped a response without an elements array")
		return false
	}
	if len(*probe.Elements) == 0 && !c.storeEmpty {
		c.log.Debug("overpass cache skipped a zero-element response")
		return false
	}
	return true
}

// writeGzipEntryAtomic writes body to a temporary file in the destination
// directory and renames it into place, so a reader never observes a partial
// entry and two concurrent writers cannot interleave. Same pattern as the
// pipeline's atomic PNG encode; no lock is needed, across goroutines or across
// processes.
//
// comment is stored in the gzip Comment header, which makes an entry
// self-describing (endpoint and query) for debugging without a sidecar file.
func writeGzipEntryAtomic(path, comment string, body []byte) (int64, error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return 0, err
	}
	tmpName := tmp.Name()

	// Best effort cleanup of the failure paths; on success the file is already
	// closed and renamed away, so both calls are no-ops.
	defer func() {
		tmp.Close()        //nolint:errcheck // best-effort cleanup
		os.Remove(tmpName) //nolint:errcheck // best-effort cleanup
	}()

	gz, err := gzip.NewWriterLevel(tmp, gzip.BestSpeed)
	if err != nil {
		return 0, err
	}
	// The gzip Comment header must be valid Latin-1 without NUL bytes; the
	// query is ASCII in practice, but a sanitized comment beats a failed write.
	gz.Comment = sanitizeGzipComment(comment)
	if _, err := gz.Write(body); err != nil {
		return 0, err
	}
	if err := gz.Close(); err != nil {
		return 0, err
	}
	if err := tmp.Chmod(0o644); err != nil {
		return 0, err
	}
	// Sync before the rename: without it a crash can publish a name that
	// points at unwritten data, which is exactly the corrupt entry the atomic
	// write is meant to prevent.
	if err := tmp.Sync(); err != nil {
		return 0, err
	}

	size, err := tmp.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return 0, err
	}
	return size, nil
}

// maxGzipCommentBytes is the largest header string Go's gzip reader accepts:
// it decodes NUL-terminated header strings into a fixed 512-byte buffer and
// answers ErrHeader for anything longer. A full tile query is roughly 2 KB, so
// the comment is a prefix — enough to identify the endpoint and the shape of
// the query, which is all it is for.
const maxGzipCommentBytes = 480

// sanitizeGzipComment strips the bytes the gzip header cannot carry and keeps
// it short enough to be read back.
func sanitizeGzipComment(s string) string {
	// Restricting to printable ASCII (plus newline) does double duty: the gzip
	// header can only carry Latin-1, and an all-ASCII string can be truncated
	// on any byte without leaving an invalid rune behind.
	clean := strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if r < 0x20 || r > 0x7E {
			return -1
		}
		return r
	}, s)

	if len(clean) > maxGzipCommentBytes {
		const marker = "...(truncated)"
		clean = clean[:maxGzipCommentBytes-len(marker)] + marker
	}
	return clean
}

// discard removes an unusable entry and records why at debug level.
func (c *ResponseCache) discard(path, reason string, err error) {
	c.log.Debug("discarding overpass cache entry", "path", path, "reason", reason, "err", err)
	if rmErr := os.RemoveAll(path); rmErr != nil {
		c.log.Debug("could not remove overpass cache entry", "path", path, "err", rmErr)
	}
}

func (c *ResponseCache) expired(mod time.Time) bool {
	return c.ttl > 0 && time.Since(mod) > c.ttl
}

// noteWrite triggers a background sweep once enough has been written since the
// last one, and never more often than sweepMinInterval.
func (c *ResponseCache) noteWrite(n int64) {
	if c.maxBytes <= 0 && c.ttl <= 0 {
		return
	}

	written := c.sinceSweep.Add(n)
	threshold := c.maxBytes / sweepWriteFraction
	if threshold <= 0 || written < threshold {
		return
	}
	last := time.Unix(0, c.lastSweep.Load())
	if time.Since(last) < sweepMinInterval {
		return
	}
	if !c.sweeping.CompareAndSwap(false, true) {
		return
	}

	c.lastSweep.Store(time.Now().UnixNano())
	c.sinceSweep.Store(0)

	// Off the request path, and panic-guarded: a background sweep must never
	// take the tile server down with it.
	safe.Go(c.log, "overpass cache sweep", func() {
		defer c.sweeping.Store(false)
		c.sweep()
	})
}

// cacheEntry is one file considered by a sweep.
type cacheEntry struct {
	mod  time.Time
	path string
	size int64
}

// scan lists every entry currently on disk, dropping expired ones as it goes.
func (c *ResponseCache) scan() ([]cacheEntry, int64) {
	var (
		entries []cacheEntry
		total   int64
	)

	err := filepath.WalkDir(c.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".json.gz") {
			return nil //nolint:nilerr // an unreadable subtree is a cache miss, not a failure
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if c.expired(info.ModTime()) {
			c.discard(path, "entry expired", nil)
			return nil
		}
		entries = append(entries, cacheEntry{path: path, size: info.Size(), mod: info.ModTime()})
		total += info.Size()
		return nil
	})
	if err != nil {
		c.log.Debug("overpass cache scan failed", "dir", c.dir, "err", err)
	}

	return entries, total
}

// sweep enforces the TTL and the size budget, evicting least-recently-modified
// entries first.
func (c *ResponseCache) sweep() {
	entries, total := c.scan()
	if c.maxBytes <= 0 || total <= c.maxBytes {
		return
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].mod.Before(entries[j].mod) })

	evicted := 0
	for _, e := range entries {
		if total <= c.maxBytes {
			break
		}
		if err := os.Remove(e.path); err != nil {
			c.log.Debug("overpass cache eviction failed", "path", e.path, "err", err)
			continue
		}
		total -= e.size
		evicted++
	}

	if evicted > 0 {
		c.log.Info("evicted overpass cache entries",
			"evicted", evicted, "bytes_on_disk", total, "budget", c.maxBytes)
	}
}

// Entries returns the number of live entries on disk.
func (c *ResponseCache) Entries() int {
	if c == nil {
		return 0
	}
	entries, _ := c.scan()
	return len(entries)
}

// Bytes returns the on-disk size of all live entries.
func (c *ResponseCache) Bytes() int64 {
	if c == nil {
		return 0
	}
	_, total := c.scan()
	return total
}

// Clear removes every entry, leaving the cache directory itself in place.
func (c *ResponseCache) Clear() error {
	if c == nil {
		return nil
	}

	names, err := os.ReadDir(c.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read overpass cache directory %q: %w", c.dir, err)
	}
	for _, n := range names {
		if err := os.RemoveAll(filepath.Join(c.dir, n.Name())); err != nil {
			return fmt.Errorf("clear overpass cache entry %q: %w", n.Name(), err)
		}
	}
	c.sinceSweep.Store(0)
	return nil
}

// ParseByteSize parses a human-written size such as "5GB", "512MB" or "1024".
//
// Suffixes are binary multiples (KB == 1024 bytes), matching how the numbers
// are read in practice when comparing against `du` output. An empty string
// means "unset" and returns 0, which callers turn into their own default.
func ParseByteSize(s string) (int64, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return 0, nil
	}

	upper := strings.ToUpper(trimmed)
	multiplier := int64(1)
	for _, unit := range []struct {
		suffixes []string
		factor   int64
	}{
		{[]string{"TIB", "TB", "T"}, 1 << 40},
		{[]string{"GIB", "GB", "G"}, 1 << 30},
		{[]string{"MIB", "MB", "M"}, 1 << 20},
		{[]string{"KIB", "KB", "K"}, 1 << 10},
		{[]string{"B"}, 1},
	} {
		matched := false
		for _, suffix := range unit.suffixes {
			if strings.HasSuffix(upper, suffix) {
				upper = strings.TrimSuffix(upper, suffix)
				multiplier = unit.factor
				matched = true
				break
			}
		}
		if matched {
			break
		}
	}

	digits := strings.TrimSpace(upper)
	value, err := strconv.ParseFloat(digits, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: expected a number optionally followed by KB/MB/GB/TB", s)
	}
	// ParseFloat accepts "NaN" and "Inf", and neither is caught by a negative
	// check — NaN compares false against everything. Both, and any value that
	// overflows the int64 range, convert to an implementation-defined result
	// that is in practice the *negative* int64 minimum, which ResponseCache
	// reads as "eviction disabled". A malformed budget would therefore become a
	// silently unbounded disk cache, which is the opposite of what configuring
	// a limit asks for, so every one of these is rejected at startup instead.
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("invalid size %q: must be a finite number", s)
	}
	if value < 0 {
		return 0, fmt.Errorf("invalid size %q: must not be negative", s)
	}
	scaled := value * float64(multiplier)
	if scaled > math.MaxInt64 {
		return 0, fmt.Errorf("invalid size %q: exceeds the maximum of %d bytes", s, int64(math.MaxInt64))
	}

	return int64(scaled), nil
}
