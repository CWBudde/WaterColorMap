package pipeline

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cwbudde/watercolormap/internal/datasource"
	"github.com/cwbudde/watercolormap/internal/tile"
	"github.com/cwbudde/watercolormap/internal/types"
	"github.com/paulmach/orb"
	"github.com/stretchr/testify/require"
)

// Test function with three subtests
func TestPipelineStages(t *testing.T) {
	t.Run("Synthetic", func(t *testing.T) {
		ds := &syntheticDataSource{}
		coords := tile.NewCoords(13, 0, 0)
		runPipelineStagesTest(t, "synthetic", ds, coords)
	})

	t.Run("Hannover_z13", func(t *testing.T) {
		requireIntegration(t)
		// Use real Overpass data source (requires network)
		ds := newTestOverpassDataSource(t)
		coords := tile.NewCoords(13, 4317, 2692)
		runPipelineStagesTest(t, "z13_x4317_y2692", ds, coords)
	})

	t.Run("Hannover_z15", func(t *testing.T) {
		requireIntegration(t)
		ds := newTestOverpassDataSource(t)
		coords := tile.NewCoords(15, 17270, 10770)
		runPipelineStagesTest(t, "z15_x17270_y10770", ds, coords)
	})
}

// Shared test runner
func runPipelineStagesTest(t *testing.T, caseName string, ds DataSource, coords tile.Coords) {
	goldenDir := filepath.Join("..", "..", "testdata", "golden", "pipeline-stages", caseName)
	debugDir := filepath.Join("..", "..", "testdata", "output", "pipeline-stages", caseName)

	require.NoError(t, os.MkdirAll(goldenDir, 0o755))
	require.NoError(t, os.MkdirAll(debugDir, 0o755))

	update := os.Getenv("UPDATE_GOLDEN") == "1"

	// Load textures, create generator
	stylesDir := filepath.Join("..", "..", "assets", "styles")
	texturesDir := filepath.Join("..", "..", "assets", "textures")

	gen, err := NewGenerator(ds, stylesDir, texturesDir, debugDir, 256, 123, false, nil, GeneratorOptions{})
	require.NoError(t, err)

	// Capture stages
	debugCtx := &DebugContext{}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	_, _, err = gen.Generate(ctx, coords, true, "", debugCtx)
	require.NoError(t, err)

	stages := debugCtx.SortedStages()
	require.NotEmpty(t, stages, "no stages captured")

	// CRITICAL ASSERTIONS
	assertRiversIncluded(t, stages)
	assertBuildingsIncluded(t, stages, caseName)
	assertMetatileCropped(t, stages)

	// Write debug outputs
	for _, stage := range stages {
		writePNG(t, filepath.Join(debugDir, stage.Name+".png"), stage.Image)
	}

	// Update or compare goldens
	for _, stage := range stages {
		goldenPath := filepath.Join(goldenDir, stage.Name+".png")

		if update {
			writePNG(t, goldenPath, stage.Image)
		} else {
			require.FileExists(t, goldenPath, "golden file missing: %s", stage.Name)
			assertImagesEqual(t, goldenPath, stage.Image, stage.Name)
		}
	}
}

// Critical assertion: Rivers must be captured
func assertRiversIncluded(t *testing.T, stages []StageCapture) {
	found := false
	for _, stage := range stages {
		if stage.Name == "02_rivers_alpha" {
			found = true
			break
		}
	}
	require.True(t, found, "CRITICAL: rivers_alpha stage not captured - production includes rivers but test doesn't")
}

// Critical assertion: Buildings must be captured (integration tests only)
func assertBuildingsIncluded(t *testing.T, stages []StageCapture, caseName string) {
	if caseName == "synthetic" {
		return // Skip for synthetic test
	}
	found := false
	for _, stage := range stages {
		if stage.Name == "00_rendered_buildings" {
			found = true
			break
		}
	}
	require.True(t, found, "CRITICAL: buildings layer missing from integration test")
}

// Critical assertion: Metatile crop must occur
func assertMetatileCropped(t *testing.T, stages []StageCapture) {
	var metatile, final image.Image
	for _, stage := range stages {
		if stage.Name == "20_combined_metatile" {
			metatile = stage.Image
		}
		if stage.Name == "21_combined_final" {
			final = stage.Image
		}
	}

	if metatile != nil && final != nil {
		require.Greater(t, metatile.Bounds().Dx(), final.Bounds().Dx(),
			"metatile should be larger than final (crop didn't occur)")
	}
}

// Synthetic data source for deterministic testing
type syntheticDataSource struct{}

func (s *syntheticDataSource) FetchTileData(ctx context.Context, coord types.TileCoordinate) (*types.TileData, error) {
	bounds := types.TileToBounds(coord)

	// Create normalized coordinates (0-1 range) and scale to tile bounds
	scale := func(x, y float64) orb.Point {
		return orb.Point{
			bounds.MinLon + x*(bounds.MaxLon-bounds.MinLon),
			bounds.MinLat + y*(bounds.MaxLat-bounds.MinLat),
		}
	}

	// Create synthetic features for all layers
	features := types.FeatureCollection{
		Water: []types.Feature{
			{
				ID:   "synthetic/water/1",
				Type: types.FeatureTypeWater,
				Geometry: orb.Polygon{
					{scale(0.2, 0.6), scale(0.8, 0.6), scale(0.8, 1.0), scale(0.2, 1.0), scale(0.2, 0.6)},
				},
				Properties: map[string]interface{}{"natural": "water"},
			},
		},
		Rivers: []types.Feature{
			{
				ID:   "synthetic/river/1",
				Type: types.FeatureTypeWater,
				Geometry: orb.LineString{
					scale(0.1, 0.1), scale(0.9, 0.9),
				},
				Properties: map[string]interface{}{"waterway": "river", "name": "Test River"},
			},
		},
		Roads: []types.Feature{
			{
				ID:   "synthetic/road/1",
				Type: types.FeatureTypeRoad,
				Geometry: orb.LineString{
					scale(0.0, 0.3), scale(1.0, 0.3),
				},
				Properties: map[string]interface{}{"highway": "secondary"},
			},
			{
				ID:   "synthetic/road/2",
				Type: types.FeatureTypeRoad,
				Geometry: orb.LineString{
					scale(0.0, 0.5), scale(1.0, 0.5),
				},
				Properties: map[string]interface{}{"highway": "residential"},
			},
			{
				ID:   "synthetic/road/3",
				Type: types.FeatureTypeRoad,
				Geometry: orb.LineString{
					scale(0.3, 0.0), scale(0.3, 1.0),
				},
				Properties: map[string]interface{}{"highway": "tertiary"},
			},
			{
				ID:   "synthetic/highway/1",
				Type: types.FeatureTypeRoad,
				Geometry: orb.LineString{
					scale(0.5, 0.0), scale(0.5, 1.0),
				},
				Properties: map[string]interface{}{"highway": "motorway"},
			},
			{
				ID:   "synthetic/highway/2",
				Type: types.FeatureTypeRoad,
				Geometry: orb.LineString{
					scale(0.0, 0.7), scale(1.0, 0.7),
				},
				Properties: map[string]interface{}{"highway": "trunk"},
			},
		},
		Parks: []types.Feature{
			{
				ID:   "synthetic/park/1",
				Type: types.FeatureTypePark,
				Geometry: orb.Polygon{
					{scale(0.0, 0.0), scale(0.4, 0.0), scale(0.4, 0.4), scale(0.0, 0.4), scale(0.0, 0.0)},
				},
				Properties: map[string]interface{}{"leisure": "park"},
			},
		},
		Buildings: []types.Feature{
			{
				ID:   "synthetic/building/1",
				Type: types.FeatureTypeBuilding,
				Geometry: orb.Polygon{
					{scale(0.3, 0.1), scale(0.4, 0.1), scale(0.4, 0.2), scale(0.3, 0.2), scale(0.3, 0.1)},
				},
				Properties: map[string]interface{}{"building": "yes"},
			},
		},
	}

	return &types.TileData{
		Coordinate: coord,
		Bounds:     bounds,
		Features:   features,
		Source:     "synthetic",
		FetchedAt:  time.Now(),
	}, nil
}

// Helper: check if integration tests are enabled
func requireIntegration(t *testing.T) {
	if os.Getenv("WATERCOLORMAP_INTEGRATION") != "1" {
		t.Skip("Skipping integration test (set WATERCOLORMAP_INTEGRATION=1 to run)")
	}
}

// Helper: create test Overpass data source
func newTestOverpassDataSource(t *testing.T) DataSource {
	return datasource.NewOverpassDataSource("")
}

// Helper: write PNG file
func writePNG(t *testing.T, path string, img image.Image) {
	if img == nil {
		t.Logf("Skipping nil image for %s", path)
		return
	}

	dir := filepath.Dir(path)
	require.NoError(t, os.MkdirAll(dir, 0o755))

	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()

	require.NoError(t, png.Encode(f, img))
}

// Helper: assert images are equal
func assertImagesEqual(t *testing.T, goldenPath string, actual image.Image, stageName string) {
	f, err := os.Open(goldenPath)
	require.NoError(t, err)
	defer f.Close()

	expected, err := png.Decode(f)
	require.NoError(t, err)

	// Check bounds
	require.Equal(t, expected.Bounds(), actual.Bounds(), "stage %s: bounds mismatch", stageName)

	// Pixel-by-pixel comparison
	bounds := expected.Bounds()
	var diffCount int
	const maxDiffToReport = 10

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			expectedColor := expected.At(x, y)
			actualColor := actual.At(x, y)

			if !colorsEqual(expectedColor, actualColor) {
				diffCount++
				if diffCount <= maxDiffToReport {
					er, eg, eb, ea := expectedColor.RGBA()
					ar, ag, ab, aa := actualColor.RGBA()
					t.Logf("stage %s: pixel diff at (%d,%d): expected RGBA(%d,%d,%d,%d) got RGBA(%d,%d,%d,%d)",
						stageName, x, y, er>>8, eg>>8, eb>>8, ea>>8, ar>>8, ag>>8, ab>>8, aa>>8)
				}
			}
		}
	}

	if diffCount > 0 {
		t.Fatalf("stage %s: %d pixels differ from golden (showing first %d)", stageName, diffCount, maxDiffToReport)
	}
}

// Helper: compare colors with tolerance
func colorsEqual(c1, c2 color.Color) bool {
	r1, g1, b1, a1 := c1.RGBA()
	r2, g2, b2, a2 := c2.RGBA()

	// Convert to 8-bit for comparison
	const tolerance = 1 // Allow 1/255 difference for PNG encoding variations

	return abs(int(r1>>8)-int(r2>>8)) <= tolerance &&
		abs(int(g1>>8)-int(g2>>8)) <= tolerance &&
		abs(int(b1>>8)-int(b2>>8)) <= tolerance &&
		abs(int(a1>>8)-int(a2>>8)) <= tolerance
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
