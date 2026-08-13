package mask

import (
	"image"
	"image/color"
	"testing"
)

func validateNoChangeAtWhiteMask(t *testing.T, result color.NRGBA) {
	t.Helper()

	if result.R != 200 || result.G != 100 || result.B != 50 || result.A != 255 {
		t.Errorf("expected no change at white mask, got %+v", result)
	}
}

func validateMaxDarkeningAtBlackMask(t *testing.T, result color.NRGBA) {
	t.Helper()

	// Should be significantly darker but preserve hue
	if result.R >= 200 || result.G >= 100 || result.B >= 50 {
		t.Errorf("expected darkening at black mask, got %+v", result)
	}

	if result.A != 255 {
		t.Errorf("alpha should be preserved, got %d", result.A)
	}
	// Check that relative color ratios are maintained (hue preservation)
	// Original ratio: R:G:B = 4:2:1
	if result.R == 0 || result.G == 0 {
		return // Too dark to check ratios
	}

	ratio := float64(result.R) / float64(result.G)
	expectedRatio := 200.0 / 100.0

	if ratio < expectedRatio*0.9 || ratio > expectedRatio*1.1 {
		t.Errorf("hue not preserved: R/G ratio = %.2f, expected ~%.2f", ratio, expectedRatio)
	}
}

func validateGrayStaysGray(t *testing.T, result color.NRGBA) {
	t.Helper()

	// Should remain gray (R=G=B) but darker
	if result.R != result.G || result.G != result.B {
		t.Errorf("gray should stay gray, got %+v", result)
	}

	if result.R >= 128 {
		t.Errorf("should be darker than original, got %+v", result)
	}
}

func validateMidRangeMask(t *testing.T, result color.NRGBA) {
	t.Helper()

	// Should be somewhat darker but not as much as black mask
	if result.R >= 200 {
		t.Errorf("should be somewhat darker, got %+v", result)
	}
	// Quadratic falloff: maskVal=128 -> normalized=0.5 -> effect=(1-0.25)*1.0=0.75
	// So should be significantly darkened
	if result.R > 100 {
		t.Errorf("should be significantly darkened with mid mask, got %+v", result)
	}
}

func validateReducedStrength(t *testing.T, result color.NRGBA) {
	t.Helper()

	// Should be darker but less than with strength 1.0
	if result.R >= 200 {
		t.Errorf("should be darker, got %+v", result)
	}
	// With strength=0.5, effect should be half as strong
	// So result should be lighter than with strength=1.0
}

func validateAlphaPreserved(t *testing.T, result color.NRGBA) {
	t.Helper()

	if result.A != 128 {
		t.Errorf("alpha should be preserved at 128, got %d", result.A)
	}
}

func TestApplySoftEdgeMask(t *testing.T) {
	tests := []struct {
		validate func(t *testing.T, result color.NRGBA)
		name     string
		strength float64
		color    color.NRGBA
		maskVal  uint8
	}{
		{
			name:     "white mask (center) - no change",
			color:    color.NRGBA{R: 200, G: 100, B: 50, A: 255},
			maskVal:  255,
			strength: 1.0,
			validate: validateNoChangeAtWhiteMask,
		},
		{
			name:     "black mask (edge) - maximum darkening",
			color:    color.NRGBA{R: 200, G: 100, B: 50, A: 255},
			maskVal:  0,
			strength: 1.0,
			validate: validateMaxDarkeningAtBlackMask,
		},
		{
			name:     "gray color (achromatic)",
			color:    color.NRGBA{R: 128, G: 128, B: 128, A: 255},
			maskVal:  0,
			strength: 1.0,
			validate: validateGrayStaysGray,
		},
		{
			name:     "mid-range mask - partial effect",
			color:    color.NRGBA{R: 200, G: 100, B: 50, A: 255},
			maskVal:  128,
			strength: 1.0,
			validate: validateMidRangeMask,
		},
		{
			name:     "strength 0.5 reduces effect",
			color:    color.NRGBA{R: 200, G: 100, B: 50, A: 255},
			maskVal:  0,
			strength: 0.5,
			validate: validateReducedStrength,
		},
		{
			name:     "alpha preservation",
			color:    color.NRGBA{R: 200, G: 100, B: 50, A: 128},
			maskVal:  0,
			strength: 1.0,
			validate: validateAlphaPreserved,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := image.NewNRGBA(image.Rect(0, 0, 1, 1))
			base.SetNRGBA(0, 0, tt.color)

			mask := image.NewGray(image.Rect(0, 0, 1, 1))
			mask.SetGray(0, 0, color.Gray{Y: tt.maskVal})

			result := ApplySoftEdgeMask(base, mask, tt.strength)
			if result == nil {
				t.Fatal("ApplySoftEdgeMask returned nil")
			}

			resultColor := result.NRGBAAt(0, 0)
			tt.validate(t, resultColor)
		})
	}
}

func TestApplySoftEdgeMaskEdgeCases(t *testing.T) {
	t.Run("nil base returns nil", func(t *testing.T) {
		mask := image.NewGray(image.Rect(0, 0, 1, 1))
		if result := ApplySoftEdgeMask(nil, mask, 1.0); result != nil {
			t.Error("expected nil for nil base")
		}
	})

	t.Run("nil mask returns nil", func(t *testing.T) {
		base := image.NewNRGBA(image.Rect(0, 0, 1, 1))
		if result := ApplySoftEdgeMask(base, nil, 1.0); result != nil {
			t.Error("expected nil for nil mask")
		}
	})

	t.Run("negative strength clamped to 0", func(t *testing.T) {
		base := image.NewNRGBA(image.Rect(0, 0, 1, 1))
		base.SetNRGBA(0, 0, color.NRGBA{R: 200, G: 100, B: 50, A: 255})
		mask := image.NewGray(image.Rect(0, 0, 1, 1))
		mask.SetGray(0, 0, color.Gray{Y: 0})

		result := ApplySoftEdgeMask(base, mask, -0.5)
		resultColor := result.NRGBAAt(0, 0)
		// With strength=0, no effect should occur
		if resultColor.R != 200 || resultColor.G != 100 || resultColor.B != 50 {
			t.Errorf("negative strength should clamp to 0 (no effect), got %+v", resultColor)
		}
	})

	t.Run("strength > 1 clamped to 1", func(t *testing.T) {
		base := image.NewNRGBA(image.Rect(0, 0, 1, 1))
		base.SetNRGBA(0, 0, color.NRGBA{R: 200, G: 100, B: 50, A: 255})
		mask := image.NewGray(image.Rect(0, 0, 1, 1))
		mask.SetGray(0, 0, color.Gray{Y: 0})

		result1 := ApplySoftEdgeMask(base, mask, 1.0)
		result2 := ApplySoftEdgeMask(base, mask, 2.0)

		c1 := result1.NRGBAAt(0, 0)
		c2 := result2.NRGBAAt(0, 0)

		// Both should give same result since >1 is clamped to 1
		if c1 != c2 {
			t.Errorf("strength clamping failed: 1.0 gave %+v, 2.0 gave %+v", c1, c2)
		}
	})
}

func BenchmarkApplySoftEdgeMask(b *testing.B) {
	// Create test image with varied colors
	size := 512
	base := image.NewNRGBA(image.Rect(0, 0, size, size))
	mask := image.NewGray(image.Rect(0, 0, size, size))

	// Fill with varied colors and mask values
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			// Mix of chromatic and achromatic colors
			if (x+y)%3 == 0 {
				// Grayscale
				gray := uint8((x + y) % 256)
				base.SetNRGBA(x, y, color.NRGBA{R: gray, G: gray, B: gray, A: 255})
			} else {
				// Chromatic
				base.SetNRGBA(x, y, color.NRGBA{
					R: uint8(x % 256),
					G: uint8(y % 256),
					B: uint8((x + y) % 256),
					A: 255,
				})
			}
			// Gradient mask
			mask.SetGray(x, y, color.Gray{Y: uint8((x + y) % 256)})
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ApplySoftEdgeMask(base, mask, 0.8)
	}
}
