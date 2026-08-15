package server

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/cwbudde/watercolormap/internal/pipeline"
	"github.com/cwbudde/watercolormap/internal/tilestamp"
)

// get drives one tile request through the handler.
func get(t *testing.T, od *OnDemandTiles, path string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	od.serveTile(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func wantCache(t *testing.T, od *OnDemandTiles, want CacheStatus) {
	t.Helper()

	if got := od.Status().Cache; got != want {
		t.Fatalf("cache status = %+v, want %+v", got, want)
	}
}

// Rate limiting cannot tell a hit from a miss and the render counters do not
// either: without these, an operator has no way to know whether the tile
// directory is doing any work at all.
func TestCacheAccountingHit(t *testing.T) {
	od, gen := newStubServer(t, OnDemandTilesConfig{GenerateMissing: true}, 0)
	writeFixtureTile(t, od.cfg.TilesDir, "z1_x0_y0.png", "cached")

	rec := get(t, od, "/tiles/z1_x0_y0.png")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(cacheStatusHeader); got != cacheStatusHit {
		t.Errorf("%s = %q, want %q", cacheStatusHeader, got, cacheStatusHit)
	}
	if got := gen.renders.Load(); got != 0 {
		t.Errorf("renders = %d, want 0 — a cache hit must not render", got)
	}
	wantCache(t, od, CacheStatus{Hits: 1})
}

func TestCacheAccountingMiss(t *testing.T) {
	od, gen := newStubServer(t, OnDemandTilesConfig{GenerateMissing: true}, 0)

	rec := get(t, od, "/tiles/z1_x0_y0.png")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(cacheStatusHeader); got != cacheStatusMiss {
		t.Errorf("%s = %q, want %q", cacheStatusHeader, got, cacheStatusMiss)
	}
	if got := gen.renders.Load(); got != 1 {
		t.Errorf("renders = %d, want 1", got)
	}
	wantCache(t, od, CacheStatus{Misses: 1})

	// The tile is on disk now, so the next request is a hit.
	if got := get(t, od, "/tiles/z1_x0_y0.png").Header().Get(cacheStatusHeader); got != cacheStatusHit {
		t.Errorf("second request %s = %q, want %q", cacheStatusHeader, got, cacheStatusHit)
	}
	wantCache(t, od, CacheStatus{Hits: 1, Misses: 1})
}

// With the cache disabled every request renders, and calling that a miss would
// read as a cache that never works rather than one that was switched off.
func TestCacheAccountingBypass(t *testing.T) {
	od, gen := newStubServer(t, OnDemandTilesConfig{GenerateMissing: true, DisableCache: true}, 0)
	writeFixtureTile(t, od.cfg.TilesDir, "z1_x0_y0.png", "cached")

	rec := get(t, od, "/tiles/z1_x0_y0.png")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(cacheStatusHeader); got != cacheStatusBypass {
		t.Errorf("%s = %q, want %q", cacheStatusHeader, got, cacheStatusBypass)
	}
	if got := gen.renders.Load(); got != 1 {
		t.Errorf("renders = %d, want 1 — the tile on disk must be ignored", got)
	}
	wantCache(t, od, CacheStatus{Bypasses: 1})
}

// Stale is the counter that says a --stale-* cutoff is re-rendering everything.
// It has to be reported once per request, not once per cache check: serveTile
// consults the cache twice.
func TestCacheAccountingStaleCountsOncePerRequest(t *testing.T) {
	tilesDir := t.TempDir()
	store, err := tilestamp.OpenFolder(tilesDir)
	if err != nil {
		t.Fatalf("OpenFolder: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	od, gen := newStubServer(t, OnDemandTilesConfig{
		TilesDir:        tilesDir,
		GenerateMissing: true,
		StampStore:      store,
		RendererRev:     "v9.9.9+test",
		Freshness:       pipeline.FreshnessPolicy{RenderedBefore: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	}, 0)

	writeFixtureTile(t, tilesDir, "z1_x0_y0.png", "ancient")
	if err := store.Put(tilestamp.Stamp{
		Z: 1, X: 0, Y: 0, Format: "png",
		RenderedAt:  time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		RendererRev: "v0.0.1+ancient",
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	rec := get(t, od, "/tiles/z1_x0_y0.png")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if got := gen.renders.Load(); got != 1 {
		t.Errorf("renders = %d, want 1 — the stale tile must be re-rendered", got)
	}
	wantCache(t, od, CacheStatus{Misses: 1, Stale: 1})
}

// A request shed by admission control never reached a cache decision, so it
// must land in no bucket at all — counting it as a miss would blame the cache
// for backpressure.
func TestCacheAccountingIgnoresShedRequests(t *testing.T) {
	od := newAdmissionTiles(t, 1)
	od.inFlightGenerations.Store(5)

	rec := get(t, od, "/tiles/z1_x0_y0.png")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if got := rec.Header().Get(cacheStatusHeader); got != "" {
		t.Errorf("%s = %q, want it unset on a shed request", cacheStatusHeader, got)
	}
	wantCache(t, od, CacheStatus{})
}

// Concurrent requests for one uncached tile: exactly one renders, the rest wait
// on the per-tile lock and are served the file that render produced. Those are
// coalesced hits, and telling them apart from ordinary hits is what shows the
// dedup working.
func TestCacheAccountingCoalescedHits(t *testing.T) {
	const callers = 8

	od, gen := newStubServer(t, OnDemandTilesConfig{
		GenerateMissing:       true,
		MaxPendingGenerations: callers,
	}, 50*time.Millisecond)

	var wg sync.WaitGroup
	statuses := make([]string, callers)
	codes := make([]int, callers)

	wg.Add(callers)
	for i := range callers {
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			od.serveTile(rec, httptest.NewRequest(http.MethodGet, "/tiles/z1_x0_y0.png", nil))
			statuses[i] = rec.Header().Get(cacheStatusHeader)
			codes[i] = rec.Code
		}()
	}
	wg.Wait()

	for i, code := range codes {
		if code != http.StatusOK {
			t.Fatalf("caller %d status = %d, want 200", i, code)
		}
	}
	if got := gen.renders.Load(); got != 1 {
		t.Fatalf("renders = %d, want 1 — the per-tile lock must coalesce the rest", got)
	}

	cache := od.Status().Cache
	if cache.Misses != 1 {
		t.Errorf("misses = %d, want 1", cache.Misses)
	}
	if cache.Hits+cache.HitsCoalesced != callers-1 {
		t.Errorf("hits+coalesced = %d, want %d", cache.Hits+cache.HitsCoalesced, callers-1)
	}
	// Every request lands in exactly one bucket.
	total := cache.Hits + cache.HitsCoalesced + cache.Misses + cache.Bypasses
	if total != callers {
		t.Errorf("total cache outcomes = %d, want %d", total, callers)
	}

	var counted int
	for _, s := range statuses {
		if s == cacheStatusHit || s == cacheStatusHitCoalesced || s == cacheStatusMiss {
			counted++
		}
	}
	if counted != callers {
		t.Errorf("%d responses carried a %s header, want %d", counted, cacheStatusHeader, callers)
	}
}

// The MBTiles backend has no generator, so every tile it answers came out of a
// finished tileset. Reporting it keeps a mixed deployment readable: a client
// cannot otherwise tell which backend served a tile.
func TestMBTilesHandlerReportsHit(t *testing.T) {
	const z, x, y = 13, 4317, 2692

	dbPath, _ := newTestMBTiles(t, z, x, y)

	h, err := NewMBTilesHandler(MBTilesConfig{MBTilesPath: dbPath}, nil)
	if err != nil {
		t.Fatalf("NewMBTilesHandler: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })

	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/tiles/z13_x4317_y2692.png", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(cacheStatusHeader); got != cacheStatusHit {
		t.Errorf("%s = %q, want %q", cacheStatusHeader, got, cacheStatusHit)
	}
}
