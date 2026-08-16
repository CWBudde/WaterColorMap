package watercolor

import (
	"bytes"
	"image"
	"image/color"
	"math/rand/v2"
	"testing"

	"github.com/cwbudde/watercolormap/internal/geojson"
	"github.com/cwbudde/watercolormap/internal/mask"
)

// processMask runs over pooled scratch buffers and writes several of its stages in
// place. referenceProcessMask is the same pipeline written the obvious way, with a
// fresh image per stage - the two must agree pixel for pixel.
func referenceProcessMask(baseMask *image.Gray, layer geojson.LayerType, params Params) *image.Gray {
	style := params.Styles[layer]

	layerBlur := params.BlurSigma
	if style.MaskBlurSigma > 0 {
		layerBlur = style.MaskBlurSigma
	}
	layerNoiseStrength := params.NoiseStrength
	if style.MaskNoiseStrength > 0 {
		layerNoiseStrength = style.MaskNoiseStrength
	}
	threshold := params.Threshold
	if style.MaskThreshold != nil {
		threshold = *style.MaskThreshold
	}

	blurred := mask.BoxBlurSigma(baseMask, layerBlur)
	noisy := blurred
	if layerNoiseStrength != 0 && params.PerlinNoise != nil {
		if style.AdaptiveNoise && style.NoiseMaxDist > 0 {
			binaryMask := mask.ApplyThreshold(blurred, threshold)
			distMap := mask.EuclideanDistanceTransform(binaryMask, style.NoiseMaxDist)
			noisy = mask.ApplyNoiseToMaskAdaptive(blurred, params.PerlinNoise, distMap,
				layerNoiseStrength, style.NoiseMinDist, style.NoiseMaxDist)
		} else {
			noisy = mask.ApplyNoiseToMask(blurred, params.PerlinNoise, layerNoiseStrength)
		}
	}

	if style.InvertMask {
		return mask.ApplyThresholdWithAntialiasAndInvert(noisy, threshold)
	}

	return mask.ApplyThresholdWithAntialias(noisy, threshold)
}

func scratchTestMask(size int, seed uint64) *image.Gray {
	m := image.NewGray(image.Rect(0, 0, size, size))
	rng := rand.New(rand.NewPCG(seed, 99))
	for y := range size {
		for x := range size {
			v := uint8(rng.UintN(256))
			if (x/9+y/7)%3 == 0 {
				v = 255
			}
			m.SetGray(x, y, color.Gray{Y: v})
		}
	}

	return m
}

func scratchTestParams(tileSize int) Params {
	tex := solidTexture(4, 4, color.NRGBA{R: 180, G: 160, B: 140, A: 255})
	textures := map[geojson.LayerType]image.Image{}
	for _, l := range []geojson.LayerType{
		geojson.LayerLand, geojson.LayerWater, geojson.LayerRoads, geojson.LayerCivic,
	} {
		textures[l] = tex
	}

	params := DefaultParams(tileSize, 42, textures)
	params.PerlinNoise = mask.GeneratePerlinNoiseWithOffset(
		tileSize, tileSize, params.NoiseScale, params.Seed, 0, 0,
	)

	return params
}

// TestProcessMaskMatchesReference covers all four shapes of the mask pipeline: adaptive
// noise, plain noise, no noise at all, and the inverted land path.
func TestProcessMaskMatchesReference(t *testing.T) {
	const tileSize = 64

	tests := []struct {
		name     string
		layer    geojson.LayerType
		noNoise  bool
		adaptive bool
	}{
		{name: "adaptive noise", layer: geojson.LayerWater, adaptive: true},
		{name: "plain noise", layer: geojson.LayerCivic},
		{name: "no noise", layer: geojson.LayerCivic, noNoise: true},
		{name: "inverted land", layer: geojson.LayerLand},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := scratchTestParams(tileSize)
			if tt.noNoise {
				params.NoiseStrength = 0
				style := params.Styles[tt.layer]
				style.MaskNoiseStrength = 0
				params.Styles[tt.layer] = style
			}
			if tt.adaptive != params.Styles[tt.layer].AdaptiveNoise && tt.adaptive {
				t.Fatalf("layer %s is expected to use adaptive noise", tt.layer)
			}

			baseMask := scratchTestMask(tileSize, 1)
			want := referenceProcessMask(baseMask, tt.layer, params)

			got, err := processMask(baseMask, tt.layer, params)
			if err != nil {
				t.Fatalf("processMask: %v", err)
			}

			if !bytes.Equal(got.Pix, want.Pix) {
				t.Error("processMask differs from the allocation-per-stage reference")
			}
		})
	}
}

// TestProcessMaskDoesNotWriteItsInput pins the one aliasing rule the pipeline depends on:
// the caller keeps the base mask (the land path hands the same union mask on to parks).
func TestProcessMaskDoesNotWriteItsInput(t *testing.T) {
	const tileSize = 64

	params := scratchTestParams(tileSize)
	baseMask := scratchTestMask(tileSize, 2)
	before := bytes.Clone(baseMask.Pix)

	if _, err := processMask(baseMask, geojson.LayerLand, params); err != nil {
		t.Fatalf("processMask: %v", err)
	}

	if !bytes.Equal(baseMask.Pix, before) {
		t.Error("processMask modified its input mask")
	}
}

// TestProcessMaskSurvivesRecycledScratch covers the reuse that actually happens: every
// layer of a tile has the same bounds, so the scratch comes back from the pool at the
// right size and full of the previous layer's pixels. ensure() keeps those buffers, so
// only the total-write property stops them from showing through.
//
// A size change is deliberately not the case under test - ensure() is exact-size, so it
// reallocates and the second layer would run on virgin buffers either way.
func TestProcessMaskSurvivesRecycledScratch(t *testing.T) {
	const tileSize = 64

	params := scratchTestParams(tileSize)

	// Fill the pooled scratch with a different layer's data first. Land inverts and
	// blurs differently, so nothing it leaves behind matches what water expects.
	if _, err := processMask(scratchTestMask(tileSize, 3), geojson.LayerLand, params); err != nil {
		t.Fatalf("priming processMask: %v", err)
	}

	baseMask := scratchTestMask(tileSize, 4)
	want := referenceProcessMask(baseMask, geojson.LayerWater, params)

	got, err := processMask(baseMask, geojson.LayerWater, params)
	if err != nil {
		t.Fatalf("second processMask: %v", err)
	}

	if !bytes.Equal(got.Pix, want.Pix) {
		t.Error("a recycled scratch changed the result")
	}
}

// TestPaintFromFinalMaskReusesEdgeMaskBuffer is the paint-side twin of the test above.
// The edge mask lives in the pooled ProcessorContext and is only written over the final
// mask's bounds, so a smaller mask after a larger one must not inherit the previous
// layer's fringe.
func TestPaintFromFinalMaskReusesEdgeMaskBuffer(t *testing.T) {
	const tileSize = 32

	params := scratchTestParams(tileSize)

	fullMask := image.NewGray(image.Rect(0, 0, tileSize, tileSize))
	for i := range fullMask.Pix {
		fullMask.Pix[i] = 255
	}
	if _, err := paintFromFinalMask(fullMask, geojson.LayerWater, params); err != nil {
		t.Fatalf("priming paint: %v", err)
	}

	// Same paint twice from a fresh context and from the primed pool must agree.
	smallMask := image.NewGray(image.Rect(0, 0, tileSize/2, tileSize/2))
	for i := range smallMask.Pix {
		smallMask.Pix[i] = 255
	}

	fromPool, err := paintFromFinalMask(smallMask, geojson.LayerWater, params)
	if err != nil {
		t.Fatalf("pooled paint: %v", err)
	}

	fromFresh, err := paintFromFinalMaskWithContext(
		smallMask, geojson.LayerWater, params, NewProcessorContext(params.TileSize),
	)
	if err != nil {
		t.Fatalf("fresh paint: %v", err)
	}

	if !bytes.Equal(fromPool.Pix, fromFresh.Pix) {
		t.Error("a recycled ProcessorContext changed the paint")
	}
}

func TestPaintLayerNilImage(t *testing.T) {
	if _, err := PaintLayer(nil, geojson.LayerWater, scratchTestParams(16)); err == nil {
		t.Fatal("expected an error for a nil layer image")
	}
}
