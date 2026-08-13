package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Per-tile locks used to be stored in a sync.Map that was never pruned, so any
// client walking the tile grid leaked one mutex per distinct tile forever.
func TestLockTileEvictsWhenUnused(t *testing.T) {
	od := &OnDemandTiles{}

	t.Run("released locks are dropped", func(t *testing.T) {
		for i := range 100 {
			unlock := od.lockTile(string(rune('a'+i%26)) + string(rune(i)))
			unlock()
		}
		if got := od.lockCount(); got != 0 {
			t.Fatalf("lockCount = %d, want 0 after all locks released", got)
		}
	})

	t.Run("held locks are retained", func(t *testing.T) {
		unlock := od.lockTile("z1_x0_y0.png")
		if got := od.lockCount(); got != 1 {
			t.Fatalf("lockCount = %d, want 1 while held", got)
		}
		unlock()
		if got := od.lockCount(); got != 0 {
			t.Fatalf("lockCount = %d, want 0 after release", got)
		}
	})

	// The entry must survive while a waiter is queued behind the holder,
	// otherwise two requests for the same tile could each get their own
	// mutex and render concurrently into the same file.
	t.Run("waiters keep the entry alive and are serialized", func(t *testing.T) {
		const waiters = 8
		var (
			mu      sync.Mutex
			inside  int
			maxSeen int
			wg      sync.WaitGroup
		)

		wg.Add(waiters)
		for range waiters {
			go func() {
				defer wg.Done()
				unlock := od.lockTile("contended.png")
				defer unlock()

				mu.Lock()
				inside++
				if inside > maxSeen {
					maxSeen = inside
				}
				mu.Unlock()

				mu.Lock()
				inside--
				mu.Unlock()
			}()
		}
		wg.Wait()

		if maxSeen != 1 {
			t.Fatalf("max concurrent holders = %d, want 1", maxSeen)
		}
		if got := od.lockCount(); got != 0 {
			t.Fatalf("lockCount = %d, want 0 after contention resolved", got)
		}
	})
}

// Error responses must not carry internal detail, and must not be cacheable —
// a cached failure would pin a tile to "broken" in browsers and proxies.
func TestWriteTileErrorIsGenericAndUncacheable(t *testing.T) {
	rec := httptest.NewRecorder()
	writeTileError(rec, "upstream data fetch failed", http.StatusBadGateway)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want %q", got, "no-store")
	}
}

// The "tile not found" path used to interpolate the on-disk filename into the
// response body.
func TestServeTileNotFoundDoesNotLeakFilename(t *testing.T) {
	od := &OnDemandTiles{
		cfg: OnDemandTilesConfig{
			TilesDir:        t.TempDir(),
			GenerateMissing: false,
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/tiles/z13_x4317_y2692.png", nil)
	od.serveTile(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if body := rec.Body.String(); strings.Contains(body, "z13_x4317_y2692") {
		t.Fatalf("response body leaks the tile filename: %q", body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want %q", got, "no-store")
	}
}

func TestRetryDelayBacksOffByZoom(t *testing.T) {
	tests := []struct {
		name    string
		zoom    uint32
		attempt int
		wantSec float64
	}{
		{"low zoom first attempt", 5, 0, 30},
		{"low zoom second attempt", 5, 1, 60},
		{"mid zoom first attempt", 9, 0, 15},
		{"high zoom first attempt", 14, 0, 5},
		{"high zoom third attempt", 14, 2, 20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := retryDelay(tt.zoom, tt.attempt).Seconds(); got != tt.wantSec {
				t.Fatalf("retryDelay(%d, %d) = %vs, want %vs", tt.zoom, tt.attempt, got, tt.wantSec)
			}
		})
	}
}
