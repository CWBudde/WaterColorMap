package blurkernel

import (
	"fmt"
	"math"
	"math/rand"
	"testing"
)

// TestBoxReciprocalExact proves the fixed-point multiplier reproduces integer
// division exactly for every window and every sum a box pass can produce. The
// reachable space is small enough to check outright, which is worth more than
// an argument about error bounds.
func TestBoxReciprocalExact(t *testing.T) {
	// Radius 512 is far beyond anything PlanFor can produce; boxesForGauss only
	// reaches radius 512 at around sigma 590 on an image that large.
	for radius := 1; radius <= 512; radius++ {
		n := 2*radius + 1
		recip := BoxReciprocal(n)
		half := uint32(n / 2)

		maxSum := uint32(255 * n)
		for sum := uint32(0); sum <= maxSum; sum++ {
			got := boxDiv(sum, half, recip)
			want := uint8((sum + half) / uint32(n))
			if got != want {
				t.Fatalf("n=%d sum=%d: boxDiv=%d, want %d", n, sum, got, want)
			}
		}
	}
}

// TestGaussianTapsSumToOne checks the normalisation invariant the kernels rely
// on: a full-white input must come back as exactly 255, with no clamping.
func TestGaussianTapsSumToOne(t *testing.T) {
	for sigma := 0.05; sigma <= 12; sigma += 0.01 {
		radius := max(int(math.Ceil(3*sigma)), 1)
		taps := GaussianTaps(sigma, radius)
		if taps == nil {
			// Identity kernel: sigma is too small to move anything.
			continue
		}

		sum := int(taps[0])
		for _, tap := range taps[1:] {
			sum += 2 * int(tap)
		}
		if sum != One {
			t.Fatalf("sigma=%.2f: taps sum to %d, want %d", sigma, sum, One)
		}
	}
}

// TestPlanNeverDegenerates guards the bug that motivated the direct-convolution
// path: the 3-pass box construction collapses to identity below sigma ~0.8, so
// an antialias pass at sigma 0.5 silently did nothing.
// The lower bound is the smallest sigma production can ask for: the 0.5
// antialias pass scaled by ZoomAdjustedBlurSigma's 0.7 factor at zoom >= 14.
func TestPlanNeverDegenerates(t *testing.T) {
	for sigma := 0.35; sigma <= 6; sigma += 0.05 {
		plan := PlanFor(sigma)
		if plan.Mode == ModeNone {
			t.Fatalf("sigma=%.2f: plan is identity", sigma)
		}
		if plan.Radius() < 1 {
			t.Fatalf("sigma=%.2f: plan radius %d, blur would be a no-op", sigma, plan.Radius())
		}
	}
}

// naiveConvRows is the obvious, slow implementation the fast row kernel must match.
func naiveConvRows(src []byte, w, h, stride int, taps []uint16) []byte {
	radius := len(taps) - 1
	dst := make([]byte, len(src))
	for y := range h {
		for x := range w {
			acc := uint32(0)
			for k := -radius; k <= radius; k++ {
				acc += uint32(src[y*stride+clampRow(x+k, w)]) * uint32(taps[abs(k)])
			}
			dst[y*stride+x] = uint8((acc + One/2) >> FracBits)
		}
	}
	return dst
}

// naiveConvCols is the obvious, slow implementation the fast column kernel must match.
func naiveConvCols(src []byte, w, h, stride int, taps []uint16) []byte {
	radius := len(taps) - 1
	dst := make([]byte, len(src))
	for y := range h {
		for x := range w {
			acc := uint32(0)
			for k := -radius; k <= radius; k++ {
				acc += uint32(src[clampRow(y+k, h)*stride+x]) * uint32(taps[abs(k)])
			}
			dst[y*stride+x] = uint8((acc + One/2) >> FracBits)
		}
	}
	return dst
}

// naiveBoxRows is the obvious, slow implementation the fast box row kernel must match.
func naiveBoxRows(src []byte, w, h, stride, radius int) []byte {
	n := uint32(2*radius + 1)
	dst := make([]byte, len(src))
	for y := range h {
		for x := range w {
			sum := uint32(0)
			for k := -radius; k <= radius; k++ {
				sum += uint32(src[y*stride+clampRow(x+k, w)])
			}
			dst[y*stride+x] = uint8((sum + n/2) / n)
		}
	}
	return dst
}

// naiveBoxCols is the obvious, slow implementation the fast box column kernel must match.
func naiveBoxCols(src []byte, w, h, stride, radius int) []byte {
	n := uint32(2*radius + 1)
	dst := make([]byte, len(src))
	for y := range h {
		for x := range w {
			sum := uint32(0)
			for k := -radius; k <= radius; k++ {
				sum += uint32(src[clampRow(y+k, h)*stride+x])
			}
			dst[y*stride+x] = uint8((sum + n/2) / n)
		}
	}
	return dst
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// kernelTestSizes deliberately includes widths that are not multiples of 16 and
// degenerate 1xN / Nx1 shapes, which are where edge and tail handling breaks.
var kernelTestSizes = [][2]int{
	{1, 1}, {1, 9}, {9, 1}, {3, 3}, {15, 4}, {16, 16}, {17, 5}, {31, 33}, {64, 8}, {67, 13},
}

func randomImage(t *testing.T, w, h, stride int) []byte {
	t.Helper()
	rng := rand.New(rand.NewSource(int64(w*7919 + h*104729 + stride)))
	buf := make([]byte, h*stride)
	for i := range buf {
		buf[i] = byte(rng.Intn(256))
	}
	return buf
}

func TestConvKernelsMatchNaive(t *testing.T) {
	for _, size := range kernelTestSizes {
		w, h := size[0], size[1]
		// A stride wider than the width catches kernels that assume they are equal.
		stride := w + 3
		src := randomImage(t, w, h, stride)

		for radius := 1; radius <= min(maxConvRadius, 8); radius++ {
			taps := GaussianTaps(float64(radius)/3, radius)

			var s Scratch
			s.Ensure(w, radius)

			gotRows := make([]byte, len(src))
			ConvRows(gotRows, src, w, h, stride, stride, taps, &s)
			assertRowsEqual(t, gotRows, naiveConvRows(src, w, h, stride, taps), w, h, stride,
				"ConvRows w=%d h=%d r=%d", w, h, radius)

			gotCols := make([]byte, len(src))
			ConvCols(gotCols, src, w, h, stride, stride, taps, &s)
			assertRowsEqual(t, gotCols, naiveConvCols(src, w, h, stride, taps), w, h, stride,
				"ConvCols w=%d h=%d r=%d", w, h, radius)
		}
	}
}

func TestBoxKernelsMatchNaive(t *testing.T) {
	for _, size := range kernelTestSizes {
		w, h := size[0], size[1]
		stride := w + 3
		src := randomImage(t, w, h, stride)

		for radius := 1; radius <= 12; radius++ {
			var s Scratch
			s.Ensure(w, radius)

			gotRows := make([]byte, len(src))
			BoxRows(gotRows, src, w, h, stride, stride, radius, &s)
			assertRowsEqual(t, gotRows, naiveBoxRows(src, w, h, stride, radius), w, h, stride,
				"BoxRows w=%d h=%d r=%d", w, h, radius)

			gotCols := make([]byte, len(src))
			BoxCols(gotCols, src, w, h, stride, stride, radius, &s)
			assertRowsEqual(t, gotCols, naiveBoxCols(src, w, h, stride, radius), w, h, stride,
				"BoxCols w=%d h=%d r=%d", w, h, radius)
		}
	}
}

// TestKernelsPreserveWhite checks the normalisation end to end: a uniform input
// must survive any kernel untouched, at every sigma.
func TestKernelsPreserveWhite(t *testing.T) {
	const w, h = 32, 24
	src := make([]byte, w*h)
	for i := range src {
		src[i] = 255
	}

	for sigma := 0.1; sigma <= 8; sigma += 0.1 {
		plan := PlanFor(sigma)
		var s Scratch
		s.Ensure(w, plan.Radius())

		// The column kernels write row y before reading it as part of row y+1's
		// window, so they must never run in place. Ping-pong instead.
		dst := make([]byte, len(src))
		tmp := make([]byte, len(src))
		switch plan.Mode {
		case ModeConv:
			ConvRows(tmp, src, w, h, w, w, plan.Taps, &s)
			ConvCols(dst, tmp, w, h, w, w, plan.Taps, &s)
		case ModeBox:
			copy(dst, src)
			for _, radius := range plan.Radii {
				if radius == 0 {
					continue
				}
				s.Ensure(w, radius)
				BoxRows(tmp, dst, w, h, w, w, radius, &s)
				BoxCols(dst, tmp, w, h, w, w, radius, &s)
			}
		case ModeNone:
			copy(dst, src)
		}

		for i, v := range dst {
			if v != 255 {
				t.Fatalf("sigma=%.1f: uniform white became %d at %d", sigma, v, i)
			}
		}
	}
}

func assertRowsEqual(t *testing.T, got, want []byte, w, h, stride int, format string, args ...any) {
	t.Helper()
	for y := range h {
		for x := range w {
			if got[y*stride+x] != want[y*stride+x] {
				t.Fatalf("%s: at (%d,%d) got %d, want %d",
					fmt.Sprintf(format, args...), x, y, got[y*stride+x], want[y*stride+x])
			}
		}
	}
}
