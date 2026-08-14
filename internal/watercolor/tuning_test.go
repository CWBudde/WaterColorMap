package watercolor

import (
	"image"
	"image/color"
	"strings"
	"testing"

	"github.com/cwbudde/watercolormap/internal/geojson"
)

func f64(v float64) *float64 { return &v }
func iptr(v int) *int        { return &v }
func bptr(v bool) *bool      { return &v }
func sptr(v string) *string  { return &v }

// sameStyle compares two styles by value. A plain == would compare
// MaskThreshold by pointer identity, and DefaultParams allocates a fresh uint8
// on every call — so == reports "changed" for nine of the ten layers even when
// nothing touched them.
func sameStyle(a, b LayerStyle) bool {
	switch {
	case a.MaskThreshold == nil && b.MaskThreshold != nil,
		a.MaskThreshold != nil && b.MaskThreshold == nil:
		return false
	case a.MaskThreshold != nil && *a.MaskThreshold != *b.MaskThreshold:
		return false
	}
	a.MaskThreshold, b.MaskThreshold = nil, nil
	return a == b
}

// TestNilTunerIsAbsolutelyInert is the golden-safety guarantee for 4.7, stated
// the same structural way 4.5's is: with no config, no arithmetic runs at all,
// so the 256px output cannot move by even one ULP.
func TestNilTunerIsAbsolutelyInert(t *testing.T) {
	tuner, err := NewTuner(nil, nil)
	if err != nil {
		t.Fatalf("NewTuner(nil): %v", err)
	}
	if tuner != nil {
		t.Fatal("NewTuner(nil) must return a nil *Tuner, not an empty one")
	}

	want := DefaultParams(256, 99, nil)
	got := DefaultParams(256, 99, nil)
	tuner.Apply(&got) // nil receiver: must not panic

	if got.BlurSigma != want.BlurSigma || got.NoiseScale != want.NoiseScale ||
		got.Threshold != want.Threshold || got.NoiseStrength != want.NoiseStrength ||
		got.AntialiasSigma != want.AntialiasSigma {
		t.Error("nil Tuner changed the global parameters")
	}
	for layer, w := range want.Styles {
		if !sameStyle(got.Styles[layer], w) {
			t.Errorf("nil Tuner changed layer %s", layer)
		}
	}
}

func TestTunerAppliesGlobalOverrides(t *testing.T) {
	o := &Overrides{Defaults: GlobalOverrides{
		BlurSigma:      f64(3.5),
		AntialiasSigma: f64(0.75),
		NoiseScale:     f64(42),
		NoiseStrength:  f64(0.5),
		Threshold:      iptr(200),
	}}

	tuner, err := NewTuner(o, nil)
	if err != nil {
		t.Fatalf("NewTuner: %v", err)
	}

	p := DefaultParams(256, 7, nil)
	tuner.Apply(&p)

	if p.BlurSigma != 3.5 || p.AntialiasSigma != 0.75 {
		t.Errorf("sigmas = %v/%v, want 3.5/0.75", p.BlurSigma, p.AntialiasSigma)
	}
	if p.NoiseScale != 42 || p.NoiseStrength != 0.5 {
		t.Errorf("noise = %v/%v, want 42/0.5", p.NoiseScale, p.NoiseStrength)
	}
	if p.Threshold != 200 {
		t.Errorf("Threshold = %d, want 200", p.Threshold)
	}
}

// TestTunerAppliesZeroValuedOverrides is the reason every scalar is a pointer.
// `edge-strength: 0` is a real request — turn edge darkening off — and it
// differs from the layer default of 0.2. A value-typed struct could not express
// it, and the setting would silently do nothing.
func TestTunerAppliesZeroValuedOverrides(t *testing.T) {
	o := &Overrides{Layers: map[string]LayerOverrides{
		"water": {
			EdgeStrength:  f64(0),
			ShadeStrength: f64(0),
			AdaptiveNoise: bptr(false),
		},
	}}

	base := DefaultParams(256, 7, nil).Styles[geojson.LayerWater]
	if base.EdgeStrength == 0 || !base.AdaptiveNoise {
		t.Fatalf("fixture assumption broken: water defaults are edge=%v adaptive=%v",
			base.EdgeStrength, base.AdaptiveNoise)
	}

	tuner, err := NewTuner(o, nil)
	if err != nil {
		t.Fatalf("NewTuner: %v", err)
	}
	p := DefaultParams(256, 7, nil)
	tuner.Apply(&p)

	got := p.Styles[geojson.LayerWater]
	if got.EdgeStrength != 0 {
		t.Errorf("EdgeStrength = %v, want 0", got.EdgeStrength)
	}
	if got.ShadeStrength != 0 {
		t.Errorf("ShadeStrength = %v, want 0", got.ShadeStrength)
	}
	if got.AdaptiveNoise {
		t.Error("AdaptiveNoise = true, want false")
	}
}

// TestTunerDoesNotMutateOtherLayers pins the blast radius: touching one layer
// must leave the other nine at their defaults.
func TestTunerDoesNotMutateOtherLayers(t *testing.T) {
	o := &Overrides{Layers: map[string]LayerOverrides{
		"roads": {EdgeSigma: f64(9)},
	}}
	tuner, err := NewTuner(o, nil)
	if err != nil {
		t.Fatalf("NewTuner: %v", err)
	}

	want := DefaultParams(256, 7, nil)
	got := DefaultParams(256, 7, nil)
	tuner.Apply(&got)

	for layer, w := range want.Styles {
		if layer == geojson.LayerRoads {
			continue
		}
		if !sameStyle(got.Styles[layer], w) {
			t.Errorf("layer %s changed although only roads was configured", layer)
		}
	}
	if got.Styles[geojson.LayerRoads].EdgeSigma != 9 {
		t.Errorf("roads EdgeSigma = %v, want 9", got.Styles[geojson.LayerRoads].EdgeSigma)
	}
}

// TestTunerDoesNotMutateCallersStyles guards the copy-on-write in Apply. The
// server keeps one generator per tile size and calls this per tile; writing
// through a shared map would make tile N's config leak into tile N+1.
func TestTunerDoesNotMutateCallersStyles(t *testing.T) {
	o := &Overrides{Layers: map[string]LayerOverrides{"parks": {EdgeGamma: f64(4)}}}
	tuner, err := NewTuner(o, nil)
	if err != nil {
		t.Fatalf("NewTuner: %v", err)
	}

	base := DefaultParams(256, 7, nil)
	shared := base.Styles
	before := shared[geojson.LayerParks].EdgeGamma

	p := base
	tuner.Apply(&p)

	if after := shared[geojson.LayerParks].EdgeGamma; after != before {
		t.Fatalf("Apply mutated the caller's Styles map: EdgeGamma %v -> %v", before, after)
	}
}

// TestTintKeyedByLayerNotTexture is the whole reason tinting is precomputed per
// layer. `rivers` deliberately shares the *water* bitmap, so a tint keyed by
// texture identity would recolour rivers the moment someone tinted water.
func TestTintKeyedByLayerNotTexture(t *testing.T) {
	waterTex := solidTexture(4, 4, color.NRGBA{R: 10, G: 10, B: 10, A: 255})
	textures := map[geojson.LayerType]image.Image{geojson.LayerWater: waterTex}

	// Precondition: the two layers really do share one bitmap today.
	styles := DefaultParams(ReferenceTileSize, 0, textures).Styles
	if styles[geojson.LayerRivers].Texture != styles[geojson.LayerWater].Texture {
		t.Skip("water and rivers no longer share a texture; this test has nothing to prove")
	}

	o := &Overrides{Layers: map[string]LayerOverrides{
		"water": {Tint: &Tint{Color: sptr("#ff0000"), Strength: f64(1)}},
	}}
	tuner, err := NewTuner(o, textures)
	if err != nil {
		t.Fatalf("NewTuner: %v", err)
	}

	p := DefaultParams(256, 7, textures)
	tuner.Apply(&p)

	tinted, ok := p.Styles[geojson.LayerWater].Texture.(*image.NRGBA)
	if !ok {
		t.Fatal("water texture was not replaced by a tinted copy")
	}
	if c := tinted.NRGBAAt(1, 1); c.R != 255 || c.G != 0 || c.B != 0 {
		t.Errorf("water texel = %v, want fully red at strength 1", c)
	}
	if p.Styles[geojson.LayerRivers].Texture != waterTex {
		t.Error("tinting water also tinted rivers; the tint must be keyed by layer")
	}
}

func TestValidateRejectsBadValues(t *testing.T) {
	cases := []struct {
		name      string
		overrides Overrides
		wantSubs  []string
	}{
		{
			name:      "unknown layer",
			overrides: Overrides{Layers: map[string]LayerOverrides{"oceans": {}}},
			wantSubs:  []string{"watercolor.layers.oceans", "unknown layer"},
		},
		{
			name:      "sigma over cap",
			overrides: Overrides{Defaults: GlobalOverrides{BlurSigma: f64(1e6)}},
			wantSubs:  []string{"watercolor.defaults.blur-sigma", "out of range"},
		},
		{
			name:      "strength out of unit range",
			overrides: Overrides{Layers: map[string]LayerOverrides{"water": {EdgeStrength: f64(1.5)}}},
			wantSubs:  []string{"watercolor.layers.water.edge-strength", "[0, 1]"},
		},
		{
			name:      "threshold out of byte range",
			overrides: Overrides{Defaults: GlobalOverrides{Threshold: iptr(300)}},
			wantSubs:  []string{"watercolor.defaults.threshold", "[0, 255]"},
		},
		{
			name: "min dist above max dist",
			overrides: Overrides{Layers: map[string]LayerOverrides{
				"roads": {NoiseMinDist: f64(20), NoiseMaxDist: f64(5)},
			}},
			wantSubs: []string{"noise-min-dist", "noise-max-dist"},
		},
		{
			name:      "ambiguous zero sentinel",
			overrides: Overrides{Layers: map[string]LayerOverrides{"water": {MaskBlurSigma: f64(0)}}},
			wantSubs:  []string{"mask-blur-sigma", "inherit"},
		},
		{
			name: "bad tint colour",
			overrides: Overrides{Layers: map[string]LayerOverrides{
				"water": {Tint: &Tint{Color: sptr("notacolor")}},
			}},
			wantSubs: []string{"watercolor.layers.water.tint.color", "hex color"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.overrides.Validate()
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			for _, sub := range c.wantSubs {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("error %q does not mention %q", err, sub)
				}
			}
		})
	}
}

// TestValidateReportsEveryProblem: a config file is edited as a whole, so
// surfacing one error per run is a miserable way to fix several typos.
func TestValidateReportsEveryProblem(t *testing.T) {
	o := Overrides{
		Defaults: GlobalOverrides{Threshold: iptr(300), NoiseStrength: f64(9)},
		Layers: map[string]LayerOverrides{
			"nope":  {},
			"water": {EdgeGamma: f64(0)},
		},
	}

	err := o.Validate()
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, sub := range []string{"threshold", "noise-strength", "layers.nope", "edge-gamma"} {
		if !strings.Contains(err.Error(), sub) {
			t.Errorf("joined error is missing %q:\n%v", sub, err)
		}
	}
}

func TestValidateAcceptsRealisticConfig(t *testing.T) {
	o := Overrides{
		Defaults: GlobalOverrides{BlurSigma: f64(2.45), Threshold: iptr(50)},
		Layers: map[string]LayerOverrides{
			"water": {
				MaskThreshold: iptr(144),
				EdgeStrength:  f64(0),
				Tint:          &Tint{Color: sptr("8ab4c8"), Strength: f64(0.25)},
			},
		},
	}
	if err := o.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestParseHexColor(t *testing.T) {
	got, err := ParseHexColor("#8AB4C8")
	if err != nil {
		t.Fatalf("ParseHexColor: %v", err)
	}
	want := color.NRGBA{R: 0x8a, G: 0xb4, B: 0xc8, A: 255}
	if got != want {
		t.Errorf("got %v, want %v", got, want)
	}

	if _, err := ParseHexColor("#8ab4c8ff"); err == nil {
		t.Error("8-digit hex must be rejected: tinting never touches alpha")
	}
}

// TestApplyPanicsAfterScale pins the ordering contract. Config values are
// lengths at the 256px reference size; applying them on top of already-scaled
// ones would halve them at @2x, which is exactly the class of bug 4.5 removed.
func TestApplyPanicsAfterScale(t *testing.T) {
	tuner, err := NewTuner(&Overrides{Defaults: GlobalOverrides{BlurSigma: f64(3)}}, nil)
	if err != nil {
		t.Fatalf("NewTuner: %v", err)
	}

	p := DefaultParamsForTileSize(512, 7, nil)
	defer func() {
		if recover() == nil {
			t.Fatal("Apply after ApplyScale must panic, not silently mis-scale")
		}
	}()
	tuner.Apply(&p)
}
