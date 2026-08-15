package server

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/cwbudde/watercolormap/internal/pipeline"
	"github.com/cwbudde/watercolormap/internal/tilestamp"
)

// The tile server had no benchmarks at all, so every claim about the hit path
// -- that it is cheap, that the per-tile lock coalesces a burst, that a
// freshness policy costs a SQLite lookup per request -- was unmeasured. These
// drive the real handler and the real middleware chain with the render stubbed
// out through the tileGenerator seam, so they measure server work and nothing
// else: no Mapnik, no Overpass, no wall clock spent on a render that would
// swamp everything being measured. (The package still *links* Mapnik through
// pipeline; only rendering is out of the loop.)
//
// Run them with `just load-test`.

// benchServer builds a stubbed server with one tile already on disk, which is
// the shape every hit-path benchmark wants.
func benchServer(b *testing.B, cfg OnDemandTilesConfig) *OnDemandTiles {
	b.Helper()

	if cfg.TilesDir == "" {
		cfg.TilesDir = b.TempDir()
	}
	if cfg.MaxConcurrentGenerations == 0 {
		cfg.MaxConcurrentGenerations = 8
	}
	cfg.GenerateMissing = true

	od, err := NewOnDemandTiles(nil, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		b.Fatalf("NewOnDemandTiles: %v", err)
	}
	b.Cleanup(od.Stop)

	gen := &stubGenerator{tilesDir: od.cfg.TilesDir}
	od.newGenerator = func(int) (tileGenerator, error) { return gen, nil }

	return od
}

// BenchmarkTileHitHandler is the handler alone: parse, cache check, file serve.
func BenchmarkTileHitHandler(b *testing.B) {
	od := benchServer(b, OnDemandTilesConfig{CacheControl: "no-store"})
	writeFixtureTile(b, od.cfg.TilesDir, "z13_x4317_y2692.png", stubTileBytes)

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest(http.MethodGet, "/tiles/z13_x4317_y2692.png", nil)
		for pb.Next() {
			rec := httptest.NewRecorder()
			od.serveTile(rec, req)
			if rec.Code != http.StatusOK {
				b.Errorf("status = %d, want 200", rec.Code)
			}
		}
	})
}

// BenchmarkTileHitServer adds the socket and the middleware chain the serve
// command wraps around the handler, so the rate limiter's per-request cost is
// visible next to the handler's.
func BenchmarkTileHitServer(b *testing.B) {
	for _, tc := range []struct {
		name      string
		rateLimit bool
	}{
		{"plain", false},
		{"ratelimited", true},
	} {
		b.Run(tc.name, func(b *testing.B) {
			od := benchServer(b, OnDemandTilesConfig{CacheControl: "no-store"})
			writeFixtureTile(b, od.cfg.TilesDir, "z13_x4317_y2692.png", stubTileBytes)

			handler := od.Handler()
			if tc.rateLimit {
				// Generous enough that the limiter never sheds: what is being
				// measured is its bookkeeping, not its rejection path.
				rl := NewIPRateLimiter(RateLimitConfig{
					Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
					RPS:        1e9,
					Burst:      1 << 30,
					TTL:        time.Minute,
					MaxEntries: 1024,
				})
				b.Cleanup(rl.Close)
				handler = rl.Middleware(handler)
			}

			srv := httptest.NewServer(handler)
			b.Cleanup(srv.Close)
			client := srv.Client()
			url := srv.URL + "/tiles/z13_x4317_y2692.png"

			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					resp, err := client.Get(url)
					if err != nil {
						b.Errorf("GET: %v", err)
						return
					}
					_, _ = io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					if resp.StatusCode != http.StatusOK {
						b.Errorf("status = %d, want 200", resp.StatusCode)
					}
				}
			})
		})
	}
}

// BenchmarkTileHitWithFreshnessPolicy is the baseline for anything that claims
// to make the stamped hit path cheaper: with a policy configured, every hit
// costs a stamp-store lookup that the unstamped path does not pay.
func BenchmarkTileHitWithFreshnessPolicy(b *testing.B) {
	tilesDir := b.TempDir()
	store, err := tilestamp.OpenFolder(tilesDir)
	if err != nil {
		b.Fatalf("OpenFolder: %v", err)
	}
	b.Cleanup(func() { _ = store.Close() })

	if err := store.Put(tilestamp.Stamp{
		Z: 13, X: 4317, Y: 2692, Format: "png",
		RenderedAt:  time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		RendererRev: "v9.9.9+bench",
	}); err != nil {
		b.Fatalf("Put: %v", err)
	}

	od := benchServer(b, OnDemandTilesConfig{
		TilesDir:     tilesDir,
		CacheControl: "no-store",
		StampStore:   store,
		RendererRev:  "v9.9.9+bench",
		Freshness:    pipeline.FreshnessPolicy{RenderedBefore: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	})
	writeFixtureTile(b, tilesDir, "z13_x4317_y2692.png", stubTileBytes)

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest(http.MethodGet, "/tiles/z13_x4317_y2692.png", nil)
		for pb.Next() {
			rec := httptest.NewRecorder()
			od.serveTile(rec, req)
			if rec.Code != http.StatusOK {
				b.Errorf("status = %d, want 200", rec.Code)
			}
		}
	})
}

// benchTileName walks a distinct z13 tile per iteration, wrapping inside the
// zoom's coordinate range: a plain 4317+i runs off the edge of z13 after a few
// thousand iterations and the handler rejects it with a 400.
func benchTileName(i int) string {
	return fmt.Sprintf("z13_x%d_y%d.png", 4000+i%2048, 2000+(i/2048)%2048)
}

// BenchmarkTileMissDedup measures the miss path under contention for a single
// tile and reports renders per request. The per-tile lock is what keeps that
// ratio near 1/N; a regression there shows up here as a number climbing
// towards 1.
func BenchmarkTileMissDedup(b *testing.B) {
	const (
		callers     = 16
		stubRenderT = 2 * time.Millisecond
	)

	od := benchServer(b, OnDemandTilesConfig{
		CacheControl:          "no-store",
		MaxPendingGenerations: callers * 2,
	})
	gen := &stubGenerator{tilesDir: od.cfg.TilesDir, delay: stubRenderT}
	od.newGenerator = func(int) (tileGenerator, error) { return gen, nil }

	var requests int64

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		// A fresh tile per iteration, or every iteration after the first would
		// be a plain cache hit and measure nothing.
		name := "/tiles/" + benchTileName(i)

		var wg sync.WaitGroup
		wg.Add(callers)
		for range callers {
			go func() {
				defer wg.Done()
				rec := httptest.NewRecorder()
				od.serveTile(rec, httptest.NewRequest(http.MethodGet, name, nil))
			}()
		}
		wg.Wait()
		requests += callers
	}
	b.StopTimer()

	if requests > 0 {
		b.ReportMetric(float64(gen.renders.Load())/float64(requests), "renders/req")
	}
}

// BenchmarkTileMissDistinct walks distinct tiles, which is the crawler shape:
// admission, a lock-map entry per tile, a render slot, and the eviction that
// keeps the lock map from growing with every tile ever requested.
func BenchmarkTileMissDistinct(b *testing.B) {
	od := benchServer(b, OnDemandTilesConfig{
		CacheControl:          "no-store",
		MaxPendingGenerations: 64,
	})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		rec := httptest.NewRecorder()
		od.serveTile(rec, httptest.NewRequest(http.MethodGet, "/tiles/"+benchTileName(i), nil))
		if rec.Code != http.StatusOK {
			b.Fatalf("status = %d, want 200", rec.Code)
		}
	}
	b.StopTimer()

	if got := od.lockCount(); got != 0 {
		b.Errorf("lockCount = %d, want 0 — the lock map must not grow with tiles requested", got)
	}
}

// BenchmarkMBTilesHit is the other backend's hit path: a SQLite read instead of
// a file open.
func BenchmarkMBTilesHit(b *testing.B) {
	dbPath, _ := newTestMBTiles(b, 13, 4317, 2692)

	h, err := NewMBTilesHandler(MBTilesConfig{MBTilesPath: dbPath}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		b.Fatalf("NewMBTilesHandler: %v", err)
	}
	b.Cleanup(func() { _ = h.Close() })

	handler := h.Handler()

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest(http.MethodGet, "/tiles/z13_x4317_y2692.png", nil)
		for pb.Next() {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				b.Errorf("status = %d, want 200", rec.Code)
			}
		}
	})
}

// The benchmarks above do not run in CI. This covers the same paths under -race
// so a data race in the cache accounting or the lock map is caught by `just
// test` rather than by whoever next runs a benchmark by hand.
func TestServeTileUnderConcurrentLoad(t *testing.T) {
	const (
		workers  = 8
		perTile  = 4
		numTiles = 12
	)

	od, gen := newStubServer(t, OnDemandTilesConfig{
		GenerateMissing:       true,
		MaxPendingGenerations: workers * numTiles,
	}, time.Millisecond)

	// Half the tiles exist already, so hits and misses run concurrently rather
	// than in two clean phases.
	for i := range numTiles / 2 {
		writeFixtureTile(t, od.cfg.TilesDir, fmt.Sprintf("z13_x%d_y2692.png", 4317+i), "cached")
	}

	var wg sync.WaitGroup
	for i := range numTiles {
		for range perTile {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rec := httptest.NewRecorder()
				od.serveTile(rec, httptest.NewRequest(http.MethodGet,
					fmt.Sprintf("/tiles/z13_x%d_y2692.png", 4317+i), nil))
				if rec.Code != http.StatusOK {
					t.Errorf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
				}
			}()
		}
	}
	wg.Wait()

	cache := od.Status().Cache
	total := cache.Hits + cache.HitsCoalesced + cache.Misses + cache.Bypasses
	if total != numTiles*perTile {
		t.Errorf("cache outcomes = %d, want %d — every request lands in exactly one bucket", total, numTiles*perTile)
	}
	// Only the tiles that were absent are rendered, and each of those once.
	if got := gen.renders.Load(); got != numTiles/2 {
		t.Errorf("renders = %d, want %d", got, numTiles/2)
	}
	if got := od.lockCount(); got != 0 {
		t.Errorf("lockCount = %d, want 0 once every request finished", got)
	}
}
