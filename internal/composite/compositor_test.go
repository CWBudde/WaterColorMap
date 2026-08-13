package composite

import (
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/cwbudde/watercolormap/internal/geojson"
)

func fillRect(img *image.NRGBA, rect image.Rectangle, c color.NRGBA) {
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
}

func blendNRGBA(top, bottom color.NRGBA) color.NRGBA {
	sa := float64(top.A) / 255.0
	ba := float64(bottom.A) / 255.0

	outA := sa + ba*(1.0-sa)
	if outA == 0 {
		return color.NRGBA{}
	}

	blend := func(s, b uint8) uint8 {
		sp := float64(s) * sa
		bp := float64(b) * ba
		outPremult := sp + bp*(1.0-sa)
		return uint8(math.Round(outPremult / outA))
	}

	return color.NRGBA{
		R: blend(top.R, bottom.R),
		G: blend(top.G, bottom.G),
		B: blend(top.B, bottom.B),
		A: uint8(math.Round(outA * 255.0)),
	}
}

func expectColor(t *testing.T, got color.NRGBA, want color.NRGBA, context string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: expected %+v, got %+v", context, want, got)
	}
}

func TestCompositeUsesOrderAndTransparency(t *testing.T) {
	tileSize := 4

	// DefaultOrder stacks land below water below roads.
	land := image.NewNRGBA(image.Rect(0, 0, tileSize, tileSize))
	fillRect(land, land.Bounds(), color.NRGBA{G: 255, A: 255})

	water := image.NewNRGBA(image.Rect(0, 0, tileSize, tileSize))
	fillRect(water, image.Rect(0, 0, tileSize/2, tileSize/2), color.NRGBA{B: 255, A: 255})

	roads := image.NewNRGBA(image.Rect(0, 0, tileSize, tileSize))
	for y := 0; y < tileSize; y++ {
		roads.SetNRGBA(1, y, color.NRGBA{R: 255, A: 128})
	}

	layers := map[geojson.LayerType]image.Image{
		geojson.LayerWater: water,
		geojson.LayerLand:  land,
		geojson.LayerRoads: roads,
	}

	out, err := CompositeLayers(layers, nil, tileSize)
	if err != nil {
		t.Fatalf("CompositeLayers returned error: %v", err)
	}

	expectColor(t, out.NRGBAAt(0, 0), color.NRGBA{B: 255, A: 255}, "water should sit above land")
	expectColor(t, out.NRGBAAt(3, 3), color.NRGBA{G: 255, A: 255}, "land should show where water is transparent")

	expectedRoad := blendNRGBA(
		color.NRGBA{R: 255, A: 128},
		color.NRGBA{B: 255, A: 255},
	)
	expectColor(t, out.NRGBAAt(1, 1), expectedRoad, "road should alpha-blend on top of water")
	expectColor(t, out.NRGBAAt(0, 1), color.NRGBA{B: 255, A: 255}, "neighbor pixel remains aligned")
}

func TestCompositeValidatesBounds(t *testing.T) {
	badLayer := image.NewNRGBA(image.Rect(1, 1, 3, 3)) // wrong origin/size
	layers := map[geojson.LayerType]image.Image{
		geojson.LayerLand: badLayer,
	}

	if _, err := CompositeLayers(layers, nil, 4); err == nil {
		t.Fatal("expected error for mismatched bounds")
	}
}
