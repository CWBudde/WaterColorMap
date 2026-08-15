package pipeline

import (
	"context"
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cwbudde/watercolormap/internal/renderer"
	"github.com/cwbudde/watercolormap/internal/tile"
	"github.com/cwbudde/watercolormap/internal/types"
)

// forbiddenDataSource fails the test if anything asks it for data.
//
// This is the regression that separates "z0-5 renders from Natural Earth" from
// "z0-5 renders from Natural Earth *and* still hammers Overpass". A single z2
// query asks a regional instance for a quarter of the planet, so the fetch not
// happening is the point of the whole tier, not a side benefit.
type forbiddenDataSource struct{ t *testing.T }

func (d forbiddenDataSource) FetchTileData(_ context.Context, coord types.TileCoordinate) (*types.TileData, error) {
	d.t.Errorf("the datasource was queried for z%d_x%d_y%d; Natural Earth zooms must not touch Overpass",
		coord.Zoom, coord.X, coord.Y)
	return &types.TileData{Coordinate: coord, Bounds: types.TileToBounds(coord), Source: "test-forbidden"}, nil
}

// requireNaturalEarth locates the downloaded shapefiles, or skips.
//
// Override the location with WATERCOLORMAP_NATURAL_EARTH_DIR; the default is
// where `just fetch-natural-earth` puts them.
func requireNaturalEarth(t *testing.T) renderer.NaturalEarthConfig {
	t.Helper()

	dir := os.Getenv("WATERCOLORMAP_NATURAL_EARTH_DIR")
	if dir == "" {
		dir = filepath.Join("..", "..", "data", "natural-earth")
	}

	cfg := renderer.NaturalEarthConfig{Dir: dir}
	if err := cfg.Validate(); err != nil {
		t.Skipf("no Natural Earth data in %s (run `just fetch-natural-earth`): %v", dir, err)
	}
	return cfg
}

// TestNaturalEarthRendering is the end-to-end check for PLAN.md 5.3: below z6
// the tile is built entirely from Natural Earth shapefiles, with no Overpass
// request at all.
//
// It needs Mapnik (as the other pipeline tests do) and the downloaded
// shapefiles, but no network — which is exactly the property under test.
func TestNaturalEarthRendering(t *testing.T) {
	ne := requireNaturalEarth(t)

	stylesDir := filepath.Join("..", "..", "assets", "styles")
	texturesDir := filepath.Join("..", "..", "assets", "textures")

	render := func(t *testing.T, coords tile.Coords) image.Image {
		t.Helper()

		gen, err := NewGenerator(forbiddenDataSource{t}, stylesDir, texturesDir, t.TempDir(), 256, 123, false, nil,
			GeneratorOptions{NaturalEarth: ne})
		require.NoError(t, err)

		ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
		defer cancel()

		path, _, err := gen.Generate(ctx, coords, true, "")
		require.NoError(t, err, "generating %s", coords.String())

		img, err := readPNG(path)
		require.NoError(t, err)
		return img
	}

	// z3_x0_y3 — 180W-135W, 0-41N: open north Pacific, no land at all.
	pacific := tile.NewCoords(3, 0, 3)

	// z2_x2_y1 — 0-90E, 0-66N: Asia, land-dominant.
	asia := tile.NewCoords(2, 2, 1)

	// z5_x16_y10 — central Europe at the Natural Earth ceiling, where the 50m
	// datasets are in use and the coastline has real structure.
	europe := tile.NewCoords(5, 16, 10)

	t.Run("OpenOceanIsWater", func(t *testing.T) {
		got := blueness(render(t, pacific))
		if got < 0.99 {
			t.Errorf("open-Pacific %s is only %.0f%% water; the Natural Earth ocean is not rendering",
				pacific, got*100)
		}
	})

	t.Run("ContinentIsLand", func(t *testing.T) {
		got := blueness(render(t, asia))
		if got > 0.4 {
			t.Errorf("continental %s is %.0f%% water; the ocean polygons are bleeding onto land", asia, got*100)
		}
	})

	t.Run("CoastlineHasBothAtTheCeiling", func(t *testing.T) {
		got := blueness(render(t, europe))
		if got < 0.05 || got > 0.95 {
			t.Errorf("coastal %s is %.0f%% water; expected a mix of land and sea", europe, got*100)
		}
	})

	// The whole-Earth sanity check, and the reason it is worth having: it is the
	// one assertion here that cannot pass by accident. Roughly 71% of the
	// planet is ocean; Mercator's area distortion inflates the polar ice and
	// tundra, so the rendered figure sits a little under that. A tile that came
	// out all land or all sea — the two ways this feature fails — lands nowhere
	// near this band.
	t.Run("WorldTileIsMostlyOcean", func(t *testing.T) {
		got := blueness(render(t, tile.NewCoords(0, 0, 0)))
		if got < 0.5 || got > 0.8 {
			t.Errorf("the z0 world tile is %.0f%% water; Earth is ~71%% ocean, so this is not a world map", got*100)
		}
	})
}

// TestNaturalEarthOnlyLowZoomLayersRender pins which layers exist below z6.
//
// The absence of roads, buildings and the rest *is* the low-zoom style — there
// is no separate "generalized" style, only a source that carries three layers.
// If a layer ever starts resolving here, a world tile would try to draw a road
// network from a dataset that does not have one.
func TestNaturalEarthOnlyLowZoomLayersRender(t *testing.T) {
	ne := requireNaturalEarth(t)

	gen, err := NewGenerator(
		forbiddenDataSource{t},
		filepath.Join("..", "..", "assets", "styles"),
		filepath.Join("..", "..", "assets", "textures"),
		t.TempDir(), 256, 123, true, nil,
		GeneratorOptions{NaturalEarth: ne},
	)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	_, layerDir, err := gen.Generate(ctx, tile.NewCoords(3, 4, 2), true, "")
	require.NoError(t, err)
	require.NotEmpty(t, layerDir, "keepLayers should have returned the layer directory")

	entries, err := os.ReadDir(layerDir)
	require.NoError(t, err)

	present := make(map[string]bool, len(entries))
	for _, e := range entries {
		present[e.Name()] = true
	}

	// land is the background fill and paints at every zoom; ocean is folded
	// into water downstream but is rendered as its own pass.
	for _, want := range []string{"z3_x4_y2_land.png", "z3_x4_y2_ocean.png"} {
		if !present[want] {
			t.Errorf("expected %s among the rendered layers, got %v", want, keys(present))
		}
	}

	for _, unwanted := range []string{
		"z3_x4_y2_roads.png", "z3_x4_y2_highways.png", "z3_x4_y2_railroads.png",
		"z3_x4_y2_buildings.png", "z3_x4_y2_civic.png", "z3_x4_y2_urban.png",
		"z3_x4_y2_parks.png",
	} {
		if present[unwanted] {
			t.Errorf("%s was rendered; Natural Earth carries no such layer below z6", unwanted)
		}
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
