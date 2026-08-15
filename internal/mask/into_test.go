package mask

import (
	"bytes"
	"image"
	"image/color"
	"math/rand/v2"
	"testing"
)

// The *Into variants exist so the tile pipeline can hand them recycled buffers. Three
// properties have to hold for that to be safe, and each is checked below for every
// operation:
//
//   - the result is identical to the allocating variant's,
//   - it does not depend on what the destination held beforehand (every pixel is written),
//   - the operations documented as safe in place really are.

// intoCase describes one destination-writing operation. run performs it; alloc produces
// the same result through the allocating API.
type intoCase struct {
	run   func(src, noise *image.Gray, ctx *DistanceContext, dst *image.Gray)
	alloc func(src, noise *image.Gray, ctx *DistanceContext) *image.Gray
	name  string
}

const (
	testThreshold = 128
	testGamma     = 2.5
	testRadius    = 12.0
	testStrength  = 0.4
	testMinDist   = 2.0
	testMaxDist   = 10.0
)

func intoCases() []intoCase {
	return []intoCase{
		{
			name: "ApplyThresholdInto",
			run: func(src, _ *image.Gray, _ *DistanceContext, dst *image.Gray) {
				ApplyThresholdInto(src, testThreshold, dst)
			},
			alloc: func(src, _ *image.Gray, _ *DistanceContext) *image.Gray {
				return ApplyThreshold(src, testThreshold)
			},
		},
		{
			name: "ApplyThresholdWithAntialiasInto",
			run: func(src, _ *image.Gray, _ *DistanceContext, dst *image.Gray) {
				ApplyThresholdWithAntialiasInto(src, testThreshold, dst)
			},
			alloc: func(src, _ *image.Gray, _ *DistanceContext) *image.Gray {
				return ApplyThresholdWithAntialias(src, testThreshold)
			},
		},
		{
			name: "ApplyThresholdWithAntialiasAndInvertInto",
			run: func(src, _ *image.Gray, _ *DistanceContext, dst *image.Gray) {
				ApplyThresholdWithAntialiasAndInvertInto(src, testThreshold, dst)
			},
			alloc: func(src, _ *image.Gray, _ *DistanceContext) *image.Gray {
				return ApplyThresholdWithAntialiasAndInvert(src, testThreshold)
			},
		},
		{
			name: "ApplyNoiseToMaskInto",
			run: func(src, noise *image.Gray, _ *DistanceContext, dst *image.Gray) {
				ApplyNoiseToMaskInto(src, noise, testStrength, dst)
			},
			alloc: func(src, noise *image.Gray, _ *DistanceContext) *image.Gray {
				return ApplyNoiseToMask(src, noise, testStrength)
			},
		},
		{
			name: "ApplyNoiseToMaskAdaptiveInto",
			run: func(src, noise *image.Gray, _ *DistanceContext, dst *image.Gray) {
				ApplyNoiseToMaskAdaptiveInto(src, noise, src, testStrength, testMinDist, testMaxDist, dst)
			},
			alloc: func(src, noise *image.Gray, _ *DistanceContext) *image.Gray {
				return ApplyNoiseToMaskAdaptive(src, noise, src, testStrength, testMinDist, testMaxDist)
			},
		},
		{
			name: "DistanceToIntensityInto",
			run: func(src, _ *image.Gray, _ *DistanceContext, dst *image.Gray) {
				DistanceToIntensityInto(src, testGamma, dst)
			},
			alloc: func(src, _ *image.Gray, _ *DistanceContext) *image.Gray {
				return DistanceToIntensity(src, testGamma)
			},
		},
		{
			name: "EuclideanDistanceTransformIntoWithContext",
			run: func(src, _ *image.Gray, ctx *DistanceContext, dst *image.Gray) {
				EuclideanDistanceTransformIntoWithContext(src, testRadius, ctx, dst)
			},
			alloc: func(src, _ *image.Gray, _ *DistanceContext) *image.Gray {
				return EuclideanDistanceTransform(src, testRadius)
			},
		},
		{
			name: "CreateDistanceEdgeMaskIntoWithContext",
			run: func(src, _ *image.Gray, ctx *DistanceContext, dst *image.Gray) {
				CreateDistanceEdgeMaskIntoWithContext(src, testRadius, testGamma, ctx, dst)
			},
			alloc: func(src, _ *image.Gray, _ *DistanceContext) *image.Gray {
				return CreateDistanceEdgeMask(src, testRadius, testGamma)
			},
		},
	}
}

// randomMask builds a deterministic mask with both flat regions and edges, so the
// distance transform and the antialias transition zone both have something to chew on.
func randomMask(bounds image.Rectangle, seed uint64) *image.Gray {
	m := image.NewGray(bounds)
	rng := rand.New(rand.NewPCG(seed, 0x5eed))

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			v := uint8(rng.UintN(256))
			// Blocky regions keep large connected areas alive; without them the
			// distance transform sees nothing but single pixels.
			if (x/8+y/8)%3 == 0 {
				v = 255
			}
			m.SetGray(x, y, color.Gray{Y: v})
		}
	}

	return m
}

func testBounds() []image.Rectangle {
	return []image.Rectangle{
		image.Rect(0, 0, 1, 1),
		image.Rect(0, 0, 3, 3),
		image.Rect(0, 0, 64, 64),
		image.Rect(0, 0, 384, 384),
		// Non-zero origin: every kernel iterates Min..Max, so an offset image must
		// work as long as the destination shares its bounds.
		image.Rect(7, 5, 71, 69),
	}
}

func TestIntoVariantsMatchAllocatingVariants(t *testing.T) {
	for _, tc := range intoCases() {
		t.Run(tc.name, func(t *testing.T) {
			for _, bounds := range testBounds() {
				src := randomMask(bounds, 1)
				noise := randomMask(bounds, 2)
				ctx := NewDistanceContext(max(bounds.Dx(), bounds.Dy()))

				want := tc.alloc(src, noise, ctx)
				got := image.NewGray(bounds)
				tc.run(src, noise, ctx, got)

				if !bytes.Equal(got.Pix, want.Pix) {
					t.Errorf("%v: Into result differs from the allocating variant", bounds)
				}
			}
		})
	}
}

// TestIntoVariantsIgnoreDirtyDestination is the stale-pixel regression test: a pooled
// buffer arrives holding the previous layer's data, and that must not be observable.
// It also catches a swapped (src, dst) pair at a call site.
func TestIntoVariantsIgnoreDirtyDestination(t *testing.T) {
	bounds := image.Rect(0, 0, 64, 64)

	for _, tc := range intoCases() {
		t.Run(tc.name, func(t *testing.T) {
			src := randomMask(bounds, 3)
			noise := randomMask(bounds, 4)
			ctx := NewDistanceContext(bounds.Dx())

			want := tc.alloc(src, noise, ctx)

			for _, fill := range []byte{0x00, 0xAA, 0x55, 0xFF} {
				dst := image.NewGray(bounds)
				for i := range dst.Pix {
					dst.Pix[i] = fill
				}
				tc.run(src, noise, ctx, dst)

				if !bytes.Equal(dst.Pix, want.Pix) {
					t.Errorf("destination prefilled with %#x changed the result", fill)
				}
			}
		})
	}
}

// TestIntoVariantsInPlace pins the aliasing the watercolor mask pipeline relies on.
func TestIntoVariantsInPlace(t *testing.T) {
	bounds := image.Rect(0, 0, 64, 64)
	ctx := NewDistanceContext(bounds.Dx())

	t.Run("ApplyThresholdInto", func(t *testing.T) {
		src := randomMask(bounds, 5)
		want := ApplyThreshold(src, testThreshold)
		ApplyThresholdInto(src, testThreshold, src)
		if !bytes.Equal(src.Pix, want.Pix) {
			t.Error("in-place threshold differs")
		}
	})

	t.Run("DistanceToIntensityInto", func(t *testing.T) {
		src := randomMask(bounds, 6)
		want := DistanceToIntensity(src, testGamma)
		DistanceToIntensityInto(src, testGamma, src)
		if !bytes.Equal(src.Pix, want.Pix) {
			t.Error("in-place intensity mapping differs")
		}
	})

	t.Run("EuclideanDistanceTransformIntoWithContext", func(t *testing.T) {
		src := randomMask(bounds, 7)
		want := EuclideanDistanceTransform(src, testRadius)
		EuclideanDistanceTransformIntoWithContext(src, testRadius, ctx, src)
		if !bytes.Equal(src.Pix, want.Pix) {
			t.Error("in-place distance transform differs")
		}
	})

	// The pipeline writes the noisy mask over the distance map it just consumed.
	t.Run("ApplyNoiseToMaskAdaptiveInto", func(t *testing.T) {
		blurred := randomMask(bounds, 8)
		noise := randomMask(bounds, 9)
		distMap := randomMask(bounds, 10)

		want := ApplyNoiseToMaskAdaptive(blurred, noise, distMap, testStrength, testMinDist, testMaxDist)
		ApplyNoiseToMaskAdaptiveInto(blurred, noise, distMap, testStrength, testMinDist, testMaxDist, distMap)
		if !bytes.Equal(distMap.Pix, want.Pix) {
			t.Error("in-place adaptive noise differs")
		}
	})
}

func TestIntoVariantsDoNotAllocate(t *testing.T) {
	bounds := image.Rect(0, 0, 64, 64)

	for _, tc := range intoCases() {
		t.Run(tc.name, func(t *testing.T) {
			src := randomMask(bounds, 11)
			noise := randomMask(bounds, 12)
			ctx := NewDistanceContext(bounds.Dx())
			dst := image.NewGray(bounds)

			// Warm the context so its grow-only buffers are already big enough.
			tc.run(src, noise, ctx, dst)

			if n := testing.AllocsPerRun(20, func() {
				tc.run(src, noise, ctx, dst)
			}); n != 0 {
				t.Errorf("got %v allocations per run, want 0", n)
			}
		})
	}
}

// TestCreateDistanceEdgeMaskIntoLeavesTheFringeAlone pins the one *Into helper whose
// destination is allowed to be larger than its source: the watercolor processor keeps a
// single tile-sized edge-mask buffer and paints masks of any size through it.
//
// Both passes must be bounded by the mask. The distance transform's writes are clipped to
// it regardless, so an intensity pass running over the whole destination would read back
// whatever the previous layer left outside the mask and fold it into the result.
func TestCreateDistanceEdgeMaskIntoLeavesTheFringeAlone(t *testing.T) {
	const fill = 0xAA

	small := image.Rect(0, 0, 32, 32)
	large := image.Rect(0, 0, 64, 64)

	src := randomMask(small, 21)
	ctx := NewDistanceContext(large.Dx())

	exact := image.NewGray(small)
	CreateDistanceEdgeMaskIntoWithContext(src, testRadius, testGamma, ctx, exact)

	oversized := image.NewGray(large)
	for i := range oversized.Pix {
		oversized.Pix[i] = fill
	}
	CreateDistanceEdgeMaskIntoWithContext(src, testRadius, testGamma, ctx, oversized)

	for y := large.Min.Y; y < large.Max.Y; y++ {
		for x := large.Min.X; x < large.Max.X; x++ {
			got := oversized.GrayAt(x, y).Y
			if image.Pt(x, y).In(small) {
				if want := exact.GrayAt(x, y).Y; got != want {
					t.Fatalf("inside the mask at (%d,%d): got %d, want %d", x, y, got, want)
				}

				continue
			}
			if got != fill {
				t.Fatalf("fringe at (%d,%d) was rewritten: got %d, want %d", x, y, got, fill)
			}
		}
	}
}

// TestExtractAlphaMaskInto covers the one *Into variant whose source is not a Gray.
func TestExtractAlphaMaskInto(t *testing.T) {
	bounds := image.Rect(0, 0, 32, 32)
	src := image.NewNRGBA(bounds)
	rng := rand.New(rand.NewPCG(13, 17))
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			src.SetNRGBA(x, y, color.NRGBA{R: 10, G: 20, B: 30, A: uint8(rng.UintN(256))})
		}
	}

	want := ExtractAlphaMask(src)
	got := image.NewGray(bounds)
	for i := range got.Pix {
		got.Pix[i] = 0xAA
	}
	ExtractAlphaMaskInto(src, got)

	if !bytes.Equal(got.Pix, want.Pix) {
		t.Error("ExtractAlphaMaskInto differs from ExtractAlphaMask")
	}
}
