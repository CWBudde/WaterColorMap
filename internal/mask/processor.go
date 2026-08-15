package mask

import (
	"image"
	"image/color"
	"math"

	"github.com/aquilax/go-perlin"
)

// getAlpha extracts the alpha value (0-255) from an image at the given coordinates.
// Uses type assertions to avoid interface boxing allocations when possible.
func getAlpha(img image.Image, x, y int) uint8 {
	switch src := img.(type) {
	case *image.NRGBA:
		return src.NRGBAAt(x, y).A
	case *image.RGBA:
		return src.RGBAAt(x, y).A
	case *image.Gray:
		return src.GrayAt(x, y).Y
	case *image.Alpha:
		return src.AlphaAt(x, y).A
	default:
		// Fallback to interface method (causes allocation)
		_, _, _, a := img.At(x, y).RGBA()
		return uint8(a >> 8)
	}
}

// ExtractAlphaMask converts an image's alpha channel into a grayscale mask (0-255).
// This preserves anti-aliased edges from the renderer and is suitable for alpha-only
// mask composition.
func ExtractAlphaMask(img image.Image) *image.Gray {
	if img == nil {
		return nil
	}

	out := image.NewGray(img.Bounds())
	ExtractAlphaMaskInto(img, out)

	return out
}

// grayRow returns the w bytes of m starting at (x, y), or nil when that run is not
// wholly inside m. A nil row tells the caller to fall back to GrayAt, which reads zero
// outside the image - the behaviour these kernels had before they indexed Pix directly.
func grayRow(m *image.Gray, x, y, w int) []uint8 {
	if m == nil || !image.Rect(x, y, x+w, y+1).In(m.Bounds()) {
		return nil
	}

	off := m.PixOffset(x, y)

	return m.Pix[off : off+w : off+w]
}

// writeRect is the rectangle a kernel iterating over src may write into dst.
//
// The accessor-based loops these kernels grew out of relied on SetGray silently
// dropping out-of-bounds writes, which is how a destination smaller than its source
// stayed safe. Direct Pix indexing would panic instead, so the clipping is done once
// here rather than per pixel.
func writeRect(src, dst image.Rectangle) image.Rectangle {
	return src.Intersect(dst)
}

// ExtractAlphaMaskInto is ExtractAlphaMask writing into a caller-owned destination,
// which must have the same bounds as img. Every pixel in bounds is written, so a
// recycled destination needs no clearing.
func ExtractAlphaMaskInto(img image.Image, dst *image.Gray) {
	if img == nil || dst == nil {
		return
	}

	r := writeRect(img.Bounds(), dst.Bounds())
	w := r.Dx()
	if w == 0 {
		return
	}

	// The type switch sits outside the inner loop, not inside getAlpha: layer images
	// come from image.Decode, so the concrete type varies, but it never varies between
	// two pixels of the same image.
	for y := r.Min.Y; y < r.Max.Y; y++ {
		dstRow := grayRow(dst, r.Min.X, y, w)

		switch src := img.(type) {
		case *image.NRGBA:
			off := src.PixOffset(r.Min.X, y)
			row := src.Pix[off : off+4*w]
			for i := range dstRow {
				dstRow[i] = row[4*i+3]
			}
		case *image.RGBA:
			off := src.PixOffset(r.Min.X, y)
			row := src.Pix[off : off+4*w]
			for i := range dstRow {
				dstRow[i] = row[4*i+3]
			}
		case *image.Gray:
			off := src.PixOffset(r.Min.X, y)
			copy(dstRow, src.Pix[off:off+w])
		case *image.Alpha:
			off := src.PixOffset(r.Min.X, y)
			copy(dstRow, src.Pix[off:off+w])
		default:
			for i := range dstRow {
				dstRow[i] = getAlpha(img, r.Min.X+i, y)
			}
		}
	}
}

// NewEmptyMask returns an all-zero grayscale mask of the given bounds.
func NewEmptyMask(bounds image.Rectangle) *image.Gray {
	return image.NewGray(bounds)
}

// MaxMask computes a pixel-wise max of two masks (union/or for alpha masks).
// Masks must have identical bounds.
func MaxMask(a, b *image.Gray) *image.Gray {
	return MaxMasks(a, b)
}

// MaxMasks computes a pixel-wise max of multiple masks (union/or for alpha masks).
// All masks must have identical bounds. Nil masks are skipped.
// Returns nil if no valid masks are provided.
func MaxMasks(masks ...*image.Gray) *image.Gray {
	// Filter out nil masks and find bounds
	var validMasks []*image.Gray
	var bounds image.Rectangle
	for _, m := range masks {
		if m != nil {
			if len(validMasks) == 0 {
				bounds = m.Bounds()
			} else if m.Bounds() != bounds {
				return nil // Mismatched bounds
			}
			validMasks = append(validMasks, m)
		}
	}

	if len(validMasks) == 0 {
		return nil
	}
	if len(validMasks) == 1 {
		// Return a copy to maintain immutability
		out := image.NewGray(bounds)
		copy(out.Pix, validMasks[0].Pix)
		return out
	}

	// out starts zeroed, so folding each mask in turn gives the same maximum as
	// scanning every mask at each pixel - one pass per mask instead of one indexed
	// read per mask per pixel.
	out := image.NewGray(bounds)
	w := bounds.Dx()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		outRow := grayRow(out, bounds.Min.X, y, w)
		for _, m := range validMasks {
			srcRow := grayRow(m, bounds.Min.X, y, w)
			for i, v := range srcRow {
				if v > outRow[i] {
					outRow[i] = v
				}
			}
		}
	}
	return out
}

// SubtractMask subtracts mask b from mask a (a AND NOT b).
// Result is min(a, 255-b) at each pixel. Masks must have identical bounds.
func SubtractMask(a, b *image.Gray) *image.Gray {
	if a == nil {
		return nil
	}
	if b == nil {
		// Nothing to subtract, return copy of a
		out := image.NewGray(a.Bounds())
		copy(out.Pix, a.Pix)
		return out
	}
	if a.Bounds() != b.Bounds() {
		return nil
	}

	bounds := a.Bounds()
	out := image.NewGray(bounds)
	w := bounds.Dx()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		aRow := grayRow(a, bounds.Min.X, y, w)
		bRow := grayRow(b, bounds.Min.X, y, w)
		outRow := grayRow(out, bounds.Min.X, y, w)
		for i, av := range aRow {
			// AND NOT: where b is opaque, result is transparent; partial
			// coverage in b caps a instead of eroding it by the full amount.
			result := av
			if inv := 255 - bRow[i]; inv < result {
				result = inv
			}
			outRow[i] = result
		}
	}
	return out
}

// MinMask computes a pixel-wise min of two masks (intersection/and for alpha masks).
// Masks must have identical bounds.
func MinMask(a, b *image.Gray) *image.Gray {
	if a == nil || b == nil {
		return nil
	}
	if a.Bounds() != b.Bounds() {
		return nil
	}

	bounds := a.Bounds()
	out := image.NewGray(bounds)
	w := bounds.Dx()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		aRow := grayRow(a, bounds.Min.X, y, w)
		bRow := grayRow(b, bounds.Min.X, y, w)
		outRow := grayRow(out, bounds.Min.X, y, w)
		for i, av := range aRow {
			if bv := bRow[i]; bv < av {
				av = bv
			}
			outRow[i] = av
		}
	}
	return out
}

// MinMaskRGBA applies a grayscale mask to an NRGBA image by taking the minimum
// of the image's alpha channel and the mask value at each pixel.
// RGB values are preserved; only alpha is modified.
func MinMaskRGBA(img *image.NRGBA, mask *image.Gray) *image.NRGBA {
	if img == nil || mask == nil {
		return nil
	}
	if img.Bounds() != mask.Bounds() {
		return nil
	}

	bounds := img.Bounds()
	out := image.NewNRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			c := img.NRGBAAt(x, y)
			maskVal := mask.GrayAt(x, y).Y
			alpha := c.A
			if maskVal < alpha {
				alpha = maskVal
			}
			out.SetNRGBA(x, y, color.NRGBA{
				R: c.R,
				G: c.G,
				B: c.B,
				A: alpha,
			})
		}
	}
	return out
}

// InvertMask inverts a grayscale mask (Y -> 255-Y).
func InvertMask(m *image.Gray) *image.Gray {
	if m == nil {
		return nil
	}
	bounds := m.Bounds()
	out := image.NewGray(bounds)
	InvertMaskInto(m, out)
	return out
}

// InvertMaskInto inverts a grayscale mask into an existing destination buffer.
// This avoids allocation when the caller can reuse a buffer.
func InvertMaskInto(m *image.Gray, dst *image.Gray) {
	if m == nil || dst == nil {
		return
	}
	r := writeRect(m.Bounds(), dst.Bounds())
	w := r.Dx()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		srcRow := grayRow(m, r.Min.X, y, w)
		dstRow := grayRow(dst, r.Min.X, y, w)
		for i, v := range srcRow {
			dstRow[i] = 255 - v
		}
	}
}

// ExtractBinaryMask converts a colored layer image into a binary mask.
// Pixels with any non-transparent color become white (255), transparent pixels become black (0).
func ExtractBinaryMask(img image.Image) *image.Gray {
	bounds := img.Bounds()
	mask := image.NewGray(bounds)

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if getAlpha(img, x, y) > 0 {
				mask.SetGray(x, y, color.Gray{Y: 255}) // White - feature present
			} else {
				mask.SetGray(x, y, color.Gray{Y: 0}) // Black - no feature
			}
		}
	}

	return mask
}

// GeneratePerlinNoise generates a grayscale Perlin noise texture.
// width, height: dimensions of the output image
// scale: controls the frequency of the noise (smaller = more detail)
// seed: random seed for deterministic noise generation
func GeneratePerlinNoise(width, height int, scale float64, seed int64) *image.Gray {
	return GeneratePerlinNoiseWithOffset(width, height, scale, seed, 0, 0)
}

// GeneratePerlinNoiseWithOffset generates Perlin noise aligned to a global grid.
// Offsets allow adjacent tiles to sample the same underlying noise field to avoid seams.
func GeneratePerlinNoiseWithOffset(
	width, height int,
	scale float64,
	seed int64,
	offsetX, offsetY int,
) *image.Gray {
	// Create Perlin noise generator with octaves, alpha, and beta parameters
	// alpha: persistence (how much each octave contributes)
	// beta: lacunarity (frequency multiplier between octaves)
	// n: number of octaves
	p := perlin.NewPerlin(2.0, 2.0, 3, seed)

	noise := image.NewGray(image.Rect(0, 0, width, height))

	for y := 0; y < height; y++ {
		row := noise.Pix[y*noise.Stride:][:width]
		for x := 0; x < width; x++ {
			// Sample Perlin noise at normalized coordinates
			nx := float64(offsetX+x) / scale
			ny := float64(offsetY+y) / scale

			// Get noise value (range approximately -1 to 1)
			val := p.Noise2D(nx, ny)

			// Normalize to 0-255 range
			// Apply a gentler mapping to get better distribution
			normalized := (val + 1.0) / 2.0
			gray := uint8(math.Max(0, math.Min(255, normalized*255)))

			row[x] = gray
		}
	}

	return noise
}

// smoothstep performs smooth Hermite interpolation between 0 and 1.
// Returns 0 if x <= edge0, 1 if x >= edge1, otherwise smooth interpolation.
func smoothstep(edge0, edge1, x float64) float64 {
	if x <= edge0 {
		return 0
	}
	if x >= edge1 {
		return 1
	}
	t := (x - edge0) / (edge1 - edge0)
	return t * t * (3 - 2*t)
}

// ApplyNoiseToMaskAdaptive overlays Perlin noise onto a blurred mask with distance-based attenuation.
// For thin structures (low distance values), noise is reduced to prevent fragmentation.
// distanceMap: euclidean distance transform of the mask (pixel values represent distance in pixels)
// strength: base noise strength (0.0 = no noise, 1.0 = full noise)
// minDist: distance below which noise is minimal
// maxDist: distance above which noise is at full strength
func ApplyNoiseToMaskAdaptive(maskImg, noise, distanceMap *image.Gray, strength float64, minDist, maxDist float64) *image.Gray {
	dst := image.NewGray(maskImg.Bounds())
	applyNoiseInto(maskImg, noise, distanceMap, strength, minDist, maxDist, dst)

	return dst
}

// ApplyNoiseToMaskAdaptiveInto is ApplyNoiseToMaskAdaptive writing into a caller-owned
// destination, which must have the same bounds as maskImg. Safe in place: each pixel is
// read before it is written, so dst may be the same image as distanceMap.
func ApplyNoiseToMaskAdaptiveInto(
	maskImg, noise, distanceMap *image.Gray, strength float64, minDist, maxDist float64, dst *image.Gray,
) {
	applyNoiseInto(maskImg, noise, distanceMap, strength, minDist, maxDist, dst)
}

// ApplyNoiseToMask overlays Perlin noise onto a blurred mask to create organic edges.
// maskImg: the blurred binary mask
// noise: the Perlin noise texture (should match or be larger than mask dimensions)
// strength: how much noise to apply (0.0 = no noise, 1.0 = full noise)
func ApplyNoiseToMask(maskImg, noise *image.Gray, strength float64) *image.Gray {
	dst := image.NewGray(maskImg.Bounds())
	applyNoiseInto(maskImg, noise, nil, strength, 0, 0, dst)

	return dst
}

// ApplyNoiseToMaskInto is ApplyNoiseToMask writing into a caller-owned destination,
// which must have the same bounds as maskImg. Safe in place.
func ApplyNoiseToMaskInto(maskImg, noise *image.Gray, strength float64, dst *image.Gray) {
	applyNoiseInto(maskImg, noise, nil, strength, 0, 0, dst)
}

// applyNoiseInto is the shared kernel behind ApplyNoiseToMask and ApplyNoiseToMaskAdaptive.
// A nil distanceMap applies the noise at full strength everywhere; otherwise the
// per-pixel strength is scaled by smoothstep(minDist, maxDist, distance).
//
// Every pixel of maskImg's bounds is written, and each one is read before it is written,
// so dst may alias maskImg or distanceMap. It must not alias noise, which is sampled at
// wrapped coordinates rather than at (x, y).
func applyNoiseInto(maskImg, noise, distanceMap *image.Gray, strength, minDist, maxDist float64, dst *image.Gray) {
	bounds := maskImg.Bounds()
	noiseBounds := noise.Bounds()
	noiseW := noiseBounds.Dx()

	r := writeRect(bounds, dst.Bounds())
	w := r.Dx()

	for y := r.Min.Y; y < r.Max.Y; y++ {
		maskRow := grayRow(maskImg, r.Min.X, y, w)
		dstRow := grayRow(dst, r.Min.X, y, w)
		// A distance map narrower than the mask still reads zero outside itself, which
		// is what GrayAt did; grayRow returns nil there and the loop falls back to it.
		distRow := grayRow(distanceMap, r.Min.X, y, w)

		// The noise is tiled, so its row is fixed for this y and the column index just
		// wraps - no modulus per pixel, and no bounds check per sample.
		ny := (y - bounds.Min.Y) % noiseBounds.Dy()
		noiseRow := grayRow(noise, noiseBounds.Min.X, noiseBounds.Min.Y+ny, noiseW)
		nx := (r.Min.X - bounds.Min.X) % noiseW

		for i, maskByte := range maskRow {
			maskVal := float64(maskByte)

			// Scale the noise by feature thickness so thin structures survive.
			// Pixel intensity in the distance map represents distance in pixels.
			noiseScale := 1.0
			if distanceMap != nil {
				dist := uint8(0)
				if distRow != nil {
					dist = distRow[i]
				} else {
					dist = distanceMap.GrayAt(r.Min.X+i, y).Y
				}
				noiseScale = smoothstep(minDist, maxDist, float64(dist))
			}

			noiseVal := float64(noiseRow[nx])
			if nx++; nx == noiseW {
				nx = 0
			}

			// Apply noise as a perturbation.
			// Noise is centered around 128, so subtract 128 to get -128 to +127 range.
			noiseDelta := (noiseVal - 128.0) * strength * noiseScale

			// Combine mask and noise
			combined := maskVal + noiseDelta

			// Clamp to valid range
			if combined < 0 {
				combined = 0
			}
			if combined > 255 {
				combined = 255
			}

			dstRow[i] = uint8(combined)
		}
	}
}

// ApplyThreshold applies a binary threshold to sharpen mask edges.
// Values below threshold become 0 (black), values at or above become 255 (white).
func ApplyThreshold(mask *image.Gray, threshold uint8) *image.Gray {
	dst := image.NewGray(mask.Bounds())
	ApplyThresholdInto(mask, threshold, dst)

	return dst
}

// ApplyThresholdInto is ApplyThreshold writing into a caller-owned destination, which
// must have the same bounds as mask. Safe in place.
func ApplyThresholdInto(mask *image.Gray, threshold uint8, dst *image.Gray) {
	r := writeRect(mask.Bounds(), dst.Bounds())
	w := r.Dx()

	for y := r.Min.Y; y < r.Max.Y; y++ {
		srcRow := grayRow(mask, r.Min.X, y, w)
		dstRow := grayRow(dst, r.Min.X, y, w)
		for i, val := range srcRow {
			if val >= threshold {
				dstRow[i] = 255
			} else {
				dstRow[i] = 0
			}
		}
	}
}

// ApplyThresholdWithAntialias applies a threshold with smooth antialiased edges.
// Uses a smoothstep (t²(3-2t)) transition zone of 20 gray levels on each side of
// the threshold value for natural-looking edges.
func ApplyThresholdWithAntialias(maskImg *image.Gray, threshold uint8) *image.Gray {
	dst := image.NewGray(maskImg.Bounds())
	applyThresholdWithAntialiasInto(maskImg, threshold, false, dst)

	return dst
}

// ApplyThresholdWithAntialiasInto is ApplyThresholdWithAntialias writing into a
// caller-owned destination, which must have the same bounds as maskImg. Safe in place.
func ApplyThresholdWithAntialiasInto(maskImg *image.Gray, threshold uint8, dst *image.Gray) {
	applyThresholdWithAntialiasInto(maskImg, threshold, false, dst)
}

// ApplyThresholdWithAntialiasAndInvert is ApplyThresholdWithAntialias with inverted
// polarity: values above the threshold become black instead of white. Used for the
// land layer, which is the inverse of everything else.
func ApplyThresholdWithAntialiasAndInvert(maskImg *image.Gray, threshold uint8) *image.Gray {
	dst := image.NewGray(maskImg.Bounds())
	applyThresholdWithAntialiasInto(maskImg, threshold, true, dst)

	return dst
}

// ApplyThresholdWithAntialiasAndInvertInto is ApplyThresholdWithAntialiasAndInvert writing
// into a caller-owned destination, which must have the same bounds as maskImg. Safe in place.
func ApplyThresholdWithAntialiasAndInvertInto(maskImg *image.Gray, threshold uint8, dst *image.Gray) {
	applyThresholdWithAntialiasInto(maskImg, threshold, true, dst)
}

// applyThresholdWithAntialiasInto is the shared kernel behind the exported variants.
// Every pixel of maskImg's bounds is written, and each is read before it is written,
// so dst may alias maskImg.
func applyThresholdWithAntialiasInto(maskImg *image.Gray, threshold uint8, invert bool, dst *image.Gray) {
	r := writeRect(maskImg.Bounds(), dst.Bounds())
	w := r.Dx()

	// Transition zone: 20 gray levels on each side of threshold
	const transitionWidth = 20

	lower := float64(int(threshold) - transitionWidth)
	upper := float64(int(threshold) + transitionWidth)

	for y := r.Min.Y; y < r.Max.Y; y++ {
		srcRow := grayRow(maskImg, r.Min.X, y, w)
		dstRow := grayRow(dst, r.Min.X, y, w)
		for i, val := range srcRow {
			t := smoothstep(lower, upper, float64(val))
			if invert {
				t = 1.0 - t
			}
			dstRow[i] = uint8(t * 255.0)
		}
	}
}
