package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func newAdmissionTiles(t *testing.T, maxPending int) *OnDemandTiles {
	t.Helper()
	return &OnDemandTiles{
		cfg: OnDemandTilesConfig{
			TilesDir:              t.TempDir(),
			GenerateMissing:       true,
			MaxPendingGenerations: maxPending,
		},
	}
}

func TestAdmitBoundsTheBacklog(t *testing.T) {
	od := newAdmissionTiles(t, 2)

	for i := range 2 {
		if !od.admit() {
			t.Fatalf("admission %d should have succeeded", i+1)
		}
	}
	if od.admit() {
		t.Fatal("the third admission should be rejected")
	}
	if got := od.rejectedBusy.Load(); got != 1 {
		t.Fatalf("rejectedBusy = %d, want 1", got)
	}
	// A rejected admission must not consume a slot.
	if got := od.inFlightGenerations.Load(); got != 2 {
		t.Fatalf("inFlightGenerations = %d, want 2 after a rejection", got)
	}

	od.release()
	if !od.admit() {
		t.Fatal("a released slot should be reusable")
	}

	od.release()
	od.release()
	if got := od.inFlightGenerations.Load(); got != 0 {
		t.Fatalf("inFlightGenerations = %d, want 0 once all slots are released", got)
	}
}

// Add-then-check must never let the limit be exceeded, even when many
// goroutines race for the last slot.
func TestAdmitNeverExceedsLimitUnderRace(t *testing.T) {
	const (
		limit   = 4
		callers = 200
	)
	od := newAdmissionTiles(t, limit)

	var (
		mu       sync.Mutex
		inside   int
		maxSeen  int
		admitted int
		wg       sync.WaitGroup
	)

	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			if !od.admit() {
				return
			}
			defer od.release()

			mu.Lock()
			admitted++
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

	if maxSeen > limit {
		t.Fatalf("max concurrent admissions = %d, want <= %d", maxSeen, limit)
	}
	if admitted == 0 {
		t.Fatal("no caller was admitted")
	}
	if got := od.inFlightGenerations.Load(); got != 0 {
		t.Fatalf("inFlightGenerations = %d, want 0 after all callers finished", got)
	}
}

// The admission gate sits below the cache check on purpose. A middleware could
// not tell a cache hit from a miss and would shed requests for tiles that
// already exist on disk — breaking the demo hardest exactly when the map is
// fully cached and there is no reason to reject anything.
func TestServeTileCacheHitBypassesAdmission(t *testing.T) {
	dir := t.TempDir()
	tilePath := filepath.Join(dir, "z1_x0_y0.png")
	want := []byte("not-really-a-png-but-served-verbatim")
	if err := os.WriteFile(tilePath, want, 0o600); err != nil {
		t.Fatalf("writing fixture tile: %v", err)
	}

	od := &OnDemandTiles{
		cfg: OnDemandTilesConfig{
			TilesDir:              dir,
			GenerateMissing:       true,
			MaxPendingGenerations: 1,
			CacheControl:          "public, max-age=60",
		},
	}
	// Saturate the backlog: a cache hit must still be served.
	od.inFlightGenerations.Store(99)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/tiles/z1_x0_y0.png", nil)
	od.serveTile(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != string(want) {
		t.Fatalf("body = %q, want %q", got, want)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=60" {
		t.Fatalf("Cache-Control = %q, want the configured tile policy", got)
	}
	if got := od.rejectedBusy.Load(); got != 0 {
		t.Fatalf("rejectedBusy = %d, want 0 — a cache hit must not be shed", got)
	}
}

func TestServeTileShedsWhenBacklogFull(t *testing.T) {
	od := newAdmissionTiles(t, 1)
	od.inFlightGenerations.Store(5)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/tiles/z1_x0_y0.png", nil)
	od.serveTile(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %q)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got != "5" {
		t.Fatalf("Retry-After = %q, want %q", got, "5")
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store — a cached 503 would pin the tile broken", got)
	}
	// The reject path must not leak a slot.
	if got := od.inFlightGenerations.Load(); got != 5 {
		t.Fatalf("inFlightGenerations = %d, want 5 (unchanged)", got)
	}
}
