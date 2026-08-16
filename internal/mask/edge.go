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
		if maskRow != nil {
			softEdgeRow(dstRow, srcRow, maskRow, strength, w)
			continue
		}

		for i := range w {
			maskVal := int(mask.GrayAt(r.Min.X+i, y).Y)
			softEdgePixel(dstRow[4*i:], srcRow[4*i:], softEdgeDarkening(maskVal, strength))
		}
	}
}

// softEdgeDarkening turns one mask level into the fixed-point lightness factor the
// pass multiplies by, in [0, 65025] where 65025 means "leave the lightness alone".
//
// The falloff is quadratic in the mask level, which is what makes the transition
// look soft rather than linear. The float64 multiply is deliberate: the assembly
// kernel reproduces it in double-precision lanes so that both paths truncate the
// same product.
func softEdgeDarkening(maskVal int, strength float64) int {
	maskSquared := maskVal * maskVal                     // max: 65025
	invMaskSquared := 65025 - maskSquared                // 255*255 = 65025
	effectInt := int(float64(invMaskSquared) * strength) // 0..65025

	return 65025 - effectInt
}

// softEdgePixel darkens one NRGBA pixel by the given fixed-point factor, preserving
// alpha. src and dst may be the same slice.
func softEdgePixel(dst, src []byte, darkening int) {
	// Convert RGB to HSL (integer-only)
	h, s, l := rgbToHSL(src[0], src[1], src[2])

	// Darken by reducing lightness: l_new = l * (1 - effect)
	lNew := uint8((int(l) * darkening) / 65025)

	// Convert back to RGB (integer-only).
	// The HSL round trip is not lossless, so it runs even where the darkening
	// factor is 1 - skipping it would change pixels.
	red, green, blue := hslToRGB(h, s, lNew)

	alpha := src[3] // preserve original alpha
	dst[0], dst[1], dst[2], dst[3] = red, green, blue, alpha
}

// softEdgeRowGo is the portable implementation of one row of the soft-edge pass, and
// the reference the assembly kernel is tested against. dst and src hold w NRGBA
// pixels; maskRow holds w mask levels.
func softEdgeRowGo(dst, src, maskRow []byte, strength float64, w int) {
	for i := range w {
		softEdgePixel(dst[4*i:], src[4*i:], softEdgeDarkening(int(maskRow[i]), strength))
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
