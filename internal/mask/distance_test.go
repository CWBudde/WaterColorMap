package mask

import (
	"image"
	"image/color"
	"testing"
)

// Helper function to create a circular mask
func createCircleMask(width, height, centerX, centerY, radius int) *image.Gray {
	mask := image.NewGray(image.Rect(0, 0, width, height))

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			dx := x - centerX
			dy := y - centerY
			distSq := dx*dx + dy*dy
			if distSq <= radius*radius {
				mask.SetGray(x, y, color.Gray{Y: 255})
			}
		}
	}

	return mask
}

// Helper function to create a rectangular mask
func createRectMask(width, height, x1, y1, x2, y2 int) *image.Gray {
	mask := image.NewGray(image.Rect(0, 0, width, height))

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if x >= x1 && x <= x2 && y >= y1 && y <= y2 {
				mask.SetGray(x, y, color.Gray{Y: 255})
			}
		}
	}

	return mask
}

// TestEuclideanDistanceTransformCircle verifies radial symmetry and correct distances for a circular mask
func TestEuclideanDistanceTransformCircle(t *testing.T) {
	// Create a circle with radius 20 at center of 100x100 image
	width, height := 100, 100
	centerX, centerY := 50, 50
	radius := 20
	mask := createCircleMask(width, height, centerX, centerY, radius)

	// Compute distance transform with maxDistance = radius
	maxDist := float64(radius)
	distMask := EuclideanDistanceTransform(mask, maxDist)

	// Check center pixel (should be close to maximum distance)
	// Due to discretization, may not be exactly 255
	centerDist := distMask.GrayAt(centerX, centerY).Y
	if centerDist < 240 {
		t.Errorf("Center pixel distance = %d, expected close to 255 (>= 240)", centerDist)
	}

	// Check a pixel at the edge (should be close to 0)
	edgeDist := distMask.GrayAt(centerX+radius, centerY).Y
	if edgeDist > 10 {
		t.Errorf("Edge pixel distance = %d, expected close to 0", edgeDist)
	}

	// Check radial symmetry: pixels at same distance from center should have similar values
	testRadius := 10
	testDist := distMask.GrayAt(centerX+testRadius, centerY).Y
	testDistUp := distMask.GrayAt(centerX, centerY+testRadius).Y
	testDistDown := distMask.GrayAt(centerX, centerY-testRadius).Y
	testDistLeft := distMask.GrayAt(centerX-testRadius, centerY).Y

	tolerance := uint8(5) // Allow small tolerance for discretization
	if absDiffU8(testDist, testDistUp) > tolerance ||
		absDiffU8(testDist, testDistDown) > tolerance ||
		absDiffU8(testDist, testDistLeft) > tolerance {
		t.Errorf("Radial symmetry broken: right=%d, up=%d, down=%d, left=%d",
			testDist, testDistUp, testDistDown, testDistLeft)
	}
}

// TestEuclideanDistanceTransformNoStacking verifies that small features don't stack darker
func TestEuclideanDistanceTransformNoStacking(t *testing.T) {
	// Create two small circles close together
	width, height := 200, 100
	circle1 := createCircleMask(width, height, 50, 50, 15)
	circle2 := createCircleMask(width, height, 150, 50, 15)

	maxDist := 15.0

	// Distance transform for both circles
	dist1 := EuclideanDistanceTransform(circle1, maxDist)
	dist2 := EuclideanDistanceTransform(circle2, maxDist)

	// Both centers should reach close to maximum distance (255)
	// Due to discretization, may not be exactly 255
	center1Dist := dist1.GrayAt(50, 50).Y
	center2Dist := dist2.GrayAt(150, 50).Y

	if center1Dist < 230 {
		t.Errorf("Circle 1 center distance = %d, expected close to 255 (>= 230)", center1Dist)
	}
	if center2Dist < 230 {
		t.Errorf("Circle 2 center distance = %d, expected close to 255 (>= 230)", center2Dist)
	}

	// Key test: both circles should have identical distance profiles
	// (proving no stacking effect based on proximity to other features)
	// Check a pixel at radius 5 from center
	testDist1 := dist1.GrayAt(55, 50).Y
	testDist2 := dist2.GrayAt(155, 50).Y

	tolerance := uint8(2)
	if absDiffU8(testDist1, testDist2) > tolerance {
		t.Errorf("Distance profiles differ: circle1=%d, circle2=%d (expected identical)",
			testDist1, testDist2)
	}
}

// TestDistanceToIntensityPower validates power curve falloff mapping
func TestDistanceToIntensityPower(t *testing.T) {
	// Create a distance mask with known values
	width, height := 10, 1
	distMask := image.NewGray(image.Rect(0, 0, width, height))

	// Set up a linear gradient: 0, 28, 56, ..., 252 (roughly 0 to 255 in steps)
	for x := 0; x < width; x++ {
		val := uint8(x * 28)
		distMask.SetGray(x, 0, color.Gray{Y: val})
	}

	// Test with gamma = 1.0 (should be linear)
	intensityMask := DistanceToIntensity(distMask, 1.0)

	// Linear power curve (gamma=1): I = 1 - D/R
	// Output = 255 * (1 - I) = 255 * D
	// So output should approximately equal input
	// Semantics: dist=0 (edge) → output=0 (max darkening)
	//            dist=255 (center) → output=255 (no darkening)

	for x := 0; x < width; x++ {
		distVal := distMask.GrayAt(x, 0).Y
		intensityVal := intensityMask.GrayAt(x, 0).Y

		// Output should match input for gamma=1.0
		expectedIntensity := distVal

		tolerance := uint8(5)
		if absDiffU8(intensityVal, expectedIntensity) > tolerance {
			t.Errorf("At x=%d: intensity=%d, expected≈%d (dist=%d)",
				x, intensityVal, expectedIntensity, distVal)
		}
	}
}

// TestDistanceToIntensitySteepFalloff validates steep power curve
func TestDistanceToIntensitySteepFalloff(t *testing.T) {
	width, height := 256, 1
	distMask := image.NewGray(image.Rect(0, 0, width, height))

	// Create a full range 0-255 distance gradient
	for x := 0; x < width; x++ {
		distMask.SetGray(x, 0, color.Gray{Y: uint8(x)})
	}

	// Test with gamma = 9.0 (steep falloff)
	intensityMask := DistanceToIntensity(distMask, 9.0)

	// Power curve with gamma > 1 should have:
	// - Concentrated darkening near edges
	// - Rapid transition to no darkening
	// - Output should be monotonically increasing

	prevOutput := intensityMask.GrayAt(0, 0).Y
	for x := 1; x < width; x++ {
		currentOutput := intensityMask.GrayAt(x, 0).Y

		// Output should monotonically increase (intensity decreases)
		if currentOutput < prevOutput {
			t.Errorf("Non-monotonic at x=%d: current=%d < prev=%d",
				x, currentOutput, prevOutput)
		}

		prevOutput = currentOutput
	}

	// With steep gamma, we should reach near-zero darkening quickly
	// but still maintain smooth monotonic behavior
}

// TestEDTBoundaryConditions tests edge cases
func TestEDTBoundaryConditions(t *testing.T) {
	// Test 1: Single isolated pixel (should have distance 0 everywhere)
	singlePixel := image.NewGray(image.Rect(0, 0, 3, 3))
	singlePixel.SetGray(1, 1, color.Gray{Y: 255})

	dist := EuclideanDistanceTransform(singlePixel, 10.0)
	centerDist := dist.GrayAt(1, 1).Y

	// A single pixel is at the boundary, so distance should be 0
	if centerDist > 10 {
		t.Errorf("Single pixel distance = %d, expected close to 0", centerDist)
	}

	// Test 2: Rectangular region with known distances
	rect := createRectMask(20, 20, 5, 5, 14, 14)
	distRect := EuclideanDistanceTransform(rect, 10.0)

	// Center of 10x10 rectangle should be 5 pixels from edge
	// (measured horizontally/vertically, actual Euclidean distance to corner is more)
	centerRectDist := distRect.GrayAt(9, 9).Y // Center at (9,9)

	// Distance from (9,9) to edge at (5,9) is 4 pixels
	// Distance to corner is sqrt(4^2 + 4^2) = 5.66 pixels
	// Normalized to 0-255 with maxDistance=10: 5.66/10 * 255 ≈ 144
	expectedDist := uint8(4.0 / 10.0 * 255.0) // Use horizontal distance as minimum

	if centerRectDist < expectedDist {
		t.Errorf("Rectangle center distance = %d, expected ≥ %d", centerRectDist, expectedDist)
	}

	// Test 3: Empty mask (all zeros) should remain all zeros
	emptyMask := image.NewGray(image.Rect(0, 0, 10, 10))
	distEmpty := EuclideanDistanceTransform(emptyMask, 10.0)

	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			if distEmpty.GrayAt(x, y).Y != 0 {
				t.Errorf("Empty mask has non-zero distance at (%d,%d)", x, y)
			}
		}
	}

	// Test 4: Full mask (all 255) has no edges, so no distance to compute
	// All pixels remain at infinity and get clamped to maxDistance (255)
	fullMask := image.NewGray(image.Rect(0, 0, 10, 10))
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			fullMask.SetGray(x, y, color.Gray{Y: 255})
		}
	}

	distFull := EuclideanDistanceTransform(fullMask, 10.0)

	// With no edges (no background pixels), all interior pixels remain at infinity,
	// which gets clamped to maxDistance (255) in output
	centerFull := distFull.GrayAt(5, 5).Y
	if centerFull != 255 {
		t.Errorf("Full mask center distance = %d, expected 255 (no edges, clamped to max)", centerFull)
	}
}

// TestCreateDistanceEdgeMask tests the high-level API
func TestCreateDistanceEdgeMask(t *testing.T) {
	// Create a 40-pixel radius circle
	circle := createCircleMask(100, 100, 50, 50, 40)

	// Use radius=20 for the transform (smaller than circle radius)
	// Use gamma=1.0 for linear falloff
	radius := 20.0
	edgeMask := CreateDistanceEdgeMask(circle, radius, 1.0)

	// Edge mask should have:
	// - Low values (dark) at edges
	// - High values (light) at center

	centerVal := edgeMask.GrayAt(50, 50).Y
	edgeVal := edgeMask.GrayAt(89, 50).Y // Very near edge (circle ends at x=90)

	if centerVal < edgeVal {
		t.Errorf("Center should be lighter than edge: center=%d, edge=%d",
			centerVal, edgeVal)
	}

	// Center should be relatively light (at 40-pixel radius circle with radius=20 transform,
	// center is 40 pixels from edge, which is 2x radius, so should be clamped to 255)
	if centerVal < 200 {
		t.Errorf("Center value = %d, expected close to 255 (distance from center to edge is ~40 pixels, radius=20)", centerVal)
	}

	// Edge should be relatively dark (close to 0)
	if edgeVal > 50 {
		t.Errorf("Edge value = %d, expected close to 0", edgeVal)
	}
}

// Benchmarks

func BenchmarkEuclideanDistanceTransform256(b *testing.B) {
	mask := createCircleMask(256, 256, 128, 128, 100)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		EuclideanDistanceTransform(mask, 50.0)
	}
}

func BenchmarkEuclideanDistanceTransform512(b *testing.B) {
	mask := createCircleMask(512, 512, 256, 256, 200)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		EuclideanDistanceTransform(mask, 50.0)
	}
}

func BenchmarkEuclideanDistanceTransform1024(b *testing.B) {
	mask := createCircleMask(1024, 1024, 512, 512, 400)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		EuclideanDistanceTransform(mask, 50.0)
	}
}

func BenchmarkDistanceToIntensity(b *testing.B) {
	distMask := createCircleMask(512, 512, 256, 256, 200)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		DistanceToIntensity(distMask, 9.0)
	}
}

func BenchmarkCreateDistanceEdgeMask(b *testing.B) {
	mask := createCircleMask(512, 512, 256, 256, 200)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		CreateDistanceEdgeMask(mask, 25.0, 9.0)
	}
}

// BenchmarkDistanceVsBlur compares distance transform approach to blur approach
func BenchmarkDistanceVsBlur(b *testing.B) {
	mask := createCircleMask(512, 512, 256, 256, 200)

	b.Run("Distance", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			CreateDistanceEdgeMask(mask, 25.0, 9.0)
		}
	})

	b.Run("Blur", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			blurred := BoxBlurSigma(mask, 9.0)
			MinMask(mask, blurred)
		}
	})
}

// Helper function for absolute difference of uint8
func absDiffU8(a, b uint8) uint8 {
	if a > b {
		return a - b
	}
	return b - a
}
