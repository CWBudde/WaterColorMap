package mbtiles

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestWriter_New(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.mbtiles")

	metadata := Metadata{
		Name:        "Test Tileset",
		Format:      "png",
		MinZoom:     Zoom(10),
		MaxZoom:     Zoom(14),
		Bounds:      [4]float64{9.5, 51.8, 9.9, 52.1},
		Center:      [3]float64{9.7, 51.95, 12},
		Attribution: "© Test",
		Description: "Test description",
		Type:        "baselayer",
		Version:     "1.0",
	}

	w, err := New(dbPath, metadata)
	if err != nil {
		t.Fatalf("Failed to create writer: %v", err)
	}
	defer w.Close()

	// Verify database file exists
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatal("Database file was not created")
	}

	// Verify schema exists
	var count int
	err = w.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='tiles'").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query schema: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected tiles table to exist, got count=%d", count)
	}

	// Verify metadata was inserted
	err = w.db.QueryRow("SELECT COUNT(*) FROM metadata").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query metadata: %v", err)
	}
	if count == 0 {
		t.Error("Expected metadata to be inserted")
	}
}

func TestWriter_WriteTile(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.mbtiles")

	metadata := Metadata{
		Name:   "Test",
		Format: "png",
	}

	w, err := New(dbPath, metadata)
	if err != nil {
		t.Fatalf("Failed to create writer: %v", err)
	}
	defer w.Close()

	// Create fake PNG data
	pngData := []byte("fake png data")

	// Write a tile
	err = w.WriteTile(13, 4317, 2692, pngData)
	if err != nil {
		t.Fatalf("Failed to write tile: %v", err)
	}

	// Flush to ensure it's written
	err = w.Flush()
	if err != nil {
		t.Fatalf("Failed to flush: %v", err)
	}

	// Verify tile was written
	var count int
	err = w.db.QueryRow("SELECT COUNT(*) FROM tiles").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query tiles: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 tile, got %d", count)
	}

	// Verify TMS coordinate conversion
	var tileData []byte
	tmsY := (1 << 13) - 1 - 2692
	err = w.db.QueryRow("SELECT tile_data FROM tiles WHERE zoom_level=? AND tile_column=? AND tile_row=?",
		13, 4317, tmsY).Scan(&tileData)
	if err != nil {
		t.Fatalf("Failed to read tile: %v", err)
	}
	if len(tileData) == 0 {
		t.Error("Expected tile data to be stored")
	}
}

// pngMagic is the 8-byte PNG signature every PNG file starts with.
var pngMagic = []byte("\x89PNG\r\n\x1a\n")

// testPNG returns a minimal but valid encoded PNG image.
func testPNG(t *testing.T) []byte {
	t.Helper()

	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("Failed to encode test PNG: %v", err)
	}

	return buf.Bytes()
}

// TestWriter_StoresRawPNG guards the MBTiles interop fix: raster tile_data must
// be the PNG itself, not a gzip stream, or QGIS/tileserver-gl/mbutil see a
// corrupt image. The blob is read straight from SQLite, bypassing the Reader.
func TestWriter_StoresRawPNG(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.mbtiles")

	w, err := New(dbPath, Metadata{Name: "Test", Format: "png"})
	if err != nil {
		t.Fatalf("Failed to create writer: %v", err)
	}
	defer w.Close()

	pngData := testPNG(t)
	if err := w.WriteTile(13, 4317, 2692, pngData); err != nil {
		t.Fatalf("Failed to write tile: %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("Failed to flush: %v", err)
	}

	var stored []byte
	tmsY := (1 << 13) - 1 - 2692
	err = w.db.QueryRow("SELECT tile_data FROM tiles WHERE zoom_level=? AND tile_column=? AND tile_row=?",
		13, 4317, tmsY).Scan(&stored)
	if err != nil {
		t.Fatalf("Failed to read tile: %v", err)
	}

	if !bytes.HasPrefix(stored, pngMagic) {
		t.Errorf("Stored blob does not start with the PNG magic: got % x", stored[:min(8, len(stored))])
	}
	if !bytes.Equal(stored, pngData) {
		t.Error("Stored blob differs from the written PNG")
	}
}

// TestWriter_MetadataUniqueName verifies the metadata table enforces the
// spec-mandated uniqueness of names.
func TestWriter_MetadataUniqueName(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.mbtiles")

	w, err := New(dbPath, Metadata{Name: "Test", Format: "png"})
	if err != nil {
		t.Fatalf("Failed to create writer: %v", err)
	}
	defer w.Close()

	if _, err := w.db.Exec("INSERT INTO metadata (name, value) VALUES ('name', 'duplicate')"); err == nil {
		t.Error("Expected duplicate metadata name to be rejected, got nil error")
	}
}

// TestWriter_GzipsPBFTiles is the counterpart of TestWriter_StoresRawPNG: the
// MBTiles 1.3 spec requires vector (pbf) tile_data to carry a gzip envelope, so
// only raster formats may skip compression.
func TestWriter_GzipsPBFTiles(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "vector.mbtiles")

	w, err := New(dbPath, Metadata{Name: "Test", Format: FormatPBF})
	if err != nil {
		t.Fatalf("Failed to create writer: %v", err)
	}
	defer w.Close()

	payload := []byte("fake vector tile payload")
	if err := w.WriteTile(13, 4317, 2692, payload); err != nil {
		t.Fatalf("Failed to write tile: %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("Failed to flush: %v", err)
	}

	var stored []byte
	tmsY := (1 << 13) - 1 - 2692
	err = w.db.QueryRow("SELECT tile_data FROM tiles WHERE zoom_level=? AND tile_column=? AND tile_row=?",
		13, 4317, tmsY).Scan(&stored)
	if err != nil {
		t.Fatalf("Failed to read tile: %v", err)
	}

	if len(stored) < 2 || stored[0] != 0x1f || stored[1] != 0x8b {
		t.Fatalf("Stored pbf blob is not gzipped: got % x", stored[:min(8, len(stored))])
	}

	roundTripped, err := maybeGunzip(stored)
	if err != nil {
		t.Fatalf("Failed to gunzip stored blob: %v", err)
	}
	if !bytes.Equal(roundTripped, payload) {
		t.Error("Gunzipped blob differs from the written payload")
	}
}

// TestWriter_LegacyMetadataGetsUniqueIndex covers reopening a database written
// by an older release: CREATE TABLE IF NOT EXISTS leaves its unconstrained
// metadata table alone, so the unique index has to supply the constraint.
func TestWriter_LegacyMetadataGetsUniqueIndex(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "legacy.mbtiles")

	legacy, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Failed to open legacy database: %v", err)
	}
	_, err = legacy.Exec(`
		CREATE TABLE metadata (name TEXT, value TEXT);
		CREATE TABLE tiles (
			zoom_level INTEGER,
			tile_column INTEGER,
			tile_row INTEGER,
			tile_data BLOB
		);
		INSERT INTO metadata (name, value) VALUES ('name', 'old'), ('name', 'duplicate');
	`)
	if err != nil {
		t.Fatalf("Failed to create legacy schema: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("Failed to close legacy database: %v", err)
	}

	w, err := New(dbPath, Metadata{Name: "Test", Format: "png"})
	if err != nil {
		t.Fatalf("Failed to reopen legacy database: %v", err)
	}
	defer w.Close()

	// The duplicate legacy rows are gone (insertMetadata clears the table)...
	var count int
	if err := w.db.QueryRow("SELECT COUNT(*) FROM metadata WHERE name='name'").Scan(&count); err != nil {
		t.Fatalf("Failed to count metadata rows: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected exactly one 'name' row, got %d", count)
	}

	// ...and new duplicates are rejected even though the table predates the
	// PRIMARY KEY.
	if _, err := w.db.Exec("INSERT INTO metadata (name, value) VALUES ('name', 'duplicate')"); err == nil {
		t.Error("Expected duplicate metadata name to be rejected on a legacy table, got nil error")
	}
}

// TestMetadata_ToMapZeroZoom pins down that zoom level 0 survives the round
// trip instead of being dropped as an unset value.
func TestMetadata_ToMapZeroZoom(t *testing.T) {
	meta := Metadata{
		Name:    "Test",
		Format:  "png",
		MinZoom: Zoom(0),
		MaxZoom: Zoom(0),
	}

	got := meta.ToMap()
	if got["minzoom"] != "0" {
		t.Errorf("minzoom mismatch: got %q, want %q", got["minzoom"], "0")
	}
	if got["maxzoom"] != "0" {
		t.Errorf("maxzoom mismatch: got %q, want %q", got["maxzoom"], "0")
	}

	// An unset zoom is still omitted entirely.
	empty := Metadata{Name: "Test"}.ToMap()
	if _, ok := empty["minzoom"]; ok {
		t.Error("Expected minzoom to be omitted when unset")
	}
	if _, ok := empty["maxzoom"]; ok {
		t.Error("Expected maxzoom to be omitted when unset")
	}
}

// TestWriter_ZeroZoomMetadataRoundTrip checks z0 survives all the way through
// the database, not just ToMap.
func TestWriter_ZeroZoomMetadataRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.mbtiles")

	w, err := New(dbPath, Metadata{Name: "Test", Format: "png", MinZoom: Zoom(0), MaxZoom: Zoom(3)})
	if err != nil {
		t.Fatalf("Failed to create writer: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Failed to close writer: %v", err)
	}

	r, err := OpenReader(dbPath)
	if err != nil {
		t.Fatalf("Failed to open reader: %v", err)
	}
	defer r.Close()

	meta, err := r.Metadata()
	if err != nil {
		t.Fatalf("Failed to read metadata: %v", err)
	}
	if meta.MinZoom == nil || *meta.MinZoom != 0 {
		t.Errorf("MinZoom mismatch: got %v, want 0", meta.MinZoom)
	}
	if meta.MaxZoom == nil || *meta.MaxZoom != 3 {
		t.Errorf("MaxZoom mismatch: got %v, want 3", meta.MaxZoom)
	}
}

func TestWriter_BatchFlush(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.mbtiles")

	metadata := Metadata{
		Name:   "Test",
		Format: "png",
	}

	w, err := New(dbPath, metadata)
	if err != nil {
		t.Fatalf("Failed to create writer: %v", err)
	}
	defer w.Close()

	// Write multiple tiles
	pngData := []byte("fake png data")
	for i := 0; i < 150; i++ {
		err = w.WriteTile(13, i, 100, pngData)
		if err != nil {
			t.Fatalf("Failed to write tile %d: %v", i, err)
		}
	}

	// Close should flush remaining tiles
	err = w.Close()
	if err != nil {
		t.Fatalf("Failed to close: %v", err)
	}

	// Re-open and verify all tiles were written
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM tiles").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query tiles: %v", err)
	}
	if count != 150 {
		t.Errorf("Expected 150 tiles, got %d", count)
	}
}

func TestWriter_ReplaceExisting(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.mbtiles")

	metadata := Metadata{
		Name:   "Test",
		Format: "png",
	}

	w, err := New(dbPath, metadata)
	if err != nil {
		t.Fatalf("Failed to create writer: %v", err)
	}
	defer w.Close()

	// Write a tile
	pngData1 := []byte("first version")
	err = w.WriteTile(13, 100, 200, pngData1)
	if err != nil {
		t.Fatalf("Failed to write first tile: %v", err)
	}
	w.Flush()

	// Write the same tile again with different data
	pngData2 := []byte("second version")
	err = w.WriteTile(13, 100, 200, pngData2)
	if err != nil {
		t.Fatalf("Failed to write second tile: %v", err)
	}
	w.Flush()

	// Verify only one tile exists (was replaced)
	var count int
	err = w.db.QueryRow("SELECT COUNT(*) FROM tiles").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query tiles: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 tile (replaced), got %d", count)
	}
}

// newTestWriter opens a raster writer on a fresh temp path and returns both.
func newTestWriter(t *testing.T) (*Writer, string) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.mbtiles")
	w, err := New(dbPath, Metadata{Name: "Test", Format: "png"})
	if err != nil {
		t.Fatalf("Failed to create writer: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	return w, dbPath
}

func TestWriter_HasTile(t *testing.T) {
	tests := []struct {
		write *[3]int // tile to write first, nil for none
		name  string
		probe [3]int
		flush bool
		want  bool
	}{
		{
			name:  "absent tile",
			probe: [3]int{13, 100, 200},
			want:  false,
		},
		{
			name:  "written and flushed",
			write: &[3]int{13, 100, 200},
			flush: true,
			probe: [3]int{13, 100, 200},
			want:  true,
		},
		{
			name:  "written but unflushed",
			write: &[3]int{13, 100, 200},
			probe: [3]int{13, 100, 200},
			want:  true,
		},
		{
			// Regression guard: HasTile must apply the same XYZ->TMS flip as
			// the insert path. Without it, this mirrored y would report true.
			name:  "mirrored y is a different tile",
			write: &[3]int{13, 100, 200},
			flush: true,
			probe: [3]int{13, 100, tmsRow(13, 200)},
			want:  false,
		},
		{
			name:  "zoom zero",
			write: &[3]int{0, 0, 0},
			flush: true,
			probe: [3]int{0, 0, 0},
			want:  true,
		},
		{
			name:  "high zoom",
			write: &[3]int{20, 543210, 345678},
			flush: true,
			probe: [3]int{20, 543210, 345678},
			want:  true,
		},
		{
			name:  "high zoom neighbour absent",
			write: &[3]int{20, 543210, 345678},
			flush: true,
			probe: [3]int{20, 543210, 345679},
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, _ := newTestWriter(t)

			if tt.write != nil {
				if err := w.WriteTile(tt.write[0], tt.write[1], tt.write[2], []byte("fake png")); err != nil {
					t.Fatalf("WriteTile failed: %v", err)
				}
				if tt.flush {
					if err := w.Flush(); err != nil {
						t.Fatalf("Flush failed: %v", err)
					}
				}
			}

			got, err := w.HasTile(tt.probe[0], tt.probe[1], tt.probe[2])
			if err != nil {
				t.Fatalf("HasTile returned error: %v", err)
			}
			if got != tt.want {
				t.Errorf("HasTile(%d,%d,%d) = %v, want %v", tt.probe[0], tt.probe[1], tt.probe[2], got, tt.want)
			}
		})
	}
}

// TestWriter_HasTileRowMatchesInsert pins the two tmsRow call sites together:
// the row the insert path stores must be the row the lookup path computes.
func TestWriter_HasTileRowMatchesInsert(t *testing.T) {
	tests := []struct{ z, x, y int }{
		{0, 0, 0},
		{1, 1, 1},
		{13, 100, 200},
		{20, 543210, 345678},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d/%d/%d", tt.z, tt.x, tt.y), func(t *testing.T) {
			w, _ := newTestWriter(t)

			if err := w.WriteTile(tt.z, tt.x, tt.y, []byte("fake png")); err != nil {
				t.Fatalf("WriteTile failed: %v", err)
			}
			if err := w.Flush(); err != nil {
				t.Fatalf("Flush failed: %v", err)
			}

			var row int
			err := w.db.QueryRow(
				"SELECT tile_row FROM tiles WHERE zoom_level=? AND tile_column=?", tt.z, tt.x,
			).Scan(&row)
			if err != nil {
				t.Fatalf("Failed to read tile_row: %v", err)
			}

			if want := tmsRow(tt.z, tt.y); row != want {
				t.Errorf("stored tile_row = %d, tmsRow(%d,%d) = %d", row, tt.z, tt.y, want)
			}
		})
	}
}

// TestWriter_HasTileAfterReopen is the actual resume scenario: a run writes
// tiles, exits, and a later run must recognize them as already done.
func TestWriter_HasTileAfterReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "resume.mbtiles")
	metadata := Metadata{Name: "Test", Format: "png"}

	first, err := New(dbPath, metadata)
	if err != nil {
		t.Fatalf("Failed to create writer: %v", err)
	}
	if err := first.WriteTile(13, 100, 200, []byte("fake png")); err != nil {
		t.Fatalf("WriteTile failed: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	second, err := New(dbPath, metadata)
	if err != nil {
		t.Fatalf("Failed to reopen writer: %v", err)
	}
	defer second.Close()

	has, err := second.HasTile(13, 100, 200)
	if err != nil {
		t.Fatalf("HasTile returned error: %v", err)
	}
	if !has {
		t.Error("HasTile = false after reopen; a resumed run would re-render everything")
	}

	if has, err := second.HasTile(13, 100, 201); err != nil {
		t.Fatalf("HasTile returned error: %v", err)
	} else if has {
		t.Error("HasTile = true for a tile that was never written")
	}
}

// TestWriter_HasTileConcurrent exercises the mutex under -race.
func TestWriter_HasTileConcurrent(t *testing.T) {
	w, _ := newTestWriter(t)

	const goroutines = 8
	const perGoroutine = 40

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				if err := w.WriteTile(13, g, i, []byte("fake png")); err != nil {
					t.Errorf("WriteTile failed: %v", err)
					return
				}
				if _, err := w.HasTile(13, g, i); err != nil {
					t.Errorf("HasTile failed: %v", err)
					return
				}
			}
		}(g)
	}
	wg.Wait()

	if err := w.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	for g := 0; g < goroutines; g++ {
		for i := 0; i < perGoroutine; i++ {
			has, err := w.HasTile(13, g, i)
			if err != nil {
				t.Fatalf("HasTile failed: %v", err)
			}
			if !has {
				t.Fatalf("HasTile(13,%d,%d) = false after concurrent writes", g, i)
			}
		}
	}
}

// TestWriter_HasTileMatchesReader asserts the two views of the same database
// agree: HasTile is true exactly when ReadTile returns bytes.
func TestWriter_HasTileMatchesReader(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "agree.mbtiles")

	w, err := New(dbPath, Metadata{Name: "Test", Format: "png"})
	if err != nil {
		t.Fatalf("Failed to create writer: %v", err)
	}
	if err := w.WriteTile(13, 100, 200, []byte("fake png")); err != nil {
		t.Fatalf("WriteTile failed: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	w2, err := New(dbPath, Metadata{Name: "Test", Format: "png"})
	if err != nil {
		t.Fatalf("Failed to reopen writer: %v", err)
	}
	defer w2.Close()

	r, err := OpenReader(dbPath)
	if err != nil {
		t.Fatalf("Failed to open reader: %v", err)
	}
	defer r.Close()

	probes := [][3]int{
		{13, 100, 200},
		{13, 100, 201},
		{13, 100, tmsRow(13, 200)},
		{12, 100, 200},
	}

	for _, p := range probes {
		has, err := w2.HasTile(p[0], p[1], p[2])
		if err != nil {
			t.Fatalf("HasTile(%v) returned error: %v", p, err)
		}

		data, readErr := r.ReadTile(p[0], p[1], p[2])
		readable := readErr == nil && len(data) > 0

		if has != readable {
			t.Errorf("HasTile(%v) = %v but ReadTile readable = %v (read err: %v)", p, has, readable, readErr)
		}
	}
}

// TestNewRejectsFormatChange guards the nastiest interaction in the tile-format
// work. New calls insertMetadata, which DELETEs and rewrites the metadata
// table, so reopening a PNG tileset as WebP would silently relabel the file.
// HasTile is keyed on z/x/y alone, so the resume check would then skip every
// existing PNG tile as "already done" — leaving a database full of PNGs whose
// metadata says WebP, with no error anywhere.
func TestNewRejectsFormatChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tiles.mbtiles")

	w, err := New(path, Metadata{Name: "test", Format: "png"})
	if err != nil {
		t.Fatalf("create png tileset: %v", err)
	}
	if err := w.WriteTile(13, 4317, 2692, []byte("png bytes")); err != nil {
		t.Fatalf("write tile: %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	_, err = New(path, Metadata{Name: "test", Format: "webp"})
	if err == nil {
		t.Fatal("reopening a png tileset as webp should be refused")
	}
	if !errors.Is(err, ErrFormatMismatch) {
		t.Errorf("expected ErrFormatMismatch, got: %v", err)
	}
	// The message has to name both formats, or the operator cannot tell what
	// happened.
	for _, want := range []string{"png", "webp"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got: %v", want, err)
		}
	}

	// And the file must be untouched: still png, still holding its tile.
	r, err := OpenReader(path)
	if err != nil {
		t.Fatalf("reopen for reading: %v", err)
	}
	defer r.Close() //nolint:errcheck // test
	meta, err := r.Metadata()
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	if meta.Format != "png" {
		t.Errorf("format = %q, want png — the refused open still relabelled the file", meta.Format)
	}
}

// TestNewAllowsFormatChangeOnEmptyTileset: with no tiles there is nothing to be
// inconsistent with, so the format may change freely.
func TestNewAllowsFormatChangeOnEmptyTileset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tiles.mbtiles")

	w, err := New(path, Metadata{Name: "test", Format: "png"})
	if err != nil {
		t.Fatalf("create png tileset: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	w2, err := New(path, Metadata{Name: "test", Format: "webp"})
	if err != nil {
		t.Fatalf("an empty tileset should accept a format change: %v", err)
	}
	if err := w2.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// TestNewAllowsSameFormatReopen is the resume path, and must keep working.
func TestNewAllowsSameFormatReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tiles.mbtiles")

	for _, format := range []string{"png", "PNG"} {
		w, err := New(path, Metadata{Name: "test", Format: format})
		if err != nil {
			t.Fatalf("reopen with format %q: %v", format, err)
		}
		if err := w.WriteTile(13, 4317, 2692, []byte("png bytes")); err != nil {
			t.Fatalf("write tile: %v", err)
		}
		if err := w.Flush(); err != nil {
			t.Fatalf("flush: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
	}
}

// newUndeclaredTileset builds a tileset holding tiles but with no `format` row
// at all — the shape of a legacy or foreign database, which this project has
// never written itself.
func newUndeclaredTileset(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "tiles.mbtiles")
	w, err := New(path, Metadata{Name: "test", Format: "png"})
	if err != nil {
		t.Fatalf("create tileset: %v", err)
	}
	if err := w.WriteTile(13, 4317, 2692, []byte("png bytes")); err != nil {
		t.Fatalf("write tile: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open for doctoring: %v", err)
	}
	defer db.Close() //nolint:errcheck // test
	if _, err := db.Exec("DELETE FROM metadata WHERE name = 'format'"); err != nil {
		t.Fatalf("drop format row: %v", err)
	}
	return path
}

// TestNewRejectsWebPOverUndeclaredTiles: a missing `format` row must not be a
// free pass into relabelling. insertMetadata rewrites the metadata table, so
// accepting a webp run here would stamp `format=webp` onto tiles it did not
// write, and HasTile — which is keyed on z/x/y alone — would then skip every
// one of them as already done. That is the permanent-hole failure the guard
// exists to prevent, reached through the undeclared case instead of the
// mismatched one.
func TestNewRejectsWebPOverUndeclaredTiles(t *testing.T) {
	path := newUndeclaredTileset(t)

	_, err := New(path, Metadata{Name: "test", Format: "webp"})
	if err == nil {
		t.Fatal("reopening an undeclared non-empty tileset as webp should be refused")
	}
	if !errors.Is(err, ErrFormatMismatch) {
		t.Errorf("expected ErrFormatMismatch, got: %v", err)
	}

	// The file must be untouched: no format stamped on, tile still present.
	r, err := OpenReader(path)
	if err != nil {
		t.Fatalf("reopen for reading: %v", err)
	}
	defer r.Close() //nolint:errcheck // test
	meta, err := r.Metadata()
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	if meta.Format != "" {
		t.Errorf("format = %q, want empty — the refused open still relabelled the file", meta.Format)
	}
	if _, err := r.ReadTile(13, 4317, 2692); err != nil {
		t.Errorf("the existing tile should still be readable: %v", err)
	}
}

// TestNewAllowsPNGOverUndeclaredTiles is the other half: undeclared tiles are
// assumed to be PNG, which is what every file predating WebP support held, so
// a PNG run has to be able to resume into one rather than being locked out.
func TestNewAllowsPNGOverUndeclaredTiles(t *testing.T) {
	path := newUndeclaredTileset(t)

	w, err := New(path, Metadata{Name: "test", Format: "png"})
	if err != nil {
		t.Fatalf("a png run should be able to resume an undeclared tileset: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// TestNewAllowsFormatChangeOnUndeclaredEmptyTileset: with no tiles there is
// still nothing to be inconsistent with, declaration or not.
func TestNewAllowsFormatChangeOnUndeclaredEmptyTileset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tiles.mbtiles")

	w, err := New(path, Metadata{Name: "test", Format: "png"})
	if err != nil {
		t.Fatalf("create tileset: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open for doctoring: %v", err)
	}
	if _, err := db.Exec("DELETE FROM metadata WHERE name = 'format'"); err != nil {
		t.Fatalf("drop format row: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close doctoring handle: %v", err)
	}

	w2, err := New(path, Metadata{Name: "test", Format: "webp"})
	if err != nil {
		t.Fatalf("an empty undeclared tileset should accept any format: %v", err)
	}
	if err := w2.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
