package watercolor

import "math"

// MinGeometryPaddingPx is the minimum padding needed to accommodate polygon
// geometry that extends beyond tile boundaries. OSM polygons (water bodies,
// parks, etc.) often extend significantly beyond the tile they intersect.
// A larger padding ensures these polygons render correctly without straight-line
// clipping artifacts at tile edges.
const MinGeometryPaddingPx = 64

// RequiredPaddingPx returns a suggested pixel padding for "metatile" rendering.
//
// The watercolor pipeline applies multiple Gaussian blurs (mask blur, antialias,
// edge halo, optional shading). Those filters need valid pixels outside the
// final tile area to avoid boundary artifacts. Rendering and processing a larger
// tile (tileSize + 2*pad) and cropping back to the center removes seams.
//
// Additionally, polygon geometry that crosses tile boundaries needs extra space
// to render correctly. The returned padding is the maximum of blur requirements
// and geometry requirements (MinGeometryPaddingPx).
//
// The whole calculation is done in world pixels and scaled to device pixels
// only at the end. That ordering is not cosmetic: a tile's metatile origin is
// (tileX*tileSize - pad)/scale in world units, so a @2x metatile covers the same
// ground as its @1x twin only if pad(2x) == 2*pad(1x) exactly. Scaling a single
// number at the end guarantees that; scaling the sigmas and then adding an
// unscaled "+2" and an unscaled 64px floor would not.
func RequiredPaddingPx(params Params) int {
	scale := params.Scale
	if scale <= 0 {
		scale = 1
	}

	maxSigma := float32(0)

	consider := func(s float32) {
		if s > maxSigma {
			maxSigma = s
		}
	}

	consider(params.BlurSigma)
	consider(params.AntialiasSigma)

	for _, style := range params.Styles {
		consider(style.MaskBlurSigma)
		consider(style.ShadeSigma)
		consider(style.EdgeSigma)
	}

	// Back to world pixels: the sigmas in params have already been scaled.
	maxSigmaWorld := float64(maxSigma) / scale

	// 3*sigma captures the vast majority of the kernel energy.
	blurPadWorld := max(int(math.Ceil(maxSigmaWorld*3.0))+2, 1)

	// Use the larger of blur padding and geometry padding
	padWorld := max(blurPadWorld, MinGeometryPaddingPx)

	if scale == 1 {
		return padWorld
	}
	return int(math.Ceil(float64(padWorld) * scale))
}
