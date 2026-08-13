package server

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cwbudde/watercolormap/internal/tile"
)

func TestParseTilePath(t *testing.T) {
	t.Run("base tile", func(t *testing.T) {
		coords, suffix, err := parseTilePath("/tiles/z13_x4317_y2692.png")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if suffix != "" {
			t.Fatalf("expected empty suffix, got %q", suffix)
		}
		if coords.String() != "z13_x4317_y2692" {
			t.Fatalf("unexpected coords: %s", coords.String())
		}
	})

	t.Run("hidpi tile", func(t *testing.T) {
		coords, suffix, err := parseTilePath("/tiles/z5_x1_y2@2x.png")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if suffix != "@2x" {
			t.Fatalf("expected @2x suffix, got %q", suffix)
		}
		if coords.String() != "z5_x1_y2" {
			t.Fatalf("unexpected coords: %s", coords.String())
		}
	})

	// Malformed paths and impossible coordinates are distinguished so the
	// handlers can answer 404 and 400 respectively.
	errKind := []struct {
		wantErr error
		name    string
		path    string
	}{
		{tile.ErrCoordsFormat, "reject non-png", "/tiles/z5_x1_y2.jpg"},
		{tile.ErrCoordsFormat, "reject other prefix", "/demo/z5_x1_y2.png"},
		{tile.ErrCoordsFormat, "reject trailing garbage", "/tiles/z13_x1_y2JUNK.png"},
		{tile.ErrCoordsOutOfRange, "reject zoom above max", "/tiles/z23_x1_y2.png"},
		{tile.ErrCoordsOutOfRange, "reject x outside grid", "/tiles/z1_x2_y0.png"},
		{tile.ErrCoordsOutOfRange, "reject y outside grid", "/tiles/z0_x0_y1.png"},
		{tile.ErrCoordsOutOfRange, "reject huge coords", "/tiles/z30_x999999999_y1.png"},
	}
	for _, tt := range errKind {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := parseTilePath(tt.path)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("parseTilePath(%q) error = %v, want %v", tt.path, err, tt.wantErr)
			}
		})
	}
}

func TestWriteTilePathErrorStatus(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		wantStatus int
	}{
		{"out of range is a client error", "/tiles/z30_x999999999_y1.png", http.StatusBadRequest},
		{"zoom above max is a client error", "/tiles/z23_x0_y0.png", http.StatusBadRequest},
		{"malformed is not found", "/tiles/nonsense.png", http.StatusNotFound},
		{"wrong extension is not found", "/tiles/z5_x1_y2.jpg", http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := parseTilePath(tt.path)
			if err == nil {
				t.Fatalf("parseTilePath(%q) unexpectedly succeeded", tt.path)
			}

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			writeTilePathError(rec, req, slog.Default(), err)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}

// An out-of-range coordinate must be rejected by the handler itself, before
// any generator, fetch queue or datasource is touched. The OnDemandTiles here
// has a nil datasource, so reaching the generate path at all would panic
// rather than quietly succeed. The semaphore is initialized so that a
// regression fails fast instead of blocking forever on a nil channel.
func TestServeTileRejectsOutOfRangeBeforeGenerating(t *testing.T) {
	od := &OnDemandTiles{
		cfg: OnDemandTilesConfig{
			TilesDir:        t.TempDir(),
			GenerateMissing: true,
		},
		sem: make(chan struct{}, 1),
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/tiles/z30_x999999999_y1.png", nil)
	od.serveTile(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}
