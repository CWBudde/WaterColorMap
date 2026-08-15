package mask

import (
	"bytes"
	"image"
	"image/color"
	"testing"
)

// 5.11.4 rewrote these kernels to index Pix directly instead of calling GrayAt/SetGray
// per pixel. TestIntoVariantsMatchAllocatingVariants cannot catch a mistake in that
// rewrite - both sides of it wrap the same kernel - so this file keeps the previous,
// accessor-based loop bodies verbatim as references and compares byte for byte.
//
// Two properties matter beyond "same pixels":
//
//   - a destination larger than the source leaves its fringe untouched,
//   - a destination smaller than the source clips instead of panicking, because that is
//     what SetGray used to do.
//
// Both are exercised below by prefilling the destination and comparing whole Pix slices.

func referenceExtractAlphaMaskInto(img image.Image, dst *image.Gray) {
	bounds := img.Bounds()

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			dst.SetGray(x, y, color.Gray{Y: getAlpha(img, x, y)})
		}
	}
}

func referenceMaxMasks(bounds image.Rectangle, masks ...*image.Gray) *image.Gray {
	out := image.NewGray(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			var maxVal uint8
			for _, m := range masks {
				if v := m.GrayAt(x, y).Y; v > maxVal {
					maxVal = v
				}
			}
			out.SetGray(x, y, color.Gray{Y: maxVal})
		}
	}

	return out
}

func referenceSubtractMask(a, b *image.Gray) *image.Gray {
	bounds := a.Bounds()
	out := image.NewGray(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			av := int(a.GrayAt(x, y).Y)
			bv := int(b.GrayAt(x, y).Y)
			result := av
			if inv := 255 - bv; inv < result {
				result = inv
			}
			out.SetGray(x, y, color.Gray{Y: uint8(result)})
		}
	}

	return out
}

func referenceMinMask(a, b *image.Gray) *image.Gray {
	bounds := a.Bounds()
	out := image.NewGray(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			av := a.GrayAt(x, y).Y
			if bv := b.GrayAt(x, y).Y; bv < av {
				av = bv
			}
			out.SetGray(x, y, color.Gray{Y: av})
		}
	}

	return out
}

func referenceInvertMaskInto(m, dst *image.Gray) {
	bounds := m.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			dst.SetGray(x, y, color.Gray{Y: 255 - m.GrayAt(x, y).Y})
		}
	}
}

func referenceApplyThresholdInto(mask *image.Gray, threshold uint8, dst *image.Gray) {
	bounds := mask.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if mask.GrayAt(x, y).Y >= threshold {
				dst.SetGray(x, y, color.Gray{Y: 255})
			} else {
				dst.SetGray(x, y, color.Gray{Y: 0})
			}
		}
	}
}

func referenceThresholdWithAntialiasInto(maskImg *image.Gray, threshold uint8, invert bool, dst *image.Gray) {
	bounds := maskImg.Bounds()

	const transitionWidth = 20

	lower := float64(int(threshold) - transitionWidth)
	upper := float64(int(threshold) + transitionWidth)

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			t := smoothstep(lower, upper, float64(maskImg.GrayAt(x, y).Y))
			if invert {
				t = 1.0 - t
			}
			dst.SetGray(x, y, color.Gray{Y: uint8(t * 255.0)})
		}
	}
}

func referenceApplyNoiseInto(
	maskImg, noise, distanceMap *image.Gray, strength, minDist, maxDist float64, dst *image.Gray,
) {
	bounds := maskImg.Bounds()
	noiseBounds := noise.Bounds()

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			maskVal := float64(maskImg.GrayAt(x, y).Y)

			noiseScale := 1.0
			if distanceMap != nil {
				noiseScale = smoothstep(minDist, maxDist, float64(distanceMap.GrayAt(x, y).Y))
			}

			nx := (x - bounds.Min.X) % noiseBounds.Dx()
			ny := (y - bounds.Min.Y) % noiseBounds.Dy()
			noiseVal := float64(noise.GrayAt(noiseBounds.Min.X+nx, noiseBounds.Min.Y+ny).Y)

			noiseDelta := (noiseVal - 128.0) * strength * noiseScale
			combined := maskVal + noiseDelta

			if combined < 0 {
				combined = 0
			}
			if combined > 255 {
				combined = 255
			}

			dst.SetGray(x, y, color.Gray{Y: uint8(combined)})
		}
	}
}

// filledGray returns a Gray of the given bounds with every byte set to fill, so that
// anything a kernel fails to write stands out in the comparison.
func filledGray(bounds image.Rectangle, fill byte) *image.Gray {
	m := image.NewGray(bounds)
	for i := range m.Pix {
		m.Pix[i] = fill
	}

	return m
}

// destinationBounds returns, for a source rectangle, the destinations a kernel has to
// cope with: the same rectangle, a larger one, a smaller one, and an offset one that
// only partly overlaps.
func destinationBounds(src image.Rectangle) []image.Rectangle {
	return []image.Rectangle{
		src,
		image.Rect(src.Min.X-3, src.Min.Y-2, src.Max.X+4, src.Max.Y+5),
		image.Rect(src.Min.X, src.Min.Y, src.Min.X+max(1, src.Dx()/2), src.Min.Y+max(1, src.Dy()/2)),
		image.Rect(src.Min.X+1, src.Min.Y+2, src.Max.X+1, src.Max.Y+2),
	}
}

// runIntoPair applies both a kernel and its reference to identically prefilled
// destinations and reports whether the results agree.
func runIntoPair(t *testing.T, name string, srcBounds image.Rectangle, run, reference func(dst *image.Gray)) {
	t.Helper()

	for _, dstBounds := range destinationBounds(srcBounds) {
		got := filledGray(dstBounds, 0xAA)
		want := filledGray(dstBounds, 0xAA)

		run(got)
		reference(want)

		if !bytes.Equal(got.Pix, want.Pix) {
			t.Errorf("%s: src %v dst %v differs from the reference implementation", name, srcBounds, dstBounds)
		}
	}
}

func TestExtractAlphaMaskIntoMatchesReference(t *testing.T) {
	for _, bounds := range testBounds() {
		// One case per concrete type the specialised loops handle, plus a type that
		// has to fall through to the generic branch.
		sources := map[string]image.Image{
			"NRGBA":   randomNRGBAImage(bounds, 1),
			"RGBA":    toRGBA(randomNRGBAImage(bounds, 2)),
			"Gray":    randomMask(bounds, 3),
			"Alpha":   toAlpha(randomMask(bounds, 4)),
			"generic": opaqueGray{randomMask(bounds, 5)},
		}

		for name, src := range sources {
			runIntoPair(t, "ExtractAlphaMaskInto/"+name, bounds,
				func(dst *image.Gray) { ExtractAlphaMaskInto(src, dst) },
				func(dst *image.Gray) { referenceExtractAlphaMaskInto(src, dst) },
			)
		}
	}
}

func TestInvertMaskIntoMatchesReference(t *testing.T) {
	for _, bounds := range testBounds() {
		src := randomMask(bounds, 6)
		runIntoPair(t, "InvertMaskInto", bounds,
			func(dst *image.Gray) { InvertMaskInto(src, dst) },
			func(dst *image.Gray) { referenceInvertMaskInto(src, dst) },
		)
	}
}

func TestApplyThresholdIntoMatchesReference(t *testing.T) {
	for _, bounds := range testBounds() {
		src := randomMask(bounds, 7)
		runIntoPair(t, "ApplyThresholdInto", bounds,
			func(dst *image.Gray) { ApplyThresholdInto(src, testThreshold, dst) },
			func(dst *image.Gray) { referenceApplyThresholdInto(src, testThreshold, dst) },
		)
	}
}

func TestThresholdWithAntialiasIntoMatchesReference(t *testing.T) {
	for _, bounds := range testBounds() {
		src := randomMask(bounds, 8)

		runIntoPair(t, "ApplyThresholdWithAntialiasInto", bounds,
			func(dst *image.Gray) { ApplyThresholdWithAntialiasInto(src, testThreshold, dst) },
			func(dst *image.Gray) { referenceThresholdWithAntialiasInto(src, testThreshold, false, dst) },
		)
		runIntoPair(t, "ApplyThresholdWithAntialiasAndInvertInto", bounds,
			func(dst *image.Gray) { ApplyThresholdWithAntialiasAndInvertInto(src, testThreshold, dst) },
			func(dst *image.Gray) { referenceThresholdWithAntialiasInto(src, testThreshold, true, dst) },
		)
	}
}

func TestApplyNoiseIntoMatchesReference(t *testing.T) {
	for _, bounds := range testBounds() {
		src := randomMask(bounds, 9)
		dist := randomMask(bounds, 10)

		// A noise texture that neither matches the mask's size nor its origin, so the
		// wrap-around indexing is actually exercised.
		noise := randomMask(image.Rect(4, 6, 4+13, 6+7), 11)

		runIntoPair(t, "ApplyNoiseToMaskInto", bounds,
			func(dst *image.Gray) { ApplyNoiseToMaskInto(src, noise, testStrength, dst) },
			func(dst *image.Gray) { referenceApplyNoiseInto(src, noise, nil, testStrength, 0, 0, dst) },
		)
		runIntoPair(t, "ApplyNoiseToMaskAdaptiveInto", bounds,
			func(dst *image.Gray) {
				ApplyNoiseToMaskAdaptiveInto(src, noise, dist, testStrength, testMinDist, testMaxDist, dst)
			},
			func(dst *image.Gray) {
				referenceApplyNoiseInto(src, noise, dist, testStrength, testMinDist, testMaxDist, dst)
			},
		)
	}
}

// A distance map narrower than the mask used to read zero outside itself, because that
// is what GrayAt returns. The row-slice loop has to keep doing so.
func TestApplyNoiseIntoWithShortDistanceMap(t *testing.T) {
	bounds := image.Rect(0, 0, 64, 64)
	src := randomMask(bounds, 12)
	noise := randomMask(image.Rect(0, 0, 16, 16), 13)
	dist := randomMask(image.Rect(0, 0, 20, 30), 14)

	got := image.NewGray(bounds)
	want := image.NewGray(bounds)

	ApplyNoiseToMaskAdaptiveInto(src, noise, dist, testStrength, testMinDist, testMaxDist, got)
	referenceApplyNoiseInto(src, noise, dist, testStrength, testMinDist, testMaxDist, want)

	if !bytes.Equal(got.Pix, want.Pix) {
		t.Error("a distance map smaller than the mask no longer reads zero outside itself")
	}
}

func TestMaskCombinatorsMatchReference(t *testing.T) {
	for _, bounds := range testBounds() {
		a := randomMask(bounds, 15)
		b := randomMask(bounds, 16)
		c := randomMask(bounds, 17)

		if got, want := MaxMasks(a, b, c), referenceMaxMasks(bounds, a, b, c); !bytes.Equal(got.Pix, want.Pix) {
			t.Errorf("%v: MaxMasks differs from the reference implementation", bounds)
		}
		if got, want := MinMask(a, b), referenceMinMask(a, b); !bytes.Equal(got.Pix, want.Pix) {
			t.Errorf("%v: MinMask differs from the reference implementation", bounds)
		}
		if got, want := SubtractMask(a, b), referenceSubtractMask(a, b); !bytes.Equal(got.Pix, want.Pix) {
			t.Errorf("%v: SubtractMask differs from the reference implementation", bounds)
		}
	}
}

// opaqueGray hides a Gray's concrete type so getAlpha's generic branch is taken.
type opaqueGray struct{ img *image.Gray }

func (o opaqueGray) ColorModel() color.Model { return o.img.ColorModel() }
func (o opaqueGray) Bounds() image.Rectangle { return o.img.Bounds() }
func (o opaqueGray) At(x, y int) color.Color { return o.img.At(x, y) }

func randomNRGBAImage(bounds image.Rectangle, seed uint64) *image.NRGBA {
	src := randomMask(bounds, seed)
	img := image.NewNRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			v := src.GrayAt(x, y).Y
			img.SetNRGBA(x, y, color.NRGBA{R: v, G: 255 - v, B: v / 2, A: v})
		}
	}

	return img
}

// toRGBA copies an NRGBA into an RGBA without touching the alpha channel, which is all
// getAlpha reads. Premultiplying the colour channels would be beside the point.
func toRGBA(src *image.NRGBA) *image.RGBA {
	dst := image.NewRGBA(src.Bounds())
	copy(dst.Pix, src.Pix)

	return dst
}

func toAlpha(src *image.Gray) *image.Alpha {
	dst := image.NewAlpha(src.Bounds())
	copy(dst.Pix, src.Pix)

	return dst
}
