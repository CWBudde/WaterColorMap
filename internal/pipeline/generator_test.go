package pipeline

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cwbudde/watercolormap/internal/tile"
	"github.com/cwbudde/watercolormap/internal/tileformat"
)

// nonProbingWriter is a TileWriter that cannot answer existence questions —
// the shape every pre-existing writer and test fake has.
type nonProbingWriter struct{}

func (nonProbingWriter) WriteTile(int, int, int, []byte) error { return nil }

// probingWriter is a TileWriter that also implements TileProber.
type probingWriter struct {
	err    error
	exists bool
}

func (probingWriter) WriteTile(int, int, int, []byte) error { return nil }

func (p probingWriter) HasTile(int, int, int) (bool, error) {
	if p.err != nil {
		return false, p.err
	}
	return p.exists, nil
}

// TestTileExists covers the skip decision for both backends. The invariant the
// table encodes: anything uncertain (no prober, failed probe) must answer false
// so the tile gets rendered, because a wrong skip leaves a permanent hole.
func TestTileExists(t *testing.T) {
	tests := []struct {
		writer     TileWriter
		name       string
		createFile bool
		want       bool
	}{
		{
			name:       "folder backend with existing file",
			writer:     nil,
			createFile: true,
			want:       true,
		},
		{
			name:       "folder backend with missing file",
			writer:     nil,
			createFile: false,
			want:       false,
		},
		{
			name:   "prober reports the tile present",
			writer: probingWriter{exists: true},
			want:   true,
		},
		{
			name:   "prober reports the tile absent",
			writer: probingWriter{exists: false},
			want:   false,
		},
		{
			// A failing probe must not be read as "already done".
			name:   "prober fails",
			writer: probingWriter{err: errors.New("database is locked")},
			want:   false,
		},
		{
			// The writer stays authoritative: never fall back to os.Stat on a
			// folder path that a writer-backed run does not populate.
			name:       "writer that cannot probe",
			writer:     nonProbingWriter{},
			createFile: true,
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			finalPath := filepath.Join(t.TempDir(), "z13_x100_y200.png")
			if tt.createFile {
				if err := os.WriteFile(finalPath, []byte("existing tile"), 0o600); err != nil {
					t.Fatalf("Failed to create tile file: %v", err)
				}
			}

			g := &Generator{
				logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
				options: GeneratorOptions{TileWriter: tt.writer},
			}

			got := g.tileExists(tile.Coords{Z: 13, X: 100, Y: 200}, finalPath)
			if got != tt.want {
				t.Errorf("tileExists = %v, want %v", got, tt.want)
			}
		})
	}
}

// newFormatGenerator builds a generator far enough to exercise TilePath, which
// needs a resolved encoder but no textures or datasource.
func newFormatGenerator(t *testing.T, outputDir, structure string, format tileformat.Format) *Generator {
	t.Helper()

	enc, err := tileformat.NewEncoder(tileformat.EncoderOptions{Format: format})
	if err != nil {
		t.Fatalf("NewEncoder(%v): %v", format, err)
	}
	return &Generator{
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		outputDir: outputDir,
		enc:       enc,
		options:   GeneratorOptions{FolderStructure: structure},
	}
}

// TestTilePath pins the on-disk naming across both layouts, both formats and
// the @2x suffix. The tile server derives its own paths from this same method,
// so a mistake here means the reader looks for a file the writer never wrote.
func TestTilePath(t *testing.T) {
	coords := tile.Coords{Z: 13, X: 4317, Y: 2692}

	tests := []struct {
		name      string
		structure string
		format    tileformat.Format
		suffix    string
		wantPath  string
		wantDir   string
	}{
		{"flat png", "flat", tileformat.PNG, "", "z13_x4317_y2692.png", ""},
		{"flat png @2x", "flat", tileformat.PNG, "@2x", "z13_x4317_y2692@2x.png", ""},
		{"flat webp", "flat", tileformat.WebP, "", "z13_x4317_y2692.webp", ""},
		{"flat webp @2x", "flat", tileformat.WebP, "@2x", "z13_x4317_y2692@2x.webp", ""},
		{"nested png", "nested", tileformat.PNG, "", "13/4317/2692.png", "13/4317"},
		{"nested png @2x", "nested", tileformat.PNG, "@2x", "13/4317/2692@2x.png", "13/4317"},
		{"nested webp", "nested", tileformat.WebP, "", "13/4317/2692.webp", "13/4317"},
		{"nested webp @2x", "nested", tileformat.WebP, "@2x", "13/4317/2692@2x.webp", "13/4317"},
		// The zero format has to keep meaning PNG: it is what every
		// pre-existing construction site passes.
		{"unset format is png", "flat", tileformat.Format(""), "", "z13_x4317_y2692.png", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			g := newFormatGenerator(t, root, tt.structure, tt.format)

			gotPath, gotDir := g.TilePath(coords, tt.suffix)

			if want := filepath.Join(root, tt.wantPath); gotPath != want {
				t.Errorf("path = %q, want %q", gotPath, want)
			}
			if want := filepath.Join(root, tt.wantDir); gotDir != want {
				t.Errorf("dir = %q, want %q", gotDir, want)
			}
		})
	}
}

// TestExistingPNGDoesNotSatisfyAWebPRun guards resume correctness across a
// format change. It holds because the extension is part of the path, but it is
// pinned anyway: a false skip leaves a permanent hole, and this is exactly the
// shape of change that could introduce one.
func TestExistingPNGDoesNotSatisfyAWebPRun(t *testing.T) {
	root := t.TempDir()
	coords := tile.Coords{Z: 13, X: 4317, Y: 2692}

	pngGen := newFormatGenerator(t, root, "flat", tileformat.PNG)
	pngPath, _ := pngGen.TilePath(coords, "")
	if err := os.WriteFile(pngPath, []byte("a rendered png tile"), 0o600); err != nil {
		t.Fatalf("write png tile: %v", err)
	}

	if !pngGen.tileExists(coords, pngPath) {
		t.Fatal("the png run should consider its own tile present")
	}

	webpGen := newFormatGenerator(t, root, "flat", tileformat.WebP)
	webpPath, _ := webpGen.TilePath(coords, "")
	if webpPath == pngPath {
		t.Fatal("the webp run must not target the same file as the png run")
	}
	if webpGen.tileExists(coords, webpPath) {
		t.Error("an existing PNG must not let a WebP run skip the tile")
	}
}

func TestNewGeneratorRejectsUnknownImageFormat(t *testing.T) {
	_, err := NewGenerator(
		nil,
		filepath.Join("..", "..", "assets", "styles"),
		filepath.Join("..", "..", "assets", "textures"),
		t.TempDir(),
		256, 1337, false,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		GeneratorOptions{ImageFormat: tileformat.Format("jpeg")},
	)
	if err == nil {
		t.Fatal("expected an error for an unsupported image format")
	}
	if !strings.Contains(err.Error(), "tile image format") {
		t.Errorf("error should name the offending setting, got: %v", err)
	}
}

// TestTileExistsHonoursTheConfiguredFormat pins the interaction between two
// changes that arrived independently: banded generation asks TileExists which
// tiles a resumed run can skip, and WebP output made the tile's extension
// configurable.
//
// If TileExists resolved the path with a hardcoded ".png" while a WebP run
// wrote ".webp" — or the reverse — a resumed banded WebP run would find none of
// its own tiles, re-query Overpass for every block it had already finished and
// re-render the lot. The two must derive the name from the same place, which is
// why this asserts the negative case as well: the file being there under the
// *other* extension must not count.
func TestTileExistsHonoursTheConfiguredFormat(t *testing.T) {
	coords := tile.Coords{Z: 13, X: 4317, Y: 2692}

	tests := []struct {
		name     string
		format   tileformat.Format
		writeExt string
		want     bool
	}{
		{"webp run finds its webp tile", tileformat.WebP, ".webp", true},
		{"png run finds its png tile", tileformat.PNG, ".png", true},
		// The cross cases are the regression: a tile in the other format is a
		// different tile, and skipping on it would leave a permanent hole.
		{"webp run ignores a png tile", tileformat.WebP, ".png", false},
		{"png run ignores a webp tile", tileformat.PNG, ".webp", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			g := newFormatGenerator(t, root, "flat", tt.format)

			existing := filepath.Join(root, "z13_x4317_y2692"+tt.writeExt)
			if err := os.WriteFile(existing, []byte("existing tile"), 0o600); err != nil {
				t.Fatalf("Failed to create tile file: %v", err)
			}

			if got := g.TileExists(coords, ""); got != tt.want {
				t.Errorf("TileExists = %v, want %v (format %v, file on disk %s)",
					got, tt.want, tt.format, tt.writeExt)
			}
		})
	}
}
