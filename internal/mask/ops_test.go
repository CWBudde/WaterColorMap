package mask

import (
	"image"
	"image/color"
	"testing"
)

func TestExtractAlphaMaskPreservesAlpha(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	img.SetNRGBA(0, 0, color.NRGBA{R: 10, G: 20, B: 30, A: 0})
	img.SetNRGBA(1, 0, color.NRGBA{R: 10, G: 20, B: 30, A: 200})

	m := ExtractAlphaMask(img)
	if m == nil {
		t.Fatal("expected non-nil mask")
	}
	if got := m.GrayAt(0, 0).Y; got != 0 {
		t.Fatalf("expected alpha 0 at (0,0), got %d", got)
	}
	if got := m.GrayAt(1, 0).Y; got != 200 {
		t.Fatalf("expected alpha 200 at (1,0), got %d", got)
	}
}

func TestMaxMaskAndMinMask(t *testing.T) {
	a := image.NewGray(image.Rect(0, 0, 2, 1))
	b := image.NewGray(image.Rect(0, 0, 2, 1))
	a.SetGray(0, 0, color.Gray{Y: 10})
	a.SetGray(1, 0, color.Gray{Y: 200})
	b.SetGray(0, 0, color.Gray{Y: 50})
	b.SetGray(1, 0, color.Gray{Y: 150})

	max := MaxMask(a, b)
	min := MinMask(a, b)
	if max == nil || min == nil {
		t.Fatal("expected non-nil results")
	}

	if got := max.GrayAt(0, 0).Y; got != 50 {
		t.Fatalf("expected max 50 at (0,0), got %d", got)
	}
	if got := max.GrayAt(1, 0).Y; got != 200 {
		t.Fatalf("expected max 200 at (1,0), got %d", got)
	}

	if got := min.GrayAt(0, 0).Y; got != 10 {
		t.Fatalf("expected min 10 at (0,0), got %d", got)
	}
	if got := min.GrayAt(1, 0).Y; got != 150 {
		t.Fatalf("expected min 150 at (1,0), got %d", got)
	}
}

func TestInvertMask(t *testing.T) {
	m := image.NewGray(image.Rect(0, 0, 2, 1))
	m.SetGray(0, 0, color.Gray{Y: 0})
	m.SetGray(1, 0, color.Gray{Y: 200})

	inv := InvertMask(m)
	if inv == nil {
		t.Fatal("expected non-nil inverted mask")
	}
	if got := inv.GrayAt(0, 0).Y; got != 255 {
		t.Fatalf("expected 255 at (0,0), got %d", got)
	}
	if got := inv.GrayAt(1, 0).Y; got != 55 {
		t.Fatalf("expected 55 at (1,0), got %d", got)
	}
}

func TestSubtractMaskPartialAlpha(t *testing.T) {
	// SubtractMask implements a AND NOT b, i.e. min(a, 255-b).
	cases := []struct {
		av, bv, want uint8
	}{
		{av: 255, bv: 0, want: 255},   // nothing subtracted
		{av: 255, bv: 255, want: 0},   // fully subtracted
		{av: 100, bv: 100, want: 100}, // partial b must not erode partial a
		{av: 200, bv: 128, want: 127}, // partial b caps a
	}

	a := image.NewGray(image.Rect(0, 0, len(cases), 1))
	b := image.NewGray(image.Rect(0, 0, len(cases), 1))
	for i, c := range cases {
		a.SetGray(i, 0, color.Gray{Y: c.av})
		b.SetGray(i, 0, color.Gray{Y: c.bv})
	}

	out := SubtractMask(a, b)
	if out == nil {
		t.Fatal("expected non-nil mask")
	}
	for i, c := range cases {
		if got := out.GrayAt(i, 0).Y; got != c.want {
			t.Errorf("SubtractMask(%d, %d) = %d, want %d", c.av, c.bv, got, c.want)
		}
	}
}
