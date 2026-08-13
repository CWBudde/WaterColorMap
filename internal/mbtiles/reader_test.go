package mbtiles

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestReader_RoundTrip(t *testing.T) {
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

	// Write tiles
	w, err := New(dbPath, metadata)
	if err != nil {
		t.Fatalf("Failed to create writer: %v", err)
	}

	pngData := []byte("fake png data for testing")
	tiles := []struct{ z, x, y int }{
		{13, 4317, 2692},
		{13, 4318, 2692},
		{14, 8634, 5384},
	}

	for _, tile := range tiles {
		err = w.WriteTile(tile.z, tile.x, tile.y, pngData)
		if err != nil {
			t.Fatalf("Failed to write tile %d/%d/%d: %v", tile.z, tile.x, tile.y, err)
		}
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Failed to close writer: %v", err)
	}

	// Read tiles back
	r, err := OpenReader(dbPath)
	if err != nil {
		t.Fatalf("Failed to open reader: %v", err)
	}
	defer r.Close()

	for _, tile := range tiles {
		data, err := r.ReadTile(tile.z, tile.x, tile.y)
		if err != nil {
			t.Fatalf("Failed to read tile %d/%d/%d: %v", tile.z, tile.x, tile.y, err)
		}

		if string(data) != string(pngData) {
			t.Errorf("Tile %d/%d/%d data mismatch: got %q, want %q",
				tile.z, tile.x, tile.y, string(data), string(pngData))
		}
	}
}

// TestReader_ReadsLegacyGzippedTile covers back-compat: .mbtiles files written
// by earlier versions of this package stored gzipped PNGs, and the reader has
// to keep serving them. The gzipped blob is inserted directly, bypassing the
// Writer, which now only stores raw PNGs.
func TestReader_ReadsLegacyGzippedTile(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "legacy.mbtiles")

	w, err := New(dbPath, Metadata{Name: "Test", Format: "png"})
	if err != nil {
		t.Fatalf("Failed to create writer: %v", err)
	}

	pngData := testPNG(t)

	var gzipped bytes.Buffer
	gw := gzip.NewWriter(&gzipped)
	if _, err := gw.Write(pngData); err != nil {
		t.Fatalf("Failed to gzip test PNG: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("Failed to close gzip writer: %v", err)
	}

	tmsY := (1 << 13) - 1 - 2692
	_, err = w.db.Exec(
		"INSERT INTO tiles (zoom_level, tile_column, tile_row, tile_data) VALUES (?, ?, ?, ?)",
		13, 4317, tmsY, gzipped.Bytes(),
	)
	if err != nil {
		t.Fatalf("Failed to insert legacy tile: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Failed to close writer: %v", err)
	}

	r, err := OpenReader(dbPath)
	if err != nil {
		t.Fatalf("Failed to open reader: %v", err)
	}
	defer r.Close()

	got, err := r.ReadTile(13, 4317, 2692)
	if err != nil {
		t.Fatalf("Failed to read legacy tile: %v", err)
	}
	if !bytes.Equal(got, pngData) {
		t.Error("Legacy gzipped tile did not decompress back to the original PNG")
	}
}

func TestReader_Metadata(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.mbtiles")

	expectedMetadata := Metadata{
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

	// Write database with metadata
	w, err := New(dbPath, expectedMetadata)
	if err != nil {
		t.Fatalf("Failed to create writer: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Failed to close writer: %v", err)
	}

	// Read metadata back
	r, err := OpenReader(dbPath)
	if err != nil {
		t.Fatalf("Failed to open reader: %v", err)
	}
	defer r.Close()

	meta, err := r.Metadata()
	if err != nil {
		t.Fatalf("Failed to read metadata: %v", err)
	}

	// Verify metadata fields
	if meta.Name != expectedMetadata.Name {
		t.Errorf("Name mismatch: got %q, want %q", meta.Name, expectedMetadata.Name)
	}
	if meta.Format != expectedMetadata.Format {
		t.Errorf("Format mismatch: got %q, want %q", meta.Format, expectedMetadata.Format)
	}
	if meta.MinZoom == nil || *meta.MinZoom != *expectedMetadata.MinZoom {
		t.Errorf("MinZoom mismatch: got %v, want %d", meta.MinZoom, *expectedMetadata.MinZoom)
	}
	if meta.MaxZoom == nil || *meta.MaxZoom != *expectedMetadata.MaxZoom {
		t.Errorf("MaxZoom mismatch: got %v, want %d", meta.MaxZoom, *expectedMetadata.MaxZoom)
	}
	if meta.Bounds != expectedMetadata.Bounds {
		t.Errorf("Bounds mismatch: got %v, want %v", meta.Bounds, expectedMetadata.Bounds)
	}
	if meta.Center != expectedMetadata.Center {
		t.Errorf("Center mismatch: got %v, want %v", meta.Center, expectedMetadata.Center)
	}
	if meta.Attribution != expectedMetadata.Attribution {
		t.Errorf("Attribution mismatch: got %q, want %q", meta.Attribution, expectedMetadata.Attribution)
	}
}

func TestReader_TileNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.mbtiles")

	metadata := Metadata{
		Name:   "Test",
		Format: "png",
	}

	// Create empty database
	w, err := New(dbPath, metadata)
	if err != nil {
		t.Fatalf("Failed to create writer: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Failed to close writer: %v", err)
	}

	// Try to read non-existent tile
	r, err := OpenReader(dbPath)
	if err != nil {
		t.Fatalf("Failed to open reader: %v", err)
	}
	defer r.Close()

	_, err = r.ReadTile(13, 4317, 2692)
	if err == nil {
		t.Error("Expected error for non-existent tile, got nil")
	}
}

func TestReader_InvalidDatabase(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "invalid.mbtiles")

	// Create an empty file
	if err := os.WriteFile(dbPath, []byte("not a database"), 0o644); err != nil {
		t.Fatalf("Failed to create invalid file: %v", err)
	}

	// Try to open it
	_, err := OpenReader(dbPath)
	if err == nil {
		t.Error("Expected error for invalid database, got nil")
	}
}
