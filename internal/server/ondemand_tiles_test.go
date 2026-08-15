package server

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/cwbudde/watercolormap/internal/tile"
	"github.com/cwbudde/watercolormap/internal/tileformat"
)

func TestParseTilePath(t *testing.T) {
	ok := []struct {
		name       string
		path       string
		wantCoords string
		wantSuffix string
		wantFormat tileformat.Format
	}{
		{"base tile", "/tiles/z13_x4317_y2692.png", "z13_x4317_y2692", "", tileformat.PNG},
		{"hidpi tile", "/tiles/z5_x1_y2@2x.png", "z5_x1_y2", "@2x", tileformat.PNG},
		{"webp tile", "/tiles/z13_x4317_y2692.webp", "z13_x4317_y2692", "", tileformat.WebP},
		{"hidpi webp tile", "/tiles/z5_x1_y2@2x.webp", "z5_x1_y2", "@2x", tileformat.WebP},
	}
	for _, tt := range ok {
		t.Run(tt.name, func(t *testing.T) {
			coords, suffix, format, err := parseTilePath(tt.path)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if suffix != tt.wantSuffix {
				t.Fatalf("suffix = %q, want %q", suffix, tt.wantSuffix)
			}
			if coords.String() != tt.wantCoords {
				t.Fatalf("coords = %s, want %s", coords.String(), tt.wantCoords)
			}
			if format != tt.wantFormat {
				t.Fatalf("format = %v, want %v", format, tt.wantFormat)
			}
		})
	}

	// Malformed paths and impossible coordinates are distinguished so the
	// handlers can answer 404 and 400 respectively.
	errKind := []struct {
		wantErr error
		name    string
		path    string
	}{
		{tile.ErrCoordsFormat, "reject unsupported extension", "/tiles/z5_x1_y2.jpg"},
		{tile.ErrCoordsFormat, "reject missing extension", "/tiles/z5_x1_y2"},
		{tile.ErrCoordsFormat, "reject trailing garbage on webp", "/tiles/z13_x1_y2JUNK.webp"},
		{tile.ErrCoordsOutOfRange, "reject out-of-range webp", "/tiles/z23_x1_y2.webp"},
		{tile.ErrCoordsFormat, "reject other prefix", "/demo/z5_x1_y2.png"},
		{tile.ErrCoordsFormat, "reject trailing garbage", "/tiles/z13_x1_y2JUNK.png"},
		{tile.ErrCoordsOutOfRange, "reject zoom above max", "/tiles/z23_x1_y2.png"},
		{tile.ErrCoordsOutOfRange, "reject x outside grid", "/tiles/z1_x2_y0.png"},
		{tile.ErrCoordsOutOfRange, "reject y outside grid", "/tiles/z0_x0_y1.png"},
		{tile.ErrCoordsOutOfRange, "reject huge coords", "/tiles/z30_x999999999_y1.png"},
	}
	for _, tt := range errKind {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, err := parseTilePath(tt.path)
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
			_, _, _, err := parseTilePath(tt.path)
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

// TestServeTileRejectsOtherFormats: the server renders exactly one format.
// Answering a .png URL with WebP bytes would be a lie that every cache
// downstream then remembers, and rendering both formats doubles fetch, render
// and disk for a choice the operator already made.
func TestServeTileRejectsOtherFormats(t *testing.T) {
	tests := []struct {
		name       string
		configured tileformat.Format
		path       string
		wantStatus int
	}{
		{"png server serves png", tileformat.PNG, "/tiles/z13_x4317_y2692.png", http.StatusOK},
		{"png server refuses webp", tileformat.PNG, "/tiles/z13_x4317_y2692.webp", http.StatusNotFound},
		{"webp server serves webp", tileformat.WebP, "/tiles/z13_x4317_y2692.webp", http.StatusOK},
		{"webp server refuses png", tileformat.WebP, "/tiles/z13_x4317_y2692.png", http.StatusNotFound},
		// The zero value has to keep behaving as PNG.
		{"unset format serves png", tileformat.Format(""), "/tiles/z13_x4317_y2692.png", http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()

			// Pre-render the tile the request should find, so the test needs
			// neither Overpass nor Mapnik.
			format := tt.configured
			if format == "" {
				format = tileformat.PNG
			}
			name := tile.Coords{Z: 13, X: 4317, Y: 2692}.FileName("", format.Ext())
			if err := os.WriteFile(filepath.Join(dir, name), []byte("tile bytes"), 0o600); err != nil {
				t.Fatalf("write tile: %v", err)
			}

			ondemand, err := NewOnDemandTiles(nil, OnDemandTilesConfig{
				TilesDir:        dir,
				ImageFormat:     tt.configured,
				GenerateMissing: false,
			}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatalf("NewOnDemandTiles: %v", err)
			}
			defer ondemand.Stop()

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			ondemand.serveTile(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantStatus == http.StatusOK {
				if got := rec.Header().Get("Content-Type"); got != format.ContentType() {
					t.Errorf("Content-Type = %q, want %q", got, format.ContentType())
				}
			}
		})
	}
}
