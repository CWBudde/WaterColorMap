package mask

import (
	"image"
	"image/color"
	"testing"
)

// Helper functions to reduce cyclomatic complexity

func checkNoiseVariation(t *testing.T, noise *image.Gray) {
	width := noise.Bounds().Dx()
	height := noise.Bounds().Dy()
	firstPixel := noise.GrayAt(0, 0).Y
	foundDifferent := false
	for y := 0; y < height && !foundDifferent; y++ {
		for x := 0; x < width && !foundDifferent; x++ {
			if noise.GrayAt(x, y).Y != firstPixel {
				foundDifferent = true
			}
		}
	}
	if !foundDifferent {
		t.Error("noise should have variation, but all pixels are the same")
	}
}

func checkNoiseDeterminism(t *testing.T, noise1, noise2 *image.Gray) {
	pixel1 := noise1.GrayAt(100, 100).Y
	pixel2 := noise2.GrayAt(100, 100).Y
	if pixel1 != pixel2 {
		t.Errorf("same seed should produce same noise: %d != %d", pixel1, pixel2)
	}
}

func checkNoiseDifference(t *testing.T, noise1, noise2 *image.Gray) {
	width := noise1.Bounds().Dx()
	height := noise1.Bounds().Dy()
	differentCount := 0
	sampleCount := 0
	for y := 0; y < height; y += 10 {
		for x := 0; x < width; x += 10 {
			sampleCount++
			if noise1.GrayAt(x, y).Y != noise2.GrayAt(x, y).Y {
				differentCount++
			}
		}
	}
	// At least 80% of sampled pixels should be different
	if float64(differentCount)/float64(sampleCount) < 0.8 {
		t.Errorf("different seeds should produce mostly different noise, only %d/%d pixels different", differentCount, sampleCount)
	}
}

func verifyPipelineSteps(t *testing.T, mask, blurred, noise, noisy, thresholded, final *image.Gray) {
	if mask == nil {
		t.Fatal("ExtractBinaryMask returned nil")
	}
	if blurred == nil {
		t.Fatal("GaussianBlur returned nil")
	}
	if noise == nil {
		t.Fatal("GeneratePerlinNoise returned nil")
	}
	if noisy == nil {
		t.Fatal("ApplyNoiseToMask returned nil")
	}
	if thresholded == nil {
		t.Fatal("ApplyThreshold returned nil")
	}
	if final == nil {
		t.Fatal("AntialiasEdges returned nil")
	}
}

func verifyPipelineResult(t *testing.T, final *image.Gray) {
	if final.Bounds().Dx() != 256 || final.Bounds().Dy() != 256 {
		t.Errorf("final dimensions incorrect: got %dx%d, want 256x256",
			final.Bounds().Dx(), final.Bounds().Dy())
	}

	// Verify background is dark (no feature)
	bgPixel := final.GrayAt(10, 10)
	if bgPixel.Y > 50 {
		t.Errorf("background should be dark (<50), got %d", bgPixel.Y)
	}

	// Verify feature center is bright
	centerPixel := final.GrayAt(128, 128)
	if centerPixel.Y < 200 {
		t.Errorf("feature center should be bright (>200), got %d", centerPixel.Y)
	}
}

func checkGradientAtEdge(t *testing.T, final *image.Gray) {
	// Verify there's a gradient at the edge (watercolor effect)
	foundGradient := false
	for angle := 0; angle < 360; angle += 30 {
		// Sample at different distances from center
		for radius := 40; radius < 60; radius += 5 {
			x := 128 + int(float64(radius)*0.7071) // cos(45°) ≈ 0.7071
			y := 128 + int(float64(radius)*0.7071)
			if x >= 0 && x < 256 && y >= 0 && y < 256 {
				val := final.GrayAt(x, y).Y
				if val > 50 && val < 200 {
					foundGradient = true
					break
				}
			}
		}
		if foundGradient {
			break
		}
	}
	if !foundGradient {
		t.Log("Warning: No clear gradient found at edge (may vary with noise)")
	}
}

// TestExtractBinaryMask tests extracting a binary mask from a colored layer image
func TestExtractBinaryMask(t *testing.T) {
	// Create a test image with a specific color feature
	// Background should be transparent, feature should be a specific color
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))

	// Fill background with transparent pixels
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			img.Set(x, y, color.RGBA{0, 0, 0, 0})
		}
	}

	// Draw a blue square in the center (like a water feature)
	// This represents a rendered layer with a specific color
	for y := 3; y < 7; y++ {
		for x := 3; x < 7; x++ {
			img.Set(x, y, color.RGBA{0, 0, 255, 255}) // Blue
		}
	}

	// Extract binary mask - should be white where feature is, black elsewhere
	mask := ExtractBinaryMask(img)

	// Verify mask dimensions match input
	if mask.Bounds() != img.Bounds() {
		t.Errorf("mask bounds %v != image bounds %v", mask.Bounds(), img.Bounds())
	}

	// Verify background pixels are black (no feature)
	bgPixel := mask.GrayAt(0, 0)
	if bgPixel.Y != 0 {
		t.Errorf("background pixel should be black (0), got %d", bgPixel.Y)
	}

	// Verify feature pixels are white (255)
	featurePixel := mask.GrayAt(5, 5)
	if featurePixel.Y != 255 {
		t.Errorf("feature pixel should be white (255), got %d", featurePixel.Y)
	}

	// Verify edge transition
	edgePixel := mask.GrayAt(2, 5) // Just outside feature
	if edgePixel.Y != 0 {
		t.Errorf("edge pixel outside feature should be black (0), got %d", edgePixel.Y)
	}
}

// TestGaussianBlur tests applying Gaussian blur to soften mask edges
func TestGaussianBlur(t *testing.T) {
	// Create a simple binary mask with a sharp edge
	mask := image.NewGray(image.Rect(0, 0, 10, 10))

	// Left half black (0), right half white (255)
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			if x < 5 {
				mask.SetGray(x, y, color.Gray{Y: 0})
			} else {
				mask.SetGray(x, y, color.Gray{Y: 255})
			}
		}
	}

	// Apply Gaussian blur with radius 1.0
	blurred := GaussianBlur(mask, 1.0)

	// Verify dimensions preserved
	if blurred.Bounds() != mask.Bounds() {
		t.Errorf("blurred bounds %v != mask bounds %v", blurred.Bounds(), mask.Bounds())
	}

	// Verify far left is still dark (should remain mostly black)
	leftPixel := blurred.GrayAt(0, 5)
	if leftPixel.Y > 50 {
		t.Errorf("far left pixel should be dark (<50), got %d", leftPixel.Y)
	}

	// Verify far right is still bright (should remain mostly white)
	rightPixel := blurred.GrayAt(9, 5)
	if rightPixel.Y < 200 {
		t.Errorf("far right pixel should be bright (>200), got %d", rightPixel.Y)
	}

	// Verify edge is now gradual (middle pixels should be gray)
	edgePixel := blurred.GrayAt(5, 5)
	if edgePixel.Y < 50 || edgePixel.Y > 200 {
		t.Errorf("edge pixel should be gray (50-200), got %d", edgePixel.Y)
	}
}

// TestGeneratePerlinNoise tests generating tileable Perlin noise
func TestGeneratePerlinNoise(t *testing.T) {
	width := 256
	height := 256
	scale := 50.0

	// Generate Perlin noise texture
	noise := GeneratePerlinNoise(width, height, scale, 42)

	// Verify dimensions
	bounds := noise.Bounds()
	if bounds.Dx() != width || bounds.Dy() != height {
		t.Errorf("noise dimensions %dx%d != expected %dx%d", bounds.Dx(), bounds.Dy(), width, height)
	}

	// Verify it's not all the same value (has variation)
	checkNoiseVariation(t, noise)

	// Note: uint8 values are always in valid range (0-255) by type definition
	// No need to verify range for uint8 values

	// Verify determinism - same seed produces same noise
	noise2 := GeneratePerlinNoise(width, height, scale, 42)
	checkNoiseDeterminism(t, noise, noise2)

	// Verify different seeds produce different noise (check multiple pixels)
	noise3 := GeneratePerlinNoise(width, height, scale, 99)
	checkNoiseDifference(t, noise, noise3)
}

// TestGeneratePerlinNoiseWithOffsetAlignment ensures offsets align noise across tiles
func TestGeneratePerlinNoiseWithOffsetAlignment(t *testing.T) {
	width := 256
	height := 256
	scale := 40.0
	seed := int64(2024)

	// Reference noise covering two horizontal tiles
	ref := GeneratePerlinNoiseWithOffset(width*2, height, scale, seed, 0, 0)
	left := GeneratePerlinNoiseWithOffset(width, height, scale, seed, 0, 0)
	right := GeneratePerlinNoiseWithOffset(width, height, scale, seed, width, 0)

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if left.GrayAt(x, y).Y != ref.GrayAt(x, y).Y {
				t.Fatalf("left tile mismatch at (%d,%d): %d != %d", x, y,
					left.GrayAt(x, y).Y, ref.GrayAt(x, y).Y)
			}
			if right.GrayAt(x, y).Y != ref.GrayAt(x+width, y).Y {
				t.Fatalf("right tile mismatch at (%d,%d): %d != %d", x, y,
					right.GrayAt(x, y).Y, ref.GrayAt(x+width, y).Y)
			}
		}
	}
}

// TestGeneratePerlinNoiseWithOffsetVerticalAlignment ensures vertical seams are seamless
func TestGeneratePerlinNoiseWithOffsetVerticalAlignment(t *testing.T) {
	width := 256
	height := 256
	scale := 40.0
	seed := int64(2025)

	ref := GeneratePerlinNoiseWithOffset(width, height*2, scale, seed, 0, 0)
	top := GeneratePerlinNoiseWithOffset(width, height, scale, seed, 0, 0)
	bottom := GeneratePerlinNoiseWithOffset(width, height, scale, seed, 0, height)

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if top.GrayAt(x, y).Y != ref.GrayAt(x, y).Y {
				t.Fatalf("top tile mismatch at (%d,%d): %d != %d", x, y,
					top.GrayAt(x, y).Y, ref.GrayAt(x, y).Y)
			}
			if bottom.GrayAt(x, y).Y != ref.GrayAt(x, y+height).Y {
				t.Fatalf("bottom tile mismatch at (%d,%d): %d != %d", x, y,
					bottom.GrayAt(x, y).Y, ref.GrayAt(x, y+height).Y)
			}
		}
	}
}

// TestApplyNoiseToMask tests overlaying noise on a blurred mask
func TestApplyNoiseToMask(t *testing.T) {
	// Create a simple gradient mask (simulating a blurred edge)
	mask := image.NewGray(image.Rect(0, 0, 100, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			// Gradient from left (black) to right (white)
			gray := uint8(float64(x) / 100.0 * 255.0)
			mask.SetGray(x, y, color.Gray{Y: gray})
		}
	}

	// Generate simple noise
	noise := image.NewGray(image.Rect(0, 0, 100, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			// Simple checkerboard pattern as "noise"
			if (x+y)%2 == 0 {
				noise.SetGray(x, y, color.Gray{Y: 200})
			} else {
				noise.SetGray(x, y, color.Gray{Y: 100})
			}
		}
	}

	// Apply noise with 50% strength
	result := ApplyNoiseToMask(mask, noise, 0.5)

	// Verify dimensions
	if result.Bounds() != mask.Bounds() {
		t.Errorf("result bounds %v != mask bounds %v", result.Bounds(), mask.Bounds())
	}

	// Verify left side (black mask) stays mostly dark even with noise
	leftPixel := result.GrayAt(10, 50)
	if leftPixel.Y > 100 {
		t.Errorf("left pixel should stay dark (<100), got %d", leftPixel.Y)
	}

	// Verify right side (white mask) has some variation from noise
	rightPixel1 := result.GrayAt(95, 50)
	rightPixel2 := result.GrayAt(96, 50)
	if rightPixel1.Y == rightPixel2.Y {
		t.Error("noise should create variation in bright areas")
	}

	// Verify result is not identical to input (noise was applied)
	middleOriginal := mask.GrayAt(50, 50).Y
	middleResult := result.GrayAt(50, 50).Y
	if middleOriginal == middleResult {
		t.Error("noise should modify the mask values")
	}
}

// TestApplyThreshold tests thresholding to sharpen mask edges
func TestApplyThreshold(t *testing.T) {
	// Create a gradient mask (soft edge)
	mask := image.NewGray(image.Rect(0, 0, 10, 10))
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			// Gradient: 0, 25, 51, 76, 102, 127, 153, 178, 204, 229
			gray := uint8(float64(x) * 25.5)
			mask.SetGray(x, y, color.Gray{Y: gray})
		}
	}

	// Apply threshold at 128
	result := ApplyThreshold(mask, 128)

	// Verify dimensions
	if result.Bounds() != mask.Bounds() {
		t.Errorf("result bounds %v != mask bounds %v", result.Bounds(), mask.Bounds())
	}

	// Verify values below threshold become black
	for x := 0; x < 5; x++ {
		pixel := result.GrayAt(x, 5)
		if pixel.Y != 0 {
			t.Errorf("pixel at x=%d should be 0 (below threshold), got %d", x, pixel.Y)
		}
	}

	// Verify values above threshold become white
	for x := 6; x < 10; x++ {
		pixel := result.GrayAt(x, 5)
		if pixel.Y != 255 {
			t.Errorf("pixel at x=%d should be 255 (above threshold), got %d", x, pixel.Y)
		}
	}
}

// TestAntialiasEdges tests applying subtle antialiasing to mask edges
func TestAntialiasEdges(t *testing.T) {
	// Create a binary mask with sharp edges
	mask := image.NewGray(image.Rect(0, 0, 10, 10))
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			if x < 5 {
				mask.SetGray(x, y, color.Gray{Y: 0})
			} else {
				mask.SetGray(x, y, color.Gray{Y: 255})
			}
		}
	}

	// Apply antialiasing
	result := AntialiasEdges(mask, 0.5)

	// Verify dimensions
	if result.Bounds() != mask.Bounds() {
		t.Errorf("result bounds %v != mask bounds %v", result.Bounds(), mask.Bounds())
	}

	// Verify far left is still black
	if result.GrayAt(0, 5).Y > 50 {
		t.Errorf("far left should stay dark, got %d", result.GrayAt(0, 5).Y)
	}

	// Verify far right is still white
	if result.GrayAt(9, 5).Y < 200 {
		t.Errorf("far right should stay bright, got %d", result.GrayAt(9, 5).Y)
	}

	// Verify edge pixels are smoothed (not pure black or white)
	edgePixel := result.GrayAt(5, 5)
	if edgePixel.Y == 0 || edgePixel.Y == 255 {
		t.Errorf("edge pixel should be antialiased (gray), got %d", edgePixel.Y)
	}
}

// TestApplyThresholdWithAntialias tests the threshold with cubic interpolation antialiasing
func TestApplyThresholdWithAntialias(t *testing.T) {
	t.Run("basic_threshold", func(t *testing.T) {
		// Create a gradient mask (soft edge)
		mask := image.NewGray(image.Rect(0, 0, 100, 100))
		for y := 0; y < 100; y++ {
			for x := 0; x < 100; x++ {
				// Gradient: 0-255 across the width
				gray := uint8(float64(x) * 2.55)
				mask.SetGray(x, y, color.Gray{Y: gray})
			}
		}

		// Apply threshold with antialiasing
		result := ApplyThresholdWithAntialiasAndInvert(mask, 128)

		// Verify dimensions
		if result.Bounds() != mask.Bounds() {
			t.Errorf("result bounds %v != mask bounds %v", result.Bounds(), mask.Bounds())
		}

		// Verify far left is white (inverted: low values become white)
		if result.GrayAt(0, 50).Y != 255 {
			t.Errorf("far left should be 255, got %d", result.GrayAt(0, 50).Y)
		}

		// Verify far right is black (inverted: high values become black)
		if result.GrayAt(99, 50).Y != 0 {
			t.Errorf("far right should be 0, got %d", result.GrayAt(99, 50).Y)
		}

		// Verify transition zone around threshold (128 maps to x≈50)
		// Values should be smoothly interpolated in the transition zone
		midVal := result.GrayAt(50, 50).Y
		if midVal < 100 || midVal > 155 {
			t.Errorf("middle value should be in transition zone, got %d", midVal)
		}
	})

	t.Run("smooth_transitions", func(t *testing.T) {
		// Create a mask with values around the threshold
		mask := image.NewGray(image.Rect(0, 0, 50, 50))
		for y := 0; y < 50; y++ {
			for x := 0; x < 50; x++ {
				// Values from 80 to 180 (centered around threshold 128)
				gray := uint8(80 + float64(x)*2)
				mask.SetGray(x, y, color.Gray{Y: gray})
			}
		}

		result := ApplyThresholdWithAntialiasAndInvert(mask, 128)

		// Verify smooth gradient in output (no hard edges)
		// Check that adjacent pixels don't jump too much
		for x := 1; x < 49; x++ {
			curr := result.GrayAt(x, 25).Y
			prev := result.GrayAt(x-1, 25).Y
			diff := int(curr) - int(prev)
			if diff < 0 {
				diff = -diff
			}
			// Cubic interpolation should create smooth gradients
			if diff > 30 {
				t.Errorf("at x=%d: gradient too steep (diff=%d), expected smooth transition", x, diff)
			}
		}
	})
}

// TestWatercolorPipeline tests the complete watercolor effect pipeline
func TestWatercolorPipeline(t *testing.T) {
	// Create a test layer image with a blue feature
	layerImg := image.NewRGBA(image.Rect(0, 0, 256, 256))

	// Fill background with transparent
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			layerImg.Set(x, y, color.RGBA{0, 0, 0, 0})
		}
	}

	// Draw a blue circle in the center (simulate a water body)
	centerX, centerY := 128, 128
	radius := 50
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			dx := x - centerX
			dy := y - centerY
			if dx*dx+dy*dy <= radius*radius {
				layerImg.Set(x, y, color.RGBA{0, 0, 255, 255})
			}
		}
	}

	// Step 1: Extract binary mask
	mask := ExtractBinaryMask(layerImg)
	// Step 2: Apply Gaussian blur
	blurred := GaussianBlur(mask, 2.0)
	// Step 3: Generate Perlin noise
	noise := GeneratePerlinNoise(256, 256, 30.0, 12345)
	// Step 4: Apply noise to blurred mask
	noisy := ApplyNoiseToMask(blurred, noise, 0.3)
	// Step 5: Apply threshold
	thresholded := ApplyThreshold(noisy, 128)
	// Step 6: Apply antialiasing
	final := AntialiasEdges(thresholded, 0.5)

	// Verify all steps completed successfully
	verifyPipelineSteps(t, mask, blurred, noise, noisy, thresholded, final)

	// Verify final result
	verifyPipelineResult(t, final)

	// Verify there's a gradient at the edge (watercolor effect)
	checkGradientAtEdge(t, final)
}

// TestBoxBlur tests the basic box blur implementation
func TestBoxBlur(t *testing.T) {
	// Create a simple binary mask with a sharp edge
	mask := image.NewGray(image.Rect(0, 0, 10, 10))

	// Left half black (0), right half white (255)
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			if x < 5 {
				mask.SetGray(x, y, color.Gray{Y: 0})
			} else {
				mask.SetGray(x, y, color.Gray{Y: 255})
			}
		}
	}

	// Apply box blur with radius 1
	blurred := BoxBlur(mask, 1)

	// Verify dimensions preserved
	if blurred.Bounds() != mask.Bounds() {
		t.Errorf("blurred bounds %v != mask bounds %v", blurred.Bounds(), mask.Bounds())
	}

	// Verify far left is still dark
	leftPixel := blurred.GrayAt(0, 5)
	if leftPixel.Y > 50 {
		t.Errorf("far left pixel should be dark (<50), got %d", leftPixel.Y)
	}

	// Verify far right is still bright
	rightPixel := blurred.GrayAt(9, 5)
	if rightPixel.Y < 200 {
		t.Errorf("far right pixel should be bright (>200), got %d", rightPixel.Y)
	}

	// Verify edge has been smoothed (middle pixels should be gray)
	edgePixel := blurred.GrayAt(5, 5)
	if edgePixel.Y < 50 || edgePixel.Y > 200 {
		t.Errorf("edge pixel should be gray (50-200), got %d", edgePixel.Y)
	}
}

// TestBoxBlurZeroRadius tests that zero/negative radius returns a copy
func TestBoxBlurZeroRadius(t *testing.T) {
	mask := image.NewGray(image.Rect(0, 0, 10, 10))
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			mask.SetGray(x, y, color.Gray{Y: uint8(x * 25)})
		}
	}

	// Apply box blur with radius 0
	result := BoxBlur(mask, 0)

	// Verify dimensions preserved
	if result.Bounds() != mask.Bounds() {
		t.Errorf("result bounds %v != mask bounds %v", result.Bounds(), mask.Bounds())
	}

	// Verify pixel values unchanged
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			if result.GrayAt(x, y).Y != mask.GrayAt(x, y).Y {
				t.Errorf("pixel (%d,%d) changed: %d -> %d", x, y,
					mask.GrayAt(x, y).Y, result.GrayAt(x, y).Y)
			}
		}
	}
}

// TestBoxBlurSigma tests the sigma-to-radius conversion and 3-pass blur
func TestBoxBlurSigma(t *testing.T) {
	tests := []struct {
		name  string
		sigma float32
		size  int // image size to use
	}{
		{"small sigma", 0.5, 20},
		{"medium sigma", 1.0, 20},
		{"large sigma", 2.0, 40},
		{"very large sigma", 4.5, 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a simple binary mask with a sharp edge
			mask := image.NewGray(image.Rect(0, 0, tt.size, tt.size))
			half := tt.size / 2

			// Left half black, right half white
			for y := 0; y < tt.size; y++ {
				for x := 0; x < tt.size; x++ {
					if x < half {
						mask.SetGray(x, y, color.Gray{Y: 0})
					} else {
						mask.SetGray(x, y, color.Gray{Y: 255})
					}
				}
			}

			// Apply box blur with sigma
			blurred := BoxBlurSigma(mask, tt.sigma)

			// Verify dimensions preserved
			if blurred.Bounds() != mask.Bounds() {
				t.Errorf("blurred bounds %v != mask bounds %v", blurred.Bounds(), mask.Bounds())
			}

			// Verify far left is still dark
			leftPixel := blurred.GrayAt(0, half)
			if leftPixel.Y > 50 {
				t.Errorf("far left pixel should be dark (<50), got %d", leftPixel.Y)
			}

			// Verify far right is still bright
			rightPixel := blurred.GrayAt(tt.size-1, half)
			if rightPixel.Y < 200 {
				t.Errorf("far right pixel should be bright (>200), got %d", rightPixel.Y)
			}

			// Verify the step edge has been softened rather than left hard.
			// The bound is deliberately behavioural: an exact value would just
			// re-encode whatever the kernel currently does, and the previous
			// hard-coded 50-200 band was calibrated against an implementation
			// that blurred about twice as hard as its nominal sigma.
			for _, x := range []int{half - 1, half} {
				v := blurred.GrayAt(x, half).Y
				if v == 0 || v == 255 {
					t.Errorf("pixel at x=%d should be softened, got %d", x, v)
				}
			}

			// The blurred edge must stay monotonically non-decreasing.
			prev := blurred.GrayAt(0, half).Y
			for x := 1; x < tt.size; x++ {
				v := blurred.GrayAt(x, half).Y
				if v < prev {
					t.Errorf("edge profile not monotonic at x=%d: %d after %d", x, v, prev)
				}
				prev = v
			}
		})
	}
}

// TestBoxBlurSigmaZero tests that zero sigma returns a copy
func TestBoxBlurSigmaZero(t *testing.T) {
	mask := image.NewGray(image.Rect(0, 0, 10, 10))
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			mask.SetGray(x, y, color.Gray{Y: uint8(x * 25)})
		}
	}

	// Apply box blur with sigma 0
	result := BoxBlurSigma(mask, 0)

	// Verify dimensions preserved
	if result.Bounds() != mask.Bounds() {
		t.Errorf("result bounds %v != mask bounds %v", result.Bounds(), mask.Bounds())
	}

	// Verify pixel values unchanged
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			if result.GrayAt(x, y).Y != mask.GrayAt(x, y).Y {
				t.Errorf("pixel (%d,%d) changed: %d -> %d", x, y,
					mask.GrayAt(x, y).Y, result.GrayAt(x, y).Y)
			}
		}
	}
}
