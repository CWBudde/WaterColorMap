package composite

import (
	"bytes"
	"image"
	"image/color"
	"math"
	"math/rand/v2"
	"testing"
)

// The compositor reads its layers through Pix rather than through image.Image, with the
// interface loop kept as a fallback for non-NRGBA sources. Both paths have to produce
// exactly what the interface loop produced before, so this file keeps that loop verbatim
// as the reference and compares byte for byte.

// referenceAlphaOver is the pre-5.11.4 implementation of alphaOver.
func referenceAlphaOver(dst *image.NRGBA, src image.Image) {
	bounds := dst.Bounds()

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			s, ok := color.NRGBAModel.Convert(src.At(x, y)).(color.NRGBA)
			if !ok || s.A == 0 {
				continue
			}

			d := dst.NRGBAAt(x, y)

			sa := float64(s.A) / 255.0
			da := float64(d.A) / 255.0

			outA := sa + da*(1.0-sa)
			if outA == 0 {
				dst.SetNRGBA(x, y, color.NRGBA{})
				continue
			}

			blend := func(srcVal, dstVal uint8) uint8 {
				srcPremult := float64(srcVal) * sa
				dstPremult := float64(dstVal) * da
				outPremult := srcPremult + dstPremult*(1.0-sa)
				return uint8(math.Round(outPremult / outA))
			}

			dst.SetNRGBA(x, y, color.NRGBA{
				R: blend(s.R, d.R),
				G: blend(s.G, d.G),
				B: blend(s.B, d.B),
				A: uint8(math.Round(outA * 255.0)),
			})
		}
	}
}

// randomNRGBA fills an image with deterministic noise, including fully transparent and
// fully opaque pixels so both branches of the blend are exercised.
func randomNRGBA(bounds image.Rectangle, seed uint64) *image.NRGBA {
	img := image.NewNRGBA(bounds)
	rng := rand.New(rand.NewPCG(seed, 0x5eed))

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			a := uint8(rng.UintN(256))
			switch rng.UintN(4) {
			case 0:
				a = 0
			case 1:
				a = 255
			}
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8(rng.UintN(256)),
				G: uint8(rng.UintN(256)),
				B: uint8(rng.UintN(256)),
				A: a,
			})
		}
	}

	return img
}

func pixelTestBounds() []image.Rectangle {
	return []image.Rectangle{
		image.Rect(0, 0, 1, 1),
		image.Rect(0, 0, 3, 3),
		image.Rect(0, 0, 64, 64),
		image.Rect(0, 0, 384, 384),
	}
}

func TestAlphaOverMatchesReference(t *testing.T) {
	for _, bounds := range pixelTestBounds() {
		src := randomNRGBA(bounds, 1)

		got := randomNRGBA(bounds, 2)
		want := image.NewNRGBA(bounds)
		copy(want.Pix, got.Pix)

		alphaOver(got, src)
		referenceAlphaOver(want, src)

		if !bytes.Equal(got.Pix, want.Pix) {
			t.Errorf("%v: alphaOver differs from the reference implementation", bounds)
		}
	}
}

// The fallback path has to agree with the fast path, so that a layer arriving as some
// other image type composites identically.
func TestAlphaOverFallbackMatchesFastPath(t *testing.T) {
	bounds := image.Rect(0, 0, 64, 64)
	src := randomNRGBA(bounds, 3)

	// image.RGBA is premultiplied, so round-tripping through it would not be lossless.
	// A wrapper that hides the concrete type keeps the same pixels while forcing the
	// generic branch.
	opaque := opaqueImage{src}

	fast := randomNRGBA(bounds, 4)
	generic := image.NewNRGBA(bounds)
	copy(generic.Pix, fast.Pix)

	alphaOver(fast, src)
	alphaOver(generic, opaque)

	if !bytes.Equal(fast.Pix, generic.Pix) {
		t.Error("the generic path differs from the *image.NRGBA fast path")
	}
}

// opaqueImage hides the concrete type of an image so alphaOver takes its generic path.
type opaqueImage struct{ img *image.NRGBA }

func (o opaqueImage) ColorModel() color.Model { return o.img.ColorModel() }
func (o opaqueImage) Bounds() image.Rectangle { return o.img.Bounds() }
func (o opaqueImage) At(x, y int) color.Color { return o.img.At(x, y) }

func TestCompositeBaseCopyMatchesReference(t *testing.T) {
	for _, bounds := range pixelTestBounds() {
		base := randomNRGBA(bounds, 5)

		got := image.NewNRGBA(bounds)
		copyBase(got, base)

		want := image.NewNRGBA(bounds)
		copyBase(want, opaqueImage{base})

		if !bytes.Equal(got.Pix, want.Pix) {
			t.Errorf("%v: the base copy differs between the fast and generic paths", bounds)
		}
	}
}

func TestAlphaOverDoesNotAllocate(t *testing.T) {
	bounds := image.Rect(0, 0, 64, 64)
	src := randomNRGBA(bounds, 6)
	dst := randomNRGBA(bounds, 7)

	if n := testing.AllocsPerRun(20, func() {
		alphaOver(dst, src)
	}); n != 0 {
		t.Errorf("got %v allocations per run, want 0", n)
	}
}
