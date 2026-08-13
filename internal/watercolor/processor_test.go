package watercolor

import (
	"image"
	"image/color"
	"testing"

	"github.com/cwbudde/watercolormap/internal/geojson"
)

func solidTexture(w, h int, c color.NRGBA) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

func TestPaintLayerAppliesMaskTintAndEdge(t *testing.T) {
	tileSize := 32 // Increased from 16 for better edge halo visibility with box blur
	layer := geojson.LayerWater

	baseColor := color.NRGBA{R: 100, G: 100, B: 100, A: 255}
	textures := map[geojson.LayerType]image.Image{
		layer: solidTexture(4, 4, baseColor),
	}

	params := DefaultParams(tileSize, 123, textures)
	params.NoiseStrength = 0.0  // deterministic
	params.AntialiasSigma = 0.0 // keep crisp for assertions
	params.BlurSigma = 1.0      // mild blur for edge halo
	params.Threshold = 128      // retain shape
	params.OffsetX = 0
	params.OffsetY = 0

	style := params.Styles[layer]
	style.EdgeStrength = 0.6 // Increased from 0.5 for stronger darkening
	style.EdgeSigma = 3.5    // Increased for box blur visibility
	style.EdgeGamma = 1.0
	style.MaskNoiseStrength = 0.0 // keep deterministic: per-layer noise would otherwise override params.NoiseStrength
	params.Styles[layer] = style

	// Build a simple square feature mask (centered, larger)
	layerImg := image.NewRGBA(image.Rect(0, 0, tileSize, tileSize))
	for y := 8; y < 24; y++ {
		for x := 8; x < 24; x++ {
			layerImg.Set(x, y, color.RGBA{R: 0, G: 0, B: 255, A: 255})
		}
	}

	out, err := PaintLayer(layerImg, layer, params)
	if err != nil {
		t.Fatalf("PaintLayer returned error: %v", err)
	}
	if out == nil {
		t.Fatal("PaintLayer returned nil output")
	}

	// Outside feature should be fully transparent
	if got := out.NRGBAAt(0, 0); got.A != 0 {
		t.Fatalf("expected outside alpha 0, got %d", got.A)
	}

	// Center should show the base texture color (no tinting anymore)
	center := out.NRGBAAt(16, 16)
	// The center should have the base color from texture (possibly slightly darker from edge effects)
	// but should not be transparent
	if center.A != 255 {
		t.Fatalf("unexpected center alpha %d, expected 255", center.A)
	}

	// Edge region should include darker pixels than center due to halo darkening
	darkest := center
	for y := 0; y < tileSize; y++ {
		for x := 0; x < tileSize; x++ {
			p := out.NRGBAAt(x, y)
			if p.A == 255 && p.R < darkest.R {
				darkest = p
			}
		}
	}
	if darkest.R >= center.R && darkest.G >= center.G && darkest.B >= center.B {
		t.Fatalf("expected some pixels to be darkened relative to center; center=%+v darkest=%+v", center, darkest)
	}
}

func TestPaintLayerMissingStyle(t *testing.T) {
	params := Params{
		TileSize:   16,
		NoiseScale: 10,
		Styles:     map[geojson.LayerType]LayerStyle{},
	}
	_, err := PaintLayer(image.NewRGBA(image.Rect(0, 0, 4, 4)), geojson.LayerLand, params)
	if err == nil {
		t.Fatal("expected error for missing style")
	}
}

// TestPaintFromFinalMaskNoStaleDataFromPool guards the pooled ProcessorContext against
// leaking pixels from a previous paint. The *Into helpers only write the final mask's
// bounds, so a mask smaller than the tile leaves the rest of the recycled buffer
// untouched and the edge pass would otherwise propagate the previous tile's pixels.
func TestPaintFromFinalMaskNoStaleDataFromPool(t *testing.T) {
	const tileSize = 16
	layer := geojson.LayerWater

	textures := map[geojson.LayerType]image.Image{
		layer: solidTexture(4, 4, color.NRGBA{R: 200, G: 40, B: 40, A: 255}),
	}
	params := DefaultParams(tileSize, 7, textures)

	// First paint: full-tile opaque mask, filling every pooled buffer.
	fullMask := image.NewGray(image.Rect(0, 0, tileSize, tileSize))
	for i := range fullMask.Pix {
		fullMask.Pix[i] = 255
	}
	if _, err := paintFromFinalMask(fullMask, layer, params); err != nil {
		t.Fatalf("first paint failed: %v", err)
	}

	// Second paint: mask smaller than the tile. Everything outside the mask bounds
	// must stay transparent instead of showing the previous paint.
	smallMask := image.NewGray(image.Rect(0, 0, tileSize/2, tileSize/2))
	for i := range smallMask.Pix {
		smallMask.Pix[i] = 255
	}
	out, err := paintFromFinalMask(smallMask, layer, params)
	if err != nil {
		t.Fatalf("second paint failed: %v", err)
	}

	for y := 0; y < tileSize; y++ {
		for x := 0; x < tileSize; x++ {
			if x < tileSize/2 && y < tileSize/2 {
				continue
			}
			if got := out.NRGBAAt(x, y).A; got != 0 {
				t.Fatalf("stale pixel at (%d,%d): alpha=%d, want 0", x, y, got)
			}
		}
	}
}
