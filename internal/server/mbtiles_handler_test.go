package server

import (
	"bytes"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

// newTestMBTilesFormat writes a single-tile MBTiles database declaring the
// given format. The payload is opaque: the handler copies tile bytes verbatim
// and never decodes them.
func newTestMBTilesFormat(t *testing.T, format string) string {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.mbtiles")
	w, err := mbtiles.New(dbPath, mbtiles.Metadata{
		Name:    "Test",
		Format:  format,
		MinZoom: mbtiles.Zoom(0),
		MaxZoom: mbtiles.Zoom(14),
	})
	if err != nil {
		t.Fatalf("Failed to create MBTiles writer: %v", err)
	}
	if err := w.WriteTile(13, 4317, 2692, []byte("tile bytes")); err != nil {
		t.Fatalf("Failed to write tile: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Failed to close MBTiles writer: %v", err)
	}
	return dbPath
}

// TestMBTilesHandlerContentTypeFollowsTheFile: the tileset's own metadata is
// the only authority on what its bytes are. Deriving the header from a flag
// would let a WebP file be served as image/png, which every cache downstream
// would then remember.
func TestMBTilesHandlerContentTypeFollowsTheFile(t *testing.T) {
	tests := []struct {
		name string
		// format is what the tileset declares; reqExt is the extension the
		// client asks for, and has to match what that declaration resolves to
		// — serving one format's bytes under the other's name is what
		// TestMBTilesHandlerRejectsTheOtherExtension covers.
		format string
		reqExt string
		want   string
	}{
		{"png tileset", "png", ".png", "image/png"},
		{"webp tileset", "webp", ".webp", "image/webp"},
		// Files written before the format was trustworthy were all PNG, so an
		// unreadable declaration must not become an error.
		{"unknown format falls back to png", "jpeg", ".png", "image/png"},
		{"empty format falls back to png", "", ".png", "image/png"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dbPath := newTestMBTilesFormat(t, tt.format)

			h, err := NewMBTilesHandler(MBTilesConfig{MBTilesPath: dbPath}, nil)
			if err != nil {
				t.Fatalf("NewMBTilesHandler: %v", err)
			}
			defer h.Close() //nolint:errcheck // test

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/tiles/z13_x4317_y2692"+tt.reqExt, nil)
			h.Handler()(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Type"); got != tt.want {
				t.Errorf("Content-Type = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestMBTilesHandlerRejectsTheOtherExtension: a tileset holds exactly one
// format, so answering the other extension with those same bytes would put
// WebP behind a .png URL — the cache/URL lie the on-demand handler already
// refuses. The two backends have to make the same promise, or the guarantee
// depends on which one happens to be serving.
func TestMBTilesHandlerRejectsTheOtherExtension(t *testing.T) {
	tests := []struct {
		name     string
		format   string
		wrongExt string
	}{
		{"png tileset asked for webp", "png", ".webp"},
		{"webp tileset asked for png", "webp", ".png"},
		// A file that declares nothing is served as PNG, so .webp is still the
		// wrong extension for it.
		{"undeclared tileset asked for webp", "", ".webp"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dbPath := newTestMBTilesFormat(t, tt.format)

			h, err := NewMBTilesHandler(MBTilesConfig{MBTilesPath: dbPath}, nil)
			if err != nil {
				t.Fatalf("NewMBTilesHandler: %v", err)
			}
			defer h.Close() //nolint:errcheck // test

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/tiles/z13_x4317_y2692"+tt.wrongExt, nil)
			h.Handler()(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404 for %s against a %q tileset",
					rec.Code, tt.wrongExt, tt.format)
			}
			if ct := rec.Header().Get("Content-Type"); strings.HasPrefix(ct, "image/") {
				t.Errorf("Content-Type = %q; a rejection must not be served as an image", ct)
			}
		})
	}
}
