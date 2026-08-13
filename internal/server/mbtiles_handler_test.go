package server

import (
	"bytes"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/cwbudde/watercolormap/internal/mbtiles"
)

// newTestMBTiles writes a single-tile MBTiles database and returns its path
// together with the raw PNG bytes it contains.
func newTestMBTiles(t *testing.T, z, x, y int) (string, []byte) {
	t.Helper()

	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatalf("Failed to encode test PNG: %v", err)
	}
	pngData := buf.Bytes()

	dbPath := filepath.Join(t.TempDir(), "test.mbtiles")
	w, err := mbtiles.New(dbPath, mbtiles.Metadata{
		Name:    "Test",
		Format:  "png",
		MinZoom: mbtiles.Zoom(0),
		MaxZoom: mbtiles.Zoom(14),
	})
	if err != nil {
		t.Fatalf("Failed to create MBTiles writer: %v", err)
	}
	if err := w.WriteTile(z, x, y, pngData); err != nil {
		t.Fatalf("Failed to write tile: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Failed to close MBTiles writer: %v", err)
	}

	return dbPath, pngData
}

func TestMBTilesHandler_ServeTile(t *testing.T) {
	const z, x, y = 13, 4317, 2692

	dbPath, pngData := newTestMBTiles(t, z, x, y)

	h, err := NewMBTilesHandler(MBTilesConfig{
		MBTilesPath:  dbPath,
		CacheControl: "public, max-age=3600",
	}, nil)
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}
	defer h.Close()

	srv := httptest.NewServer(h.Handler())
	defer srv.Close()

	t.Run("serves the stored PNG", func(t *testing.T) {
		resp, err := srv.Client().Get(srv.URL + "/tiles/z13_x4317_y2692.png")
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("Status mismatch: got %d, want %d", resp.StatusCode, http.StatusOK)
		}
		if got := resp.Header.Get("Content-Type"); got != "image/png" {
			t.Errorf("Content-Type mismatch: got %q, want %q", got, "image/png")
		}
		if got := resp.Header.Get("Cache-Control"); got != "public, max-age=3600" {
			t.Errorf("Cache-Control mismatch: got %q", got)
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("Failed to read body: %v", err)
		}
		if !bytes.Equal(body, pngData) {
			t.Error("Response body does not match the stored PNG")
		}
	})

	t.Run("missing tile is a 404", func(t *testing.T) {
		resp, err := srv.Client().Get(srv.URL + "/tiles/z13_x4318_y2692.png")
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("Status mismatch: got %d, want %d", resp.StatusCode, http.StatusNotFound)
		}
	})
}
