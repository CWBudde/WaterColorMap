package mbtiles

import (
	"bytes"
	"database/sql"
	"image"
	"image/png"
	"os"
	"path/filepath"
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
