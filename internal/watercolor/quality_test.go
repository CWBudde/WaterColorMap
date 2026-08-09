package watercolor

import (
	"image"
	"image/color"
	"testing"

	"github.com/MeKo-Tech/watercolormap/internal/geojson"
)

// coverage counts pixels with alpha > 0.
func coverage(img *image.NRGBA) int {
	if img == nil {
		return 0
	}
	count := 0
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			if img.NRGBAAt(x, y).A > 0 {
				count++
			}
		}
	}
	return count
}

func absDiff(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}

func buildTestLayer(tileSize int) (*image.RGBA, map[geojson.LayerType]image.Image) {
	layerImg := image.NewRGBA(image.Rect(0, 0, tileSize, tileSize))
	// simple centered square
	for y := tileSize / 4; y < 3*tileSize/4; y++ {
		for x := tileSize / 4; x < 3*tileSize/4; x++ {
			layerImg.Set(x, y, color.RGBA{R: 0, G: 0, B: 255, A: 255})
		}
	}
	textures := map[geojson.LayerType]image.Image{
		geojson.LayerWater: solidTexture(4, 4, color.NRGBA{R: 120, G: 150, B: 200, A: 255}),
	}
	return layerImg, textures
}

func TestVisualQualityBlurImpactsCoverage(t *testing.T) {
	tileSize := 64
	layerImg, textures := buildTestLayer(tileSize)
	baseParams := DefaultParams(tileSize, 99, textures)
	baseParams.OffsetX = 0
	baseParams.OffsetY = 0
	baseParams.NoiseStrength = 0
	baseParams.AntialiasSigma = 0
	baseParams.Threshold = 128

	// small blur. Clear the per-layer MaskBlurSigma so the global params.BlurSigma
	// under test actually takes effect (otherwise the style override wins).
	paramsA := baseParams
	styleA := paramsA.Styles[geojson.LayerWater]
	styleA.MaskBlurSigma = 0
	paramsA.BlurSigma = 1.0
	paramsA.Styles[geojson.LayerWater] = styleA

	imgA, err := PaintLayer(layerImg, geojson.LayerWater, paramsA)
	if err != nil {
		t.Fatalf("PaintLayer blur small failed: %v", err)
	}

	// larger blur should grow coverage after thresholding
	paramsB := baseParams
	styleB := paramsB.Styles[geojson.LayerWater]
	styleB.MaskBlurSigma = 0
	paramsB.BlurSigma = 4.0
	paramsB.Styles[geojson.LayerWater] = styleB

	imgB, err := PaintLayer(layerImg, geojson.LayerWater, paramsB)
	if err != nil {
		t.Fatalf("PaintLayer blur large failed: %v", err)
	}

	if covA, covB := coverage(imgA), coverage(imgB); absDiff(covA, covB) < 10 {
		t.Fatalf("expected blur sigma change to affect coverage: small=%d large=%d", covA, covB)
	}
}

func TestVisualQualityThresholdImpactsCoverage(t *testing.T) {
	tileSize := 64
	layerImg, textures := buildTestLayer(tileSize)
	baseParams := DefaultParams(tileSize, 100, textures)
	baseParams.OffsetX = 0
	baseParams.OffsetY = 0
	baseParams.NoiseStrength = 0
	baseParams.AntialiasSigma = 0
	baseParams.BlurSigma = 2.0

	// Clear the per-layer MaskThreshold so the global params.Threshold under test
	// actually takes effect (otherwise the style override wins).
	paramsLow := baseParams
	styleLow := paramsLow.Styles[geojson.LayerWater]
	styleLow.MaskThreshold = nil
	paramsLow.Styles[geojson.LayerWater] = styleLow
	paramsLow.Threshold = 100
	imgLow, err := PaintLayer(layerImg, geojson.LayerWater, paramsLow)
	if err != nil {
		t.Fatalf("PaintLayer low threshold failed: %v", err)
	}

	paramsHigh := baseParams
	styleHigh := paramsHigh.Styles[geojson.LayerWater]
	styleHigh.MaskThreshold = nil
	paramsHigh.Styles[geojson.LayerWater] = styleHigh
	paramsHigh.Threshold = 180
	imgHigh, err := PaintLayer(layerImg, geojson.LayerWater, paramsHigh)
	if err != nil {
		t.Fatalf("PaintLayer high threshold failed: %v", err)
	}

	if covLow, covHigh := coverage(imgLow), coverage(imgHigh); covLow <= covHigh {
		t.Fatalf("expected higher threshold to reduce coverage: low=%d high=%d", covLow, covHigh)
	}
}
