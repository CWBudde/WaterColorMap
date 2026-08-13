package renderer

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cwbudde/watercolormap/internal/datasource"
	"github.com/cwbudde/watercolormap/internal/geojson"
	"github.com/cwbudde/watercolormap/internal/tile"
	"github.com/cwbudde/watercolormap/internal/types"
)

func assertPNGHasAnyNonTransparentPixel(t *testing.T, path string) {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("Failed to open PNG %s: %v", path, err)
	}
	defer f.Close()

	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("Failed to decode PNG %s: %v", path, err)
	}

	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			_, _, _, a := img.At(x, y).RGBA()
			if a != 0 {
				return
			}
		}
	}

	t.Fatalf("PNG is fully transparent: %s", path)
}

func assertPNGOnlyContainsColorWhenOpaque(t *testing.T, path string, expected color.NRGBA, tolerance uint8) {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("Failed to open PNG %s: %v", path, err)
	}
	defer f.Close()

	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("Failed to decode PNG %s: %v", path, err)
	}

	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := img.At(x, y).RGBA()
			if a == 0 {
				continue
			}

			// Convert 16-bit to 8-bit.
			assertOpaquePixelMatchesMask(t, path, x, y, uint8(r>>8), uint8(g>>8), uint8(bl>>8), expected, tolerance)
		}
	}
}

// assertOpaquePixelMatchesMask verifies a single opaque pixel against the expected mask color.
func assertOpaquePixelMatchesMask(
	t *testing.T,
	path string,
	x, y int,
	r8, g8, b8 uint8,
	expected color.NRGBA,
	tolerance uint8,
) {
	t.Helper()

	// Mapnik will anti-alias edges, which effectively yields a premultiplied-alpha
	// version of the mask color (scaled down), rather than always the full 0/255 values.
	// We still want strict separation: colors must be on the expected mask hue/ratios.

	// Channels that are zero in the expected mask color must stay near zero.
	if expected.R == 0 && r8 > tolerance {
		t.Fatalf("Unexpected red leakage at (%d,%d) in %s: got r=%d expected r≈0±%d", x, y, path, r8, tolerance)
	}
	if expected.G == 0 && g8 > tolerance {
		t.Fatalf("Unexpected green leakage at (%d,%d) in %s: got g=%d expected g≈0±%d", x, y, path, g8, tolerance)
	}
	if expected.B == 0 && b8 > tolerance {
		t.Fatalf("Unexpected blue leakage at (%d,%d) in %s: got b=%d expected b≈0±%d", x, y, path, b8, tolerance)
	}

	// If the expected color has any non-zero channel, verify the pixel channels
	// match a scaled version of expected (preserving ratios).
	scaleNum, scaleDen := pickScale(r8, g8, b8, expected)
	if scaleDen == 0 {
		// expected is fully black; nothing meaningful to assert beyond zero-channel checks
		return
	}

	wantR := uint8((uint16(expected.R) * uint16(scaleNum)) / uint16(scaleDen))
	wantG := uint8((uint16(expected.G) * uint16(scaleNum)) / uint16(scaleDen))
	wantB := uint8((uint16(expected.B) * uint16(scaleNum)) / uint16(scaleDen))

	if absDiff(r8, wantR) > tolerance || absDiff(g8, wantG) > tolerance || absDiff(b8, wantB) > tolerance {
		t.Fatalf(
			"Unexpected opaque color at (%d,%d) in %s: got rgb(%d,%d,%d) expected scaled rgb(%d,%d,%d) (base rgb(%d,%d,%d))±%d",
			x, y, path,
			r8, g8, b8,
			wantR, wantG, wantB,
			expected.R, expected.G, expected.B,
			tolerance,
		)
	}
}

func absDiff(a, b uint8) uint8 {
	if a >= b {
		return a - b
	}
	return b - a
}

func pickScale(r8, g8, b8 uint8, expected color.NRGBA) (scaleNum uint8, scaleDen uint8) {
	// Choose a non-zero expected channel to derive a scale factor.
	// Prefer the channel with the highest expected value for stability.
	if expected.R >= expected.G && expected.R >= expected.B && expected.R != 0 {
		return r8, expected.R
	}
	if expected.G >= expected.R && expected.G >= expected.B && expected.G != 0 {
		return g8, expected.G
	}
	if expected.B != 0 {
		return b8, expected.B
	}
	return 0, 0
}

func loadPNG(t *testing.T, path string) image.Image {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("Failed to open PNG %s: %v", path, err)
	}
	defer f.Close()

	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("Failed to decode PNG %s: %v", path, err)
	}
	return img
}

func checkEdgeAlignment(t *testing.T, rendered map[tile.Coords]map[geojson.LayerType]string, tiles []tile.Coords) {
	t.Helper()

	layers := []geojson.LayerType{geojson.LayerLand, geojson.LayerWater, geojson.LayerParks, geojson.LayerUrban, geojson.LayerRoads}

	for _, coords := range tiles {
		checkEastEdgeAlignment(t, rendered, coords, layers)
		checkSouthEdgeAlignment(t, rendered, coords, layers)
	}
}

// checkEastEdgeAlignment compares a tile against its eastern neighbour, layer by layer.
func checkEastEdgeAlignment(
	t *testing.T,
	rendered map[tile.Coords]map[geojson.LayerType]string,
	coords tile.Coords,
	layers []geojson.LayerType,
) {
	t.Helper()

	east := tile.NewCoords(coords.Z, coords.X+1, coords.Y)
	if _, ok := rendered[east]; !ok {
		return
	}

	for _, layer := range layers {
		leftPath := rendered[coords][layer]
		rightPath := rendered[east][layer]

		if leftPath == "" || rightPath == "" {
			continue // Skip if either tile doesn't have this layer
		}

		t.Run(fmt.Sprintf("%s_to_%s_%s_vertical", coords.String(), east.String(), layer), func(t *testing.T) {
			compareVerticalSeam(t, leftPath, rightPath)
		})
	}
}

// compareVerticalSeam compares the right column of the left tile with the left
// column of the right tile.
func compareVerticalSeam(t *testing.T, leftPath, rightPath string) {
	t.Helper()

	leftImg := loadPNG(t, leftPath)
	rightImg := loadPNG(t, rightPath)

	bounds := leftImg.Bounds()
	rightEdgeX := bounds.Max.X - 1
	leftEdgeX := bounds.Min.X

	// Sample every 4th pixel to reduce test time while still catching major misalignments
	for y := bounds.Min.Y; y < bounds.Max.Y; y += 4 {
		lr, lg, lb, la := leftImg.At(rightEdgeX, y).RGBA()
		rr, rg, rb, ra := rightImg.At(leftEdgeX, y).RGBA()

		// Convert to 8-bit
		lr8, lg8, lb8, la8 := uint8(lr>>8), uint8(lg>>8), uint8(lb>>8), uint8(la>>8)
		rr8, rg8, rb8, ra8 := uint8(rr>>8), uint8(rg>>8), uint8(rb>>8), uint8(ra>>8)

		// For semi-transparent pixels (alpha < 128), differences are expected due to
		// anti-aliasing being applied differently from different tile perspectives.
		// This is not a visual problem as browsers alpha-blend when displaying tiles.
		if la8 < 128 || ra8 < 128 {
			continue
		}

		// For opaque pixels, check they match closely
		// Allow larger tolerance for anti-aliased edges (up to 60 in alpha/color)
		// as Mapnik applies different anti-aliasing from different tile perspectives
		tolerance := uint8(60)
		if absDiff(lr8, rr8) > tolerance || absDiff(lg8, rg8) > tolerance ||
			absDiff(lb8, rb8) > tolerance || absDiff(la8, ra8) > tolerance {
			t.Errorf("Edge mismatch at y=%d: left rgba(%d,%d,%d,%d) != right rgba(%d,%d,%d,%d)",
				y, lr8, lg8, lb8, la8, rr8, rg8, rb8, ra8)
			return // Report first mismatch only
		}
	}
}

// checkSouthEdgeAlignment compares a tile against its southern neighbour, layer by layer.
func checkSouthEdgeAlignment(
	t *testing.T,
	rendered map[tile.Coords]map[geojson.LayerType]string,
	coords tile.Coords,
	layers []geojson.LayerType,
) {
	t.Helper()

	south := tile.NewCoords(coords.Z, coords.X, coords.Y+1)
	if _, ok := rendered[south]; !ok {
		return
	}

	for _, layer := range layers {
		topPath := rendered[coords][layer]
		bottomPath := rendered[south][layer]

		if topPath == "" || bottomPath == "" {
			continue // Skip if either tile doesn't have this layer
		}

		t.Run(fmt.Sprintf("%s_to_%s_%s_horizontal", coords.String(), south.String(), layer), func(t *testing.T) {
			compareHorizontalSeam(t, topPath, bottomPath)
		})
	}
}

// compareHorizontalSeam compares the bottom row of the top tile with the top row
// of the bottom tile.
func compareHorizontalSeam(t *testing.T, topPath, bottomPath string) {
	t.Helper()

	topImg := loadPNG(t, topPath)
	bottomImg := loadPNG(t, bottomPath)

	bounds := topImg.Bounds()
	bottomEdgeY := bounds.Max.Y - 1
	topEdgeY := bounds.Min.Y

	// Sample every 4th pixel to reduce test time while still catching major misalignments
	for x := bounds.Min.X; x < bounds.Max.X; x += 4 {
		tr, tg, tb, ta := topImg.At(x, bottomEdgeY).RGBA()
		br, bg, bb, ba := bottomImg.At(x, topEdgeY).RGBA()

		// Convert to 8-bit
		tr8, tg8, tb8, ta8 := uint8(tr>>8), uint8(tg>>8), uint8(tb>>8), uint8(ta>>8)
		br8, bg8, bb8, ba8 := uint8(br>>8), uint8(bg>>8), uint8(bb>>8), uint8(ba>>8)

		// For semi-transparent pixels (alpha < 128), differences are expected due to
		// anti-aliasing being applied differently from different tile perspectives.
		// This is not a visual problem as browsers alpha-blend when displaying tiles.
		if ta8 < 128 || ba8 < 128 {
			continue
		}

		// For opaque pixels, check they match closely
		// Allow larger tolerance for anti-aliased edges (up to 60 in alpha/color)
		// as Mapnik applies different anti-aliasing from different tile perspectives
		tolerance := uint8(60)
		if absDiff(tr8, br8) > tolerance || absDiff(tg8, bg8) > tolerance ||
			absDiff(tb8, bb8) > tolerance || absDiff(ta8, ba8) > tolerance {
			t.Errorf("Edge mismatch at x=%d: top rgba(%d,%d,%d,%d) != bottom rgba(%d,%d,%d,%d)",
				x, tr8, tg8, tb8, ta8, br8, bg8, bb8, ba8)
			return // Report first mismatch only
		}
	}
}

func expectedMaskColor(layer geojson.LayerType) (color.NRGBA, bool) {
	switch layer {
	case geojson.LayerWater:
		return color.NRGBA{R: 0, G: 0, B: 255, A: 255}, true
	case geojson.LayerParks:
		return color.NRGBA{R: 0, G: 255, B: 0, A: 255}, true
	case geojson.LayerUrban:
		return color.NRGBA{R: 192, G: 128, B: 192, A: 255}, true
	case geojson.LayerRoads:
		return color.NRGBA{R: 255, G: 255, B: 0, A: 255}, true
	default:
		return color.NRGBA{}, false
	}
}

func TestMultiPassRendererCreation(t *testing.T) {
	requireIntegration(t)

	stylesDir := "../../assets/styles"
	outputDir := "../../testdata/output/multipass"

	renderer, err := NewMultiPassRenderer(stylesDir, outputDir, 256, 0)
	if err != nil {
		t.Fatalf("Failed to create renderer: %v", err)
	}
	defer renderer.Close()

	t.Log("MultiPassRenderer created successfully")

	// Verify directories were created
	if _, err := os.Stat(renderer.outputDir); err != nil {
		t.Errorf("Output directory not created: %v", err)
	}
	if _, err := os.Stat(renderer.tempDir); err != nil {
		t.Errorf("Temp directory not created: %v", err)
	}
}

func TestRenderTileWithRealData(t *testing.T) {
	requireIntegration(t)

	// Create renderer
	stylesDir := "../../assets/styles"
	outputDir := "../../testdata/output/multipass"
	renderer, err := NewMultiPassRenderer(stylesDir, outputDir, 256, 0)
	if err != nil {
		t.Fatalf("Failed to create renderer: %v", err)
	}
	defer renderer.Close()

	// Fetch real OSM data for a test tile
	coords := tile.NewCoords(13, 4317, 2692) // Hanover test tile
	t.Logf("Fetching OSM data for tile %s", coords.String())

	ds := datasource.NewOverpassDataSource("")
	defer ds.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Fetch tile data
	tileData, err := ds.FetchTileData(ctx, types.TileCoordinate{
		Zoom: int(coords.Z),
		X:    int(coords.X),
		Y:    int(coords.Y),
	})
	if err != nil {
		t.Fatalf("Failed to fetch tile data: %v", err)
	}

	t.Logf("Fetched %d features: %v", tileData.Features.Count(), tileData.Features.FeatureCounts())

	// Render all layers
	t.Logf("Rendering all layers for tile %s", coords.String())
	result, err := renderer.RenderTile(coords, tileData)
	if err != nil {
		t.Fatalf("Failed to render tile: %v", err)
	}

	// Verify results
	t.Logf("Render results:")
	for layer, layerResult := range result.Layers {
		switch {
		case layerResult.Error != nil:
			t.Logf("  %s: ERROR - %v", layer, layerResult.Error)
		case layerResult.OutputPath == "":
			t.Logf("  %s: SKIPPED (no features)", layer)
		default:
			t.Logf("  %s: SUCCESS - %s", layer, layerResult.OutputPath)

			// Verify file exists
			if _, err := os.Stat(layerResult.OutputPath); err != nil {
				t.Errorf("Layer output file not found: %s", layerResult.OutputPath)
			}

			// Non-land layers should not be fully transparent when we rendered them
			if layer != geojson.LayerLand {
				assertPNGHasAnyNonTransparentPixel(t, layerResult.OutputPath)
			}
		}
	}

	// Verify at least land and water layers were rendered
	landResult := result.Layers[geojson.LayerLand]
	if landResult == nil || landResult.Error != nil {
		t.Errorf("Land layer should always render successfully")
	}

	// Check if water layer has features
	waterResult := result.Layers[geojson.LayerWater]
	if waterResult != nil && waterResult.Error != nil {
		t.Errorf("Water layer rendering failed: %v", waterResult.Error)
	}

	t.Logf("Multi-pass rendering completed successfully")
}

func TestRenderAdjacentTilesWithRealData(t *testing.T) {
	requireIntegration(t)

	stylesDir := "../../assets/styles"
	outputDir := "../../testdata/output/multipass"
	renderer, err := NewMultiPassRenderer(stylesDir, outputDir, 256, 0)
	if err != nil {
		t.Fatalf("Failed to create renderer: %v", err)
	}
	defer renderer.Close()

	ds := datasource.NewOverpassDataSource("")
	defer ds.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Render a small 3x3 grid around the Hannover reference tile so we can
	// validate basic edge consistency across neighbors.
	center := tile.NewCoords(13, 4317, 2692)
	var tiles []tile.Coords
	for dy := int32(-1); dy <= 1; dy++ {
		for dx := int32(-1); dx <= 1; dx++ {
			tiles = append(tiles, tile.NewCoords(center.Z, uint32(int32(center.X)+dx), uint32(int32(center.Y)+dy)))
		}
	}

	// Store rendered paths for edge comparison
	rendered := make(map[tile.Coords]map[geojson.LayerType]string)

	for _, coords := range tiles {
		coords := coords
		t.Run(coords.String(), func(t *testing.T) {
			rendered[coords] = renderAndValidateTile(t, ctx, ds, renderer, coords)
		})
	}

	// Verify edge alignment between adjacent tiles
	t.Run("EdgeAlignment", func(t *testing.T) {
		checkEdgeAlignment(t, rendered, tiles)
	})
}

// renderAndValidateTile fetches and renders one tile, validates its layers and
// returns the rendered layer paths.
func renderAndValidateTile(
	t *testing.T,
	ctx context.Context,
	ds *datasource.OverpassDataSource,
	renderer *MultiPassRenderer,
	coords tile.Coords,
) map[geojson.LayerType]string {
	t.Helper()

	tileData, err := ds.FetchTileData(ctx, types.TileCoordinate{Zoom: int(coords.Z), X: int(coords.X), Y: int(coords.Y)})
	if err != nil {
		t.Fatalf("Failed to fetch tile data: %v", err)
	}

	result, err := renderer.RenderTile(coords, tileData)
	if err != nil {
		t.Fatalf("Failed to render tile: %v", err)
	}

	// Land should always render
	land := result.Layers[geojson.LayerLand]
	if land == nil || land.Error != nil || land.OutputPath == "" {
		t.Fatalf("Land layer did not render")
	}

	paths := make(map[geojson.LayerType]string)
	for layer, lr := range result.Layers {
		if lr == nil {
			continue
		}
		if lr.Error != nil {
			t.Fatalf("Layer %s render error: %v", layer, lr.Error)
		}
		paths[layer] = lr.OutputPath
		assertRenderedLayer(t, layer, lr.OutputPath)
	}

	return paths
}

// assertRenderedLayer validates the rendered output of a single non-land layer.
func assertRenderedLayer(t *testing.T, layer geojson.LayerType, outputPath string) {
	t.Helper()

	if layer == geojson.LayerLand || outputPath == "" {
		return
	}
	assertPNGHasAnyNonTransparentPixel(t, outputPath)
	if expected, ok := expectedMaskColor(layer); ok {
		assertPNGOnlyContainsColorWhenOpaque(t, outputPath, expected, 6)
	}
}

func TestLayerPathHelpers(t *testing.T) {
	outputDir := "/tmp/test_tiles"
	coords := tile.NewCoords(13, 4297, 2754)

	// Test GetLayerPath
	path := GetLayerPath(outputDir, coords, geojson.LayerWater)
	expected := filepath.Join(outputDir, "z13_x4297_y2754_water.png")
	if path != expected {
		t.Errorf("GetLayerPath() = %s, want %s", path, expected)
	}

	// Test LayerExists (should return false for non-existent file)
	if LayerExists(outputDir, coords, geojson.LayerWater) {
		t.Error("LayerExists() should return false for non-existent file")
	}
}

func TestRenderLandLayerOnly(t *testing.T) {
	requireIntegration(t)

	// Test rendering just the land layer (no features required)
	stylesDir := "../../assets/styles"
	outputDir := "../../testdata/output/land_only"

	renderer, err := NewMultiPassRenderer(stylesDir, outputDir, 256, 0)
	if err != nil {
		t.Fatalf("Failed to create renderer: %v", err)
	}
	defer renderer.Close()

	coords := tile.NewCoords(13, 4317, 2692)
	bounds := coords.BoundsMercator()

	// Render land layer
	stylePath := filepath.Join(stylesDir, "layers", "land.xml")
	result := renderer.renderLandLayer(coords, stylePath, bounds)

	if result.Error != nil {
		t.Fatalf("Failed to render land layer: %v", result.Error)
	}

	if result.OutputPath == "" {
		t.Error("Expected output path for land layer")
	}

	// Verify file exists
	if _, err := os.Stat(result.OutputPath); err != nil {
		t.Errorf("Land layer output not found: %s", result.OutputPath)
	}

	t.Logf("Land layer rendered successfully: %s", result.OutputPath)
}
