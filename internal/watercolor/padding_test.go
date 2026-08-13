package watercolor

import (
	"image"
	"testing"

	"github.com/cwbudde/watercolormap/internal/geojson"
)

func TestRequiredPaddingPx(t *testing.T) {
	textures := map[geojson.LayerType]image.Image{
		geojson.LayerLand:     image.NewNRGBA(image.Rect(0, 0, 2, 2)),
		geojson.LayerWater:    image.NewNRGBA(image.Rect(0, 0, 2, 2)),
		geojson.LayerParks:    image.NewNRGBA(image.Rect(0, 0, 2, 2)),
		geojson.LayerUrban:    image.NewNRGBA(image.Rect(0, 0, 2, 2)),
		geojson.LayerRoads:    image.NewNRGBA(image.Rect(0, 0, 2, 2)),
		geojson.LayerHighways: image.NewNRGBA(image.Rect(0, 0, 2, 2)),
	}

	params := DefaultParams(256, 123, textures)
	pad := RequiredPaddingPx(params)
	if pad <= 0 {
		t.Fatalf("expected pad > 0 for default params, got %d", pad)
	}

	params.BlurSigma = 0
	params.AntialiasSigma = 0
	for k, s := range params.Styles {
		s.MaskBlurSigma = 0
		s.ShadeSigma = 0
		s.EdgeSigma = 0
		params.Styles[k] = s
	}
	// Even with all sigmas at 0, we need MinGeometryPaddingPx to handle
	// polygon geometry that extends beyond tile boundaries.
	if got := RequiredPaddingPx(params); got != MinGeometryPaddingPx {
		t.Fatalf("expected pad %d (MinGeometryPaddingPx) when all sigmas are 0, got %d", MinGeometryPaddingPx, got)
	}
}
