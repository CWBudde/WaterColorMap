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
	TileTextureScaledInto(src, tileSize, offsetX, offsetY, 1, dst)
}

// TileTextureScaled is TileTexture with a device-pixels-per-texture-pixel scale.
func TileTextureScaled(src image.Image, tileSize int, offsetX, offsetY int, scale float64) *image.NRGBA {
	if src == nil || tileSize <= 0 {
		return nil
	}

	dst := image.NewNRGBA(image.Rect(0, 0, tileSize, tileSize))
	TileTextureScaledInto(src, tileSize, offsetX, offsetY, scale, dst)
	return dst
}

// TileTextureScaledInto tiles a texture into dst, magnifying it by scale.
//
// scale is device pixels per texture pixel. At scale 1 the texture is laid down
// 1:1 and this is the plain integer tiling. At scale 2 (a @2x tile) each texture
// pixel covers 2x2 device pixels, so the pattern keeps the same footprint on the
// ground as it has on the matching 256px tile — which is the entire point:
// offsets are in device pixels, so without this the grain would be half the
// ground size at @2x and the two tile sets would not match.
//
// Magnified sampling is bilinear, and the wrap happens *inside* the
// interpolation. Upscaling first and taking the modulus afterwards would blend
// the texture's last column with black instead of with its first column, laying
// a visible seam at every texture period.
func TileTextureScaledInto(src image.Image, tileSize int, offsetX, offsetY int, scale float64, dst *image.NRGBA) {
	if src == nil || tileSize <= 0 || dst == nil {
		return
	}

	bounds := src.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	if width == 0 || height == 0 {
		return
	}

	if scale == 1 || scale <= 0 {
		for y := 0; y < tileSize; y++ {
			sy := bounds.Min.Y + mod(offsetY+y, height)
			for x := 0; x < tileSize; x++ {
				sx := bounds.Min.X + mod(offsetX+x, width)
				dst.SetNRGBA(x, y, getNRGBA(src, sx, sy))
			}
		}
		return
	}

	// The x mapping is the same for every row, so resolve it once. This keeps
	// the inner loop to four texel fetches and the blend.
	x0 := make([]int, tileSize)
	x1 := make([]int, tileSize)
	fx := make([]float64, tileSize)
	for x := 0; x < tileSize; x++ {
		u := float64(offsetX+x) / scale
		fu := math.Floor(u)
		i := mod(int(fu), width)
		x0[x] = bounds.Min.X + i
		x1[x] = bounds.Min.X + mod(i+1, width)
		fx[x] = u - fu
	}

	for y := 0; y < tileSize; y++ {
		v := float64(offsetY+y) / scale
		fv := math.Floor(v)
		j := mod(int(fv), height)
		y0 := bounds.Min.Y + j
		y1 := bounds.Min.Y + mod(j+1, height)
		fy := v - fv

		for x := 0; x < tileSize; x++ {
			c00 := getNRGBA(src, x0[x], y0)
			c10 := getNRGBA(src, x1[x], y0)
			c01 := getNRGBA(src, x0[x], y1)
			c11 := getNRGBA(src, x1[x], y1)
			dst.SetNRGBA(x, y, bilerpNRGBA(c00, c10, c01, c11, fx[x], fy))
		}
	}
}

// mod is a modulus that returns a non-negative result for negative operands,
// which matters because tile offsets go negative at the metatile's left/top pad.
func mod(a, b int) int {
	r := a % b
	if r < 0 {
		r += b
	}
	return r
}

// bilerpNRGBA blends four texels. Channels are interpolated independently;
// alpha in these textures is effectively constant, so no premultiplication
// round-trip is warranted.
func bilerpNRGBA(c00, c10, c01, c11 color.NRGBA, fx, fy float64) color.NRGBA {
	blend := func(a, b, c, d uint8) uint8 {
		top := float64(a) + (float64(b)-float64(a))*fx
		bot := float64(c) + (float64(d)-float64(c))*fx
		return uint8(top + (bot-top)*fy + 0.5)
	}
	return color.NRGBA{
		R: blend(c00.R, c10.R, c01.R, c11.R),
		G: blend(c00.G, c10.G, c01.G, c11.G),
		B: blend(c00.B, c10.B, c01.B, c11.B),
		A: blend(c00.A, c10.A, c01.A, c11.A),
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
