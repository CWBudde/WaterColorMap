package watercolor

import (
	"image"

	"github.com/cwbudde/watercolormap/internal/geojson"
)

// ReferenceTileSize is the tile size every watercolor length parameter is
// expressed in. Sigmas, noise scale and edge distances in DefaultParams are all
// "pixels at 256 px per tile".
const ReferenceTileSize = 256

// ScaleForTileSize returns device pixels per world pixel for an output tile
// size: 1.0 for a 256 px tile, 2.0 for a 512 px (@2x) tile.
//
// "World pixels" are Web-Mercator tile pixels at ReferenceTileSize per tile.
// The watercolor pipeline anchors noise and textures to world position via
// Params.OffsetX/OffsetY, which are already in device pixels and already
// correct. What is *not* automatically correct is every length those offsets
// are measured against — a noise period of 30 covers 30 world pixels at 1x but
// only 15 at 2x. Scaling those lengths by this factor is what keeps a tile and
// its @2x twin showing the same grain over the same ground.
func ScaleForTileSize(tileSize int) float64 {
	if tileSize <= 0 {
		return 1
	}
	return float64(tileSize) / float64(ReferenceTileSize)
}

// ApplyScale multiplies every length-valued parameter by s, converting params
// expressed at ReferenceTileSize into device pixels for a tile of scale s.
//
// Lengths scale; ratios, gammas and thresholds do not. Seed in particular must
// not change: an identical seed across tile sizes is precisely what makes the
// noise field the same field.
//
// s == 1 returns immediately without touching a single field, so the 256 px
// path is bit-identical rather than merely arithmetically equal.
func (p *Params) ApplyScale(s float64) {
	if s == 1 || s <= 0 {
		return
	}

	p.Scale = s

	f32 := float32(s)
	p.NoiseScale *= s
	p.BlurSigma *= f32
	p.AntialiasSigma *= f32

	// Styles is a map of values; mutate through a copy and write it back so we
	// never depend on the caller's map being freshly built.
	scaled := make(map[geojson.LayerType]LayerStyle, len(p.Styles))
	for layer, style := range p.Styles {
		style.MaskBlurSigma *= f32
		style.ShadeSigma *= f32
		style.EdgeSigma *= f32
		style.NoiseMinDist *= s
		style.NoiseMaxDist *= s
		scaled[layer] = style
	}
	p.Styles = scaled
}

// DefaultParamsForTileSize returns DefaultParams with every length parameter
// scaled to the given output tile size (the final tile, not the metatile).
//
// DefaultParams itself deliberately stays scale-free: several tests build it at
// tile sizes of 16, 32 and 64 to keep fixtures small, and deriving a scale from
// those would shrink their sigmas by 4-16x and quietly change what those tests
// assert.
func DefaultParamsForTileSize(outputTileSize int, seed int64, textures map[geojson.LayerType]image.Image) Params {
	p := DefaultParams(outputTileSize, seed, textures)
	p.ApplyScale(ScaleForTileSize(outputTileSize))
	return p
}
