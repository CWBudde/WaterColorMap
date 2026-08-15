package watercolor

import (
	"errors"
	"fmt"
	"image"
	"sync"

	"github.com/cwbudde/watercolormap/internal/geojson"
	"github.com/cwbudde/watercolormap/internal/mask"
	"github.com/cwbudde/watercolormap/internal/texture"
)

// ProcessorContext holds reusable buffers for watercolor processing.
// Reusing these buffers across multiple calls significantly reduces allocations.
type ProcessorContext struct {
	distCtx   *mask.DistanceContext
	tiledTex  *image.NRGBA // buffer for tiled texture
	painted   *image.NRGBA // buffer for painted result
	tempNRGBA *image.NRGBA // temporary NRGBA buffer for edge operations
	tempGray  *image.Gray  // temporary Gray buffer for inverted mask
	edgeMask  *image.Gray  // buffer for the distance-based edge mask
	tileSize  int          // current buffer size
}

// NewProcessorContext creates a context sized for the given tile size.
func NewProcessorContext(tileSize int) *ProcessorContext {
	c := &ProcessorContext{distCtx: mask.NewDistanceContext(tileSize)}
	c.allocate(tileSize)

	return c
}

// EnsureCapacity resizes the buffers when they do not match the given tile size.
//
// This is exact-size rather than grow-only on purpose: the result bounds of a paint are
// taken from these buffers, so an oversized buffer would silently change the output. The
// distance context underneath stays grow-only - every loop in it is bounded by the width
// and height it is given, never by the buffer length.
func (c *ProcessorContext) EnsureCapacity(tileSize int) {
	if tileSize == c.tileSize {
		return
	}
	c.distCtx.EnsureCapacity(tileSize, tileSize)
	c.allocate(tileSize)
}

func (c *ProcessorContext) allocate(tileSize int) {
	bounds := image.Rect(0, 0, tileSize, tileSize)
	c.tiledTex = image.NewNRGBA(bounds)
	c.painted = image.NewNRGBA(bounds)
	c.tempNRGBA = image.NewNRGBA(bounds)
	c.tempGray = image.NewGray(bounds)
	c.edgeMask = image.NewGray(bounds)
	c.tileSize = tileSize
}

// LayerStyle defines per-layer watercolor styling parameters.
type LayerStyle struct {
	Texture           image.Image
	MaskThreshold     *uint8 // Optional per-layer threshold override (if nil, uses global Params.Threshold)
	Layer             geojson.LayerType
	EdgeStrength      float64
	MaskNoiseStrength float64
	ShadeStrength     float64
	EdgeGamma         float64
	NoiseMinDist      float64 // Distance below which noise is minimal (for adaptive noise)
	NoiseMaxDist      float64 // Distance above which noise is at full strength (for adaptive noise)
	MaskBlurSigma     float32
	ShadeSigma        float32
	EdgeSigma         float32
	InvertMask        bool // If true, invert the mask after threshold (used for land = invert of non-land)
	AdaptiveNoise     bool // If true, scale noise based on feature distance (protects thin structures)
}

// Params define the common watercolor processing knobs.
type Params struct {
	Styles        map[geojson.LayerType]LayerStyle
	PerlinNoise   *image.Gray // Pre-generated noise texture, reused across all layers to avoid redundant allocations
	TileSize      int
	NoiseScale    float64
	NoiseStrength float64
	// Scale is device pixels per world pixel: 1 for 256px tiles, 2 for @2x.
	// Every length in this struct is already multiplied by it; it is kept so
	// that consumers which need lengths back in world units (RequiredPaddingPx)
	// or which sample a fixed-resolution bitmap (texture tiling) can undo it.
	// Zero is treated as 1 so zero-valued Params stay usable.
	Scale          float64
	Seed           int64
	OffsetX        int
	OffsetY        int
	BlurSigma      float32
	AntialiasSigma float32
	Threshold      uint8
}

// ZoomAdjustedBlurSigma returns blur sigma adjusted for zoom level.
// Higher zoom levels (more detail) get sharper edges (less blur).
// baseBlurSigma is the blur at zoom 13; sigma decreases at higher zooms.
func ZoomAdjustedBlurSigma(baseBlurSigma float32, zoom int) float32 {
	// At zoom 10-11: use base * 1.4 (softer for overview)
	// At zoom 12-13: use base (reference level)
	// At zoom 14+: use base * 0.7 (sharper for detail)
	if zoom <= 11 {
		return baseBlurSigma * 1.4
	} else if zoom >= 14 {
		return baseBlurSigma * 0.7
	}
	return baseBlurSigma
}

// ptr is a helper to create uint8 pointers for optional threshold values.
func ptr(v uint8) *uint8 { return &v }

// DefaultParams returns sensible defaults for the watercolor pipeline.
// textures provides base textures per layer; caller may omit entries for layers they won't process.
//
// The blur sigmas look oddly precise (2.45, 1.41, 7.48) because they are not
// hand-picked: they are the blur widths this renderer was already producing.
// The previous blur derived a single box radius as int(sqrt(12σ²/3 + 1)) and
// applied it three times, which blurred about twice as hard as the sigma asked
// for — a nominal 1.2 came out at 2.45, a nominal 0.5 at 1.41, and 3.5 at 7.48.
// The sigmas that fed it had been tuned by eye against that behaviour. Now that
// BoxBlurSigma applies the sigma it is given, the tuned appearance is preserved
// by asking for what was actually being rendered.
//
// So these are a look, not a law. Retune them freely — but retune them as blur
// widths in pixels, which is now what they mean.
func DefaultParams(tileSize int, seed int64, textures map[geojson.LayerType]image.Image) Params {
	return Params{
		TileSize:       tileSize,
		Scale:          1,
		BlurSigma:      2.45,
		NoiseScale:     30.0,
		NoiseStrength:  0.28,
		Threshold:      50,
		AntialiasSigma: 1.41,
		Seed:           seed,
		Styles: map[geojson.LayerType]LayerStyle{
			geojson.LayerLand: {
				Layer:         geojson.LayerLand,
				Texture:       textures[geojson.LayerLand],
				ShadeSigma:    7.48,
				ShadeStrength: 0.12,
				EdgeStrength:  0.3,  // Match old generator shadow strength
				EdgeSigma:     3.0,  // radius = 3.0 * 3 = 9.0 (matches old)
				EdgeGamma:     9.0,  // Match old generator gamma
				InvertMask:    true, // Land is the inverse of non-land (water+rivers+roads)
			},
			geojson.LayerWater: {
				Layer:             geojson.LayerWater,
				Texture:           textures[geojson.LayerWater],
				MaskBlurSigma:     2.45, // Moderate blur for subtle softening
				MaskNoiseStrength: 0.18, // Moderate noise for organic edges
				AdaptiveNoise:     true, // Protect thin roads from fragmentation
				NoiseMinDist:      2.0,  // Minimal noise below 2px from edge
				NoiseMaxDist:      10.0, // Full noise above 10px from edge
				ShadeSigma:        0,
				ShadeStrength:     0,
				EdgeStrength:      0.2,
				EdgeSigma:         3.5,
				EdgeGamma:         9.3,
				MaskThreshold:     ptr(144),
			},
			geojson.LayerRivers: {
				Layer:             geojson.LayerRivers,
				Texture:           textures[geojson.LayerWater], // Use same texture as water
				MaskThreshold:     ptr(98),                      // Balanced threshold for rivers
				MaskBlurSigma:     1.41,                         // Light blur for natural edges
				MaskNoiseStrength: 0.15,                         // Subtle noise for organic feel
				AdaptiveNoise:     true,                         // Protect narrow streams from fragmentation
				NoiseMinDist:      2.0,                          // Minimal noise below 2px from edge
				NoiseMaxDist:      10.0,                         // Full noise above 10px from edge
				ShadeSigma:        0,
				ShadeStrength:     0,
				EdgeStrength:      0.2,
				EdgeSigma:         2.5,
				EdgeGamma:         9.3,
			},
			geojson.LayerParks: {
				Layer:         geojson.LayerParks,
				Texture:       textures[geojson.LayerParks],
				MaskThreshold: ptr(120), // Higher threshold for layers after land
				ShadeSigma:    0,
				ShadeStrength: 0,
				EdgeStrength:  0.2,
				EdgeSigma:     3.0,
				EdgeGamma:     8.6,
			},
			geojson.LayerRoads: {
				Layer:             geojson.LayerRoads,
				Texture:           textures[geojson.LayerRoads],
				MaskThreshold:     ptr(100), // Balanced threshold for roads
				MaskBlurSigma:     2.45,     // Moderate blur for subtle softening
				MaskNoiseStrength: 0.18,     // Moderate noise for organic edges
				AdaptiveNoise:     true,     // Protect thin roads from fragmentation
				NoiseMinDist:      2.0,      // Minimal noise below 2px from edge
				NoiseMaxDist:      10.0,     // Full noise above 10px from edge
				ShadeSigma:        0,
				ShadeStrength:     0,
				EdgeStrength:      0.2,
				EdgeSigma:         2.8,
				EdgeGamma:         8.9,
			},
			geojson.LayerRailroads: {
				Layer:             geojson.LayerRailroads,
				Texture:           textures[geojson.LayerRailroads],
				MaskThreshold:     ptr(100), // Balanced threshold for railroads
				MaskBlurSigma:     2.45,     // Moderate blur for subtle softening
				MaskNoiseStrength: 0.18,     // Moderate noise for organic edges
				AdaptiveNoise:     true,     // Protect thin railroad lines from fragmentation
				NoiseMinDist:      2.0,      // Minimal noise below 2px from edge
				NoiseMaxDist:      10.0,     // Full noise above 10px from edge
				ShadeSigma:        0,
				ShadeStrength:     0,
				EdgeStrength:      0.2,
				EdgeSigma:         2.8,
				EdgeGamma:         8.9,
			},
			geojson.LayerHighways: {
				Layer:             geojson.LayerHighways,
				Texture:           textures[geojson.LayerHighways],
				MaskThreshold:     ptr(120), // Higher threshold for layers after land
				MaskBlurSigma:     2.45,
				MaskNoiseStrength: 0.18,
				AdaptiveNoise:     true, // Protect highways from fragmentation
				NoiseMinDist:      4.0,  // Minimal noise below 4px from edge
				NoiseMaxDist:      15.0, // Full noise above 15px from edge
				ShadeSigma:        0,
				ShadeStrength:     0,
				EdgeStrength:      0.2,
				EdgeSigma:         2.9,
				EdgeGamma:         9.2,
			},
			geojson.LayerUrban: {
				Layer:         geojson.LayerUrban,
				Texture:       textures[geojson.LayerUrban],
				MaskThreshold: ptr(160),
				ShadeSigma:    0,
				ShadeStrength: 0,
				EdgeStrength:  0.2,
				EdgeSigma:     3.1,
				EdgeGamma:     8.8,
			},
			geojson.LayerCivic: {
				Layer:         geojson.LayerCivic,
				Texture:       textures[geojson.LayerCivic],
				MaskThreshold: ptr(155),
				ShadeSigma:    0,
				ShadeStrength: 0,
				EdgeStrength:  0.2,
				EdgeSigma:     3.15,
				EdgeGamma:     8.7,
			},
			geojson.LayerBuildings: {
				Layer:         geojson.LayerBuildings,
				Texture:       textures[geojson.LayerBuildings],
				MaskThreshold: ptr(150),
				ShadeSigma:    0,
				ShadeStrength: 0,
				EdgeStrength:  0.2,
				EdgeSigma:     3.2,
				EdgeGamma:     8.6,
			},
		},
	}
}

// maskScratch holds the intermediate buffers of the mask pipeline. Every stage between
// the input mask and the final mask is a throwaway, so a layer used to allocate four to
// six full-size Gray images to produce one. Nothing in here escapes processMask.
type maskScratch struct {
	alpha   *image.Gray // extracted alpha of the layer image (PaintLayer path only)
	blurred *image.Gray // output of the blur stage
	work    *image.Gray // binary mask -> distance map -> noisy mask, in that order
	dist    *mask.DistanceContext
	bounds  image.Rectangle
}

// ensure resizes the buffers to exactly bounds. Exact rather than grow-only: every stage
// writes the whole of its own bounds, so a larger buffer would leave stale pixels around
// the edge of the region the next stage reads.
func (s *maskScratch) ensure(bounds image.Rectangle) {
	if s.bounds == bounds && s.blurred != nil {
		return
	}
	s.alpha = image.NewGray(bounds)
	s.blurred = image.NewGray(bounds)
	s.work = image.NewGray(bounds)
	s.bounds = bounds
	if s.dist == nil {
		s.dist = mask.NewDistanceContext(max(bounds.Dx(), bounds.Dy()))
	} else {
		s.dist.EnsureCapacity(bounds.Dx(), bounds.Dy())
	}
}

// maskScratchPool recycles the mask pipeline's intermediate buffers across layers.
var maskScratchPool sync.Pool

func acquireMaskScratch(bounds image.Rectangle) *maskScratch {
	sc, ok := maskScratchPool.Get().(*maskScratch)
	if !ok || sc == nil {
		sc = &maskScratch{}
	}
	sc.ensure(bounds)

	return sc
}

func releaseMaskScratch(sc *maskScratch) {
	maskScratchPool.Put(sc)
}

func processMask(baseMask *image.Gray, layer geojson.LayerType, params Params) (*image.Gray, error) {
	if baseMask == nil {
		return nil, errors.New("base mask is nil")
	}

	sc := acquireMaskScratch(baseMask.Bounds())
	defer releaseMaskScratch(sc)

	return processMaskWithScratch(baseMask, layer, params, sc)
}

// processMaskWithScratch runs blur -> noise -> threshold/antialias over sc's buffers.
//
// The buffer discipline, and why it is safe:
//
//   - baseMask is never written. Only the blur reads it, and the blur's destination is
//     sc.blurred. That matters because on the PaintLayerFromMask path baseMask belongs to
//     the pipeline, which keeps it (and may have handed it to the debug capture).
//   - sc.work carries the binary mask, then the distance map, then the noisy mask. The
//     distance transform and the noise pass are both safe in place, and the noise pass
//     reads its mask input from sc.blurred and its noise from params.PerlinNoise, so no
//     two arguments of any stage alias in a way that would tear.
//   - the returned final mask is freshly allocated: it outlives the scratch. On the land
//     path the pipeline keeps it to constrain the parks layer.
func processMaskWithScratch(
	baseMask *image.Gray, layer geojson.LayerType, params Params, sc *maskScratch,
) (*image.Gray, error) {
	style, ok := params.Styles[layer]
	if !ok {
		return nil, fmt.Errorf("missing style for layer %s", layer)
	}

	// Blur/noise/threshold/AA pipeline on the provided mask.
	layerBlur := params.BlurSigma
	if style.MaskBlurSigma > 0 {
		layerBlur = style.MaskBlurSigma
	}
	layerNoiseStrength := params.NoiseStrength
	if style.MaskNoiseStrength > 0 {
		layerNoiseStrength = style.MaskNoiseStrength
	}

	// Use per-layer threshold if specified, otherwise use global threshold
	threshold := params.Threshold
	if style.MaskThreshold != nil {
		threshold = *style.MaskThreshold
	}

	blurred := sc.blurred
	mask.BoxBlurSigmaInto(blurred, baseMask, layerBlur)

	noisy := blurred
	// Skip the noise stage when no noise texture is available. Production always sets
	// params.PerlinNoise (pipeline and WASM paths), so this only guards against a missing
	// texture instead of dereferencing nil inside the noise application.
	if layerNoiseStrength != 0 && params.PerlinNoise != nil {
		noisy = sc.work
		if style.AdaptiveNoise && style.NoiseMaxDist > 0 {
			// Compute distance transform of thresholded mask to measure feature thickness
			// Use NoiseMaxDist as the max distance since we only need to distinguish up to that point
			mask.ApplyThresholdInto(blurred, threshold, sc.work)
			mask.EuclideanDistanceTransformIntoWithContext(sc.work, style.NoiseMaxDist, sc.dist, sc.work)
			mask.ApplyNoiseToMaskAdaptiveInto(blurred, params.PerlinNoise, sc.work,
				layerNoiseStrength, style.NoiseMinDist, style.NoiseMaxDist, sc.work)
		} else {
			mask.ApplyNoiseToMaskInto(blurred, params.PerlinNoise, layerNoiseStrength, sc.work)
		}
	}

	// Apply threshold with antialiasing, optionally inverting (for land = invert of non-land)
	finalMask := image.NewGray(baseMask.Bounds())
	if style.InvertMask {
		mask.ApplyThresholdWithAntialiasAndInvertInto(noisy, threshold, finalMask)
	} else {
		mask.ApplyThresholdWithAntialiasInto(noisy, threshold, finalMask)
	}

	return finalMask, nil
}

// processorContextPool recycles ProcessorContext buffers across paint calls.
// A context holds three NRGBA buffers plus a Gray buffer sized for the padded
// metatile, so a fresh one per layer per tile was several megabytes of garbage.
var processorContextPool sync.Pool

func paintFromFinalMask(finalMask *image.Gray, layer geojson.LayerType, params Params) (*image.NRGBA, error) {
	// Borrow a context from the pool. EnsureCapacity (called by the WithContext
	// variant) resizes the buffers in either direction, so a pooled context of any
	// size can be reused - which matters for a server that serves @1x and @2x tiles
	// from the same process.
	ctx, ok := processorContextPool.Get().(*ProcessorContext)
	if !ok || ctx == nil {
		ctx = NewProcessorContext(params.TileSize)
	}
	defer processorContextPool.Put(ctx)

	return paintFromFinalMaskWithContext(finalMask, layer, params, ctx)
}

func paintFromFinalMaskWithContext(finalMask *image.Gray, layer geojson.LayerType, params Params, ctx *ProcessorContext) (*image.NRGBA, error) {
	style, ok := params.Styles[layer]
	if !ok {
		return nil, fmt.Errorf("missing style for layer %s", layer)
	}
	if params.TileSize <= 0 {
		return nil, errors.New("tile size must be positive")
	}
	if style.Texture == nil {
		return nil, fmt.Errorf("texture is nil for layer %s", layer)
	}
	if finalMask == nil {
		return nil, errors.New("final mask is nil")
	}

	// Ensure context has enough capacity
	ctx.EnsureCapacity(params.TileSize)

	// The *Into helpers below are bounded by the final mask, not by the buffer:
	// ApplyMaskToTextureInto, InvertMaskInto and the edge-mask pass iterate over the
	// mask's bounds only. If the mask is smaller than the tile, the remainder of a
	// recycled buffer would still hold the previous paint, and the edge pass (which
	// runs over the full buffer) would carry those stale pixels into the result. Zero
	// the affected buffers in that case; when the mask covers the whole tile they are
	// fully overwritten anyway and clearing is skipped.
	//
	// Zero is also what the edge mask used to read outside the mask region back when it
	// was allocated at the mask's own bounds: image.Gray.GrayAt returns the zero value
	// out of bounds. Clearing keeps that behaviour bit-for-bit.
	if finalMask.Bounds() != ctx.painted.Bounds() {
		clear(ctx.painted.Pix)
		clear(ctx.tempGray.Pix)
		clear(ctx.edgeMask.Pix)
	}

	// Texture + mask using pooled buffers
	texture.TileTextureScaledInto(style.Texture, params.TileSize, params.OffsetX, params.OffsetY, params.Scale, ctx.tiledTex)
	texture.ApplyMaskToTextureInto(ctx.tiledTex, finalMask, ctx.painted)

	// result points to the current result buffer; we'll swap between painted and tempNRGBA
	result := ctx.painted

	// Optional additional shading: blur the final mask further and apply a subtle darkening.
	if style.ShadeSigma > 0 && style.ShadeStrength > 0 {
		shade := mask.BoxBlurSigma(finalMask, style.ShadeSigma)
		// Invert shade mask: we want to darken where the feature IS (high values in finalMask)
		// ApplySoftEdgeMask expects 255=no change, 0=darken, so invert the blurred mask
		mask.InvertMaskInto(shade, ctx.tempGray)
		mask.ApplySoftEdgeMaskInto(result, ctx.tempGray, style.ShadeStrength, ctx.tempNRGBA)
		// Swap the context's own buffers so painted and tempNRGBA stay distinct;
		// swapping only the local `result` would leave both fields pointing at the
		// same buffer once the context is recycled.
		ctx.painted, ctx.tempNRGBA = ctx.tempNRGBA, ctx.painted
		result = ctx.painted
	}

	// Edge darkening using distance-based edge mask
	// Convert sigma parameters to radius (approximation: radius ≈ 3*sigma)
	radius := float64(style.EdgeSigma * 3.0)
	gamma := style.EdgeGamma
	if gamma <= 0 {
		gamma = 1.0
	}

	mask.CreateDistanceEdgeMaskIntoWithContext(finalMask, radius, gamma, ctx.distCtx, ctx.edgeMask)

	// The result has to be a buffer of its own - it outlives the pooled context, which
	// the compositor holds every painted layer of a tile at once. Since it is allocated
	// anyway, write the last pass straight into it instead of copying out of the context:
	// ApplySoftEdgeMaskInto writes every pixel of result's bounds, which is exactly what
	// output is allocated with.
	//
	// ApplySoftEdgeMask expects: 255=no change, 0=maximum effect
	// CreateDistanceEdgeMask produces: 255=no effect (center), 0=max effect (edges)
	output := image.NewNRGBA(result.Bounds())
	mask.ApplySoftEdgeMaskInto(result, ctx.edgeMask, style.EdgeStrength, output)

	return output, nil
}

// PaintLayer applies the watercolor pipeline to a single rendered layer image.
func PaintLayer(layerImage image.Image, layer geojson.LayerType, params Params) (*image.NRGBA, error) {
	// Explicit rather than incidental: the scratch is sized from these bounds, so a nil
	// image would panic here where it used to fall through to "base mask is nil".
	if layerImage == nil {
		return nil, errors.New("layer image is nil")
	}
	style, ok := params.Styles[layer]
	if !ok {
		return nil, fmt.Errorf("missing style for layer %s", layer)
	}
	if params.TileSize <= 0 {
		return nil, errors.New("tile size must be positive")
	}
	if style.Texture == nil {
		return nil, fmt.Errorf("texture is nil for layer %s", layer)
	}
	if params.NoiseScale <= 0 {
		return nil, errors.New("noise scale must be positive")
	}

	// Use alpha-only mask as the base input for the mask pipeline. The scratch is
	// borrowed here rather than inside processMask so that the alpha mask - which nothing
	// downstream keeps - comes out of the pool too.
	sc := acquireMaskScratch(layerImage.Bounds())
	defer releaseMaskScratch(sc)

	mask.ExtractAlphaMaskInto(layerImage, sc.alpha)
	finalMask, err := processMaskWithScratch(sc.alpha, layer, params, sc)
	if err != nil {
		return nil, err
	}
	return paintFromFinalMask(finalMask, layer, params)
}

// PaintLayerFromMask runs the mask pipeline (blur/noise/threshold/AA) on a provided alpha mask,
// then applies texture/tinting and edge/shading. This is used for cross-layer workflows.
func PaintLayerFromMask(baseMask *image.Gray, layer geojson.LayerType, params Params) (*image.NRGBA, error) {
	painted, _, err := PaintLayerFromMaskWithMask(baseMask, layer, params)
	return painted, err
}

// PaintLayerFromMaskWithMask is like PaintLayerFromMask but also returns the processed final mask.
// This is useful when the caller needs the mask for constraining other layers (e.g., land mask for parks).
func PaintLayerFromMaskWithMask(baseMask *image.Gray, layer geojson.LayerType, params Params) (*image.NRGBA, *image.Gray, error) {
	if params.NoiseScale <= 0 {
		return nil, nil, errors.New("noise scale must be positive")
	}
	finalMask, err := processMask(baseMask, layer, params)
	if err != nil {
		return nil, nil, err
	}
	painted, err := paintFromFinalMask(finalMask, layer, params)
	if err != nil {
		return nil, nil, err
	}
	return painted, finalMask, nil
}

// PaintLayerFromFinalMask skips the blur/noise/threshold steps and paints directly from a final mask.
// Useful when the final mask is derived from other layers (e.g. landMask = invert(nonLandMask)).
func PaintLayerFromFinalMask(finalMask *image.Gray, layer geojson.LayerType, params Params) (*image.NRGBA, error) {
	return paintFromFinalMask(finalMask, layer, params)
}
