package texture

import (
	"image"
	"image/color"
	"math"
	"sync"

	"github.com/cwbudde/watercolormap/internal/geojson"
)

// ToNRGBA returns img as an *image.NRGBA, converting it if it is not one already.
//
// Textures are sampled millions of times per tile and only decoded once, so the
// concrete type they happen to decode to should not reach the sampling loops: every
// PNG in assets/ decodes to *image.RGBA, and white.png - the paper base - decodes to
// *image.Paletted, whose only reader is image.Image.At. Converting at load time turns
// both into the one type the tiling fast paths can move with copy().
//
// The conversion goes through getNRGBA, which is what the sampling loops would have
// applied to each texel, so the pixels are bit-identical to sampling the original.
// The bounds (origin included) are preserved because the tiling offsets are taken
// relative to them.
func ToNRGBA(img image.Image) *image.NRGBA {
	if img == nil {
		return nil
	}
	if src, ok := img.(*image.NRGBA); ok {
		return src
	}

	b := img.Bounds()
	dst := image.NewNRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		row := dst.Pix[dst.PixOffset(b.Min.X, y):][:4*b.Dx()]
		for i := 0; i < len(row); i += 4 {
			c := getNRGBA(img, b.Min.X+i/4, y)
			row[i], row[i+1], row[i+2], row[i+3] = c.R, c.G, c.B, c.A
		}
	}

	return dst
}

// getNRGBA extracts an NRGBA color from an image at the given coordinates.
// Uses type assertions to avoid interface boxing allocations when possible.
func getNRGBA(img image.Image, x, y int) color.NRGBA {
	switch src := img.(type) {
	case *image.NRGBA:
		return src.NRGBAAt(x, y)
	case *image.RGBA:
		c := src.RGBAAt(x, y)
		// Convert premultiplied alpha to non-premultiplied
		return unpremultiply(c.R, c.G, c.B, c.A)
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

	// The type switch happens once per call rather than once per texel: a texture is
	// decoded from PNG, so its concrete type varies between textures but never between
	// two pixels of one.
	sample := samplerFor(src)

	// The tile is laid down at coordinates 0..tileSize whatever the destination's own
	// origin is, and SetNRGBA used to drop whatever fell outside it. Clip once instead.
	r := image.Rect(0, 0, tileSize, tileSize).Intersect(dst.Bounds())
	if r.Empty() {
		return
	}
	rowLen := 4 * r.Dx()

	if scale == 1 || scale <= 0 {
		tileUnscaledInto(sample, bounds, width, height, offsetX, offsetY, r, dst)
		return
	}

	// The x mapping is the same for every row, so resolve it once. This keeps
	// the inner loop to four texel fetches and the blend. The scratch is pooled:
	// three per-call slices are exactly what the *Into convention exists to avoid.
	sc := acquireScaleScratch(tileSize)
	defer releaseScaleScratch(sc)
	x0, x1, fx := sc.x0, sc.x1, sc.fx
	for x := 0; x < tileSize; x++ {
		u := float64(offsetX+x) / scale
		fu := math.Floor(u)
		i := mod(int(fu), width)
		x0[x] = bounds.Min.X + i
		x1[x] = bounds.Min.X + mod(i+1, width)
		fx[x] = u - fu
	}

	for y := r.Min.Y; y < r.Max.Y; y++ {
		v := float64(offsetY+y) / scale
		fv := math.Floor(v)
		j := mod(int(fv), height)
		y0 := bounds.Min.Y + j
		y1 := bounds.Min.Y + mod(j+1, height)
		fy := v - fv

		dstRow := dst.Pix[dst.PixOffset(r.Min.X, y):][:rowLen]
		for i := 0; i < rowLen; i += 4 {
			x := r.Min.X + i/4
			c00 := sample.at(x0[x], y0)
			c10 := sample.at(x1[x], y0)
			c01 := sample.at(x0[x], y1)
			c11 := sample.at(x1[x], y1)
			c := bilerpNRGBA(c00, c10, c01, c11, fx[x], fy)
			dstRow[i], dstRow[i+1], dstRow[i+2], dstRow[i+3] = c.R, c.G, c.B, c.A
		}
	}
}

// tileUnscaledInto lays a texture down 1:1 over r.
//
// The unscaled tiling is doubly periodic: destination row y reads source row
// (offsetY+y) mod height, and every row reads the same, rotated, source row. So the
// work per destination pixel is a memory move, not a sample:
//
//   - one destination row is built by copying at most one texture period out of the
//     source row (two copies when the rotation wraps) and then doubling that period
//     across the rest of the row;
//   - a row whose source row was already materialised `height` rows earlier is copied
//     from that row instead of being built again.
//
// For the shipped 1024x1024 textures on a 384px metatile neither shortcut repeats
// (one period is wider and taller than the whole destination), but the per-pixel
// sampler call and its modulus still disappear.
func tileUnscaledInto(
	sample sampler, bounds image.Rectangle, width, height, offsetX, offsetY int,
	r image.Rectangle, dst *image.NRGBA,
) {
	dx := r.Dx()
	rowLen := 4 * dx

	// seg is one destination-side period: a full texture width, or the whole row when
	// the row is narrower than the texture.
	seg := width
	if seg > dx {
		seg = dx
	}

	sx0 := mod(offsetX+r.Min.X, width)

	for y := r.Min.Y; y < r.Max.Y; y++ {
		dstRow := dst.Pix[dst.PixOffset(r.Min.X, y):][:rowLen]

		// The source row for y and for y-height is the same row, and the x mapping does
		// not depend on y, so the finished row can be copied wholesale.
		if y-r.Min.Y >= height {
			copy(dstRow, dst.Pix[dst.PixOffset(r.Min.X, y-height):][:rowLen])
			continue
		}

		sy := bounds.Min.Y + mod(offsetY+y, height)
		fillTexelRow(sample, bounds.Min.X, sy, width, sx0, seg, dstRow)
	}
}

// fillTexelRow writes one destination row of tiled texels: the texture row sy read from
// source column sx0, wrapping at width, and then that one period replicated across the
// rest of the row. seg is the period in pixels, capped at the row length.
//
// The *image.NRGBA case is why textures are normalised at load time (see ToNRGBA): the
// destination and the source have the same byte layout, so the row is memory moves
// rather than one sampler call per pixel.
func fillTexelRow(sample sampler, minX, sy, width, sx0, seg int, dstRow []uint8) {
	segLen := 4 * seg

	if src := sample.nrgba; src != nil {
		srcRow := src.Pix[src.PixOffset(minX, sy):][:4*width]
		n := width - sx0
		if n > seg {
			n = seg
		}
		copy(dstRow[:4*n], srcRow[4*sx0:4*(sx0+n)])
		if n < seg {
			copy(dstRow[4*n:segLen], srcRow[:4*(seg-n)])
		}
	} else {
		sx := sx0
		for i := 0; i < segLen; i += 4 {
			c := sample.at(minX+sx, sy)
			dstRow[i], dstRow[i+1], dstRow[i+2], dstRow[i+3] = c.R, c.G, c.B, c.A
			sx++
			if sx == width {
				sx = 0
			}
		}
	}

	for filled := segLen; filled < len(dstRow); {
		filled += copy(dstRow[filled:], dstRow[:filled])
	}
}

// scaleScratch holds the per-column mapping the magnified path resolves once per call.
type scaleScratch struct {
	x0 []int
	x1 []int
	fx []float64
}

var scaleScratchPool = sync.Pool{New: func() any { return new(scaleScratch) }}

func acquireScaleScratch(n int) *scaleScratch {
	sc, ok := scaleScratchPool.Get().(*scaleScratch)
	if !ok || sc == nil {
		sc = new(scaleScratch)
	}
	if cap(sc.x0) < n {
		sc.x0 = make([]int, n)
		sc.x1 = make([]int, n)
		sc.fx = make([]float64, n)
	}
	sc.x0, sc.x1, sc.fx = sc.x0[:n], sc.x1[:n], sc.fx[:n]

	return sc
}

func releaseScaleScratch(sc *scaleScratch) {
	scaleScratchPool.Put(sc)
}

// sampler resolves a texture's concrete type once so that reading a texel does not run
// the type switch inside getNRGBA every time - the magnified path samples four texels
// per destination pixel.
//
// It is a value, not a closure, so hoisting the switch costs no allocation. The
// fallback is getNRGBA itself: textures come from image.Decode, so their concrete type
// depends on how the PNG was written.
type sampler struct {
	nrgba *image.NRGBA
	rgba  *image.RGBA
	img   image.Image
}

func samplerFor(img image.Image) sampler {
	switch src := img.(type) {
	case *image.NRGBA:
		return sampler{nrgba: src}
	case *image.RGBA:
		// Every texture that ships in assets/ decodes to this: image/png returns
		// *image.RGBA for a truecolor PNG without an alpha channel.
		return sampler{rgba: src}
	default:
		return sampler{img: img}
	}
}

func (s sampler) at(x, y int) color.NRGBA {
	switch {
	case s.nrgba != nil:
		if !(image.Point{X: x, Y: y}).In(s.nrgba.Rect) {
			return color.NRGBA{}
		}
		off := s.nrgba.PixOffset(x, y)

		return color.NRGBA{R: s.nrgba.Pix[off], G: s.nrgba.Pix[off+1], B: s.nrgba.Pix[off+2], A: s.nrgba.Pix[off+3]}
	case s.rgba != nil:
		if !(image.Point{X: x, Y: y}).In(s.rgba.Rect) {
			return color.NRGBA{}
		}
		off := s.rgba.PixOffset(x, y)

		return unpremultiply(s.rgba.Pix[off], s.rgba.Pix[off+1], s.rgba.Pix[off+2], s.rgba.Pix[off+3])
	default:
		return getNRGBA(s.img, x, y)
	}
}

// unpremultiply converts a premultiplied RGBA texel to straight alpha exactly the way
// getNRGBA's *image.RGBA case does; the two must agree bit for bit.
func unpremultiply(rr, gg, bb, aa uint8) color.NRGBA {
	if aa == 0 {
		return color.NRGBA{}
	}
	if aa == 255 {
		return color.NRGBA{R: rr, G: gg, B: bb, A: 255}
	}

	return color.NRGBA{
		R: uint8(uint16(rr) * 255 / uint16(aa)),
		G: uint8(uint16(gg) * 255 / uint16(aa)),
		B: uint8(uint16(bb) * 255 / uint16(aa)),
		A: aa,
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

	sample := samplerFor(tex)

	// The loop is bounded by the mask, but dst is often the pooled tile-size buffer and
	// may be smaller; SetNRGBA used to drop those writes.
	r := mask.Bounds().Intersect(dst.Bounds())
	if r.Empty() {
		return
	}
	dx := r.Dx()
	rowLen := 4 * dx

	// Same periodicity as the tiling path (see tileUnscaledInto): the texture repeats
	// every texW pixels across a row, so the RGB of a row is a rotation-free copy that
	// only has to be materialised once per period. The alpha is not periodic - it comes
	// from the mask - so it is written in a second, contiguous pass.
	//
	// In the pipeline this is called with the already-tiled texture, which is *image.NRGBA
	// and exactly as wide as the destination, so the whole row is one copy plus one
	// strided store.
	seg := texW
	if seg > dx {
		seg = dx
	}

	sx0 := mod(r.Min.X, texW)

	for y := r.Min.Y; y < r.Max.Y; y++ {
		sy := texBounds.Min.Y + mod(y, texH)

		maskOff := mask.PixOffset(r.Min.X, y)
		maskRow := mask.Pix[maskOff : maskOff+dx]
		dstRow := dst.Pix[dst.PixOffset(r.Min.X, y):][:rowLen]

		fillTexelRow(sample, texBounds.Min.X, sy, texW, sx0, seg, dstRow)

		// Mask controls the alpha channel; RGB comes from the texture.
		for i, m := range maskRow {
			dstRow[4*i+3] = m
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
