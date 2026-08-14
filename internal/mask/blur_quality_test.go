package mask

import (
	"image"
	"image/color"
	"math"
	"testing"
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
// dark", which passed just as happily when the blur was three times stronger
// than its nominal sigma.
func TestBlurAccuracyVsGaussian(t *testing.T) {
	// The direct-convolution path is a real Gaussian, quantised to 16-bit
	// weights, so it should track gift closely. The box path is an
	// approximation by construction and gets a wider budget.
	tests := []struct {
		sigma  float32
		maxRMS float64
	}{
		{0.35, 2.0},
		{0.5, 2.0},
		{0.7, 2.0},
		{0.9, 2.0},
		{1.1, 2.0},
		{1.2, 2.0},
		{1.68, 2.0},
		{2.5, 2.0},
		{3.5, 12.0},
		{4.9, 12.0},
	}

	for _, tt := range tests {
		got := BoxBlurSigma(qualityPattern(), tt.sigma)
		rms := rmseVsGaussian(t, got, tt.sigma)
		if rms > tt.maxRMS {
			t.Errorf("sigma=%.2f: RMSE vs true Gaussian is %.2f levels, budget %.2f",
				tt.sigma, rms, tt.maxRMS)
		}
		t.Logf("sigma=%.2f: RMSE %.2f levels", tt.sigma, rms)
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
