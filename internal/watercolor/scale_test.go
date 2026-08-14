package watercolor

import (
	"image"
	"reflect"
	"sort"
	"testing"

	"github.com/cwbudde/watercolormap/internal/geojson"
	"github.com/cwbudde/watercolormap/internal/mask"
)

const testSeed = int64(1337)

// metatileFor reproduces the offset and padding arithmetic that
// pipeline.Generator applies, so these tests pin the numbers the renderer
// actually uses rather than an idealised version of them.
func metatileFor(t *testing.T, tileSize int, tileX, tileY int) (params Params, size, offX, offY int) {
	t.Helper()

	params = DefaultParamsForTileSize(tileSize, testSeed, nil)
	pad := min(RequiredPaddingPx(params), tileSize)

	size = tileSize + 2*pad
	offX = tileX*tileSize - pad
	offY = tileY*tileSize - pad
	params.TileSize = size
	params.OffsetX = offX
	params.OffsetY = offY
	return params, size, offX, offY
}

// TestNoiseFieldAlignedAcrossScales is the strongest statement of what 4.5
// requires: a @2x tile must sample the *same* noise field as its 256px twin,
// just at twice the resolution. Device pixel (2i,2j) of the 512 metatile covers
// exactly world pixel (i,j) of the 256 one, so the two must agree exactly — not
// approximately. Before the scale fix, NoiseScale stayed at 30 device px while
// the offsets doubled, and essentially every pixel here disagreed.
func TestNoiseFieldAlignedAcrossScales(t *testing.T) {
	const tileX, tileY = 4317, 2692

	p1, size1, offX1, offY1 := metatileFor(t, 256, tileX, tileY)
	p2, size2, offX2, offY2 := metatileFor(t, 512, tileX, tileY)

	if size2 != 2*size1 {
		t.Fatalf("metatile sizes must stay proportional: 256 -> %d, 512 -> %d", size1, size2)
	}
	if offX2 != 2*offX1 || offY2 != 2*offY1 {
		t.Fatalf("offsets must stay proportional: (%d,%d) vs (%d,%d)", offX1, offY1, offX2, offY2)
	}

	n1 := mask.GeneratePerlinNoiseWithOffset(size1, size1, p1.NoiseScale, p1.Seed, offX1, offY1)
	n2 := mask.GeneratePerlinNoiseWithOffset(size2, size2, p2.NoiseScale, p2.Seed, offX2, offY2)

	for j := range size1 {
		for i := range size1 {
			want := n1.GrayAt(i, j).Y
			got := n2.GrayAt(2*i, 2*j).Y
			if got != want {
				t.Fatalf("noise mismatch at world (%d,%d): 256px=%d, 512px@(%d,%d)=%d",
					i, j, want, 2*i, 2*j, got)
			}
		}
	}
}

// TestRequiredPaddingProportional guards the precondition the alignment rests
// on. The metatile origin in world units is (tileX*tileSize - pad)/scale, so
// the 512 metatile covers the same ground as the 256 one only when the padding
// is exactly double. This is what would break first if someone retuned a sigma
// past the MinGeometryPaddingPx floor.
func TestRequiredPaddingProportional(t *testing.T) {
	for zoom := 10; zoom <= 18; zoom++ {
		p1 := DefaultParamsForTileSize(256, testSeed, nil)
		p1.BlurSigma = ZoomAdjustedBlurSigma(p1.BlurSigma, zoom)
		p1.AntialiasSigma = ZoomAdjustedBlurSigma(p1.AntialiasSigma, zoom)

		p2 := DefaultParamsForTileSize(512, testSeed, nil)
		p2.BlurSigma = ZoomAdjustedBlurSigma(p2.BlurSigma, zoom)
		p2.AntialiasSigma = ZoomAdjustedBlurSigma(p2.AntialiasSigma, zoom)

		pad1 := RequiredPaddingPx(p1)
		pad2 := RequiredPaddingPx(p2)

		if pad2 != 2*pad1 {
			t.Errorf("zoom %d: pad(512)=%d, want 2*pad(256)=%d", zoom, pad2, 2*pad1)
		}
	}
}

// TestScaleIsNoOpAt256 pins the golden-safety guarantee structurally: the 256px
// path must not merely produce equal numbers, it must not be touched at all.
func TestScaleIsNoOpAt256(t *testing.T) {
	if got := ScaleForTileSize(256); got != 1 {
		t.Fatalf("ScaleForTileSize(256) = %v, want exactly 1", got)
	}

	base := DefaultParams(256, testSeed, nil)
	scaled := DefaultParamsForTileSize(256, testSeed, nil)

	if !reflect.DeepEqual(base, scaled) {
		t.Fatal("DefaultParamsForTileSize(256) must equal DefaultParams(256) field for field")
	}
}

// lengthFields and unitlessFields classify every parameter that ApplyScale has
// an opinion about. The reflection guard below turns "someone added a field and
// forgot to decide" into a build failure instead of a silently mis-scaled @2x
// tile, which is a bug nobody would notice for months.
var (
	paramsLengthFields = []string{"NoiseScale", "BlurSigma", "AntialiasSigma"}

	paramsUnitlessFields = []string{"NoiseStrength", "Seed", "Threshold"}

	// Not parameters in their own right: bookkeeping, buffers, or values the
	// caller overwrites with metatile-specific numbers after scaling.
	paramsIgnoredFields = []string{"Styles", "PerlinNoise", "TileSize", "Scale", "OffsetX", "OffsetY"}

	styleLengthFields = []string{"MaskBlurSigma", "ShadeSigma", "EdgeSigma", "NoiseMinDist", "NoiseMaxDist"}

	styleUnitlessFields = []string{
		"EdgeStrength", "MaskNoiseStrength", "ShadeStrength", "EdgeGamma",
		"InvertMask", "AdaptiveNoise", "MaskThreshold",
	}

	styleIgnoredFields = []string{"Texture", "Layer"}
)

func TestScaleClassificationCoversEveryField(t *testing.T) {
	check := func(typ reflect.Type, groups ...[]string) {
		t.Helper()

		var classified []string
		for _, g := range groups {
			classified = append(classified, g...)
		}
		sort.Strings(classified)

		actual := make([]string, 0, typ.NumField())
		for i := range typ.NumField() {
			actual = append(actual, typ.Field(i).Name)
		}
		sort.Strings(actual)

		if !reflect.DeepEqual(classified, actual) {
			t.Errorf("%s fields are not fully classified for scaling.\n classified: %v\n actual:     %v\n"+
				"Add the new field to one of the lists in scale_test.go and, if it is a length, to ApplyScale.",
				typ.Name(), classified, actual)
		}
	}

	check(reflect.TypeFor[Params](), paramsLengthFields, paramsUnitlessFields, paramsIgnoredFields)
	check(reflect.TypeFor[LayerStyle](), styleLengthFields, styleUnitlessFields, styleIgnoredFields)
}

func TestApplyScaleScalesGlobalLengths(t *testing.T) {
	base := DefaultParams(256, testSeed, nil)
	scaled := DefaultParamsForTileSize(512, testSeed, nil)

	if scaled.Scale != 2 {
		t.Fatalf("Scale = %v, want 2", scaled.Scale)
	}

	cases := []struct {
		name      string
		got, want float64
	}{
		{"NoiseScale", scaled.NoiseScale, 2 * base.NoiseScale},
		{"BlurSigma", float64(scaled.BlurSigma), 2 * float64(base.BlurSigma)},
		{"AntialiasSigma", float64(scaled.AntialiasSigma), 2 * float64(base.AntialiasSigma)},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestApplyScaleLeavesGlobalUnitlessAlone(t *testing.T) {
	base := DefaultParams(256, testSeed, nil)
	scaled := DefaultParamsForTileSize(512, testSeed, nil)

	if scaled.NoiseStrength != base.NoiseStrength {
		t.Errorf("NoiseStrength changed: %v -> %v", base.NoiseStrength, scaled.NoiseStrength)
	}
	if scaled.Threshold != base.Threshold {
		t.Errorf("Threshold changed: %v -> %v", base.Threshold, scaled.Threshold)
	}
	// Seed especially: an identical seed across sizes is what makes the noise
	// at 256 and at 512 the same field rather than two unrelated ones.
	if scaled.Seed != base.Seed {
		t.Errorf("Seed changed: %v -> %v", base.Seed, scaled.Seed)
	}
}

func TestApplyScaleScalesPerLayerLengths(t *testing.T) {
	base := DefaultParams(256, testSeed, nil)
	scaled := DefaultParamsForTileSize(512, testSeed, nil)

	for layer, want := range base.Styles {
		got, ok := scaled.Styles[layer]
		if !ok {
			t.Errorf("layer %s missing after scaling", layer)
			continue
		}

		lengths := []struct {
			name      string
			got, want float64
		}{
			{"MaskBlurSigma", float64(got.MaskBlurSigma), 2 * float64(want.MaskBlurSigma)},
			{"ShadeSigma", float64(got.ShadeSigma), 2 * float64(want.ShadeSigma)},
			{"EdgeSigma", float64(got.EdgeSigma), 2 * float64(want.EdgeSigma)},
			{"NoiseMinDist", got.NoiseMinDist, 2 * want.NoiseMinDist},
			{"NoiseMaxDist", got.NoiseMaxDist, 2 * want.NoiseMaxDist},
		}
		for _, c := range lengths {
			if c.got != c.want {
				t.Errorf("%s %s = %v, want %v", layer, c.name, c.got, c.want)
			}
		}
	}
}

func TestApplyScaleLeavesPerLayerUnitlessAlone(t *testing.T) {
	base := DefaultParams(256, testSeed, nil)
	scaled := DefaultParamsForTileSize(512, testSeed, nil)

	for layer, want := range base.Styles {
		got, ok := scaled.Styles[layer]
		if !ok {
			continue
		}

		unitless := []struct {
			name      string
			got, want float64
		}{
			{"EdgeStrength", got.EdgeStrength, want.EdgeStrength},
			{"EdgeGamma", got.EdgeGamma, want.EdgeGamma},
			{"MaskNoiseStrength", got.MaskNoiseStrength, want.MaskNoiseStrength},
			{"ShadeStrength", got.ShadeStrength, want.ShadeStrength},
		}
		for _, c := range unitless {
			if c.got != c.want {
				t.Errorf("%s %s changed: %v -> %v", layer, c.name, c.want, c.got)
			}
		}

		if got.InvertMask != want.InvertMask || got.AdaptiveNoise != want.AdaptiveNoise {
			t.Errorf("%s boolean flags changed", layer)
		}
	}
}

// TestApplyScaleDoesNotMutateSharedStyles guards against ApplyScale writing
// through a Styles map that a caller still holds a reference to.
func TestApplyScaleDoesNotMutateSharedStyles(t *testing.T) {
	base := DefaultParams(256, testSeed, nil)
	shared := base.Styles
	before := shared[geojson.LayerLand].EdgeSigma

	p := base
	p.ApplyScale(2)

	if after := shared[geojson.LayerLand].EdgeSigma; after != before {
		t.Fatalf("ApplyScale mutated the caller's Styles map: EdgeSigma %v -> %v", before, after)
	}
}

// unused keeps the image import honest if the fixtures above ever drop it.
var _ image.Image = (*image.NRGBA)(nil)
