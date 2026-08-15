package datasource

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cwbudde/go-overpass"

	"github.com/cwbudde/watercolormap/internal/types"
)

// countingUpstream is a fake Overpass endpoint that records how many requests
// it served and what query text it was asked.
type countingUpstream struct {
	server  *httptest.Server
	queries []string
	calls   atomic.Int32
}

// newUpstream starts a fake Overpass endpoint. respond decides the reply for
// call number n (1-based).
func newUpstream(t *testing.T, respond func(n int, w http.ResponseWriter)) *countingUpstream {
	t.Helper()

	up := &countingUpstream{}
	up.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(up.calls.Add(1))
		if err := r.ParseForm(); err == nil {
			up.queries = append(up.queries, r.FormValue("data"))
		}
		respond(n, w)
	}))
	t.Cleanup(up.server.Close)
	return up
}

func okJSON(body string) func(int, http.ResponseWriter) {
	return func(_ int, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body) //nolint:errcheck // test server
	}
}

// sampleResponse is a minimal but realistic Overpass answer: one way with
// geometry, which ExtractFeaturesFromOverpassResult turns into a real feature.
const sampleResponse = `{"version":0.6,"generator":"Overpass API","elements":[
{"type":"way","id":11,"tags":{"highway":"residential"},"geometry":[
{"lat":52.33,"lon":9.66},{"lat":52.34,"lon":9.67},{"lat":52.35,"lon":9.68}]}]}`

// newCachedSource builds a datasource pointed at up, with an on-disk cache in
// dir. A nil dir means caching is disabled.
func newCachedSource(t *testing.T, endpoint string, cache *ResponseCache) *OverpassDataSource {
	t.Helper()

	cfg := DefaultOverpassConfig()
	cfg.Endpoint = endpoint
	cfg.Workers = 1
	cfg.Cache = cache
	// Retry off by default: the tests that care about retry opt in explicitly,
	// so an unexpected retry cannot mask a wrong request count. It has to be an
	// explicit zero-retry config rather than nil — go-overpass's plain
	// constructor installs DefaultRetryConfig on its own.
	cfg.RetryConfig = &overpass.RetryConfig{MaxRetries: 0}
	cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}

	return NewOverpassDataSourceWithConfig(cfg)
}

func fetch(t *testing.T, ds *OverpassDataSource) (*types.TileData, error) {
	t.Helper()
	tile := types.TileCoordinate{Zoom: 13, X: 4321, Y: 2718}
	return ds.FetchTileDataWithBounds(context.Background(), tile, goldenQueryBounds)
}

// TestCacheTransportServesSecondFetchFromDisk is the headline behaviour: the
// second identical fetch never reaches the network.
func TestCacheTransportServesSecondFetchFromDisk(t *testing.T) {
	up := newUpstream(t, okJSON(sampleResponse))
	cache := testCache(t, nil)
	ds := newCachedSource(t, up.server.URL, cache)

	if _, err := fetch(t, ds); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if _, err := fetch(t, ds); err != nil {
		t.Fatalf("second fetch: %v", err)
	}

	if got := up.calls.Load(); got != 1 {
		t.Errorf("upstream saw %d requests, want 1", got)
	}
	if cache.Entries() != 1 {
		t.Errorf("cache holds %d entries, want 1", cache.Entries())
	}
}

// TestCacheTransportHitIsByteIdentical is the determinism test: what the
// go-overpass decoder receives on a hit must be exactly what it received on the
// miss. It is checked at the byte level, by capturing the raw response the
// client parsed, and then again at the feature level.
func TestCacheTransportHitIsByteIdentical(t *testing.T) {
	up := newUpstream(t, okJSON(sampleResponse))
	cache := testCache(t, nil)
	ds := newCachedSource(t, up.server.URL, cache).WithRawResponseStorage(true)

	miss, err := fetch(t, ds)
	if err != nil {
		t.Fatalf("miss fetch: %v", err)
	}
	hit, err := fetch(t, ds)
	if err != nil {
		t.Fatalf("hit fetch: %v", err)
	}
	if up.calls.Load() != 1 {
		t.Fatalf("expected the second fetch to be a cache hit, upstream saw %d requests", up.calls.Load())
	}

	// The bytes the client decoded, recovered from the cache entry itself.
	stored, ok := cache.Get(up.server.URL, ds.buildTileQuery(goldenQueryBounds, 13))
	if !ok {
		t.Fatal("expected the response to be cached")
	}
	if string(stored) != sampleResponse {
		t.Errorf("cached bytes differ from the upstream body:\n got %q\nwant %q", stored, sampleResponse)
	}

	// And the decode of those bytes matches the decode of the live response.
	if miss.OverpassResult == nil || hit.OverpassResult == nil {
		t.Fatal("expected both fetches to keep their raw result")
	}
	if !sameResult(miss.OverpassResult, hit.OverpassResult) {
		t.Error("a cache hit must decode to the same Overpass result as the miss did")
	}
	if len(miss.Features.Roads) != len(hit.Features.Roads) {
		t.Errorf("roads: miss %d, hit %d", len(miss.Features.Roads), len(hit.Features.Roads))
	}
}

// sameResult compares the parts of an overpass.Result that the renderer reads.
// Map iteration order is irrelevant here, which is the point: the cache
// preserves the input bytes, not an ordering the upstream never guaranteed.
func sameResult(a, b *overpass.Result) bool {
	if a.Count != b.Count || len(a.Ways) != len(b.Ways) || len(a.Nodes) != len(b.Nodes) {
		return false
	}
	for id, way := range a.Ways {
		other, ok := b.Ways[id]
		if !ok || len(way.Geometry) != len(other.Geometry) {
			return false
		}
	}
	return true
}

// TestCacheTransportSkipsZeroElementResponses: a 200 with no elements is what a
// silent Overpass failure looks like, so it must not be persisted.
func TestCacheTransportSkipsZeroElementResponses(t *testing.T) {
	up := newUpstream(t, okJSON(`{"version":0.6,"elements":[]}`))
	cache := testCache(t, nil)
	// z13 with no features is an error from validateFeatureResponse; allow it
	// so the test exercises the caching decision, not the validation.
	ds := newCachedSource(t, up.server.URL, cache).WithEmptyResponsesAllowed(true)

	for i := range 2 {
		if _, err := fetch(t, ds); err != nil {
			t.Fatalf("fetch %d: %v", i, err)
		}
	}

	if got := up.calls.Load(); got != 2 {
		t.Errorf("upstream saw %d requests, want 2 — an empty response must not be cached", got)
	}
	if cache.Entries() != 0 {
		t.Errorf("cache holds %d entries, want 0", cache.Entries())
	}
}

// TestCacheTransportDoesNotCacheFailures covers the shapes that must stay
// visible to the retry logic.
func TestCacheTransportDoesNotCacheFailures(t *testing.T) {
	tests := []struct {
		respond func(int, http.ResponseWriter)
		name    string
	}{
		{name: "rate limited", respond: func(_ int, w http.ResponseWriter) {
			http.Error(w, "429 Too Many Requests", http.StatusTooManyRequests)
		}},
		{name: "gateway timeout", respond: func(_ int, w http.ResponseWriter) {
			http.Error(w, "504 Gateway Timeout", http.StatusGatewayTimeout)
		}},
		{name: "html error page", respond: func(_ int, w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusBadGateway)
			io.WriteString(w, "<html><body>error</body></html>") //nolint:errcheck // test server
		}},
		{name: "malformed json with a 200", respond: okJSON(`{"elements":[{"type":`)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			up := newUpstream(t, tc.respond)
			cache := testCache(t, nil)
			ds := newCachedSource(t, up.server.URL, cache)

			if _, err := fetch(t, ds); err == nil {
				t.Fatal("expected the failure to surface")
			}
			if cache.Entries() != 0 {
				t.Errorf("cache holds %d entries, want 0", cache.Entries())
			}

			// The second attempt must reach upstream again, otherwise retry
			// would be answering itself from a cached failure.
			if _, err := fetch(t, ds); err == nil {
				t.Fatal("expected the failure to surface again")
			}
			if got := up.calls.Load(); got != 2 {
				t.Errorf("upstream saw %d requests, want 2", got)
			}
		})
	}
}

// TestCacheTransportKeepsRetryWorking checks that a transient failure followed
// by success still retries — and caches only the success.
func TestCacheTransportKeepsRetryWorking(t *testing.T) {
	up := newUpstream(t, func(n int, w http.ResponseWriter) {
		if n == 1 {
			http.Error(w, "429 Too Many Requests", http.StatusTooManyRequests)
			return
		}
		okJSON(sampleResponse)(n, w)
	})

	cache := testCache(t, nil)
	cfg := DefaultOverpassConfig()
	cfg.Endpoint = up.server.URL
	cfg.Workers = 1
	cfg.Cache = cache
	cfg.RetryConfig = &overpass.RetryConfig{
		MaxRetries:        3,
		InitialBackoff:    time.Millisecond,
		MaxBackoff:        10 * time.Millisecond,
		BackoffMultiplier: 2,
	}
	ds := NewOverpassDataSourceWithConfig(cfg)

	if _, err := fetch(t, ds); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got := up.calls.Load(); got != 2 {
		t.Fatalf("upstream saw %d requests, want 2 (one rejected, one retried)", got)
	}
	if cache.Entries() != 1 {
		t.Errorf("cache holds %d entries, want 1 — only the success is cacheable", cache.Entries())
	}

	// And the retried success is now served from disk.
	if _, err := fetch(t, ds); err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if got := up.calls.Load(); got != 2 {
		t.Errorf("upstream saw %d requests after the cached fetch, want 2", got)
	}
}

// TestCacheTransportRespectsResponseLimit: the cache sits outside the size cap,
// so an oversized body must still fail — and must leave nothing behind.
func TestCacheTransportRespectsResponseLimit(t *testing.T) {
	big := bigElementsJSON(256 << 10)
	up := newUpstream(t, func(_ int, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(big) //nolint:errcheck // test server
	})

	cache := testCache(t, nil)
	cfg := DefaultOverpassConfig()
	cfg.Endpoint = up.server.URL
	cfg.Workers = 1
	cfg.Cache = cache
	cfg.RetryConfig = &overpass.RetryConfig{MaxRetries: 0}
	cfg.MaxResponseBytes = 4 << 10

	ds := NewOverpassDataSourceWithConfig(cfg)

	_, err := fetch(t, ds)
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err = %v, want ErrResponseTooLarge", err)
	}
	if cache.Entries() != 0 {
		t.Errorf("cache holds %d entries, want 0", cache.Entries())
	}
	if leftovers := strayFiles(t, cache.Dir()); len(leftovers) != 0 {
		t.Errorf("oversized response left files behind: %v", leftovers)
	}
}

// TestCacheDisabledTouchesNothing: with caching off, no directory appears and
// every fetch goes upstream.
func TestCacheDisabledTouchesNothing(t *testing.T) {
	up := newUpstream(t, okJSON(sampleResponse))
	ds := newCachedSource(t, up.server.URL, nil)

	dir := filepath.Join(t.TempDir(), "cache")
	t.Chdir(filepath.Dir(dir))

	for i := range 3 {
		if _, err := fetch(t, ds); err != nil {
			t.Fatalf("fetch %d: %v", i, err)
		}
	}

	if got := up.calls.Load(); got != 3 {
		t.Errorf("upstream saw %d requests, want 3", got)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("a disabled cache must not create %s", dir)
	}
}

// strayFiles lists every regular file under dir, including the temporary files
// an interrupted write would leave.
func strayFiles(t *testing.T, dir string) []string {
	t.Helper()

	var found []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil //nolint:nilerr // best effort listing
		}
		found = append(found, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return found
}

// TestCacheableRequestIgnoresNonOverpassRequests keeps the transport from
// caching anything it does not understand.
func TestCacheableRequestIgnoresNonOverpassRequests(t *testing.T) {
	tests := []struct {
		build func() *http.Request
		name  string
		want  bool
	}{
		{name: "overpass post", build: func() *http.Request {
			req, _ := http.NewRequest(http.MethodPost, testEndpoint,
				strings.NewReader("data=%5Bout%3Ajson%5D%3B"))
			return req
		}, want: true},
		{name: "get", build: func() *http.Request {
			req, _ := http.NewRequest(http.MethodGet, testEndpoint, nil)
			return req
		}, want: false},
		{name: "post without a data field", build: func() *http.Request {
			req, _ := http.NewRequest(http.MethodPost, testEndpoint, strings.NewReader("other=1"))
			return req
		}, want: false},
		{name: "post without a replayable body", build: func() *http.Request {
			// A body of an unrecognised type leaves GetBody nil, so the
			// transport cannot read it without destroying it.
			req, _ := http.NewRequest(http.MethodPost, testEndpoint,
				io.NopCloser(strings.NewReader("data=x")))
			return req
		}, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, ok := cacheableRequest(tc.build())
			if ok != tc.want {
				t.Errorf("cacheableRequest ok = %v, want %v", ok, tc.want)
			}
		})
	}
}

// TestCachedFetchDoesNotConsumeTheRequestBody guards the GetBody replay: if the
// key derivation drained the body, the upstream would receive an empty query.
func TestCachedFetchDoesNotConsumeTheRequestBody(t *testing.T) {
	up := newUpstream(t, okJSON(sampleResponse))
	cache := testCache(t, nil)
	ds := newCachedSource(t, up.server.URL, cache)

	if _, err := fetch(t, ds); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(up.queries) != 1 {
		t.Fatalf("upstream recorded %d queries, want 1", len(up.queries))
	}
	if want := ds.buildTileQuery(goldenQueryBounds, 13); up.queries[0] != want {
		t.Errorf("upstream received a different query than the one built:\n got %q\nwant %q", up.queries[0], want)
	}
}
