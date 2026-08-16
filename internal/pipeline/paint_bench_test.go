package pipeline

import (
	"fmt"
	"image"
	"image/color"
	"testing"

	"github.com/cwbudde/watercolormap/internal/geojson"
	"github.com/cwbudde/watercolormap/internal/mask"
	"github.com/cwbudde/watercolormap/internal/watercolor"
)

// benchPaintLayers is the set paintAllLayers actually paints, plus paper for the
// land-on-canvas capture. Ten of them are painted; the eleventh is a texture only.
var benchPaintLayers = []geojson.LayerType{
	geojson.LayerLand,
	geojson.LayerWater,
	geojson.LayerRivers,
	geojson.LayerRoads,
	geojson.LayerRailroads,
	geojson.LayerHighways,
	geojson.LayerUrban,
	geojson.LayerCivic,
	geojson.LayerParks,
	geojson.LayerBuildings,
}

// benchLayerImage fabricates a rendered layer: a disc and a rectangle over
// transparency, at a per-layer offset so no two layers cover the same pixels.
func benchLayerImage(size, phase int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	cx := size/3 + phase*7
	cy := size/3 + phase*5
	r := size / 5
	opaque := color.NRGBA{R: 0, G: 0, B: 255, A: 255}
	for y := range size {
		for x := range size {
			dx, dy := x-cx, y-cy
			inDisc := dx*dx+dy*dy <= r*r
			inRect := x >= size/2 && x < size*3/4 && y >= size/2+phase && y < size*3/4+phase
			if inDisc || inRect {
				img.SetNRGBA(x, y, opaque)
			}
		}
	}

	return img
}

// benchSolidTexture stands in for a paper texture. Small on purpose: the tiling
// loop is not what this benchmark is about.
func benchSolidTexture(c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}

	return img
}

// benchPaintInput builds everything paintAllLayers reads, once.
//
// The size is 384 rather than 256 on purpose: production never paints a 256² image.
// RequiredPaddingPx puts a 256 tile on a 384² metatile, so that is what the paint
// stage costs in practice.
func benchPaintInput(size int) (map[geojson.LayerType]image.Image, *maskSet, watercolor.Params, map[geojson.LayerType]image.Image) {
	textures := make(map[geojson.LayerType]image.Image, len(benchPaintLayers)+1)
	rawLayers := make(map[geojson.LayerType]image.Image, len(benchPaintLayers))
	for i, layer := range benchPaintLayers {
		textures[layer] = benchSolidTexture(color.NRGBA{R: uint8(30 * i), G: 128, B: 200, A: 255}) // nolint:gosec // small loop index
		rawLayers[layer] = benchLayerImage(size, i)
	}
	textures[geojson.LayerPaper] = benchSolidTexture(color.NRGBA{R: 250, G: 248, B: 240, A: 255})

	params := watercolor.DefaultParams(size, 42, textures)
	params.PerlinNoise = mask.GeneratePerlinNoiseWithOffset(
		size, size, params.NoiseScale, params.Seed, params.OffsetX, params.OffsetY)

	masks := buildMasks(rawLayers, params, nil)

	return rawLayers, masks, params, textures
}

// BenchmarkPaintAllLayers measures the paint stage of a metatile at a range of paint
// worker counts. workers=1 is the serial pipeline, so the ratio against it is the
// speedup the concurrency actually buys.
//
// Wall time is the quantity of interest here, not CPU time: parallelism does not
// reduce the work, it only spreads it. Run it on an otherwise idle machine and
// interleave the settings (-count=N puts them in one process back to back).
func BenchmarkPaintAllLayers(b *testing.B) {
	const size = 384
	rawLayers, masks, params, textures := benchPaintInput(size)

	for _, workers := range []int{1, 2, 4, 6, maxPaintWorkers} {
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := paintAllLayers(rawLayers, masks, params, textures, nil, nil, workers); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
