package texture

import (
	"image"
	"image/color"
	"math"

	"github.com/cwbudde/watercolormap/internal/geojson"
)

// getNRGBA extracts an NRGBA color from an image at the given coordinates.
// Uses type assertions to avoid interface boxing allocations when possible.
func getNRGBA(img image.Image, x, y int) color.NRGBA {
	switch src := img.(type) {
	case *image.NRGBA:
		return src.NRGBAAt(x, y)
	case *image.RGBA:
		c := src.RGBAAt(x, y)
		// Convert premultiplied alpha to non-premultiplied
		if c.A == 0 {
			return color.NRGBA{}
		}
		if c.A == 255 {
			return color.NRGBA{R: c.R, G: c.G, B: c.B, A: 255}
		}
		return color.NRGBA{
			R: uint8(uint16(c.R) * 255 / uint16(c.A)),
			G: uint8(uint16(c.G) * 255 / uint16(c.A)),
			B: uint8(uint16(c.B) * 255 / uint16(c.A)),
			A: c.A,
		}
	default:
		// Fallback to interface method (causes allocation)
		nrgba, ok := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
		if !ok {
			return color.NRGBA{}
		}
		return nrgba
	}
}

// DefaultLayerTextures maps layer types to their default texture filenames.
var DefaultLayerTextures = map[geojson.LayerType]string{
	geojson.LayerLand:      "land.png",
	geojson.LayerWater:     "water.png",
	geojson.LayerParks:     "green.png",
	geojson.LayerUrban:     "urban.png",
	geojson.LayerCivic:     "civic.png",
	geojson.LayerBuildings: "urban.png",
	geojson.LayerRoads:     "gray.png",
	geojson.LayerRailroads: "railroad.png",
	geojson.LayerHighways:  "yellow.png",
	geojson.LayerPaper:     "white.png",
}

// TextureNameForLayer returns the default texture filename for a layer.
func TextureNameForLayer(layer geojson.LayerType) (string, bool) {
	name, ok := DefaultLayerTextures[layer]
	return name, ok
}

// TileTexture tiles a source texture into a square tile of the given size.
// Offsets align the sampling to a global texture grid to keep seams invisible across tiles.
func TileTexture(src image.Image, tileSize int, offsetX, offsetY int) *image.NRGBA {
	if src == nil || tileSize <= 0 {
		return nil
	}

	dst := image.NewNRGBA(image.Rect(0, 0, tileSize, tileSize))
	TileTextureInto(src, tileSize, offsetX, offsetY, dst)
	return dst
}

// TileTextureInto tiles a source texture into an existing destination buffer.
// This avoids allocation when the caller can reuse a buffer.
func TileTextureInto(src image.Image, tileSize int, offsetX, offsetY int, dst *image.NRGBA) {
	if src == nil || tileSize <= 0 || dst == nil {
		return
	}

	bounds := src.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	if width == 0 || height == 0 {
		return
	}

	mod := func(a, b int) int {
		r := a % b
		if r < 0 {
			r += b
		}
		return r
	}

	for y := 0; y < tileSize; y++ {
		sy := bounds.Min.Y + mod(offsetY+y, height)
		for x := 0; x < tileSize; x++ {
			sx := bounds.Min.X + mod(offsetX+x, width)
			dst.SetNRGBA(x, y, getNRGBA(src, sx, sy))
		}
	}
}

// ApplyMaskToTexture applies a grayscale mask as the alpha channel to a texture.
// The texture is tiled if smaller than the mask to avoid seams at the edges.
func ApplyMaskToTexture(tex image.Image, mask *image.Gray) *image.NRGBA {
	if tex == nil || mask == nil {
		return nil
	}

	dst := image.NewNRGBA(mask.Bounds())
	ApplyMaskToTextureInto(tex, mask, dst)
	return dst
}

// ApplyMaskToTextureInto applies a grayscale mask as the alpha channel into an existing buffer.
// This avoids allocation when the caller can reuse a buffer.
func ApplyMaskToTextureInto(tex image.Image, mask *image.Gray, dst *image.NRGBA) {
	if tex == nil || mask == nil || dst == nil {
		return
	}

	texBounds := tex.Bounds()
	texW := texBounds.Dx()
	texH := texBounds.Dy()

	if texW == 0 || texH == 0 {
		return
	}

	mod := func(a, b int) int {
		r := a % b
		if r < 0 {
			r += b
		}
		return r
	}

	for y := mask.Bounds().Min.Y; y < mask.Bounds().Max.Y; y++ {
		sy := texBounds.Min.Y + mod(y, texH)
		for x := mask.Bounds().Min.X; x < mask.Bounds().Max.X; x++ {
			sx := texBounds.Min.X + mod(x, texW)

			c := getNRGBA(tex, sx, sy)
			// Mask controls the alpha channel; RGB comes from the texture
			c.A = mask.GrayAt(x, y).Y
			dst.SetNRGBA(x, y, c)
		}
	}
}

// TintTexture overlays a tint color onto a texture with the given strength (0-1).
// The alpha channel is preserved from the original texture.
func TintTexture(tex image.Image, tint color.NRGBA, strength float64) *image.NRGBA {
	if tex == nil {
		return nil
	}

	if strength < 0 {
		strength = 0
	}
	if strength > 1 {
		strength = 1
	}

	bounds := tex.Bounds()
	dst := image.NewNRGBA(bounds)

	blend := func(src, tgt uint8) uint8 {
		val := (1.0-strength)*float64(src) + strength*float64(tgt)
		return uint8(math.Round(val))
	}

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			srcColor := getNRGBA(tex, x, y)

			dst.SetNRGBA(x, y, color.NRGBA{
				R: blend(srcColor.R, tint.R),
				G: blend(srcColor.G, tint.G),
				B: blend(srcColor.B, tint.B),
				A: srcColor.A, // Preserve original alpha; tint applies to color only
			})
		}
	}

	return dst
}
