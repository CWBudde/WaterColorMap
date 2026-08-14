package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/viper"

	"github.com/cwbudde/watercolormap/internal/watercolor"
)

func TestLoadWatercolorOverridesAbsentIsNil(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.SetConfigType("yaml")
	if err := viper.ReadConfig(strings.NewReader("tile-size: 256\n")); err != nil {
		t.Fatal(err)
	}

	got, err := loadWatercolorOverrides()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatal("a config with no watercolor block must yield nil, which is the untouched-defaults path")
	}
}

// TestLoadWatercolorOverridesDecodesKebabKeys is the regression test for the
// dead-config failure mode: without mapstructure tags every one of these keys
// decodes to nothing and the file silently does nothing at all.
func TestLoadWatercolorOverridesDecodesKebabKeys(t *testing.T) {
	got := decodeFixture(t)

	d := got.Defaults
	if d.BlurSigma == nil || *d.BlurSigma != 3.2 {
		t.Errorf("blur-sigma = %v, want 3.2", d.BlurSigma)
	}
	if d.AntialiasSigma == nil || *d.AntialiasSigma != 1.0 {
		t.Errorf("antialias-sigma = %v, want 1.0", d.AntialiasSigma)
	}
	// noise-scale is an int in the YAML: WeaklyTypedInput must widen it.
	if d.NoiseScale == nil || *d.NoiseScale != 40 {
		t.Errorf("noise-scale = %v, want 40", d.NoiseScale)
	}
	if d.NoiseStrength == nil || *d.NoiseStrength != 0.4 {
		t.Errorf("noise-strength = %v, want 0.4", d.NoiseStrength)
	}
	if d.Threshold == nil || *d.Threshold != 60 {
		t.Errorf("threshold = %v, want 60", d.Threshold)
	}

	if _, ok := got.Layers["water"]; !ok {
		t.Error("layers.water did not decode")
	}
}

// decodeFixture runs one realistic config through the real viper path.
func decodeFixture(t *testing.T) *watercolor.Overrides {
	t.Helper()

	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.SetConfigType("yaml")

	const cfg = `
watercolor:
  defaults:
    blur-sigma: 3.2
    antialias-sigma: 1.0
    noise-scale: 40
    noise-strength: 0.4
    threshold: 60
  layers:
    water:
      mask-threshold: 150
      mask-blur-sigma: 2.0
      mask-noise-strength: 0.2
      adaptive-noise: false
      noise-min-dist: 3
      noise-max-dist: 12
      edge-strength: 0
      edge-sigma: 4.0
      edge-gamma: 9.5
      shade-sigma: 1.5
      shade-strength: 0.1
      invert-mask: true
      tint:
        color: "#8ab4c8"
        strength: 0.25
`
	if err := viper.ReadConfig(strings.NewReader(cfg)); err != nil {
		t.Fatal(err)
	}

	got, err := loadWatercolorOverrides()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("watercolor block present but decoded to nil")
	}
	return got
}

// TestLoadWatercolorOverridesDecodesLayerKeys is the per-layer half of the
// tag regression test; it is split from the global half only to keep each
// function's branch count reviewable.
func TestLoadWatercolorOverridesDecodesLayerKeys(t *testing.T) {
	got := decodeFixture(t)

	w, ok := got.Layers["water"]
	if !ok {
		t.Fatal("layers.water did not decode")
	}
	// edge-strength: 0 and adaptive-noise: false are the pointer-motivating
	// cases — they must arrive as set-to-zero, not as absent.
	if w.EdgeStrength == nil || *w.EdgeStrength != 0 {
		t.Errorf("edge-strength = %v, want a set 0", w.EdgeStrength)
	}
	if w.AdaptiveNoise == nil || *w.AdaptiveNoise {
		t.Errorf("adaptive-noise = %v, want a set false", w.AdaptiveNoise)
	}
	if w.InvertMask == nil || !*w.InvertMask {
		t.Errorf("invert-mask = %v, want a set true", w.InvertMask)
	}
	if w.MaskThreshold == nil || *w.MaskThreshold != 150 {
		t.Errorf("mask-threshold = %v, want 150", w.MaskThreshold)
	}
	if w.NoiseMaxDist == nil || *w.NoiseMaxDist != 12 {
		t.Errorf("noise-max-dist = %v, want 12", w.NoiseMaxDist)
	}
}

func TestLoadWatercolorOverridesDecodesTint(t *testing.T) {
	w := decodeFixture(t).Layers["water"]

	if w.Tint == nil || w.Tint.Color == nil || *w.Tint.Color != "#8ab4c8" {
		t.Fatalf("tint.color did not decode: %+v", w.Tint)
	}
	if w.Tint.Strength == nil || *w.Tint.Strength != 0.25 {
		t.Errorf("tint.strength = %v, want 0.25", w.Tint.Strength)
	}
}

// TestLoadWatercolorOverridesRejectsTypos: a misspelled key must stop the run,
// not render with defaults and leave the operator wondering why nothing changed.
func TestLoadWatercolorOverridesRejectsTypos(t *testing.T) {
	cases := map[string]string{
		"misspelled global key": "watercolor:\n  defaults:\n    blursigma: 3.2\n",
		"misspelled layer key":  "watercolor:\n  layers:\n    water:\n      edge-strngth: 0.5\n",
		"unknown layer":         "watercolor:\n  layers:\n    oceans:\n      edge-sigma: 3\n",
		"out of range":          "watercolor:\n  defaults:\n    threshold: 900\n",
	}

	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			viper.Reset()
			t.Cleanup(viper.Reset)
			viper.SetConfigType("yaml")
			if err := viper.ReadConfig(strings.NewReader(cfg)); err != nil {
				t.Fatal(err)
			}

			if _, err := loadWatercolorOverrides(); err == nil {
				t.Fatal("expected an error; a bad key must fail at startup, not silently no-op")
			}
		})
	}
}
