package mask

import (
	"bytes"
	"image"
	"image/color"
	"math"
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

// --- distance transform ---------------------------------------------------------

func referenceDetectEdgePixels(mask *image.Gray, isEdge []bool, width, height int) {
	bounds := mask.Bounds()

	for i := 0; i < width*height; i++ {
		isEdge[i] = false
	}

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			val := mask.GrayAt(bounds.Min.X+x, bounds.Min.Y+y).Y
			if val == 0 {
				continue
			}

			isEdgePixel := false
			if x > 0 && mask.GrayAt(bounds.Min.X+x-1, bounds.Min.Y+y).Y == 0 {
				isEdgePixel = true
			}
			if x < width-1 && mask.GrayAt(bounds.Min.X+x+1, bounds.Min.Y+y).Y == 0 {
				isEdgePixel = true
			}
			if y > 0 && mask.GrayAt(bounds.Min.X+x, bounds.Min.Y+y-1).Y == 0 {
				isEdgePixel = true
			}
			if y < height-1 && mask.GrayAt(bounds.Min.X+x, bounds.Min.Y+y+1).Y == 0 {
				isEdgePixel = true
			}

			isEdge[y*width+x] = isEdgePixel
		}
	}
}

func referenceInitDistanceField(
	mask *image.Gray, temp []float64, isEdge []bool, width, height int, infinity float64,
) {
	bounds := mask.Bounds()

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			idx := y*width + x
			val := mask.GrayAt(bounds.Min.X+x, bounds.Min.Y+y).Y

			if val > 0 && isEdge[idx] {
				temp[idx] = 0.0

				continue
			}

			temp[idx] = infinity
		}
	}
}

func referenceNormalizeDistanceFieldInto(
	mask *image.Gray, temp []float64, width, height int, maxDistance, infinity float64, output *image.Gray,
) {
	bounds := mask.Bounds()
	maxDistSq := maxDistance * maxDistance

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			distSq := temp[y*width+x]
			val := mask.GrayAt(bounds.Min.X+x, bounds.Min.Y+y).Y

			output.SetGray(
				bounds.Min.X+x, bounds.Min.Y+y,
				color.Gray{Y: normalizedDistanceValue(val, distSq, maxDistSq, maxDistance, infinity)},
			)
		}
	}
}

func referenceDistanceToIntensityIntoRect(
	distMask *image.Gray, gamma float64, output *image.Gray, bounds image.Rectangle,
) {
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			distNorm := float64(distMask.GrayAt(x, y).Y) / 255.0
			base := math.Max(0, 1.0-distNorm)
			intensity := math.Pow(base, gamma)
			output.SetGray(x, y, color.Gray{Y: uint8(255.0 * (1.0 - intensity))})
		}
	}
}

func TestDetectEdgePixelsMatchesReference(t *testing.T) {
	for _, bounds := range testBounds() {
		mask := randomMask(bounds, 18)
		w, h := bounds.Dx(), bounds.Dy()

		got := make([]bool, w*h)
		want := make([]bool, w*h)
		for i := range got {
			// A dirty buffer: the rewrite dropped the caller's clearing pass, so every
			// entry has to be assigned rather than left alone.
			got[i] = true
		}

		detectEdgePixels(mask, got, w, h)
		referenceDetectEdgePixels(mask, want, w, h)

		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%v: edge detection differs at index %d", bounds, i)
			}
		}
	}
}

func TestInitDistanceFieldMatchesReference(t *testing.T) {
	for _, bounds := range testBounds() {
		mask := randomMask(bounds, 19)
		w, h := bounds.Dx(), bounds.Dy()

		isEdge := make([]bool, w*h)
		detectEdgePixels(mask, isEdge, w, h)

		got := make([]float64, w*h)
		want := make([]float64, w*h)

		initDistanceField(mask, got, isEdge, w, h, 1234.5)
		referenceInitDistanceField(mask, want, isEdge, w, h, 1234.5)

		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%v: distance seeding differs at index %d", bounds, i)
			}
		}
	}
}

func TestNormalizeDistanceFieldIntoMatchesReference(t *testing.T) {
	for _, bounds := range testBounds() {
		mask := randomMask(bounds, 20)
		w, h := bounds.Dx(), bounds.Dy()

		// A plausible field: some pixels at zero, some past the cap, some in between.
		temp := make([]float64, w*h)
		for i := range temp {
			temp[i] = float64((i * 37) % 400)
		}

		const (
			maxDistance = 12.0
			infinity    = maxDistance * maxDistance * 2.0
		)

		runIntoPair(t, "normalizeDistanceFieldInto", bounds,
			func(dst *image.Gray) {
				normalizeDistanceFieldInto(mask, temp, w, maxDistance, infinity, dst)
			},
			func(dst *image.Gray) {
				referenceNormalizeDistanceFieldInto(mask, temp, w, h, maxDistance, infinity, dst)
			},
		)
	}
}

func TestDistanceToIntensityIntoRectMatchesReference(t *testing.T) {
	for _, bounds := range testBounds() {
		src := randomMask(bounds, 21)

		runIntoPair(t, "distanceToIntensityIntoRect", bounds,
			func(dst *image.Gray) { distanceToIntensityIntoRect(src, testGamma, dst, bounds) },
			func(dst *image.Gray) { referenceDistanceToIntensityIntoRect(src, testGamma, dst, bounds) },
		)
	}
}

// The edge mask is the one helper whose destination is routinely a different size from
// its source, so run the whole of it over every destination shape.
func TestCreateDistanceEdgeMaskIntoMatchesReference(t *testing.T) {
	for _, bounds := range testBounds() {
		src := randomMask(bounds, 22)
		ctx := NewDistanceContext(max(bounds.Dx(), bounds.Dy()))

		for _, dstBounds := range destinationBounds(bounds) {
			got := filledGray(dstBounds, 0x5A)
			want := filledGray(dstBounds, 0x5A)

			CreateDistanceEdgeMaskIntoWithContext(src, testRadius, testGamma, ctx, got)

			// The reference runs the same two passes through the accessor-based loops.
			w, h := bounds.Dx(), bounds.Dy()
			isEdge := make([]bool, w*h)
			temp := make([]float64, w*h)
			infinity := testRadius * testRadius * 2.0

			referenceDetectEdgePixels(src, isEdge, w, h)
			referenceInitDistanceField(src, temp, isEdge, w, h, infinity)
			distanceTransformRows(temp, ctx, w, h)
			distanceTransformColumns(temp, ctx, w, h)
			referenceNormalizeDistanceFieldInto(src, temp, w, h, testRadius, infinity, want)
			referenceDistanceToIntensityIntoRect(want, testGamma, want, bounds)

			if !bytes.Equal(got.Pix, want.Pix) {
				t.Errorf("src %v dst %v: edge mask differs from the reference implementation",
					bounds, dstBounds)
			}
		}
	}
}

// --- edge darkening -------------------------------------------------------------

func referenceApplySoftEdgeMaskInto(base *image.NRGBA, mask *image.Gray, strength float64, dst *image.NRGBA) {
	if strength < 0 {
		strength = 0
	}
	if strength > 1 {
		strength = 1
	}

	bounds := base.Bounds()

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			src := base.NRGBAAt(x, y)
			maskVal := int(mask.GrayAt(x, y).Y)

			maskSquared := maskVal * maskVal
			invMaskSquared := 65025 - maskSquared
			effectInt := int(float64(invMaskSquared) * strength)

			h, s, l := rgbToHSL(src.R, src.G, src.B)

			darkening := 65025 - effectInt
			lNew := uint8((int(l) * darkening) / 65025)

			r, g, b := hslToRGB(h, s, lNew)

			dst.SetNRGBA(x, y, color.NRGBA{R: r, G: g, B: b, A: src.A})
		}
	}
}

func filledNRGBA(bounds image.Rectangle, fill byte) *image.NRGBA {
	img := image.NewNRGBA(bounds)
	for i := range img.Pix {
		img.Pix[i] = fill
	}

	return img
}

func TestApplySoftEdgeMaskIntoMatchesReference(t *testing.T) {
	for _, bounds := range testBounds() {
		base := randomNRGBAImage(bounds, 23)
		edge := randomMask(bounds, 24)

		for _, dstBounds := range destinationBounds(bounds) {
			got := filledNRGBA(dstBounds, 0x3C)
			want := filledNRGBA(dstBounds, 0x3C)

			ApplySoftEdgeMaskInto(base, edge, testStrength, got)
			referenceApplySoftEdgeMaskInto(base, edge, testStrength, want)

			if !bytes.Equal(got.Pix, want.Pix) {
				t.Errorf("src %v dst %v: soft edge mask differs from the reference implementation",
					bounds, dstBounds)
			}
		}
	}
}

// The lightness round trip has to run even at full mask (no darkening), because
// rgbToHSL/hslToRGB is lossy and skipping it would move pixels.
func TestApplySoftEdgeMaskIntoKeepsTheLossyRoundTrip(t *testing.T) {
	bounds := image.Rect(0, 0, 64, 64)
	base := randomNRGBAImage(bounds, 25)

	white := image.NewGray(bounds)
	for i := range white.Pix {
		white.Pix[i] = 255
	}

	got := image.NewNRGBA(bounds)
	want := image.NewNRGBA(bounds)

	ApplySoftEdgeMaskInto(base, white, 1.0, got)
	referenceApplySoftEdgeMaskInto(base, white, 1.0, want)

	if !bytes.Equal(got.Pix, want.Pix) {
		t.Error("a fully white mask no longer round-trips through HSL the way it used to")
	}
	if bytes.Equal(got.Pix, base.Pix) {
		t.Skip("the round trip happens to be lossless for this input; the test proves nothing")
	}
}

// A mask smaller than the base used to read zero outside itself, which darkens rather
// than panicking.
func TestApplySoftEdgeMaskIntoWithShortMask(t *testing.T) {
	bounds := image.Rect(0, 0, 64, 64)
	base := randomNRGBAImage(bounds, 26)
	short := randomMask(image.Rect(0, 0, 30, 20), 27)

	got := image.NewNRGBA(bounds)
	want := image.NewNRGBA(bounds)

	ApplySoftEdgeMaskInto(base, short, testStrength, got)
	referenceApplySoftEdgeMaskInto(base, short, testStrength, want)

	if !bytes.Equal(got.Pix, want.Pix) {
		t.Error("a mask smaller than the base no longer reads zero outside itself")
	}
}
