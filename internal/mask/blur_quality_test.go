package mask

import (
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/cwbudde/watercolormap/internal/mask/blurkernel"
)

// rmseVsGaussian measures how far a blur is from a true Gaussian of the same
// sigma, in 8-bit levels, over a test pattern with edges at several angles.
func rmseVsGaussian(t *testing.T, got *image.Gray, sigma float32) float64 {
	t.Helper()

	want := GaussianBlur(qualityPattern(), sigma)
	bounds := got.Bounds()

	// Skip a border of 3 sigma: gift and this package handle the image edge
	// differently, and the comparison is about the kernel, not the border rule.
	margin := int(math.Ceil(3*float64(sigma))) + 1

	var sum float64
	var n int
	for y := bounds.Min.Y + margin; y < bounds.Max.Y-margin; y++ {
		for x := bounds.Min.X + margin; x < bounds.Max.X-margin; x++ {
			d := float64(got.GrayAt(x, y).Y) - float64(want.GrayAt(x, y).Y)
			sum += d * d
			n++
		}
	}
	return math.Sqrt(sum / float64(n))
}

// qualityPattern is a mask with a straight edge, a diagonal edge and a disc, so
// the error measure sees more than one edge orientation.
func qualityPattern() *image.Gray {
	const size = 128
	m := image.NewGray(image.Rect(0, 0, size, size))
	for y := range size {
		for x := range size {
			var v uint8
			switch {
			case x < size/4:
				v = 255
			case x+y > 3*size/2:
				v = 255
			default:
				dx, dy := float64(x-size/2), float64(y-size/2)
				if math.Hypot(dx, dy) < 18 {
					v = 255
				}
			}
			m.SetGray(x, y, color.Gray{Y: v})
		}
	}
	return m
}

// TestBlurAccuracyVsGaussian holds the blur to a stated accuracy budget against
// a true Gaussian, at the sigmas the renderer actually uses.
//
// This replaces an earlier test that only asserted "centre bright, corners
// dark", which passed just as happily when the blur ran two to four times wider
// than its nominal sigma.
func TestBlurAccuracyVsGaussian(t *testing.T) {
	// Budgets are per kernel family, not per sigma. The direct path is a real
	// Gaussian quantised to 16-bit weights and tracks gift to within ~0.2
	// levels; the box path approximates by construction and drifts with sigma,
	// reaching ~1.6 at the widest sigma in use. Both budgets sit a little above
	// the measured worst case so quantisation noise cannot flake the test.
	const (
		convBudget = 0.5
		boxBudget  = 2.5
	)

	// Each case states which kernel it is meant to exercise. Asserting that
	// keeps the budgets honest: when maxConvRadius moved from 8 to 12, sigma
	// 3.5 crossed from the box path to the direct one and silently kept a
	// box-sized budget. Now that crossing fails the test instead.
	tests := []struct {
		sigma float32
		mode  blurkernel.Mode
	}{
		{0.35, blurkernel.ModeConv}, // antialias at zoom >= 14
		{0.5, blurkernel.ModeConv},
		{0.99, blurkernel.ModeConv}, // global blur at zoom >= 14
		{1.41, blurkernel.ModeConv}, // antialias, rivers
		{2.45, blurkernel.ModeConv}, // global blur, most layer masks
		{3.43, blurkernel.ModeConv}, // global blur at zoom <= 11
		{3.5, blurkernel.ModeConv},
		{4.0, blurkernel.ModeConv}, // widest sigma still convolved
		{4.9, blurkernel.ModeBox},  // narrowest sigma on the box path
		{7.48, blurkernel.ModeBox}, // land shade
	}

	for _, tt := range tests {
		mode := blurkernel.PlanFor(float64(tt.sigma)).Mode
		if mode != tt.mode {
			t.Errorf("sigma=%.2f: kernel is mode %v, test expects %v — "+
				"the accuracy budget below no longer matches the path being taken",
				tt.sigma, mode, tt.mode)
			continue
		}

		budget := convBudget
		if mode == blurkernel.ModeBox {
			budget = boxBudget
		}

		got := BoxBlurSigma(qualityPattern(), tt.sigma)
		rms := rmseVsGaussian(t, got, tt.sigma)
		if rms > budget {
			t.Errorf("sigma=%.2f (%v): RMSE vs true Gaussian is %.2f levels, budget %.2f",
				tt.sigma, mode, rms, budget)
		}
		t.Logf("sigma=%.2f (%v): RMSE %.2f levels", tt.sigma, mode, rms)
	}
}

// TestBlurStrengthMatchesSigma is the regression guard for the bug this rewrite
// fixed: the previous implementation derived a single box radius by truncating
// sqrt(4*sigma^2+1) and applied it three times, which blurred roughly twice as
// hard as the requested sigma at every setting.
//
// A Gaussian of sigma s turns a step edge into an error function, so the value
// one sigma past the edge is a fixed fraction of full scale regardless of s.
// Checking that fraction catches a blur whose real width has drifted from its
// nominal one.
func TestBlurStrengthMatchesSigma(t *testing.T) {
	const size = 129
	const edge = size / 2

	for _, sigma := range []float32{0.9, 1.2, 2.0, 3.5} {
		m := image.NewGray(image.Rect(0, 0, size, size))
		for y := range size {
			for x := edge; x < size; x++ {
				m.SetGray(x, y, color.Gray{Y: 255})
			}
		}

		blurred := BoxBlurSigma(m, sigma)

		// Pixel `edge` is the first bright one, so the continuous step sits half
		// a pixel earlier and the probe's true distance from it is +0.5.
		probe := edge + int(math.Round(float64(sigma)))
		distance := float64(probe-edge) + 0.5
		got := float64(blurred.GrayAt(probe, size/2).Y) / 255
		want := 0.5 * (1 + math.Erf(distance/(float64(sigma)*math.Sqrt2)))

		if math.Abs(got-want) > 0.06 {
			t.Errorf("sigma=%.1f: edge profile at +%d is %.3f of full scale, want %.3f "+
				"(blur is %s than its nominal sigma)",
				sigma, probe-edge, got, want, strongerOrWeaker(got, want))
		}
	}
}

func strongerOrWeaker(got, want float64) string {
	if got < want {
		return "stronger"
	}
	return "weaker"
}

// TestBlurDegenerateImages guards the entry points against images with a zero
// dimension. The kernels assume at least one pixel each way — the row pass
// replicates row[0] into its pad, the column pass clamps against h-1 — so a
// missing guard here is a panic, not a wrong pixel.
func TestBlurDegenerateImages(t *testing.T) {
	bounds := []image.Rectangle{
		image.Rect(0, 0, 0, 0),
		image.Rect(0, 0, 0, 5),
		image.Rect(0, 0, 5, 0),
	}

	for _, r := range bounds {
		t.Run(r.String(), func(t *testing.T) {
			for _, radius := range []int{0, 1, 4} {
				got := BoxBlur(image.NewGray(r), radius)
				if got.Bounds() != r {
					t.Errorf("BoxBlur(radius=%d) bounds %v, want %v", radius, got.Bounds(), r)
				}
			}

			// Cover both kernel families and the identity path.
			for _, sigma := range []float32{0, 0.5, 2.45, 7.48} {
				got := BoxBlurSigma(image.NewGray(r), sigma)
				if got.Bounds() != r {
					t.Errorf("BoxBlurSigma(sigma=%.2f) bounds %v, want %v", sigma, got.Bounds(), r)
				}
			}
		})
	}
}
