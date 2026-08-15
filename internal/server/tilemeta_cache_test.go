package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cwbudde/watercolormap/internal/pipeline"
	"github.com/cwbudde/watercolormap/internal/tilestamp"
)

// countingStampStore records how often the freshness policy reached SQLite.
type countingStampStore struct {
	pipeline.StampStore
	gets atomic.Int64
}

func (s *countingStampStore) Get(z, x, y int, suffix, format string) (tilestamp.Stamp, bool, error) {
	s.gets.Add(1)
	return s.StampStore.Get(z, x, y, suffix, format)
}

// The point of the cache: with a --stale-* policy configured, every single
// cache hit used to cost a SQLite lookup on top of two stats, on a path whose
// whole purpose is to be cheaper than rendering.
func TestTileMetaCacheSkipsTheStampLookup(t *testing.T) {
	tilesDir := t.TempDir()
	inner, err := tilestamp.OpenFolder(tilesDir)
	if err != nil {
		t.Fatalf("OpenFolder: %v", err)
	}
	t.Cleanup(func() { _ = inner.Close() })

	store := &countingStampStore{StampStore: inner}
	if err := inner.Put(tilestamp.Stamp{
		Z: 1, X: 0, Y: 0, Format: "png",
		RenderedAt:  time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		RendererRev: "v9.9.9+test",
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	od, _ := newStubServer(t, OnDemandTilesConfig{
		TilesDir:        tilesDir,
		GenerateMissing: true,
		StampStore:      store,
		RendererRev:     "v9.9.9+test",
		Freshness:       pipeline.FreshnessPolicy{RenderedBefore: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	}, 0)
	writeFixtureTile(t, tilesDir, "z1_x0_y0.png", "cached")

	for range 5 {
		if rec := get(t, od, "/tiles/z1_x0_y0.png"); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	}

	if got := store.gets.Load(); got != 1 {
		t.Errorf("stamp lookups = %d, want 1 for five requests", got)
	}
	if got := od.Status().Cache.MetaCacheHits; got != 4 {
		t.Errorf("meta_cache_hits = %d, want 4", got)
	}
}

// The TTL is what bounds the staleness the cache introduces. Once it lapses the
// stamp store is authoritative again.
func TestTileMetaCacheEntriesExpire(t *testing.T) {
	tilesDir := t.TempDir()
	inner, err := tilestamp.OpenFolder(tilesDir)
	if err != nil {
		t.Fatalf("OpenFolder: %v", err)
	}
	t.Cleanup(func() { _ = inner.Close() })

	store := &countingStampStore{StampStore: inner}
	if err := inner.Put(tilestamp.Stamp{
		Z: 1, X: 0, Y: 0, Format: "png",
		RenderedAt:  time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		RendererRev: "v9.9.9+test",
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	od, _ := newStubServer(t, OnDemandTilesConfig{
		TilesDir:         tilesDir,
		GenerateMissing:  true,
		StampStore:       store,
		RendererRev:      "v9.9.9+test",
		Freshness:        pipeline.FreshnessPolicy{RenderedBefore: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		TileMetaCacheTTL: 20 * time.Millisecond,
	}, 0)
	writeFixtureTile(t, tilesDir, "z1_x0_y0.png", "cached")

	get(t, od, "/tiles/z1_x0_y0.png")
	get(t, od, "/tiles/z1_x0_y0.png")
	if got := store.gets.Load(); got != 1 {
		t.Fatalf("stamp lookups = %d, want 1 before the TTL lapses", got)
	}

	time.Sleep(40 * time.Millisecond)

	get(t, od, "/tiles/z1_x0_y0.png")
	if got := store.gets.Load(); got != 2 {
		t.Errorf("stamp lookups = %d, want 2 once the entry expired", got)
	}
}

// An entry whose file has gone -- `purge` runs out of process and deletes tiles
// this server knows nothing about -- must never produce a 200. Serving a
// deleted tile from a memory of it is the one failure this cache could cause
// that a user would call data loss.
func TestTileMetaCacheDoesNotServeADeletedTile(t *testing.T) {
	od, _ := newStubServer(t, OnDemandTilesConfig{GenerateMissing: false}, 0)
	path := writeFixtureTile(t, od.cfg.TilesDir, "z1_x0_y0.png", "cached")

	if rec := get(t, od, "/tiles/z1_x0_y0.png"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	rec := get(t, od, "/tiles/z1_x0_y0.png")
	if rec.Code == http.StatusOK {
		t.Fatalf("a deleted tile was served from the metadata cache (body %q)", rec.Body.String())
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	// The bad entry must be gone, not merely stepped over once.
	if _, ok := od.metaCache.Get(path); ok {
		t.Error("the entry for a missing file survived the failed open")
	}
}

// A render replaces the file the cached validator described, so the entry has
// to go with it or the client would keep revalidating against the old tile.
func TestRenderInvalidatesTheTileMetaEntry(t *testing.T) {
	od, _ := newStubServer(t, OnDemandTilesConfig{GenerateMissing: true}, 0)
	path := filepath.Join(od.cfg.TilesDir, "z1_x0_y0.png")

	first := get(t, od, "/tiles/z1_x0_y0.png")
	if first.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", first.Code)
	}
	if got := first.Header().Get(cacheStatusHeader); got != cacheStatusMiss {
		t.Fatalf("%s = %q, want a render", cacheStatusHeader, got)
	}
	if _, ok := od.metaCache.Get(path); ok {
		t.Fatal("a render left a metadata entry behind")
	}

	// Move the file on by a whole second -- some filesystems only keep mtime to
	// that resolution -- so a stale entry would show up as an unchanged
	// validator on the hit that follows.
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	second := get(t, od, "/tiles/z1_x0_y0.png")
	if got := second.Header().Get(cacheStatusHeader); got != cacheStatusHit {
		t.Fatalf("%s = %q, want a hit", cacheStatusHeader, got)
	}
	if got, want := second.Header().Get("ETag"), first.Header().Get("ETag"); got == want {
		t.Errorf("ETag = %q unchanged, so the entry the render should have dropped was reused", got)
	}
}

// Wiring check: the disabled cache has to be a working cache that stores
// nothing, so no call site needs a nil guard.
func TestTileMetaCacheCanBeDisabled(t *testing.T) {
	od, _ := newStubServer(t, OnDemandTilesConfig{
		GenerateMissing:      true,
		TileMetaCacheEntries: -1,
	}, 0)
	writeFixtureTile(t, od.cfg.TilesDir, "z1_x0_y0.png", "cached")

	for range 3 {
		if rec := get(t, od, "/tiles/z1_x0_y0.png"); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	}

	if got := od.Status().Cache; got.MetaCacheHits != 0 || got.MetaCacheMisses != 0 {
		t.Errorf("meta cache statistics = %+v, want all zero when disabled", got)
	}
}

// The lock map used to leak an entry per tile ever requested; the metadata
// cache is the same shape of risk one layer up.
func TestTileMetaCacheIsBounded(t *testing.T) {
	const entries = 8

	od, _ := newStubServer(t, OnDemandTilesConfig{
		GenerateMissing:       true,
		TileMetaCacheEntries:  entries,
		MaxPendingGenerations: 64,
	}, 0)

	for i := range 200 {
		rec := httptest.NewRecorder()
		od.serveTile(rec, httptest.NewRequest(http.MethodGet,
			"/tiles/"+benchTileName(i), nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	}

	if got := od.metaCache.Len(); got > entries {
		t.Errorf("metadata cache holds %d entries, want at most %d", got, entries)
	}
}
