package mask

import (
	"image"
	"image/color"
	"math"

	"github.com/aquilax/go-perlin"
	"github.com/disintegration/gift"
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

	bounds := img.Bounds()
	out := image.NewGray(bounds)

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			out.SetGray(x, y, color.Gray{Y: getAlpha(img, x, y)})
		}
	}

	return out
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

	out := image.NewGray(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			var maxVal uint8
			for _, m := range validMasks {
				v := m.GrayAt(x, y).Y
				if v > maxVal {
					maxVal = v
				}
			}
			out.SetGray(x, y, color.Gray{Y: maxVal})
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
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			av := int(a.GrayAt(x, y).Y)
			bv := int(b.GrayAt(x, y).Y)
			// AND NOT: where b is opaque, result is transparent; partial
			// coverage in b caps a instead of eroding it by the full amount.
			result := av
			if inv := 255 - bv; inv < result {
				result = inv
			}
			out.SetGray(x, y, color.Gray{Y: uint8(result)})
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
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			av := a.GrayAt(x, y).Y
			bv := b.GrayAt(x, y).Y
			if bv < av {
				av = bv
			}
			out.SetGray(x, y, color.Gray{Y: av})
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
	bounds := m.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			v := m.GrayAt(x, y).Y
			dst.SetGray(x, y, color.Gray{Y: 255 - v})
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

// GaussianBlur applies a Gaussian blur filter to soften mask edges.
// The sigma parameter controls the blur radius (larger = more blur).
func GaussianBlur(mask *image.Gray, sigma float32) *image.Gray {
	g := gift.New(gift.GaussianBlur(sigma))

	// Create output image
	dst := image.NewGray(g.Bounds(mask.Bounds()))

	// Apply the blur filter
	g.Draw(dst, mask)

	return dst
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

			noise.SetGray(x, y, color.Gray{Y: gray})
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
	bounds := maskImg.Bounds()
	result := image.NewGray(bounds)

	noiseBounds := noise.Bounds()

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			// Get mask value
			maskVal := float64(maskImg.GrayAt(x, y).Y)

			// Get distance value (pixel intensity represents distance in pixels)
			distVal := float64(distanceMap.GrayAt(x, y).Y)

			// Calculate noise scaling based on distance
			// Use smoothstep for gradual transition
			noiseScale := smoothstep(minDist, maxDist, distVal)

			// Get noise value
			nx := (x - bounds.Min.X) % noiseBounds.Dx()
			ny := (y - bounds.Min.Y) % noiseBounds.Dy()
			noiseVal := float64(noise.GrayAt(noiseBounds.Min.X+nx, noiseBounds.Min.Y+ny).Y)

			// Apply noise as a perturbation, scaled by distance
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

			result.SetGray(x, y, color.Gray{Y: uint8(combined)})
		}
	}

	return result
}

// ApplyNoiseToMask overlays Perlin noise onto a blurred mask to create organic edges.
// mask: the blurred binary mask
// noise: the Perlin noise texture (should match or be larger than mask dimensions)
// strength: how much noise to apply (0.0 = no noise, 1.0 = full noise)
func ApplyNoiseToMask(mask, noise *image.Gray, strength float64) *image.Gray {
	bounds := mask.Bounds()
	result := image.NewGray(bounds)

	noiseBounds := noise.Bounds()

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			// Get mask value
			maskVal := float64(mask.GrayAt(x, y).Y)

			// Get noise value (tile if noise is smaller, or sample if larger)
			nx := (x - bounds.Min.X) % noiseBounds.Dx()
			ny := (y - bounds.Min.Y) % noiseBounds.Dy()
			noiseVal := float64(noise.GrayAt(noiseBounds.Min.X+nx, noiseBounds.Min.Y+ny).Y)

			// Apply noise as a perturbation
			// Noise is centered around 128, so subtract 128 to get -128 to +127 range
			noiseDelta := (noiseVal - 128.0) * strength

			// Combine mask and noise
			combined := maskVal + noiseDelta

			// Clamp to valid range
			if combined < 0 {
				combined = 0
			}
			if combined > 255 {
				combined = 255
			}

			result.SetGray(x, y, color.Gray{Y: uint8(combined)})
		}
	}

	return result
}

// ApplyThreshold applies a binary threshold to sharpen mask edges.
// Values below threshold become 0 (black), values at or above become 255 (white).
func ApplyThreshold(mask *image.Gray, threshold uint8) *image.Gray {
	bounds := mask.Bounds()
	result := image.NewGray(bounds)

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			val := mask.GrayAt(x, y).Y

			if val >= threshold {
				result.SetGray(x, y, color.Gray{Y: 255})
			} else {
				result.SetGray(x, y, color.Gray{Y: 0})
			}
		}
	}

	return result
}

// ApplyThresholdWithAntialias applies a threshold with smooth antialiased edges.
// Uses a fixed transition zone with cubic interpolation (smootherstep) for natural-looking edges.
// The transition zone is 20 gray levels on each side of the threshold value.
func ApplyThresholdWithAntialias(mask *image.Gray, threshold uint8) *image.Gray {
	bounds := mask.Bounds()
	result := image.NewGray(bounds)

	// Transition zone: 20 gray levels on each side of threshold
	const transitionWidth = 20

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			val := mask.GrayAt(x, y).Y

			// Smooth threshold with cubic interpolation
			lower := int(threshold) - transitionWidth
			upper := int(threshold) + transitionWidth

			var outVal uint8

			switch {
			case int(val) <= lower:
				outVal = 0
			case int(val) >= upper:
				outVal = 255
			default:
				// Cubic interpolation: smootherstep (3t² - 2t³)
				t := float32(int(val)-lower) / float32(2*transitionWidth)
				smoothT := t * t * (3.0 - 2.0*t)
				outVal = uint8((smoothT) * 255.0)
			}
			result.SetGray(x, y, color.Gray{Y: outVal})
		}
	}

	return result
}

// ApplyThresholdWithAntialiasAndInvert applies a threshold with smooth antialiased edges.
// Uses a fixed transition zone with cubic interpolation (smootherstep) for natural-looking edges.
// The transition zone is 20 gray levels on each side of the threshold value.
func ApplyThresholdWithAntialiasAndInvert(mask *image.Gray, threshold uint8) *image.Gray {
	bounds := mask.Bounds()
	result := image.NewGray(bounds)

	// Transition zone: 20 gray levels on each side of threshold
	const transitionWidth = 20

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			val := mask.GrayAt(x, y).Y

			// Smooth threshold with cubic interpolation
			lower := int(threshold) - transitionWidth
			upper := int(threshold) + transitionWidth

			var outVal uint8

			switch {
			case int(val) <= lower:
				outVal = 255
			case int(val) >= upper:
				outVal = 0
			default:
				// Cubic interpolation: smootherstep (3t² - 2t³)
				t := float32(int(val)-lower) / float32(2*transitionWidth)
				smoothT := t * t * (3.0 - 2.0*t)
				outVal = uint8((1.0 - smoothT) * 255.0)
			}
			result.SetGray(x, y, color.Gray{Y: outVal})
		}
	}

	return result
}

// BoxBlur applies a fast box blur with the given radius using a sliding window algorithm.
// This is significantly faster than Gaussian blur (O(1) per pixel vs O(k) per pixel).
// The blur is applied in two separable passes (horizontal then vertical).
func BoxBlur(mask *image.Gray, radius int) *image.Gray {
	if radius < 1 {
		// No blur needed, return a copy
		bounds := mask.Bounds()
		dst := image.NewGray(bounds)
		copy(dst.Pix, mask.Pix)
		return dst
	}

	bounds := mask.Bounds()

	// Temporary buffer for horizontal pass
	temp := image.NewGray(bounds)
	boxBlurHorizontal(mask, temp, bounds.Dx(), bounds.Dy(), radius)

	// Vertical pass (on temp -> dst)
	dst := image.NewGray(bounds)
	boxBlurVertical(temp, dst, bounds.Dx(), bounds.Dy(), radius)

	return dst
}

// boxBlurHorizontal runs the horizontal sliding-window pass of BoxBlur from src into dst.
func boxBlurHorizontal(src, dst *image.Gray, width, height, radius int) {
	for y := 0; y < height; y++ {
		// Sliding window sum
		sum := 0
		count := 0

		// Initialize window
		for x := -radius; x <= radius; x++ {
			if x >= 0 && x < width {
				idx := y*src.Stride + x
				sum += int(src.Pix[idx])
				count++
			}
		}

		// Pre-calculate reciprocal for multiplication instead of division
		invCount := 1.0 / float64(count)

		// First pixel - truncate to match integer division behavior
		dst.Pix[y*dst.Stride] = uint8(int(float64(sum) * invCount))

		// Slide window across row
		for x := 1; x < width; x++ {
			// Track if count changes (only at edges)
			prevCount := count

			// Remove left pixel from window
			leftX := x - radius - 1
			if leftX >= 0 {
				idx := y*src.Stride + leftX
				sum -= int(src.Pix[idx])
				count--
			}

			// Add right pixel to window
			rightX := x + radius
			if rightX < width {
				idx := y*src.Stride + rightX
				sum += int(src.Pix[idx])
				count++
			}

			// Recalculate reciprocal only if count changed
			if count != prevCount {
				invCount = 1.0 / float64(count)
			}

			dst.Pix[y*dst.Stride+x] = uint8(int(float64(sum) * invCount))
		}
	}
}

// boxBlurVertical runs the vertical sliding-window pass of BoxBlur from src into dst.
func boxBlurVertical(src, dst *image.Gray, width, height, radius int) {
	for x := 0; x < width; x++ {
		// Sliding window sum
		sum := 0
		count := 0

		// Initialize window
		for y := -radius; y <= radius; y++ {
			if y >= 0 && y < height {
				idx := y*src.Stride + x
				sum += int(src.Pix[idx])
				count++
			}
		}

		// Pre-calculate reciprocal for multiplication instead of division
		invCount := 1.0 / float64(count)

		// First pixel
		dst.Pix[x] = uint8(float64(sum) * invCount)

		// Slide window down column
		for y := 1; y < height; y++ {
			// Track if count changes (only at edges)
			prevCount := count

			// Remove top pixel from window
			topY := y - radius - 1
			if topY >= 0 {
				idx := topY*src.Stride + x
				sum -= int(src.Pix[idx])
				count--
			}

			// Add bottom pixel to window
			bottomY := y + radius
			if bottomY < height {
				idx := bottomY*src.Stride + x
				sum += int(src.Pix[idx])
				count++
			}

			// Recalculate reciprocal only if count changed
			if count != prevCount {
				invCount = 1.0 / float64(count)
			}

			dst.Pix[y*dst.Stride+x] = uint8(float64(sum) * invCount)
		}
	}
}

// BoxBlurSigma applies a 3-pass box blur to approximate a Gaussian blur.
// This is optimized for small sigma values (σ < 5) and provides significant
// performance improvement over true Gaussian blur while maintaining good quality.
//
// The function converts sigma to box radius using the formula:
// r = sqrt((12 * σ² / N) + 1) where N = 3 (number of passes)
//
// Expected speedup: 3-7x faster than Gaussian blur for σ < 5.
func BoxBlurSigma(mask *image.Gray, sigma float32) *image.Gray {
	if sigma <= 0 {
		// No blur needed, return a copy
		bounds := mask.Bounds()
		dst := image.NewGray(bounds)
		copy(dst.Pix, mask.Pix)
		return dst
	}

	// Convert sigma to box radius for 3-pass approximation
	// Formula: r = sqrt((12 * σ² / N) + 1) where N = 3
	sigmaSquared := float64(sigma) * float64(sigma)
	radiusFloat := math.Sqrt((12.0*sigmaSquared)/3.0 + 1.0)
	radius := int(radiusFloat)

	// Ensure minimum radius of 1
	if radius < 1 {
		radius = 1
	}

	// Apply box blur 3 times to approximate Gaussian
	result := BoxBlur(mask, radius)
	result = BoxBlur(result, radius)
	result = BoxBlur(result, radius)

	return result
}

// AntialiasEdges applies subtle antialiasing to smooth sharp mask edges.
// This is essentially a light blur to soften transitions.
func AntialiasEdges(mask *image.Gray, sigma float32) *image.Gray {
	// Use BoxBlurSigma for fast antialiasing
	return BoxBlurSigma(mask, sigma)
}
