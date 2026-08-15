package datasource

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cwbudde/watercolormap/internal/types"
)

// testCache builds a cache under t.TempDir with generous limits.
func testCache(t *testing.T, mutate func(*CacheConfig)) *ResponseCache {
	t.Helper()

	cfg := CacheConfig{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Dir:      t.TempDir(),
		TTL:      DefaultCacheTTL,
		MaxBytes: DefaultCacheMaxBytes,
	}
	if mutate != nil {
		mutate(&cfg)
	}

	cache, err := NewResponseCache(cfg)
	if err != nil {
		t.Fatalf("NewResponseCache: %v", err)
	}
	return cache
}

// queryFor builds the real query text for a tile, so the key tests exercise
// the same input the transport will see.
func queryFor(bounds types.BoundingBox, zoom int) string {
	ds := &OverpassDataSource{}
	return ds.buildTileQuery(bounds, zoom)
}

const testEndpoint = "https://overpass.example/api/interpreter"

// TestCacheKeyIsStable pins one key so a change to the key derivation cannot
// slip through unnoticed: it would silently invalidate every cache on disk.
func TestCacheKeyIsStable(t *testing.T) {
	got := CacheKey(testEndpoint, queryFor(goldenQueryBounds, 13))
	const want = "5865a1ef25837352964fc406cf43b5c67b36d555c1535fe6473669fd598ea239"
	if got != want {
		t.Errorf("CacheKey = %q, want %q (regenerate deliberately: it invalidates every cached entry)", got, want)
	}
}

// TestCacheKeyIgnoresTileIdentity is the determinism assertion: the key must
// depend on what actually determines the response — endpoint plus query text —
// and never on which tile happened to ask. Two different tiles resolving to the
// same bounds are the same question and must share one entry.
func TestCacheKeyIgnoresTileIdentity(t *testing.T) {
	ds := &OverpassDataSource{}

	// Two unrelated tiles that are handed the same bounds — which is exactly
	// what FetchTileDataWithBounds does, and what a 256px tile and its 512px
	// @2x twin do: RequiredPaddingPx computes padding in world pixels, so the
	// @2x metatile expands the tile bounds by the same fraction and asks the
	// same question.
	left := types.TileCoordinate{Zoom: 13, X: 4321, Y: 2718}
	right := types.TileCoordinate{Zoom: 13, X: 7, Y: 9}

	a := CacheKey(testEndpoint, ds.buildTileQuery(goldenQueryBounds, left.Zoom))
	b := CacheKey(testEndpoint, ds.buildTileQuery(goldenQueryBounds, right.Zoom))

	if a != b {
		t.Fatalf("identical bounds must produce one key, whatever tile asked for them: %s != %s", a, b)
	}
}

func TestCacheKeyDistinguishesInputs(t *testing.T) {
	shifted := goldenQueryBounds
	shifted.MinLat += 0.01

	base := CacheKey(testEndpoint, queryFor(goldenQueryBounds, 13))

	tests := []struct {
		name     string
		endpoint string
		query    string
	}{
		{"different endpoint", "https://other.example/api/interpreter", queryFor(goldenQueryBounds, 13)},
		{"different bounds", testEndpoint, queryFor(shifted, 13)},
		{"different zoom", testEndpoint, queryFor(goldenQueryBounds, 14)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CacheKey(tc.endpoint, tc.query); got == base {
				t.Errorf("%s must not collide with the base key", tc.name)
			}
		})
	}
}

func TestCacheRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{"typical", []byte(`{"elements":[{"type":"way","id":1}]}`)},
		{"empty body", []byte{}},
		{"five megabytes", bigElementsJSON(5 << 20)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// A zero-length body is not valid JSON and is deliberately not
			// storable, so that case asserts a miss rather than a round-trip.
			cache := testCache(t, func(cfg *CacheConfig) { cfg.StoreEmpty = len(tc.body) == 0 })
			query := queryFor(goldenQueryBounds, 13)

			if _, hit := cache.Get(testEndpoint, query); hit {
				t.Fatal("expected a miss on an empty cache")
			}

			cache.Put(testEndpoint, query, tc.body)

			got, hit := cache.Get(testEndpoint, query)
			if !hit {
				t.Fatal("expected a hit after Put")
			}
			if !bytes.Equal(got, tc.body) {
				t.Errorf("round-trip changed the body: got %d bytes, want %d", len(got), len(tc.body))
			}
		})
	}
}

// TestCacheEntryIsSelfDescribing checks the gzip Comment header carries the
// originating endpoint and query, so an entry can be identified on disk
// without a sidecar file.
func TestCacheEntryIsSelfDescribing(t *testing.T) {
	cache := testCache(t, nil)
	query := queryFor(goldenQueryBounds, 13)
	cache.Put(testEndpoint, query, []byte(`{"elements":[{"type":"node","id":7}]}`))

	f, err := os.Open(cache.entryPath(testEndpoint, CacheKey(testEndpoint, query)))
	if err != nil {
		t.Fatalf("open entry: %v", err)
	}
	defer f.Close() //nolint:errcheck // test cleanup

	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	if !strings.HasPrefix(gz.Comment, testEndpoint+"\n") {
		t.Errorf("gzip comment = %q, want it to start with the endpoint", gz.Comment)
	}
	if !strings.Contains(gz.Comment, "[out:json]") {
		t.Error("gzip comment should carry the originating query")
	}
	// Go's gzip reader rejects header strings over 512 bytes, so a full ~2 KB
	// query is stored as a prefix.
	if len(gz.Comment) > maxGzipCommentBytes {
		t.Errorf("gzip comment is %d bytes, over the %d the reader accepts", len(gz.Comment), maxGzipCommentBytes)
	}
}

func TestCacheTTLExpiry(t *testing.T) {
	cache := testCache(t, func(cfg *CacheConfig) { cfg.TTL = time.Hour })
	query := queryFor(goldenQueryBounds, 13)
	body := []byte(`{"elements":[{"type":"way","id":1}]}`)
	cache.Put(testEndpoint, query, body)

	path := cache.entryPath(testEndpoint, CacheKey(testEndpoint, query))
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	if _, hit := cache.Get(testEndpoint, query); hit {
		t.Error("an entry older than the TTL must be a miss")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("an expired entry must be unlinked, not left to be re-checked forever")
	}
}

// TestCacheCorruptEntriesFallThrough is the safety property: a corrupt cache
// must degrade to a network fetch, never to a failed render.
func TestCacheCorruptEntriesFallThrough(t *testing.T) {
	tests := []struct {
		corrupt func(t *testing.T, path string)
		name    string
	}{
		{name: "truncated gzip", corrupt: func(t *testing.T, path string) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			// Drop the trailing CRC32 and length; the payload still
			// decompresses, so only the checksum catches this.
			if err := os.WriteFile(path, data[:len(data)-6], 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
		}},
		{name: "garbage", corrupt: func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("this is not gzip at all"), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
		}},
		{name: "empty file", corrupt: func(t *testing.T, path string) {
			if err := os.WriteFile(path, nil, 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
		}},
		{name: "directory in place of a file", corrupt: func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatalf("remove: %v", err)
			}
			if err := os.MkdirAll(filepath.Join(path, "surprise"), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cache := testCache(t, nil)
			query := queryFor(goldenQueryBounds, 13)
			cache.Put(testEndpoint, query, []byte(`{"elements":[{"type":"way","id":1}]}`))

			path := cache.entryPath(testEndpoint, CacheKey(testEndpoint, query))
			tc.corrupt(t, path)

			if _, hit := cache.Get(testEndpoint, query); hit {
				t.Error("a corrupt entry must read as a miss")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Error("a corrupt entry must be unlinked")
			}
		})
	}
}

func TestCacheSkipsZeroElementResponses(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		storeEmpty bool
		wantHit    bool
	}{
		{"zero elements", `{"elements":[]}`, false, false},
		{"zero elements, store-empty", `{"elements":[]}`, true, true},
		{"missing elements key", `{"version":0.6}`, false, false},
		{"one element", `{"elements":[{"type":"node","id":1}]}`, false, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cache := testCache(t, func(cfg *CacheConfig) { cfg.StoreEmpty = tc.storeEmpty })
			query := queryFor(goldenQueryBounds, 13)
			cache.Put(testEndpoint, query, []byte(tc.body))

			if _, hit := cache.Get(testEndpoint, query); hit != tc.wantHit {
				t.Errorf("hit = %v, want %v", hit, tc.wantHit)
			}
		})
	}
}

// TestCacheEvictionKeepsNewest checks the budget is enforced and that eviction
// is least-recently-modified first.
func TestCacheEvictionKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	body := bigElementsJSON(64 << 10)

	// Fill well past a small budget, stamping each entry with a distinct mtime.
	seed, err := NewResponseCache(CacheConfig{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Dir:    dir,
		TTL:    DefaultCacheTTL,
		// No budget yet: this pass only populates.
		MaxBytes: 0,
	})
	if err != nil {
		t.Fatalf("NewResponseCache: %v", err)
	}

	const entries = 20
	newestQuery := ""
	base := time.Now().Add(-time.Duration(entries) * time.Hour)
	for i := range entries {
		query := fmt.Sprintf("%s\n// entry %d", queryFor(goldenQueryBounds, 13), i)
		seed.Put(testEndpoint, query, body)
		stamp := base.Add(time.Duration(i) * time.Hour)
		path := seed.entryPath(testEndpoint, CacheKey(testEndpoint, query))
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
		newestQuery = query
	}

	total := seed.Bytes()
	if total == 0 {
		t.Fatal("expected the seeded entries to occupy disk")
	}
	budget := total / 2

	// Opening with a budget sweeps once, synchronously.
	cache, err := NewResponseCache(CacheConfig{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Dir:      dir,
		TTL:      DefaultCacheTTL,
		MaxBytes: budget,
	})
	if err != nil {
		t.Fatalf("NewResponseCache: %v", err)
	}

	if got := cache.Bytes(); got > budget {
		t.Errorf("after the sweep %d bytes remain, over the %d byte budget", got, budget)
	}
	if cache.Entries() == 0 {
		t.Fatal("eviction must not empty the cache when the budget allows entries")
	}
	if _, hit := cache.Get(testEndpoint, newestQuery); !hit {
		t.Error("the most recently modified entry must survive eviction")
	}
}

func TestCacheClear(t *testing.T) {
	cache := testCache(t, nil)
	query := queryFor(goldenQueryBounds, 13)
	cache.Put(testEndpoint, query, []byte(`{"elements":[{"type":"way","id":1}]}`))

	if cache.Entries() != 1 {
		t.Fatalf("Entries = %d, want 1", cache.Entries())
	}
	if err := cache.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if cache.Entries() != 0 {
		t.Errorf("Entries after Clear = %d, want 0", cache.Entries())
	}
	if _, err := os.Stat(cache.Dir()); err != nil {
		t.Errorf("Clear must leave the cache directory in place: %v", err)
	}
}

// TestCacheConcurrentAccess runs readers and writers together; the point is the
// -race detector and the absence of torn reads, which the atomic rename is
// there to guarantee.
func TestCacheConcurrentAccess(t *testing.T) {
	cache := testCache(t, nil)
	body := []byte(`{"elements":[{"type":"way","id":42,"nodes":[1,2,3]}]}`)

	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Deliberate overlap: eight distinct keys across 32 goroutines, so
			// several writers race on the same entry.
			query := fmt.Sprintf("%s\n// %d", queryFor(goldenQueryBounds, 13), i%8)
			for range 20 {
				cache.Put(testEndpoint, query, body)
				if got, hit := cache.Get(testEndpoint, query); hit && !bytes.Equal(got, body) {
					t.Errorf("torn read: %d bytes, want %d", len(got), len(body))
					return
				}
			}
		}(i)
	}
	wg.Wait()
}

func TestNilCacheIsInert(t *testing.T) {
	var cache *ResponseCache

	if _, hit := cache.Get(testEndpoint, "q"); hit {
		t.Error("a nil cache must always miss")
	}
	cache.Put(testEndpoint, "q", []byte(`{"elements":[{}]}`))
	if cache.Entries() != 0 || cache.Bytes() != 0 {
		t.Error("a nil cache must report nothing stored")
	}
	if err := cache.Clear(); err != nil {
		t.Errorf("Clear on a nil cache: %v", err)
	}
}

func TestParseByteSize(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    int64
		wantErr bool
	}{
		{name: "gigabytes", in: "5GB", want: 5 << 30},
		{name: "megabytes", in: "512MB", want: 512 << 20},
		{name: "bare bytes", in: "1024", want: 1024},
		{name: "empty means unset", in: "", want: 0},
		{name: "whitespace means unset", in: "   ", want: 0},
		{name: "lowercase", in: "2gb", want: 2 << 30},
		{name: "explicit binary unit", in: "1GiB", want: 1 << 30},
		{name: "fractional", in: "1.5GB", want: 1610612736},
		{name: "nonsense", in: "nonsense", wantErr: true},
		{name: "negative", in: "-1GB", wantErr: true},
		{name: "unit only", in: "GB", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseByteSize(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseByteSize(%q) = %d, want an error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseByteSize(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ParseByteSize(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// bigElementsJSON builds a valid, non-empty Overpass-shaped response of roughly
// n bytes.
func bigElementsJSON(n int) []byte {
	var buf bytes.Buffer
	buf.WriteString(`{"elements":[`)
	for i := 0; buf.Len() < n; i++ {
		if i > 0 {
			buf.WriteByte(',')
		}
		fmt.Fprintf(&buf, `{"type":"node","id":%d,"lat":52.3,"lon":9.7}`, i)
	}
	buf.WriteString(`]}`)
	return buf.Bytes()
}
