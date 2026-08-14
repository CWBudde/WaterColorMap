package pipeline

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/paulmach/orb"
	"github.com/stretchr/testify/require"

	"github.com/cwbudde/watercolormap/internal/tile"
	"github.com/cwbudde/watercolormap/internal/types"
)

// Tolerances for the composited-tile seam check.
//
// The two pixels that touch across a tile border are *neighbours* in world
// space, not copies of each other, so they are never expected to be equal. And
// unlike the raw Mapnik masks compared by the seam test in internal/renderer
// (flat colour, so a fixed per-channel tolerance of 60 is meaningful there), the
// composited tile is paper texture plus watercolor noise: adjacent pixels
// legitimately differ a lot, everywhere. Measured on this fixture, the step
// between two neighbouring pixels *inside* one tile has mean ~10 and reaches 69
// in the water texture. A strict per-pixel tolerance was tried first and failed
// on texture alone — it was measuring grain, not seams.
//
// So the border is judged against a control: the step across the border must
// look like the steps just inside the two tiles it joins. A real seam — a phase
// jump in the noise, an edge-darkening halo, a mis-anchored texture — shifts the
// whole distribution, which the mean and p95 margins catch, while a single
// broken pixel is caught by the absolute cap.
const (
	// seamMaxPixelDelta is an absolute backstop, set above the worst in-tile
	// step measured on this fixture (69) with headroom for style changes.
	seamMaxPixelDelta = 96

	// seamMeanMargin bounds how much larger the average cross-border step may be
	// than the average in-tile step. This is the sensitive check: on this fixture
	// the real margin is ≤ +0.5, while injecting a one-pixel dark line of 10/255
	// along the border — a plausible edge-darkening halo, and barely visible by
	// eye — pushes it to +2.8. 1.5 leaves ~3x headroom and still catches that.
	seamMeanMargin = 1.5

	// seamP95Margin does the same for the 95th percentile, catching an artefact
	// that hits only part of the border rather than shifting the whole
	// distribution. Measured margin on this fixture: ≤ +3.
	seamP95Margin = 6
)

// TestCompositedTileSeams renders a 2x2 block of neighbouring tiles through the
// full generator and checks that the composited output is continuous across the
// two internal borders.
//
// TestPipelineStages/Synthetic already proves the pipeline runs, and the seam
// test in internal/renderer already proves the raw Mapnik layers line up — but
// that is precisely the stage *before* blur, noise, threshold and edge
// darkening, which are the stages that can actually manufacture a seam. This
// test is the only one that looks at what the browser will display.
//
// It is intentionally not integration-gated: it needs no network, and CI
// installs Mapnik.
func TestCompositedTileSeams(t *testing.T) {
	// z13 over Hannover, the same neighbourhood the other tests use. The exact
	// location is irrelevant — the synthetic data source ignores real OSM — but
	// keeping it consistent makes debug output comparable.
	const zoom = 13
	const originX, originY = 4317, 2692

	// The 2x2 block, in tile coordinates:
	//   (x,   y  ) (x+1, y  )
	//   (x,   y+1) (x+1, y+1)
	coords := [2][2]tile.Coords{
		{tile.NewCoords(zoom, originX, originY), tile.NewCoords(zoom, originX+1, originY)},
		{tile.NewCoords(zoom, originX, originY+1), tile.NewCoords(zoom, originX+1, originY+1)},
	}

	block := blockBounds(coords[0][0], coords[1][1])
	ds := newWorldSyntheticDataSource(block)

	outputDir := t.TempDir()
	stylesDir := filepath.Join("..", "..", "assets", "styles")
	texturesDir := filepath.Join("..", "..", "assets", "textures")

	gen, err := NewGenerator(ds, stylesDir, texturesDir, outputDir, 256, 123, false, nil, GeneratorOptions{})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	// Render all four tiles first; the seam checks need both sides.
	tiles := make(map[tile.Coords]image.Image, 4)
	for _, row := range coords {
		for _, c := range row {
			path, _, err := gen.Generate(ctx, c, true, "")
			require.NoError(t, err, "generating %s", c.String())

			img, err := readPNG(path)
			require.NoError(t, err, "reading %s", path)
			tiles[c] = img
		}
	}

	t.Run("Vertical_top", func(t *testing.T) {
		collectVerticalSeam(t, tiles[coords[0][0]], tiles[coords[0][1]]).assertContinuous(t)
	})
	t.Run("Vertical_bottom", func(t *testing.T) {
		collectVerticalSeam(t, tiles[coords[1][0]], tiles[coords[1][1]]).assertContinuous(t)
	})
	t.Run("Horizontal_left", func(t *testing.T) {
		collectHorizontalSeam(t, tiles[coords[0][0]], tiles[coords[1][0]]).assertContinuous(t)
	})
	t.Run("Horizontal_right", func(t *testing.T) {
		collectHorizontalSeam(t, tiles[coords[0][1]], tiles[coords[1][1]]).assertContinuous(t)
	})
}

// seamSamples holds, for one border, the per-pixel step across it and the
// control: the equivalent steps one pixel inside each of the two tiles.
type seamSamples struct {
	cross    []int
	control  []int
	position []int    // Index (x or y) of each cross sample, for the failure message.
	axis     string   // "y" or "x", likewise.
	edge     [][2]int // Cross-border colors, kept only to describe the worst pixel.
}

// collectVerticalSeam samples the right column of the left tile against the left
// column of the right tile.
//
// Shaped after compareVerticalSeam in internal/renderer/multipass_test.go, with
// two differences: composited tiles are opaque (they sit on the paper base), so
// there is no semi-transparent pixel to skip, and every row is sampled rather
// than every fourth — a one-pixel-wide artefact is exactly what this test exists
// to catch, and 256 rows costs nothing next to the render itself.
func collectVerticalSeam(t *testing.T, left, right image.Image) seamSamples {
	t.Helper()

	bounds := left.Bounds()
	require.Equal(t, bounds, right.Bounds(), "tiles differ in size")

	s := seamSamples{axis: "y"}
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		a := left.At(bounds.Max.X-1, y)
		b := right.At(bounds.Min.X, y)

		s.add(y, a, b,
			maxChannelDiff(left.At(bounds.Max.X-2, y), a),
			maxChannelDiff(b, right.At(bounds.Min.X+1, y)))
	}
	return s
}

// collectHorizontalSeam samples the bottom row of the top tile against the top
// row of the bottom tile. See collectVerticalSeam.
func collectHorizontalSeam(t *testing.T, top, bottom image.Image) seamSamples {
	t.Helper()

	bounds := top.Bounds()
	require.Equal(t, bounds, bottom.Bounds(), "tiles differ in size")

	s := seamSamples{axis: "x"}
	for x := bounds.Min.X; x < bounds.Max.X; x++ {
		a := top.At(x, bounds.Max.Y-1)
		b := bottom.At(x, bounds.Min.Y)

		s.add(x, a, b,
			maxChannelDiff(top.At(x, bounds.Max.Y-2), a),
			maxChannelDiff(b, bottom.At(x, bounds.Min.Y+1)))
	}
	return s
}

func (s *seamSamples) add(pos int, a, b color.Color, controlA, controlB int) {
	s.cross = append(s.cross, maxChannelDiff(a, b))
	s.control = append(s.control, controlA, controlB)
	s.position = append(s.position, pos)
	s.edge = append(s.edge, [2]int{packColor(a), packColor(b)})
}

// assertContinuous fails if the step across the border stands out against the
// steps inside the tiles.
func (s seamSamples) assertContinuous(t *testing.T) {
	t.Helper()

	crossMean, crossP95, crossMax, crossMaxAt := mean(s.cross), percentile(s.cross, 0.95), 0, 0
	for i, d := range s.cross {
		if d > crossMax {
			crossMax, crossMaxAt = d, i
		}
	}
	controlMean, controlP95 := mean(s.control), percentile(s.control, 0.95)

	// Always logged: when this test does fail, the first question is "by how much
	// worse than normal?", and the numbers are cheap.
	t.Logf("cross-border step: mean=%.1f p95=%d max=%d | in-tile control: mean=%.1f p95=%d",
		crossMean, crossP95, crossMax, controlMean, controlP95)

	if crossMax > seamMaxPixelDelta {
		t.Errorf("seam at %s=%d: cross-border delta %d exceeds the absolute cap %d (%s vs %s)",
			s.axis, s.position[crossMaxAt], crossMax, seamMaxPixelDelta,
			describeColor(s.edge[crossMaxAt][0]), describeColor(s.edge[crossMaxAt][1]))
	}
	if crossMean > controlMean+seamMeanMargin {
		t.Errorf("seam: mean cross-border step %.1f exceeds in-tile control %.1f by more than %.1f",
			crossMean, controlMean, seamMeanMargin)
	}
	if crossP95 > controlP95+seamP95Margin {
		t.Errorf("seam: p95 cross-border step %d exceeds in-tile control %d by more than %d",
			crossP95, controlP95, seamP95Margin)
	}
}

func mean(v []int) float64 {
	sum := 0
	for _, x := range v {
		sum += x
	}
	return float64(sum) / float64(len(v))
}

func percentile(v []int, q float64) int {
	sorted := append([]int(nil), v...)
	sort.Ints(sorted)
	return sorted[int(float64(len(sorted)-1)*q)]
}

// maxChannelDiff returns the largest 8-bit per-channel difference between two colors.
func maxChannelDiff(c1, c2 color.Color) int {
	r1, g1, b1, a1 := c1.RGBA()
	r2, g2, b2, a2 := c2.RGBA()

	worst := 0
	for _, d := range []int{
		abs(int(r1>>8) - int(r2>>8)),
		abs(int(g1>>8) - int(g2>>8)),
		abs(int(b1>>8) - int(b2>>8)),
		abs(int(a1>>8) - int(a2>>8)),
	} {
		if d > worst {
			worst = d
		}
	}
	return worst
}

// packColor squeezes an 8-bit RGBA color into an int so samples stay cheap to
// keep around for the failure message.
func packColor(c color.Color) int {
	r, g, b, a := c.RGBA()
	return int(r>>8)<<24 | int(g>>8)<<16 | int(b>>8)<<8 | int(a>>8)
}

func describeColor(packed int) string {
	return fmt.Sprintf("rgba(%d,%d,%d,%d)",
		(packed>>24)&0xff, (packed>>16)&0xff, (packed>>8)&0xff, packed&0xff)
}

// blockBounds returns the bounding box spanning a rectangular block of tiles,
// given its north-west and south-east corner tiles.
func blockBounds(northWest, southEast tile.Coords) types.BoundingBox {
	nw := types.TileToBounds(types.TileCoordinate{Zoom: int(northWest.Z), X: int(northWest.X), Y: int(northWest.Y)})
	se := types.TileToBounds(types.TileCoordinate{Zoom: int(southEast.Z), X: int(southEast.X), Y: int(southEast.Y)})

	return types.BoundingBox{
		MinLon: nw.MinLon,
		MaxLon: se.MaxLon,
		MinLat: se.MinLat,
		MaxLat: nw.MaxLat,
	}
}

// worldSyntheticDataSource serves one fixed set of features regardless of which
// tile is requested.
//
// This is the whole point of the type, and the one thing syntheticDataSource in
// pipeline_stages_test.go cannot do: that one scales its geometry into the
// bounds of whatever tile is asked for, so two neighbouring tiles get two
// different copies of the map and share no feature at all — which makes it
// useless for seam testing. Here the geometry is fixed lon/lat, built once, so
// neighbouring tiles genuinely render two halves of the same polygon.
type worldSyntheticDataSource struct {
	features types.FeatureCollection
}

// newWorldSyntheticDataSource builds features positioned relative to the given
// block, so the caller can place the block anywhere without editing coordinates.
//
// Feature edges are kept away from the block's mid-lines (the seams under test):
// an edge running parallel to and exactly on a seam would put a genuine, correct
// colour step right where we measure, and the test would be reporting geometry,
// not a seam.
func newWorldSyntheticDataSource(block types.BoundingBox) *worldSyntheticDataSource {
	at := func(fx, fy float64) orb.Point {
		return orb.Point{
			block.MinLon + fx*(block.MaxLon-block.MinLon),
			block.MinLat + fy*(block.MaxLat-block.MinLat),
		}
	}

	return &worldSyntheticDataSource{
		features: types.FeatureCollection{
			// A single large lake covering the middle of the block: it crosses
			// both seams, so all four tiles render a piece of the same polygon
			// and the blurred water edge has to line up.
			Water: []types.Feature{
				{
					ID:   "world/water/1",
					Type: types.FeatureTypeWater,
					Geometry: orb.Polygon{
						{at(0.15, 0.15), at(0.85, 0.15), at(0.85, 0.85), at(0.15, 0.85), at(0.15, 0.15)},
					},
					Properties: map[string]interface{}{"natural": "water"},
				},
			},
			// Diagonals cross both seams at an angle, which is the case most
			// likely to expose per-tile rounding: a straight-on crossing can
			// look continuous even when the two tiles disagree by a pixel.
			Rivers: []types.Feature{
				{
					ID:         "world/river/1",
					Type:       types.FeatureTypeWater,
					Geometry:   orb.LineString{at(0.02, 0.05), at(0.98, 0.95)},
					Properties: map[string]interface{}{"waterway": "river", "name": "Seam River"},
				},
			},
			Railroads: []types.Feature{
				{
					ID:         "world/railroad/1",
					Type:       types.FeatureTypeRailroad,
					Geometry:   orb.LineString{at(0.02, 0.95), at(0.98, 0.05)},
					Properties: map[string]interface{}{"railway": "rail"},
				},
			},
			Roads: []types.Feature{
				{
					ID:         "world/road/1",
					Type:       types.FeatureTypeRoad,
					Geometry:   orb.LineString{at(0.0, 0.28), at(1.0, 0.28)},
					Properties: map[string]interface{}{"highway": "secondary"},
				},
				{
					ID:         "world/road/2",
					Type:       types.FeatureTypeRoad,
					Geometry:   orb.LineString{at(0.72, 0.0), at(0.72, 1.0)},
					Properties: map[string]interface{}{"highway": "residential"},
				},
				{
					ID:         "world/highway/1",
					Type:       types.FeatureTypeRoad,
					Geometry:   orb.LineString{at(0.0, 0.78), at(1.0, 0.78)},
					Properties: map[string]interface{}{"highway": "motorway"},
				},
			},
			Parks: []types.Feature{
				{
					ID:   "world/park/1",
					Type: types.FeatureTypePark,
					Geometry: orb.Polygon{
						{at(0.30, 0.86), at(0.70, 0.86), at(0.70, 0.97), at(0.30, 0.97), at(0.30, 0.86)},
					},
					Properties: map[string]interface{}{"leisure": "park"},
				},
			},
			Urban: []types.Feature{
				{
					ID:   "world/urban/1",
					Type: types.FeatureTypeUrban,
					Geometry: orb.Polygon{
						{at(0.86, 0.20), at(0.99, 0.20), at(0.99, 0.80), at(0.86, 0.80), at(0.86, 0.20)},
					},
					Properties: map[string]interface{}{"landuse": "residential"},
				},
			},
			Civic: []types.Feature{
				{
					ID:   "world/civic/1",
					Type: types.FeatureTypeCivic,
					Geometry: orb.Polygon{
						{at(0.02, 0.20), at(0.13, 0.20), at(0.13, 0.80), at(0.02, 0.80), at(0.02, 0.20)},
					},
					Properties: map[string]interface{}{"amenity": "school"},
				},
			},
		},
	}
}

func (s *worldSyntheticDataSource) FetchTileData(_ context.Context, coord types.TileCoordinate) (*types.TileData, error) {
	return &types.TileData{
		Coordinate: coord,
		Bounds:     types.TileToBounds(coord),
		Features:   s.features,
		Source:     "world-synthetic",
		FetchedAt:  time.Now(),
	}, nil
}
