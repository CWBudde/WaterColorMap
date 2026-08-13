package renderer

import (
	"image/png"
	"os"
	"testing"

	"github.com/cwbudde/watercolormap/internal/geojson"
	"github.com/cwbudde/watercolormap/internal/tile"
	"github.com/cwbudde/watercolormap/internal/types"
	"github.com/paulmach/orb"
	"github.com/paulmach/orb/maptile"
)

// measureRoadWidth finds the widest contiguous run of non-transparent pixels across a handful
// of rows near the vertical center line. This approximates the rendered stroke width.
func measureRoadWidth(t *testing.T, path string) int {
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

	b := img.Bounds()
	midY := b.Min.Y + (b.Dy() / 2)
	maxWidth := 0

	for dy := -2; dy <= 2; dy++ {
		y := midY + dy
		if y < b.Min.Y || y >= b.Max.Y {
			continue
		}

		minX := b.Max.X
		maxX := -1
		for x := b.Min.X; x < b.Max.X; x++ {
			_, _, _, a := img.At(x, y).RGBA()
			if a > 0 {
				if x < minX {
					minX = x
				}
				if x > maxX {
					maxX = x
				}
			}
		}

		if maxX >= minX {
			width := maxX - minX + 1
			if width > maxWidth {
				maxWidth = width
			}
		}
	}

	if maxWidth == 0 {
		t.Fatalf("no road pixels found near center rows in %s", path)
	}

	return maxWidth
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

	road := types.Feature{
		ID:       "test-primary",
		Type:     types.FeatureTypeRoad,
		Name:     "Primary Test Road",
		Geometry: orb.LineString{{lon - 0.02, lat}, {lon + 0.02, lat}},
		Properties: map[string]interface{}{
			"highway": "primary",
		},
	}

	data := &types.TileData{
		Features: types.FeatureCollection{
			Roads: []types.Feature{road},
		},
	}

	widths := make(map[uint32]int)
	for _, z := range []uint32{11, 14} {
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

	if widths[14] <= widths[11] {
		t.Fatalf("expected thicker primary roads at higher zoom: z11=%dpx, z14=%dpx", widths[11], widths[14])
	}
}
