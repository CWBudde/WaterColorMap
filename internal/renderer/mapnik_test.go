package renderer

import (
	"context"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/cwbudde/watercolormap/internal/datasource"
	"github.com/cwbudde/watercolormap/internal/types"
)

func TestMapnikRenderer_Basic(t *testing.T) {
	requireIntegration(t)

	// Create renderer
	renderer, err := NewMapnikRenderer("", 256)
	if err != nil {
		t.Fatalf("Failed to create renderer: %v", err)
	}
	defer renderer.Close()

	// Set basic background
	if err := renderer.SetBackgroundColor("#f8f4e8"); err != nil {
		t.Fatalf("Failed to set background color: %v", err)
	}

	// Test tile (Hanover)
	tile := types.TileCoordinate{
		Zoom: 13,
		X:    4317,
		Y:    2692,
	}

	// Create a simple test image
	img, err := renderer.RenderTile(tile, nil)
	if err != nil {
		t.Fatalf("Failed to render tile: %v", err)
	}

	if img == nil {
		t.Fatal("Rendered image is nil")
	}

	// Check image dimensions
	bounds := img.Bounds()
	if bounds.Dx() != 256 || bounds.Dy() != 256 {
		t.Errorf("Expected 256x256 image, got %dx%d", bounds.Dx(), bounds.Dy())
	}

	t.Logf("Successfully rendered %dx%d tile", bounds.Dx(), bounds.Dy())
}

func TestMapnikRenderer_WithOSMData(t *testing.T) {
	requireIntegration(t)

	// Create output directory
	outputDir := "../../testdata/output"
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatalf("Failed to create output directory: %v", err)
	}

	// Fetch OSM data
	ds := datasource.NewOverpassDataSource("")
	tile := types.TileCoordinate{
		Zoom: 13,
		X:    4317,
		Y:    2692,
	}

	t.Log("Fetching OSM data for tile...")
	data, err := ds.FetchTileData(context.Background(), tile)
	if err != nil {
		t.Fatalf("Failed to fetch OSM data: %v", err)
	}

	t.Logf("Fetched %d features", data.Features.Count())

	// Create renderer
	renderer, err := NewMapnikRenderer("", 256)
	if err != nil {
		t.Fatalf("Failed to create renderer: %v", err)
	}
	defer renderer.Close()

	// Set background
	renderer.SetBackgroundColor("#f8f4e8")

	// Render tile
	t.Log("Rendering tile...")
	img, err := renderer.RenderTile(tile, data)
	if err != nil {
		t.Fatalf("Failed to render tile: %v", err)
	}

	// Save to file
	outputPath := filepath.Join(outputDir, "test_render_basic.png")
	f, err := os.Create(outputPath)
	if err != nil {
		t.Fatalf("Failed to create output file: %v", err)
	}
	defer f.Close()

	if err := png.Encode(f, img); err != nil {
		t.Fatalf("Failed to encode PNG: %v", err)
	}

	t.Logf("Successfully rendered tile to %s", outputPath)
}

func TestMapnikRenderer_RenderToFile(t *testing.T) {
	requireIntegration(t)

	// Create output directory
	outputDir := "../../testdata/output"
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatalf("Failed to create output directory: %v", err)
	}

	// Create renderer
	renderer, err := NewMapnikRenderer("", 256)
	if err != nil {
		t.Fatalf("Failed to create renderer: %v", err)
	}
	defer renderer.Close()

	renderer.SetBackgroundColor("#f8f4e8")

	// Test tile
	tile := types.TileCoordinate{
		Zoom: 13,
		X:    4317,
		Y:    2692,
	}

	// Render directly to file
	outputPath := filepath.Join(outputDir, "test_render_direct.png")
	if err := renderer.RenderToFile(tile, outputPath); err != nil {
		t.Fatalf("Failed to render to file: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(outputPath); os.IsNotExist(err) {
		t.Fatalf("Output file was not created: %s", outputPath)
	}

	t.Logf("Successfully rendered tile to %s", outputPath)
}

// LoadStyle and LoadXML both call resetMapObject, which frees the map object and
// builds a new one. Anything configured on the old object is gone unless the
// renderer re-applies it — which is exactly how the buffer set once in
// NewMultiPassRenderer used to be lost before the first layer was ever drawn.
func TestBufferSizeSurvivesStyleReload(t *testing.T) {
	requireIntegration(t)

	r, err := NewMapnikRenderer("", 256)
	if err != nil {
		t.Fatalf("NewMapnikRenderer: %v", err)
	}
	defer r.Close() //nolint:errcheck // test cleanup

	r.SetBufferSize(128)
	if got := r.bufferSize; got != 128 {
		t.Fatalf("bufferSize after SetBufferSize = %d, want 128", got)
	}

	if err := r.LoadXML(`<?xml version="1.0" encoding="utf-8"?><Map srs="+proj=merc +a=6378137 +b=6378137 +lat_ts=0.0 +lon_0=0.0 +x_0=0.0 +y_0=0 +k=1.0 +units=m +nadgrids=@null +no_defs +over"></Map>`); err != nil {
		t.Fatalf("LoadXML: %v", err)
	}
	if got := r.bufferSize; got != 128 {
		t.Errorf("bufferSize after LoadXML = %d, want 128 — the reset dropped it", got)
	}
}
