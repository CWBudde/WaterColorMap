package watercolor

import (
	"image"
	"image/color"
	"testing"

	"github.com/cwbudde/watercolormap/internal/geojson"
	"github.com/cwbudde/watercolormap/internal/mask"
)

// BenchmarkFullPipeline benchmarks the complete watercolor rendering pipeline
func BenchmarkFullPipeline(b *testing.B) {
	tileSize := 256
	seed := int64(42)

	// Create test layers with realistic complexity
	waterLayer := createComplexLayer(tileSize, color.NRGBA{R: 100, G: 150, B: 200, A: 255})
	landLayer := createComplexLayer(tileSize, color.NRGBA{R: 220, G: 200, B: 170, A: 255})
	parksLayer := createComplexLayer(tileSize, color.NRGBA{R: 120, G: 180, B: 120, A: 255})
	roadsLayer := createComplexLayer(tileSize, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
	highwaysLayer := createComplexLayer(tileSize, color.NRGBA{R: 255, G: 230, B: 120, A: 255})

	textures := map[geojson.LayerType]image.Image{
		geojson.LayerLand:     benchSolidTexture(8, 8, color.NRGBA{R: 240, G: 235, B: 220, A: 255}),
		geojson.LayerWater:    benchSolidTexture(8, 8, color.NRGBA{R: 120, G: 150, B: 200, A: 255}),
		geojson.LayerParks:    benchSolidTexture(8, 8, color.NRGBA{R: 140, G: 180, B: 140, A: 255}),
		geojson.LayerRoads:    benchSolidTexture(8, 8, color.NRGBA{R: 255, G: 255, B: 255, A: 255}),
		geojson.LayerHighways: benchSolidTexture(8, 8, color.NRGBA{R: 255, G: 230, B: 120, A: 255}),
	}

	params := DefaultParams(tileSize, seed, textures)
	params.OffsetX = 0
	params.OffsetY = 0

	// Pre-generate noise once (as done in production)
	params.PerlinNoise = mask.GeneratePerlinNoiseWithOffset(
		tileSize, tileSize,
		params.NoiseScale, params.Seed,
		params.OffsetX, params.OffsetY,
	)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// Paint all layers
		_, _ = PaintLayer(waterLayer, geojson.LayerWater, params)
		_, _ = PaintLayer(landLayer, geojson.LayerLand, params)
		_, _ = PaintLayer(parksLayer, geojson.LayerParks, params)
		_, _ = PaintLayer(roadsLayer, geojson.LayerRoads, params)
		_, _ = PaintLayer(highwaysLayer, geojson.LayerHighways, params)
	}
}

// BenchmarkMaskProcessing benchmarks just the mask processing pipeline
func BenchmarkMaskProcessing(b *testing.B) {
	tileSize := 256
	seed := int64(42)

	// Create a realistic alpha mask
	baseMask := createAlphaMask(tileSize)

	textures := map[geojson.LayerType]image.Image{
		geojson.LayerWater: benchSolidTexture(8, 8, color.NRGBA{R: 120, G: 150, B: 200, A: 255}),
	}

	params := DefaultParams(tileSize, seed, textures)
	params.OffsetX = 0
	params.OffsetY = 0

	// Pre-generate noise
	params.PerlinNoise = mask.GeneratePerlinNoiseWithOffset(
		tileSize, tileSize,
		params.NoiseScale, params.Seed,
		params.OffsetX, params.OffsetY,
	)

	b.ReportAllocs()

	for b.Loop() {
		_, _ = processMask(baseMask, geojson.LayerWater, params)
	}
}

// BenchmarkPerlinNoiseGeneration benchmarks Perlin noise generation
func BenchmarkPerlinNoiseGeneration(b *testing.B) {
	tileSize := 256
	seed := int64(42)
	scale := 30.0

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = mask.GeneratePerlinNoiseWithOffset(tileSize, tileSize, scale, seed, 0, 0)
	}
}

// BenchmarkApplyNoiseToMask benchmarks noise application to mask
func BenchmarkApplyNoiseToMask(b *testing.B) {
	tileSize := 256
	baseMask := createAlphaMask(tileSize)
	noise := mask.GeneratePerlinNoiseWithOffset(tileSize, tileSize, 30.0, 42, 0, 0)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = mask.ApplyNoiseToMask(baseMask, noise, 0.28)
	}
}

// BenchmarkThresholding benchmarks threshold operation
func BenchmarkThresholding(b *testing.B) {
	tileSize := 256
	baseMask := createAlphaMask(tileSize)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = mask.ApplyThreshold(baseMask, 128)
	}
}

// BenchmarkAntialiasing benchmarks edge antialiasing
func BenchmarkAntialiasing(b *testing.B) {
	tileSize := 256
	baseMask := createAlphaMask(tileSize)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = mask.AntialiasEdges(baseMask, 0.5)
	}
}

// BenchmarkPaintFromMask benchmarks the painting operation
func BenchmarkPaintFromMask(b *testing.B) {
	tileSize := 256
	seed := int64(42)
	finalMask := createAlphaMask(tileSize)

	textures := map[geojson.LayerType]image.Image{
		geojson.LayerWater: benchSolidTexture(8, 8, color.NRGBA{R: 120, G: 150, B: 200, A: 255}),
	}

	params := DefaultParams(tileSize, seed, textures)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, _ = paintFromFinalMask(finalMask, geojson.LayerWater, params)
	}
}

// BenchmarkEdgeDarkening benchmarks edge darkening operation using ApplySoftEdgeMask
func BenchmarkEdgeDarkening(b *testing.B) {
	tileSize := 256
	seed := int64(42)
	finalMask := createAlphaMask(tileSize)

	textures := map[geojson.LayerType]image.Image{
		geojson.LayerWater: benchSolidTexture(8, 8, color.NRGBA{R: 120, G: 150, B: 200, A: 255}),
	}

	params := DefaultParams(tileSize, seed, textures)
	style := params.Styles[geojson.LayerWater]

	painted := image.NewNRGBA(image.Rect(0, 0, tileSize, tileSize))
	edgeStrength := style.EdgeStrength

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = mask.ApplySoftEdgeMask(painted, finalMask, edgeStrength)
	}
}

// Helper functions

func createComplexLayer(size int, c color.NRGBA) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))

	// Create a pattern with varying alpha to simulate real features
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			// Create some features (circles, rectangles)
			alpha := uint8(0)

			// Add some circular features
			centerX, centerY := size/3, size/3
			dx, dy := x-centerX, y-centerY
			if dx*dx+dy*dy < (size/6)*(size/6) {
				alpha = 255
			}

			// Add rectangular features
			if x > size/2 && x < size*3/4 && y > size/2 && y < size*3/4 {
				alpha = 255
			}

			img.SetNRGBA(x, y, color.NRGBA{R: c.R, G: c.G, B: c.B, A: alpha})
		}
	}

	return img
}

func createAlphaMask(size int) *image.Gray {
	mask := image.NewGray(image.Rect(0, 0, size, size))

	// Create a gradient pattern
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			// Gradient from center
			centerX, centerY := size/2, size/2
			dx, dy := float64(x-centerX), float64(y-centerY)
			dist := (dx*dx + dy*dy) / float64(size*size)

			val := uint8(255 * (1.0 - dist))
			mask.SetGray(x, y, color.Gray{Y: val})
		}
	}

	return mask
}

func benchSolidTexture(w, h int, c color.NRGBA) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}
