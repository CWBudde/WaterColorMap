package texture

import (
	"image"
	"image/color"
	"testing"

	"github.com/cwbudde/watercolormap/internal/geojson"
)

func TestTextureNameForLayer(t *testing.T) {
	expected := map[geojson.LayerType]string{
		geojson.LayerPaper:     "white.png",
		geojson.LayerLand:      "land.png",
		geojson.LayerWater:     "water.png",
		geojson.LayerParks:     "green.png",
		geojson.LayerUrban:     "urban.png",
		geojson.LayerCivic:     "civic.png",
		geojson.LayerBuildings: "urban.png",
		geojson.LayerRoads:     "gray.png",
		geojson.LayerHighways:  "yellow.png",
	}

	for layer, want := range expected {
		got, ok := TextureNameForLayer(layer)
		if !ok {
			t.Fatalf("layer %s missing from DefaultLayerTextures", layer)
		}
		if got != want {
			t.Fatalf("layer %s texture mismatch: got %s, want %s", layer, got, want)
		}
	}
}

func TestTileTextureWithOffsetsSeamless(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			src.SetNRGBA(x, y, color.NRGBA{
				R: uint8(10*x + y),
				G: uint8(20*y + x),
				B: uint8(x + 2*y),
				A: 255,
			})
		}
	}

	ref := TileTexture(src, 8, 0, 0)
	left := TileTexture(src, 4, 0, 0)
	right := TileTexture(src, 4, 4, 0)
	bottom := TileTexture(src, 4, 0, 4)

	assertMatchesSubregion(t, left, ref, 0, 0)
	assertMatchesSubregion(t, right, ref, 4, 0)
	assertMatchesSubregion(t, bottom, ref, 0, 4)
}

func TestTileTextureUsesOffsets(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			src.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 0, A: 255})
		}
	}

	tile := TileTexture(src, 2, 1, 1)

	if tile.NRGBAAt(0, 0) != src.NRGBAAt(1, 1) {
		t.Fatalf("expected offset top-left to match source(1,1)")
	}
	if tile.NRGBAAt(1, 1) != src.NRGBAAt(2, 2) {
		t.Fatalf("expected offset bottom-right to match source(2,2)")
	}
}

func TestApplyMaskToTexture(t *testing.T) {
	tex := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	tex.SetNRGBA(0, 0, color.NRGBA{R: 10, G: 20, B: 30, A: 255})
	tex.SetNRGBA(1, 0, color.NRGBA{R: 40, G: 50, B: 60, A: 255})
	tex.SetNRGBA(0, 1, color.NRGBA{R: 70, G: 80, B: 90, A: 255})
	tex.SetNRGBA(1, 1, color.NRGBA{R: 100, G: 110, B: 120, A: 255})

	mask := image.NewGray(image.Rect(0, 0, 2, 2))
	mask.SetGray(0, 0, color.Gray{Y: 0})
	mask.SetGray(1, 0, color.Gray{Y: 64})
	mask.SetGray(0, 1, color.Gray{Y: 128})
	mask.SetGray(1, 1, color.Gray{Y: 255})

	result := ApplyMaskToTexture(tex, mask)
	if result == nil {
		t.Fatal("ApplyMaskToTexture returned nil")
	}

	tests := []struct {
		x, y int
		want color.NRGBA
	}{
		{0, 0, color.NRGBA{R: 10, G: 20, B: 30, A: 0}},
		{1, 0, color.NRGBA{R: 40, G: 50, B: 60, A: 64}},
		{0, 1, color.NRGBA{R: 70, G: 80, B: 90, A: 128}},
		{1, 1, color.NRGBA{R: 100, G: 110, B: 120, A: 255}},
	}

	for _, tc := range tests {
		if got := result.NRGBAAt(tc.x, tc.y); got != tc.want {
			t.Errorf("pixel (%d,%d) mismatch: got %+v, want %+v", tc.x, tc.y, got, tc.want)
		}
	}
}

func TestTintTexture(t *testing.T) {
	tex := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	tex.SetNRGBA(0, 0, color.NRGBA{R: 100, G: 100, B: 100, A: 200})

	tint := color.NRGBA{R: 200, G: 0, B: 0, A: 255}
	result := TintTexture(tex, tint, 0.5)
	if result == nil {
		t.Fatal("TintTexture returned nil")
	}

	got := result.NRGBAAt(0, 0)
	want := color.NRGBA{R: 150, G: 50, B: 50, A: 200}
	if got != want {
		t.Fatalf("tinted pixel mismatch: got %+v, want %+v", got, want)
	}
}

// assertMatchesSubregion compares a tile against a region in the reference image.
func assertMatchesSubregion(t *testing.T, tile *image.NRGBA, ref *image.NRGBA, startX, startY int) {
	t.Helper()

	if tile == nil || ref == nil {
		t.Fatalf("nil image provided")
	}

	for y := 0; y < tile.Bounds().Dy(); y++ {
		for x := 0; x < tile.Bounds().Dx(); x++ {
			refColor := ref.NRGBAAt(startX+x, startY+y)
			tileColor := tile.NRGBAAt(x, y)
			if tileColor != refColor {
				t.Fatalf("mismatch at (%d,%d): tile=%+v ref=%+v", x, y, tileColor, refColor)
			}
		}
	}
}
