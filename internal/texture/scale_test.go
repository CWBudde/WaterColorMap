package texture

import (
	"image"
	"image/color"
	"testing"
)

// noisyTexture builds a small texture where every texel differs, so a phase or
// scale error cannot hide behind flat colour.
func noisyTexture(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8((x*37 + y*11) % 256),
				G: uint8((x*5 + y*97) % 256),
				B: uint8((x*61 + y*29) % 256),
				A: 255,
			})
		}
	}
	return img
}

// TestTexturePhaseAlignedAcrossScales is the texture counterpart of
// TestNoiseFieldAlignedAcrossScales: at @2x the texture must cover the same
// ground, so device pixel (2i,2j) must carry exactly the texel that world pixel
// (i,j) carries at 1x. The comparison is exact because at even device
// coordinates the bilinear weights land on 0 and the blend returns the texel
// itself.
func TestTexturePhaseAlignedAcrossScales(t *testing.T) {
	src := noisyTexture(64, 64)

	const (
		size = 96
		offX = -37 // negative: metatile padding pushes offsets left of the origin
		offY = 250
	)

	base := TileTexture(src, size, offX, offY)
	scaled := TileTextureScaled(src, 2*size, 2*offX, 2*offY, 2)

	for j := 0; j < size; j++ {
		for i := 0; i < size; i++ {
			want := base.NRGBAAt(i, j)
			got := scaled.NRGBAAt(2*i, 2*j)
			if got != want {
				t.Fatalf("texture mismatch at world (%d,%d): 1x=%v, 2x@(%d,%d)=%v",
					i, j, want, 2*i, 2*j, got)
			}
		}
	}
}

// TestTileTextureScaledWrapsInsideInterpolation is the reason the modulus lives
// inside the blend rather than after an upscale. Device pixels between the
// texture's last column and its first must interpolate between those two
// texels. Upscaling first and wrapping afterwards would instead blend the last
// column toward whatever lies past the image edge, laying a visible seam every
// texture period.
func TestTileTextureScaledWrapsInsideInterpolation(t *testing.T) {
	const (
		texW  = 8
		scale = 4
	)
	src := noisyTexture(texW, texW)

	// Cover one full period plus the wrap into the next.
	out := TileTextureScaled(src, texW*scale+scale, 0, 0, scale)

	last := src.NRGBAAt(texW-1, 0)
	first := src.NRGBAAt(0, 0)

	for k := 1; k < scale; k++ {
		x := (texW-1)*scale + k
		fx := float64(k) / scale

		want := uint8(float64(last.R) + (float64(first.R)-float64(last.R))*fx + 0.5)
		if got := out.NRGBAAt(x, 0).R; got != want {
			t.Errorf("device x=%d (fx=%.2f): R=%d, want %d — the wrap must blend texel %d into texel 0",
				x, fx, got, want, texW-1)
		}
	}
}

// TestTileTextureScaledUnitScaleMatchesPlain pins the fast path: at scale 1 the
// scaled tiler must produce byte-identical output to the original integer
// tiling, which is what keeps 256px goldens from moving.
func TestTileTextureScaledUnitScaleMatchesPlain(t *testing.T) {
	src := noisyTexture(16, 16)

	plain := TileTexture(src, 40, -5, 7)
	scaled := TileTextureScaled(src, 40, -5, 7, 1)

	if len(plain.Pix) != len(scaled.Pix) {
		t.Fatalf("buffer sizes differ: %d vs %d", len(plain.Pix), len(scaled.Pix))
	}
	for i := range plain.Pix {
		if plain.Pix[i] != scaled.Pix[i] {
			t.Fatalf("scale 1 is not byte-identical to plain tiling at byte %d", i)
		}
	}
}
