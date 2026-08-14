package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// assertNoCORSHeaders fails when a response carries any cross-origin header.
func assertNoCORSHeaders(t *testing.T, header http.Header) {
	t.Helper()

	for _, name := range []string{
		"Access-Control-Allow-Origin",
		"Access-Control-Allow-Methods",
		"Access-Control-Allow-Headers",
	} {
		if got := header.Get(name); got != "" {
			t.Errorf("%s = %q, want it absent; CORS belongs to the serve middleware alone", name, got)
		}
	}
}

// The tile handler used to hardcode Access-Control-Allow-Origin: *, which
// would silently override the --cors-origin toggle in the serve command. The
// headers now come from that middleware alone.
func TestOnDemandTileHandlerSetsNoCORSHeaders(t *testing.T) {
	od := &OnDemandTiles{
		cfg: OnDemandTilesConfig{
			TilesDir:        t.TempDir(),
			GenerateMissing: false,
		},
		sem: make(chan struct{}, 1),
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/tiles/z13_x4297_y2754.png", nil)
	req.Header.Set("Origin", "https://example.com")
	od.Handler().ServeHTTP(rec, req)

	assertNoCORSHeaders(t, rec.Header())
}

func TestStatusHandlersSetNoCORSHeaders(t *testing.T) {
	od := &OnDemandTiles{cfg: OnDemandTilesConfig{TilesDir: t.TempDir()}}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/tiles/status", nil)
	od.StatusHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	assertNoCORSHeaders(t, rec.Header())
}

func TestMBTilesHandlerSetsNoCORSHeaders(t *testing.T) {
	const z, x, y = 13, 4317, 2692

	dbPath, _ := newTestMBTiles(t, z, x, y)

	h, err := NewMBTilesHandler(MBTilesConfig{
		MBTilesPath:  dbPath,
		CacheControl: "public, max-age=3600",
	}, nil)
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}
	defer h.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/tiles/z13_x4317_y2692.png", nil)
	req.Header.Set("Origin", "https://example.com")
	h.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	assertNoCORSHeaders(t, rec.Header())
}
