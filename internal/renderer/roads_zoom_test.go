package renderer

import (
	"image/png"
	"math"
	"os"
	"testing"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/maptile"

	"github.com/cwbudde/watercolormap/internal/geojson"
	"github.com/cwbudde/watercolormap/internal/tile"
	"github.com/cwbudde/watercolormap/internal/types"
)

// measureRoadWidth returns the rendered stroke width, in pixels, of an east-west
// road in a rendered roads layer.
//
// The roads in these tests run east-west, so the stroke width is a *vertical*
// measurement: the longest contiguous run of covered pixels within a single
// column, maximised over every column. Scanning rows instead would measure how
// far the road reaches across the tile, which is a function of the road's length
// and the zoom, not of stroke-width at all.
//
// Every column is scanned rather than just the middle ones because the road is
// only guaranteed to pass through the centre of the tile it was anchored to; at
// other zooms or tile sizes it lands wherever the projection puts it.
//
// The result is the summed alpha coverage of the column, not a count of opaque
// pixels. For an antialiased line the coverage in a crossing column adds up to
// the stroke width, which gives sub-pixel resolution; counting thresholded pixels
// instead quantises 3.5 and 4.5 to the same 4 and cannot tell the two apart.
func measureRoadWidth(t *testing.T, path string) float64 {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("failed to open rendered road PNG %s: %v", path, err)
	}
	defer f.Close()

	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("failed to decode rendered road PNG %s: %v", path, err)
	}

	const opaque = 0xffff // RGBA() returns 16-bit values

	b := img.Bounds()
	maxCoverage := 0.0
	for x := b.Min.X; x < b.Max.X; x++ {
		coverage := 0.0
		for y := b.Min.Y; y < b.Max.Y; y++ {
			_, _, _, a := img.At(x, y).RGBA()
			coverage += float64(a) / opaque
		}
		if coverage > maxCoverage {
			maxCoverage = coverage
		}
	}

	if maxCoverage == 0 {
		t.Fatalf("no road pixels found in %s", path)
	}

	return maxCoverage
}

// syntheticPrimaryRoad builds an east-west primary road through the given point,
// long enough to cross the whole tile at the zooms these tests use.
func syntheticPrimaryRoad(lon, lat float64) *types.TileData {
	return &types.TileData{
		Features: types.FeatureCollection{
			Roads: []types.Feature{{
				ID:       "test-primary",
				Type:     types.FeatureTypeRoad,
				Name:     "Primary Test Road",
				Geometry: orb.LineString{{lon - 0.02, lat}, {lon + 0.02, lat}},
				Properties: map[string]any{
					"highway": "primary",
				},
			}},
		},
	}
}

func TestRoadStrokeScalesWithZoom(t *testing.T) {
	requireIntegration(t)

	stylesDir := "../../assets/styles"
	outputDir := t.TempDir()

	renderer, err := NewMultiPassRenderer(stylesDir, outputDir, 256, 0)
	if err != nil {
		t.Fatalf("failed to create renderer: %v", err)
	}
	defer renderer.Close()

	// Use a known Hanover tile center to anchor a synthetic east-west primary road.
	center := tile.NewCoords(13, 4317, 2692)
	lon, lat := center.Center()

	data := syntheticPrimaryRoad(lon, lat)

	// z11 and z13 both keep 'primary' in roads.xml (scale denominators ~273k and
	// ~68k, i.e. the 1M-150k and 75k-50k tiers, giving stroke widths 3.5 and 4.5).
	// z14 would not work: at ~34k roads.xml deliberately hands primary off to
	// highways.xml, so the roads layer comes back empty.
	widths := make(map[uint32]float64)
	for _, z := range []uint32{11, 13} {
		tileIdx := maptile.At(orb.Point{lon, lat}, maptile.Zoom(z))
		coords := tile.NewCoords(uint32(tileIdx.Z), tileIdx.X, tileIdx.Y)

		result, err := renderer.RenderTile(coords, data)
		if err != nil {
			t.Fatalf("failed to render roads at z%d: %v", z, err)
		}

		roadsLayer := result.Layers[geojson.LayerRoads]
		if roadsLayer == nil || roadsLayer.OutputPath == "" {
			t.Fatalf("no roads layer output for z%d", z)
		}

		widths[z] = measureRoadWidth(t, roadsLayer.OutputPath)
	}

	if widths[13] <= widths[11] {
		t.Fatalf("expected thicker primary roads at higher zoom: z11=%.2fpx, z13=%.2fpx", widths[11], widths[13])
	}
}

// TestRoadStrokeScalesWithTileSize is the @2x half of the world-anchoring rule: the
// same road at the same place must cover the same ground width regardless of the
// tile size it is rendered into.
//
// It fails without MapnikRenderer.SetScaleFactor, and it fails in two distinct
// ways, because Mapnik's scale_factor governs two things at once:
//
//   - stroke-width in assets/styles/layers/*.xml is a fixed device-pixel value, so
//     unscaled the 512px tile draws the road at exactly the 256px width;
//   - Mapnik multiplies the scale denominator by the scale factor before evaluating
//     Min/MaxScaleDenominator, and the two sizes have denominators an octave apart
//     over the same extent, so unscaled they resolve different detail tiers.
//
// At z13 the second effect dominates and is stark: the 256px render sits in the
// 75000-50000 tier where roads.xml draws 'primary', while an unscaled 512px render
// halves the denominator into the 50000-25000 tier, where primary has been handed
// off to highways.xml — so the roads layer comes back completely empty and this
// test fails with "no road pixels found" rather than with a width mismatch.
func TestRoadStrokeScalesWithTileSize(t *testing.T) {
	requireIntegration(t)

	stylesDir := "../../assets/styles"

	// One fixed tile, so the only thing varying between the two renders is the
	// tile size. z13 is the interesting zoom: it straddles a road tier boundary.
	coords := tile.NewCoords(13, 4317, 2692)
	lon, lat := coords.Center()
	data := syntheticPrimaryRoad(lon, lat)

	widths := make(map[int]float64)
	for _, tileSize := range []int{256, 512} {
		renderer, err := NewMultiPassRenderer(stylesDir, t.TempDir(), tileSize, 0)
		if err != nil {
			t.Fatalf("failed to create renderer at tileSize %d: %v", tileSize, err)
		}

		result, err := renderer.RenderTile(coords, data)
		if err != nil {
			renderer.Close() // nolint:errcheck // Already failing
			t.Fatalf("failed to render roads at tileSize %d: %v", tileSize, err)
		}

		roadsLayer := result.Layers[geojson.LayerRoads]
		if roadsLayer == nil || roadsLayer.OutputPath == "" {
			renderer.Close() // nolint:errcheck // Already failing
			t.Fatalf("no roads layer output at tileSize %d", tileSize)
		}

		widths[tileSize] = measureRoadWidth(t, roadsLayer.OutputPath)
		if err := renderer.Close(); err != nil {
			t.Fatalf("failed to close renderer at tileSize %d: %v", tileSize, err)
		}
	}

	// Rounded linecaps and rasterisation leave a little slack, so don't demand
	// exact doubling; half a pixel is far tighter than the 2x error being caught.
	const tolerance = 0.5
	want := 2 * widths[256]
	if diff := math.Abs(widths[512] - want); diff > tolerance {
		t.Fatalf("@2x road width = %.2fpx, want %.2fpx (2x the 256px width of %.2fpx) within %.2fpx; "+
			"the stylesheet's device-pixel stroke widths are not being scaled for hi-DPI",
			widths[512], want, widths[256], tolerance)
	}
}
