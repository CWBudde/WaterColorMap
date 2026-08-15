package pipeline

import (
	"image"
	"image/color"
	"testing"

	"github.com/cwbudde/watercolormap/internal/geojson"
)

// maskBlue is the fill both water.xml and ocean.xml use.
var maskBlue = color.NRGBA{R: 0, G: 0, B: 255, A: 255}

// halfFilled paints the left or right half of a size×size tile opaque blue,
// standing in for a coastline running down the middle.
func halfFilled(size int, leftHalf bool) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			inHalf := x < size/2
			if !leftHalf {
				inHalf = x >= size/2
			}
			if inHalf {
				img.SetNRGBA(x, y, maskBlue)
			}
		}
	}
	return img
}

func TestFoldOceanIntoWaterNoOcean(t *testing.T) {
	water := halfFilled(8, true)
	layers := map[geojson.LayerType]image.Image{geojson.LayerWater: water}

	foldOceanIntoWater(layers)

	if layers[geojson.LayerWater] != image.Image(water) {
		t.Error("with no ocean layer the water layer must be left untouched")
	}
}

func TestFoldOceanIntoWaterWithoutWater(t *testing.T) {
	ocean := halfFilled(8, true)
	layers := map[geojson.LayerType]image.Image{geojson.LayerOcean: ocean}

	foldOceanIntoWater(layers)

	if _, ok := layers[geojson.LayerOcean]; ok {
		t.Error("the ocean key must be consumed; nothing downstream knows it exists")
	}
	if layers[geojson.LayerWater] != image.Image(ocean) {
		t.Error("an ocean-only tile must become the water layer")
	}
}

func TestFoldOceanIntoWaterUnionsBoth(t *testing.T) {
	const size = 8
	layers := map[geojson.LayerType]image.Image{
		geojson.LayerOcean: halfFilled(size, true),  // sea on the left
		geojson.LayerWater: halfFilled(size, false), // a lake on the right
	}

	foldOceanIntoWater(layers)

	if _, ok := layers[geojson.LayerOcean]; ok {
		t.Fatal("the ocean key must be consumed")
	}

	merged := layers[geojson.LayerWater]
	if merged == nil {
		t.Fatal("expected a merged water layer")
	}
	if merged.Bounds() != image.Rect(0, 0, size, size) {
		t.Fatalf("bounds = %v, want the tile bounds", merged.Bounds())
	}

	// Every pixel must be opaque blue: the two halves together cover the tile.
	// If the merge dropped either input, one half would still be transparent —
	// and a transparent half is exactly the bug 4.10 is about, since land is
	// painted by inverting this mask.
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			r, g, b, a := merged.At(x, y).RGBA()
			if a>>8 != 255 || r>>8 != 0 || g>>8 != 0 || b>>8 != 255 {
				t.Fatalf("pixel (%d,%d) = (%d,%d,%d,%d), want opaque blue", x, y, r>>8, g>>8, b>>8, a>>8)
			}
		}
	}
}

func TestFoldOceanIntoWaterMismatchedBoundsKeepsOcean(t *testing.T) {
	ocean := halfFilled(8, true)
	layers := map[geojson.LayerType]image.Image{
		geojson.LayerOcean: ocean,
		geojson.LayerWater: halfFilled(4, false),
	}

	foldOceanIntoWater(layers)

	if layers[geojson.LayerWater] != image.Image(ocean) {
		t.Error("mismatched bounds must not be unioned; unioning them would shift the coastline")
	}
}
