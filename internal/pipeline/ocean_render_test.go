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

// emptyDataSource returns no OSM features at all.
//
// That is not a degenerate case here, it is the case under test: OSM maps no
// ocean, so an open-sea tile really does come back empty from Overpass. Using it
// also keeps this test off the network — the ocean has to come from the
// shapefile or from nowhere.
type emptyDataSource struct{}

func (emptyDataSource) FetchTileData(_ context.Context, coord types.TileCoordinate) (*types.TileData, error) {
	return &types.TileData{
		Coordinate: coord,
		Bounds:     types.TileToBounds(coord),
		Source:     "test-empty",
	}, nil
}

// requireWaterPolygons locates the downloaded water polygons, or skips.
//
// Override the location with WATERCOLORMAP_WATER_POLYGONS_DIR; the default is
// where `just fetch-water-polygons` puts them.
func requireWaterPolygons(t *testing.T) renderer.OceanConfig {
	t.Helper()

	dir := os.Getenv("WATERCOLORMAP_WATER_POLYGONS_DIR")
	if dir == "" {
		dir = filepath.Join("..", "..", "data", "water-polygons")
	}

	cfg := renderer.OceanConfig{
		FullPath:       filepath.Join(dir, "water-polygons-split-3857", "water_polygons.shp"),
		SimplifiedPath: filepath.Join(dir, "simplified-water-polygons-split-3857", "simplified_water_polygons.shp"),
	}

	if _, err := os.Stat(cfg.SimplifiedPath); err != nil {
		cfg.SimplifiedPath = ""
	}
	if _, err := os.Stat(cfg.FullPath); err != nil {
		cfg.FullPath = ""
	}
	if !cfg.Enabled() {
		t.Skipf("no water polygons in %s (run `just fetch-water-polygons`)", dir)
	}

	return cfg
}

// blueness counts how much of a tile reads as water rather than land.
//
// The composited tile is watercolor texture, not flat fill, so no pixel is
// exactly #0000FF. What separates the two is unambiguous all the same: the water
// texture is blue-dominant, the land texture is tan, i.e. red-dominant.
func blueness(img image.Image) float64 {
	b := img.Bounds()
	var blue, total float64
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, _, bb, _ := img.At(x, y).RGBA()
			total++
			if bb > r {
				blue++
			}
		}
	}
	if total == 0 {
		return 0
	}
	return blue / total
}

// TestOceanRendering is the end-to-end check for PLAN.md 4.10: with the water
// polygons configured and Overpass returning nothing, open sea must render as
// water and inland tiles must stay land.
//
// It needs Mapnik (as the other pipeline tests do) and the downloaded
// shapefiles, but no network.
func TestOceanRendering(t *testing.T) {
	ocean := requireWaterPolygons(t)

	stylesDir := filepath.Join("..", "..", "assets", "styles")
	texturesDir := filepath.Join("..", "..", "assets", "textures")

	render := func(t *testing.T, coords tile.Coords, cfg renderer.OceanConfig) image.Image {
		t.Helper()

		gen, err := NewGenerator(emptyDataSource{}, stylesDir, texturesDir, t.TempDir(), 256, 123, false, nil,
			GeneratorOptions{Ocean: cfg})
		require.NoError(t, err)

		ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
		defer cancel()

		path, _, err := gen.Generate(ctx, coords, true, "")
		require.NoError(t, err, "generating %s", coords.String())

		img, err := readPNG(path)
		require.NoError(t, err)
		return img
	}

	// z9_x266_y164 — roughly 7.0-7.7E, 53.8-54.2N: open North Sea, the tile
	// named in PLAN.md 4.10 as rendering tan today.
	northSea := tile.NewCoords(9, 266, 164)

	// z9_x269_y168 — Hannover, ~250 km inland. The regression guard: enabling
	// the ocean pass must not touch tiles that have no coast.
	hannover := tile.NewCoords(9, 269, 168)

	t.Run("OpenSeaIsWater", func(t *testing.T) {
		got := blueness(render(t, northSea, ocean))
		if got < 0.9 {
			t.Errorf("North Sea tile is only %.0f%% water; the ocean is still rendering as land", got*100)
		}
	})

	t.Run("InlandIsLand", func(t *testing.T) {
		got := blueness(render(t, hannover, ocean))
		if got > 0.05 {
			t.Errorf("inland tile is %.0f%% water; the ocean pass is bleeding onto land", got*100)
		}
	})

	t.Run("InlandUnchangedByOceanPass", func(t *testing.T) {
		withOcean := render(t, hannover, ocean)
		withoutOcean := render(t, hannover, renderer.OceanConfig{})

		require.Equal(t, withoutOcean.Bounds(), withOcean.Bounds())
		b := withOcean.Bounds()
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				if withOcean.At(x, y) != withoutOcean.At(x, y) {
					t.Fatalf("pixel (%d,%d) differs: ocean rendering must be a no-op away from the coast", x, y)
				}
			}
		}
	})
}

// TestCoastalTileHasBothWaterAndLand covers the second symptom in PLAN.md 4.10:
// coastal tiles used to come out inverted, with the sea painted as land.
func TestCoastalTileHasBothWaterAndLand(t *testing.T) {
	ocean := requireWaterPolygons(t)

	stylesDir := filepath.Join("..", "..", "assets", "styles")
	texturesDir := filepath.Join("..", "..", "assets", "textures")

	gen, err := NewGenerator(emptyDataSource{}, stylesDir, texturesDir, t.TempDir(), 256, 123, false, nil,
		GeneratorOptions{Ocean: ocean})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	// z9_x267_y165 — ~7.7-8.4E, 53.4-53.8N: the East Frisian coast, so the tile
	// straddles the shoreline.
	coords := tile.NewCoords(9, 267, 165)
	path, _, err := gen.Generate(ctx, coords, true, "")
	require.NoError(t, err)

	img, err := readPNG(path)
	require.NoError(t, err)

	got := blueness(img)
	if got < 0.15 || got > 0.85 {
		t.Errorf("coastal tile is %.0f%% water; expected a mix of sea and land", got*100)
	}
}
