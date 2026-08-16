package texture

import (
	"image"
	"image/color"
	"math/rand/v2"
	"testing"
)

// The sizes here are the production ones, not the ones the pipeline benchmark uses.
// `assets/textures` ships 1024x1024 PNGs (urban.png is 512x512) and a 256px tile is
// rendered on a 384px padded metatile, so the destination is smaller than one texture
// period in both axes - which is the case the row replication cannot shortcut.
const (
	benchTextureSize = 1024
	benchTileSize    = 384
)

// benchNRGBATexture builds a texture with the concrete type an RGBA PNG with an alpha
// channel decodes to.
func benchNRGBATexture(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	rng := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i] = uint8(rng.UintN(256))
		img.Pix[i+1] = uint8(rng.UintN(256))
		img.Pix[i+2] = uint8(rng.UintN(256))
		img.Pix[i+3] = 255
	}

	return img
}

// benchRGBATexture builds a texture with the concrete type the shipped textures actually
// decode to: image/png returns *image.RGBA for a truecolor PNG without an alpha channel.
func benchRGBATexture(size int) *image.RGBA {
	src := benchNRGBATexture(size)
	dst := image.NewRGBA(src.Bounds())
	copy(dst.Pix, src.Pix) // opaque, so premultiplied and straight alpha agree

	return dst
}

// benchPalettedTexture builds a paletted texture - white.png, the paper base, is one.
func benchPalettedTexture(size int) *image.Paletted {
	pal := make(color.Palette, 0, 256)
	for i := 0; i < 256; i++ {
		pal = append(pal, color.NRGBA{R: uint8(i), G: uint8(255 - i), B: uint8(i / 2), A: 255})
	}
	img := image.NewPaletted(image.Rect(0, 0, size, size), pal)
	rng := rand.New(rand.NewPCG(3, 4))
	for i := range img.Pix {
		img.Pix[i] = uint8(rng.UintN(256))
	}

	return img
}

func benchTextures() map[string]image.Image {
	return map[string]image.Image{
		"NRGBA":    benchNRGBATexture(benchTextureSize),
		"RGBA":     benchRGBATexture(benchTextureSize),
		"Paletted": benchPalettedTexture(benchTextureSize),
	}
}

func BenchmarkTileTextureInto(b *testing.B) {
	dst := image.NewNRGBA(image.Rect(0, 0, benchTileSize, benchTileSize))
	for name, src := range benchTextures() {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				TileTextureInto(src, benchTileSize, 613, 907, dst)
			}
		})
	}
}

// BenchmarkTileTextureIntoSmall keeps the pipeline benchmark's case: a texture whose
// period is much smaller than the tile, so a row is mostly replication.
func BenchmarkTileTextureIntoSmall(b *testing.B) {
	src := benchNRGBATexture(8)
	dst := image.NewNRGBA(image.Rect(0, 0, benchTileSize, benchTileSize))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		TileTextureInto(src, benchTileSize, 3, 5, dst)
	}
}

func BenchmarkTileTextureScaledInto(b *testing.B) {
	dst := image.NewNRGBA(image.Rect(0, 0, benchTileSize, benchTileSize))
	for name, src := range benchTextures() {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				TileTextureScaledInto(src, benchTileSize, 613, 907, 2, dst)
			}
		})
	}
}

func BenchmarkApplyMaskToTextureInto(b *testing.B) {
	msk := image.NewGray(image.Rect(0, 0, benchTileSize, benchTileSize))
	rng := rand.New(rand.NewPCG(5, 6))
	for i := range msk.Pix {
		msk.Pix[i] = uint8(rng.UintN(256))
	}
	dst := image.NewNRGBA(image.Rect(0, 0, benchTileSize, benchTileSize))

	// The pipeline calls this with the tiled texture, which is always *image.NRGBA and
	// exactly tile sized; the RGBA case covers a caller passing a raw texture.
	tiled := image.NewNRGBA(image.Rect(0, 0, benchTileSize, benchTileSize))
	TileTextureInto(benchNRGBATexture(benchTextureSize), benchTileSize, 613, 907, tiled)

	srcs := map[string]image.Image{
		"TiledNRGBA": tiled,
		"RGBA":       benchRGBATexture(benchTextureSize),
	}
	for name, src := range srcs {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				ApplyMaskToTextureInto(src, msk, dst)
			}
		})
	}
}
