package mask

import (
	"image"
	"image/color"
	"math"
)

// ApplySoftEdgeMask applies a soft edge darkening effect while increasing saturation at edges.
// The mask controls effect intensity: 255 (white) = no change, 0 (black) = maximum darkening + saturation.
// This simulates watercolor pigment concentrating at edges - both darker and more vibrant.
// The strength parameter (0.0-1.0) controls how aggressive the effect is.
func ApplySoftEdgeMask(base *image.NRGBA, mask *image.Gray, strength float64) *image.NRGBA {
	if base == nil || mask == nil {
		return nil
	}

	bounds := base.Bounds()
	dst := image.NewNRGBA(bounds)
	ApplySoftEdgeMaskInto(base, mask, strength, dst)
	return dst
}

// ApplySoftEdgeMaskInto applies a soft edge darkening effect into an existing destination buffer.
// This avoids allocation when the caller can reuse a buffer.
// The dst buffer must have the same bounds as base.
func ApplySoftEdgeMaskInto(base *image.NRGBA, mask *image.Gray, strength float64, dst *image.NRGBA) {
	if base == nil || mask == nil || dst == nil {
		return
	}

	if strength < 0 {
		strength = 0
	}
	if strength > 1 {
		strength = 1
	}

	r := writeRect(base.Bounds(), dst.Bounds())
	w := r.Dx()

	for y := r.Min.Y; y < r.Max.Y; y++ {
		srcOff := base.PixOffset(r.Min.X, y)
		srcRow := base.Pix[srcOff : srcOff+4*w]
		dstOff := dst.PixOffset(r.Min.X, y)
		dstRow := dst.Pix[dstOff : dstOff+4*w]

		// A mask narrower than the base read zero outside itself, which is what GrayAt
		// returns and what a nil row falls back to.
		maskRow := grayRow(mask, r.Min.X, y, w)

		for i := 0; i < w; i++ {
			maskVal := 0
			if maskRow != nil {
				maskVal = int(maskRow[i])
			} else {
				maskVal = int(mask.GrayAt(r.Min.X+i, y).Y)
			}

			// Quadratic falloff: creates softer, more natural transition
			// Effect amount: 0 at white (center), strength at black (edges)
			// maskVal^2 / 255^2 gives normalized squared value
			maskSquared := maskVal * maskVal                     // max: 65025
			invMaskSquared := 65025 - maskSquared                // 255*255 = 65025
			effectInt := int(float64(invMaskSquared) * strength) // 0..65025

			// Convert RGB to HSL (integer-only)
			p := 4 * i
			h, s, l := rgbToHSL(srcRow[p], srcRow[p+1], srcRow[p+2])

			// Darken by reducing lightness
			// l_new = l * (1 - effect) = l * (65025 - effectInt) / 65025
			darkening := 65025 - effectInt
			lNew := uint8((int(l) * darkening) / 65025)

			// Convert back to RGB (integer-only)
			// The HSL round trip is not lossless, so it runs even where the darkening
			// factor is 1 - skipping it would change pixels.
			red, green, blue := hslToRGB(h, s, lNew)

			alpha := srcRow[p+3] // preserve original alpha
			dstRow[p], dstRow[p+1], dstRow[p+2], dstRow[p+3] = red, green, blue, alpha
		}
	}
}

// MultiplyRGBByMask multiplies the RGB color values of an image by a grayscale mask.
// The mask values (0-255) are normalized to (0-1) and multiplied with RGB values.
// Alpha channel is preserved from the base image.
func MultiplyRGBByMask(base *image.NRGBA, mask *image.Gray) *image.NRGBA {
	if base == nil || mask == nil {
		return nil
	}

	bounds := base.Bounds()
	dst := image.NewNRGBA(bounds)

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			src := base.NRGBAAt(x, y)
			maskVal := float64(mask.GrayAt(x, y).Y) / 255.0

			dst.SetNRGBA(x, y, color.NRGBA{
				R: uint8(math.Round(float64(src.R) * maskVal)),
				G: uint8(math.Round(float64(src.G) * maskVal)),
				B: uint8(math.Round(float64(src.B) * maskVal)),
				A: src.A, // preserve base alpha
			})
		}
	}

	return dst
}
