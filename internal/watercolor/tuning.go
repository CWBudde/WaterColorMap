package watercolor

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"maps"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/cwbudde/watercolormap/internal/geojson"
	"github.com/cwbudde/watercolormap/internal/texture"
)

// MaxTunableSigma caps every blur radius a config file may ask for.
//
// The cap is not cosmetic. RequiredPaddingPx derives the metatile size from the
// largest sigma in Params, so an unbounded sigma lets a one-line config entry
// quadruple the rendered area and the Overpass fetch bbox along with it. The
// value is expressed at the 256px reference size, like every other tuning key.
const MaxTunableSigma = 20.0

// Overrides is the config-file view of the watercolor parameters.
//
// Every scalar is a pointer, which is a requirement rather than a style choice:
// `edge-strength: 0`, `shade-strength: 0` and `adaptive-noise: false` are all
// meaningful settings, and all of them differ from some layer's default. A
// value type could not tell "the user asked for zero" from "the user said
// nothing".
//
// The mapstructure tags are equally load-bearing. Viper's tag-free fallback
// matches field names case-insensitively only, so every kebab-case key here
// would silently decode to nothing — exactly the dead-config failure mode this
// package exists to avoid.
type Overrides struct {
	// Defaults applies to the global (non per-layer) parameters.
	Defaults GlobalOverrides `mapstructure:"defaults"`

	// Layers is keyed by the geojson.LayerType strings ("water", "roads", ...).
	// Unknown keys are a hard error, not a silent no-op.
	Layers map[string]LayerOverrides `mapstructure:"layers"`
}

// GlobalOverrides are the parameters that are not per-layer.
type GlobalOverrides struct {
	BlurSigma      *float64 `mapstructure:"blur-sigma"`
	AntialiasSigma *float64 `mapstructure:"antialias-sigma"`
	NoiseScale     *float64 `mapstructure:"noise-scale"`
	NoiseStrength  *float64 `mapstructure:"noise-strength"`
	Threshold      *int     `mapstructure:"threshold"`
}

// Tint recolors a layer's paper texture toward a solid color.
type Tint struct {
	// Color is a hex string, with or without the leading '#': "#8ab4c8".
	Color *string `mapstructure:"color"`
	// Strength is the blend factor, 0 (untinted) to 1 (flat color).
	Strength *float64 `mapstructure:"strength"`
}

// LayerOverrides are the per-layer knobs.
//
// Note there is deliberately no edge *color* key. The edge pass
// (mask.ApplySoftEdgeMaskInto) only reduces HSL lightness; it has no color
// input, so such a key could not do anything.
type LayerOverrides struct {
	MaskThreshold     *int     `mapstructure:"mask-threshold"`
	MaskBlurSigma     *float64 `mapstructure:"mask-blur-sigma"`
	MaskNoiseStrength *float64 `mapstructure:"mask-noise-strength"`
	ShadeSigma        *float64 `mapstructure:"shade-sigma"`
	ShadeStrength     *float64 `mapstructure:"shade-strength"`
	EdgeStrength      *float64 `mapstructure:"edge-strength"`
	EdgeSigma         *float64 `mapstructure:"edge-sigma"`
	EdgeGamma         *float64 `mapstructure:"edge-gamma"`
	NoiseMinDist      *float64 `mapstructure:"noise-min-dist"`
	NoiseMaxDist      *float64 `mapstructure:"noise-max-dist"`
	AdaptiveNoise     *bool    `mapstructure:"adaptive-noise"`
	InvertMask        *bool    `mapstructure:"invert-mask"`
	Tint              *Tint    `mapstructure:"tint"`
}

// tunableLayers is the set of layers a config file may name. It is derived from
// DefaultParams rather than written out by hand so that adding a layer to the
// renderer automatically makes it tunable, and so a layer that is renamed can
// never leave a stale accepted key behind.
func tunableLayers() map[geojson.LayerType]struct{} {
	styles := DefaultParams(ReferenceTileSize, 0, nil).Styles
	set := make(map[geojson.LayerType]struct{}, len(styles))
	for layer := range styles {
		set[layer] = struct{}{}
	}
	return set
}

func knownLayerNames() []string {
	set := tunableLayers()
	names := make([]string, 0, len(set))
	for layer := range set {
		names = append(names, string(layer))
	}
	sort.Strings(names)
	return names
}

// Validate reports every problem in the overrides at once rather than the first
// one, because a config file is edited as a whole and a one-error-per-run loop
// is a miserable way to fix five typos. Keys are named by their full path so the
// message can be pasted back into the file.
func (o *Overrides) Validate() error {
	if o == nil {
		return nil
	}

	var errs []error

	errs = append(errs, o.Defaults.validate()...)

	defaults := DefaultParams(ReferenceTileSize, 0, nil).Styles
	names := make([]string, 0, len(o.Layers))
	for name := range o.Layers {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic error order

	for _, name := range names {
		def, ok := defaults[geojson.LayerType(name)]
		if !ok {
			errs = append(errs, fmt.Errorf(
				"watercolor.layers.%s: unknown layer (known layers: %s)",
				name, strings.Join(knownLayerNames(), ", ")))
			continue
		}
		lo := o.Layers[name]
		errs = append(errs, lo.validate("watercolor.layers."+name, def)...)
	}

	return errors.Join(errs...)
}

func (g *GlobalOverrides) validate() []error {
	const prefix = "watercolor.defaults."

	var errs []error
	errs = appendErr(errs, checkSigma(prefix+"blur-sigma", g.BlurSigma))
	errs = appendErr(errs, checkSigma(prefix+"antialias-sigma", g.AntialiasSigma))
	errs = appendErr(errs, checkPositive(prefix+"noise-scale", g.NoiseScale))
	errs = appendErr(errs, checkUnit(prefix+"noise-strength", g.NoiseStrength))
	errs = appendErr(errs, checkByte(prefix+"threshold", g.Threshold))
	return errs
}

// validate checks one layer's overrides. def is that layer's default style: the
// adaptive-noise check below needs it, because an override of one distance is
// only meaningful against the value the other one inherits.
func (l *LayerOverrides) validate(prefix string, def LayerStyle) []error {
	var errs []error

	errs = appendErr(errs, checkByte(prefix+".mask-threshold", l.MaskThreshold))
	errs = appendErr(errs, checkSigma(prefix+".mask-blur-sigma", l.MaskBlurSigma))
	errs = appendErr(errs, checkUnit(prefix+".mask-noise-strength", l.MaskNoiseStrength))
	errs = appendErr(errs, checkSigma(prefix+".shade-sigma", l.ShadeSigma))
	errs = appendErr(errs, checkUnit(prefix+".shade-strength", l.ShadeStrength))
	errs = appendErr(errs, checkUnit(prefix+".edge-strength", l.EdgeStrength))
	errs = appendErr(errs, checkSigma(prefix+".edge-sigma", l.EdgeSigma))
	errs = appendErr(errs, checkPositive(prefix+".edge-gamma", l.EdgeGamma))
	errs = appendErr(errs, checkNonNegative(prefix+".noise-min-dist", l.NoiseMinDist))
	errs = appendErr(errs, checkNonNegative(prefix+".noise-max-dist", l.NoiseMaxDist))

	// Compare the *effective* pair, not just the overridden one. Overriding a
	// single distance is the common case, and it can still invert the order
	// against the inherited value: noise-min-dist: 12 with the default max of 10
	// turns smoothstep(12, 10, d) into a discontinuous step instead of the
	// gradual attenuation the key promises. Scaling multiplies both by the same
	// factor, so checking at the reference tile size is enough.
	if l.NoiseMinDist != nil || l.NoiseMaxDist != nil {
		minDist, minSrc := def.NoiseMinDist, "default"
		maxDist, maxSrc := def.NoiseMaxDist, "default"
		if l.NoiseMinDist != nil {
			minDist, minSrc = *l.NoiseMinDist, "configured"
		}
		if l.NoiseMaxDist != nil {
			maxDist, maxSrc = *l.NoiseMaxDist, "configured"
		}
		if minDist > maxDist {
			errs = append(errs, fmt.Errorf(
				"%s.noise-min-dist (%g, %s) must not exceed noise-max-dist (%g, %s)",
				prefix, minDist, minSrc, maxDist, maxSrc))
		}
	}

	// mask-blur-sigma and mask-noise-strength use a `> 0` sentinel in
	// processMask, so an explicit zero does not mean "no blur" — it means
	// "fall back to the global value". Rather than change that behaviour here
	// (it would move the goldens), refuse the ambiguous value and say so.
	if l.MaskBlurSigma != nil && *l.MaskBlurSigma == 0 {
		errs = append(errs, fmt.Errorf(
			"%s.mask-blur-sigma: 0 means \"inherit watercolor.defaults.blur-sigma\", not \"no blur\"; "+
				"remove the key to inherit, or use a small positive value", prefix))
	}
	if l.MaskNoiseStrength != nil && *l.MaskNoiseStrength == 0 {
		errs = append(errs, fmt.Errorf(
			"%s.mask-noise-strength: 0 means \"inherit watercolor.defaults.noise-strength\", not \"no noise\"; "+
				"remove the key to inherit, or use a small positive value", prefix))
	}

	if l.Tint != nil {
		errs = append(errs, l.Tint.validate(prefix+".tint")...)
	}

	return errs
}

func (t *Tint) validate(prefix string) []error {
	var errs []error

	if t.Color == nil {
		errs = append(errs, fmt.Errorf("%s.color is required when a tint is given", prefix))
	} else if _, err := ParseHexColor(*t.Color); err != nil {
		errs = append(errs, fmt.Errorf("%s.color: %w", prefix, err))
	}

	errs = appendErr(errs, checkUnit(prefix+".strength", t.Strength))
	return errs
}

func appendErr(errs []error, err error) []error {
	if err != nil {
		errs = append(errs, err)
	}
	return errs
}

// checkFinite rejects NaN and ±Inf before any range comparison. Every ordered
// comparison against NaN is false, so a range check like `*v < 0 || *v > 1`
// accepts it — and YAML spells it `.nan`, so it is reachable from a config
// file. A non-finite sigma or strength then propagates into the padding
// calculation and the blur kernel and corrupts pixels, instead of producing the
// startup error the other invalid values get.
func checkFinite(key string, v *float64) error {
	if v == nil {
		return nil
	}
	if math.IsNaN(*v) || math.IsInf(*v, 0) {
		return fmt.Errorf("%s: %g is not a finite number", key, *v)
	}
	return nil
}

func checkSigma(key string, v *float64) error {
	if v == nil {
		return nil
	}
	if err := checkFinite(key, v); err != nil {
		return err
	}
	if *v < 0 || *v > MaxTunableSigma {
		return fmt.Errorf("%s: %g out of range [0, %g] — larger blurs would grow the metatile and the data fetch with it",
			key, *v, MaxTunableSigma)
	}
	return nil
}

func checkUnit(key string, v *float64) error {
	if v == nil {
		return nil
	}
	if err := checkFinite(key, v); err != nil {
		return err
	}
	if *v < 0 || *v > 1 {
		return fmt.Errorf("%s: %g out of range [0, 1]", key, *v)
	}
	return nil
}

func checkPositive(key string, v *float64) error {
	if v == nil {
		return nil
	}
	if err := checkFinite(key, v); err != nil {
		return err
	}
	if *v <= 0 {
		return fmt.Errorf("%s: %g must be greater than 0", key, *v)
	}
	return nil
}

func checkNonNegative(key string, v *float64) error {
	if v == nil {
		return nil
	}
	if err := checkFinite(key, v); err != nil {
		return err
	}
	if *v < 0 {
		return fmt.Errorf("%s: %g must not be negative", key, *v)
	}
	return nil
}

func checkByte(key string, v *int) error {
	if v == nil {
		return nil
	}
	if *v < 0 || *v > 255 {
		return fmt.Errorf("%s: %d out of range [0, 255]", key, *v)
	}
	return nil
}

// ParseHexColor accepts "#rrggbb" or "rrggbb". Alpha is not accepted: tinting
// blends color only and preserves the texture's own alpha.
func ParseHexColor(s string) (color.NRGBA, error) {
	h := strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(h) != 6 {
		return color.NRGBA{}, fmt.Errorf("%q is not a 6-digit hex color like #8ab4c8", s)
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return color.NRGBA{}, fmt.Errorf("%q is not a 6-digit hex color like #8ab4c8", s)
	}
	return color.NRGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 255}, nil
}

// Tuner applies validated Overrides to a Params.
//
// It exists as a separate type from Overrides so the expensive part of tinting
// — building a recolored copy of a texture bitmap — happens once at
// construction instead of once per layer per tile on the paint path.
type Tuner struct {
	overrides Overrides
	// tinted holds the recolored texture for each layer that asked for one.
	// It is keyed by layer rather than by texture, which is what makes
	// tinting `water` leave `rivers` alone even though the two share the
	// same source bitmap.
	tinted map[geojson.LayerType]image.Image
}

// NewTuner validates the overrides and precomputes any tinted textures.
//
// A nil or empty *Overrides yields a nil *Tuner, and a nil *Tuner's Apply is a
// no-op that performs no arithmetic at all. That is the golden-safety
// guarantee: with no config present, the parameters are DefaultParams verbatim.
func NewTuner(o *Overrides, textures map[geojson.LayerType]image.Image) (*Tuner, error) {
	if o == nil {
		return nil, nil
	}
	if err := o.Validate(); err != nil {
		return nil, err
	}

	t := &Tuner{overrides: *o}

	// Read the texture assignment back out of DefaultParams so the mapping
	// from layer to source bitmap cannot drift from the renderer's.
	styles := DefaultParams(ReferenceTileSize, 0, textures).Styles
	for name, lo := range o.Layers {
		if lo.Tint == nil || lo.Tint.Color == nil {
			continue
		}
		layer := geojson.LayerType(name)
		style, ok := styles[layer]
		if !ok || style.Texture == nil {
			// Validate already rejected unknown layers; a nil texture just
			// means this build has no bitmap for it, so there is nothing to
			// tint and nothing to complain about.
			continue
		}
		tint, err := ParseHexColor(*lo.Tint.Color)
		if err != nil {
			return nil, err // unreachable: Validate parsed it already
		}
		strength := 1.0
		if lo.Tint.Strength != nil {
			strength = *lo.Tint.Strength
		}
		if t.tinted == nil {
			t.tinted = make(map[geojson.LayerType]image.Image, len(o.Layers))
		}
		t.tinted[layer] = texture.TintTexture(style.Texture, tint, strength)
	}

	return t, nil
}

// Apply overwrites the parameters the config file set, in place.
//
// It must run on parameters expressed at the 256px reference size, before
// ApplyScale: config values are lengths on the ground, and scaling them is the
// hi-DPI path's job, not the user's.
func (t *Tuner) Apply(p *Params) {
	if t == nil || p == nil {
		return
	}

	if p.Scale != 0 && p.Scale != 1 {
		// Applying reference-size values on top of already-scaled ones would
		// silently halve them at @2x. Fail loudly in tests rather than ship a
		// subtly wrong tile.
		panic("watercolor: Tuner.Apply must run before ApplyScale")
	}

	d := t.overrides.Defaults
	setF32(&p.BlurSigma, d.BlurSigma)
	setF32(&p.AntialiasSigma, d.AntialiasSigma)
	setF64(&p.NoiseScale, d.NoiseScale)
	setF64(&p.NoiseStrength, d.NoiseStrength)
	if d.Threshold != nil {
		p.Threshold = uint8(*d.Threshold)
	}

	if len(t.overrides.Layers) == 0 {
		return
	}

	// Copy-on-write: DefaultParams hands out a fresh map, but callers may pass
	// a Params whose Styles they still hold, and a per-tile tuner must never
	// mutate a shared style.
	styles := make(map[geojson.LayerType]LayerStyle, len(p.Styles))
	maps.Copy(styles, p.Styles)

	for name, lo := range t.overrides.Layers {
		layer := geojson.LayerType(name)
		style, ok := styles[layer]
		if !ok {
			continue // Validate rejects these; be permissive if it was skipped
		}

		if lo.MaskThreshold != nil {
			v := uint8(*lo.MaskThreshold)
			style.MaskThreshold = &v
		}
		setF32(&style.MaskBlurSigma, lo.MaskBlurSigma)
		setF64(&style.MaskNoiseStrength, lo.MaskNoiseStrength)
		setF32(&style.ShadeSigma, lo.ShadeSigma)
		setF64(&style.ShadeStrength, lo.ShadeStrength)
		setF64(&style.EdgeStrength, lo.EdgeStrength)
		setF32(&style.EdgeSigma, lo.EdgeSigma)
		setF64(&style.EdgeGamma, lo.EdgeGamma)
		setF64(&style.NoiseMinDist, lo.NoiseMinDist)
		setF64(&style.NoiseMaxDist, lo.NoiseMaxDist)
		if lo.AdaptiveNoise != nil {
			style.AdaptiveNoise = *lo.AdaptiveNoise
		}
		if lo.InvertMask != nil {
			style.InvertMask = *lo.InvertMask
		}
		if tinted, ok := t.tinted[layer]; ok {
			style.Texture = tinted
		}

		styles[layer] = style
	}

	p.Styles = styles
}

func setF64(dst *float64, src *float64) {
	if src != nil {
		*dst = *src
	}
}

func setF32(dst *float32, src *float64) {
	if src != nil {
		*dst = float32(*src)
	}
}
