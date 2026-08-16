package texture

import (
	"bytes"
	"image"
	"image/color"
	"math"
	"math/rand/v2"
	"testing"
)

// 5.11.4 moved the texture loops off SetNRGBA and hoisted the per-texel type switch in
// getNRGBA out into a sampler resolved once per call. This file keeps the previous loop
// bodies verbatim as references and compares whole Pix slices.
//
// Two behaviours the accessors used to provide for free have to survive: a destination
// smaller than the tile clips rather than panicking, and a texture whose concrete type
// is not one the sampler specialises still goes through getNRGBA.

func referenceTileTextureScaledInto(
	src image.Image, tileSize int, offsetX, offsetY int, scale float64, dst *image.NRGBA,
) {
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

func referenceApplyMaskToTextureInto(tex image.Image, mask *image.Gray, dst *image.NRGBA) {
	texBounds := tex.Bounds()
	texW := texBounds.Dx()
	texH := texBounds.Dy()

	if texW == 0 || texH == 0 {
		return
	}

	for y := mask.Bounds().Min.Y; y < mask.Bounds().Max.Y; y++ {
		sy := texBounds.Min.Y + mod(y, texH)
		for x := mask.Bounds().Min.X; x < mask.Bounds().Max.X; x++ {
			sx := texBounds.Min.X + mod(x, texW)

			c := getNRGBA(tex, sx, sy)
			c.A = mask.GrayAt(x, y).Y
			dst.SetNRGBA(x, y, c)
		}
	}
}

func randomTexture(bounds image.Rectangle, seed uint64) *image.NRGBA {
	img := image.NewNRGBA(bounds)
	rng := rand.New(rand.NewPCG(seed, 0x7e47))

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8(rng.UintN(256)),
				G: uint8(rng.UintN(256)),
				B: uint8(rng.UintN(256)),
				A: uint8(rng.UintN(256)),
			})
		}
	}

	return img
}

func randomGray(bounds image.Rectangle, seed uint64) *image.Gray {
	m := image.NewGray(bounds)
	rng := rand.New(rand.NewPCG(seed, 0x7e47))
	for i := range m.Pix {
		m.Pix[i] = uint8(rng.UintN(256))
	}

	return m
}

func filledNRGBA(bounds image.Rectangle, fill byte) *image.NRGBA {
	img := image.NewNRGBA(bounds)
	for i := range img.Pix {
		img.Pix[i] = fill
	}

	return img
}

// randomRGBA builds a premultiplied texture - the concrete type every PNG in assets/
// decodes to - with alpha values that make the un-premultiplication non-trivial.
func randomRGBA(bounds image.Rectangle, seed uint64) *image.RGBA {
	img := image.NewRGBA(bounds)
	rng := rand.New(rand.NewPCG(seed, 0x7e47))
	for i := 0; i < len(img.Pix); i += 4 {
		a := uint8(rng.UintN(256))
		img.Pix[i] = uint8(rng.UintN(uint(a) + 1))
		img.Pix[i+1] = uint8(rng.UintN(uint(a) + 1))
		img.Pix[i+2] = uint8(rng.UintN(uint(a) + 1))
		img.Pix[i+3] = a
	}

	return img
}

// randomPaletted builds a paletted texture - white.png, the paper base, is one.
func randomPaletted(bounds image.Rectangle, seed uint64) *image.Paletted {
	pal := make(color.Palette, 0, 64)
	for i := 0; i < 64; i++ {
		pal = append(pal, color.NRGBA{R: uint8(4 * i), G: uint8(255 - 4*i), B: uint8(2 * i), A: uint8(128 + 2*i)})
	}
	img := image.NewPaletted(bounds, pal)
	rng := rand.New(rand.NewPCG(seed, 0x7e47))
	for i := range img.Pix {
		img.Pix[i] = uint8(rng.UintN(64))
	}

	return img
}

// opaqueImage hides a texture's concrete type so the sampler falls through to getNRGBA.
type opaqueImage struct{ img *image.NRGBA }

func (o opaqueImage) ColorModel() color.Model { return o.img.ColorModel() }
func (o opaqueImage) Bounds() image.Rectangle { return o.img.Bounds() }
func (o opaqueImage) At(x, y int) color.Color { return o.img.At(x, y) }

func TestTileTextureScaledIntoMatchesReference(t *testing.T) {
	const tileSize = 48

	// A texture that is neither square, nor a divisor of the tile, nor at the origin,
	// so the wrap-around indexing is genuinely exercised.
	textures := map[string]image.Image{
		"NRGBA":   randomTexture(image.Rect(0, 0, 13, 7), 1),
		"offset":  randomTexture(image.Rect(5, 9, 5+11, 9+6), 2),
		"generic": opaqueImage{randomTexture(image.Rect(0, 0, 13, 7), 3)},
		// Production textures are 1024px on a 384px metatile, so the texture is wider
		// and taller than the destination and neither the row replication nor the row
		// reuse ever repeats. The small textures above are the opposite case.
		"larger than tile": randomTexture(image.Rect(0, 0, tileSize+9, tileSize+5), 9),
		"RGBA":             randomRGBA(image.Rect(0, 0, 13, 7), 10),
		"paletted":         randomPaletted(image.Rect(0, 0, 13, 7), 11),
	}

	// Negative offsets happen at the metatile's left and top pad.
	offsets := [][2]int{{0, 0}, {7, 3}, {-5, -9}}
	scales := []float64{1, 2, 1.5}

	for name, tex := range textures {
		for _, off := range offsets {
			for _, scale := range scales {
				for _, dstBounds := range destinationBounds(tileSize) {
					got := filledNRGBA(dstBounds, 0x6B)
					want := filledNRGBA(dstBounds, 0x6B)

					TileTextureScaledInto(tex, tileSize, off[0], off[1], scale, got)
					referenceTileTextureScaledInto(tex, tileSize, off[0], off[1], scale, want)

					if !bytes.Equal(got.Pix, want.Pix) {
						t.Errorf("%s: offset %v scale %v dst %v differs from the reference implementation",
							name, off, scale, dstBounds)
					}
				}
			}
		}
	}
}

func TestApplyMaskToTextureIntoMatchesReference(t *testing.T) {
	texs := map[string]image.Image{
		"NRGBA":            randomTexture(image.Rect(0, 0, 9, 5), 4),
		"generic":          opaqueImage{randomTexture(image.Rect(0, 0, 9, 5), 5)},
		"larger than mask": randomTexture(image.Rect(0, 0, 40, 37), 12),
		"RGBA":             randomRGBA(image.Rect(0, 0, 9, 5), 13),
		"paletted":         randomPaletted(image.Rect(0, 0, 9, 5), 14),
	}

	maskBounds := []image.Rectangle{
		image.Rect(0, 0, 1, 1),
		image.Rect(0, 0, 32, 32),
		image.Rect(3, 4, 3+20, 4+17),
	}

	for name, tex := range texs {
		for _, mb := range maskBounds {
			mask := randomGray(mb, 6)

			for _, dstBounds := range destinationBoundsFor(mb) {
				got := filledNRGBA(dstBounds, 0x21)
				want := filledNRGBA(dstBounds, 0x21)

				ApplyMaskToTextureInto(tex, mask, got)
				referenceApplyMaskToTextureInto(tex, mask, want)

				if !bytes.Equal(got.Pix, want.Pix) {
					t.Errorf("%s: mask %v dst %v differs from the reference implementation",
						name, mb, dstBounds)
				}
			}
		}
	}
}

// destinationBounds returns the destination shapes a tiling call has to cope with: the
// tile itself, something larger, and something smaller than the tile.
func destinationBounds(tileSize int) []image.Rectangle {
	return destinationBoundsFor(image.Rect(0, 0, tileSize, tileSize))
}

func destinationBoundsFor(src image.Rectangle) []image.Rectangle {
	return []image.Rectangle{
		src,
		image.Rect(src.Min.X-2, src.Min.Y-3, src.Max.X+4, src.Max.Y+2),
		image.Rect(src.Min.X, src.Min.Y, src.Min.X+max(1, src.Dx()/2), src.Min.Y+max(1, src.Dy()/3)),
	}
}

func TestTextureLoopsDoNotAllocate(t *testing.T) {
	const tileSize = 64

	tex := randomTexture(image.Rect(0, 0, 16, 16), 7)
	dst := image.NewNRGBA(image.Rect(0, 0, tileSize, tileSize))
	mask := randomGray(image.Rect(0, 0, tileSize, tileSize), 8)

	// The sampler is a value, not a closure, so hoisting the type switch must not have
	// put an allocation back on the per-tile path.
	if n := testing.AllocsPerRun(20, func() {
		TileTextureScaledInto(tex, tileSize, 3, 5, 1, dst)
	}); n != 0 {
		t.Errorf("TileTextureScaledInto: got %v allocations per run, want 0", n)
	}

	if n := testing.AllocsPerRun(20, func() {
		ApplyMaskToTextureInto(tex, mask, dst)
	}); n != 0 {
		t.Errorf("ApplyMaskToTextureInto: got %v allocations per run, want 0", n)
	}

	// The magnified path resolves a per-column mapping; it comes out of a pool, so the
	// hi-DPI path allocates nothing per call either.
	if n := testing.AllocsPerRun(20, func() {
		TileTextureScaledInto(tex, tileSize, 3, 5, 2, dst)
	}); n != 0 {
		t.Errorf("TileTextureScaledInto(scale=2): got %v allocations per run, want 0", n)
	}
}

// TestToNRGBAIsBitIdentical pins the invariant the load-time normalisation rests on:
// tiling a converted texture must produce exactly the bytes tiling the original did,
// because that conversion is what keeps the rendered tiles unchanged.
func TestToNRGBAIsBitIdentical(t *testing.T) {
	const tileSize = 48

	originals := map[string]image.Image{
		"RGBA":     randomRGBA(image.Rect(0, 0, 13, 7), 20),
		"paletted": randomPaletted(image.Rect(0, 0, 13, 7), 21),
		"generic":  opaqueImage{randomTexture(image.Rect(0, 0, 13, 7), 22)},
		"offset":   randomRGBA(image.Rect(5, 9, 5+11, 9+6), 23),
	}

	for name, orig := range originals {
		converted := ToNRGBA(orig)
		if converted.Bounds() != orig.Bounds() {
			t.Errorf("%s: ToNRGBA changed the bounds: %v vs %v", name, converted.Bounds(), orig.Bounds())
		}

		for _, scale := range []float64{1, 2} {
			got := image.NewNRGBA(image.Rect(0, 0, tileSize, tileSize))
			want := image.NewNRGBA(image.Rect(0, 0, tileSize, tileSize))

			TileTextureScaledInto(converted, tileSize, -5, 7, scale, got)
			TileTextureScaledInto(orig, tileSize, -5, 7, scale, want)

			if !bytes.Equal(got.Pix, want.Pix) {
				t.Errorf("%s: tiling the converted texture differs at scale %v", name, scale)
			}
		}

		mask := randomGray(image.Rect(0, 0, tileSize, tileSize), 24)
		gotMasked := image.NewNRGBA(mask.Bounds())
		wantMasked := image.NewNRGBA(mask.Bounds())
		ApplyMaskToTextureInto(converted, mask, gotMasked)
		ApplyMaskToTextureInto(orig, mask, wantMasked)

		if !bytes.Equal(gotMasked.Pix, wantMasked.Pix) {
			t.Errorf("%s: masking the converted texture differs", name)
		}
	}

	if ToNRGBA(nil) != nil {
		t.Error("ToNRGBA(nil): want nil")
	}
	src := randomTexture(image.Rect(0, 0, 4, 4), 25)
	if ToNRGBA(src) != src {
		t.Error("ToNRGBA: an *image.NRGBA should be returned as-is, not copied")
	}
}
