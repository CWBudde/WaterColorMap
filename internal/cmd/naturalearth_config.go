package cmd

import (
	"fmt"

	"github.com/spf13/viper"

	"github.com/cwbudde/watercolormap/internal/renderer"
)

// Config keys for the Natural Earth low-zoom layers. The directory holds the
// shapefiles from https://www.naturalearthdata.com/; `just fetch-natural-earth`
// puts them where the example config expects.
const (
	naturalEarthEnabledKey = "natural-earth.enabled"
	naturalEarthDirKey     = "natural-earth.dir"
	naturalEarthMaxZoomKey = "natural-earth.max-zoom"
)

// naturalEarthConfig reads the natural-earth block from viper.
//
// Like ocean rendering, this is opt-in: with no config the returned value is
// disabled and every zoom goes through Overpass exactly as before.
// `natural-earth.enabled: false` switches it off again while leaving the path in
// place, which is what you want when comparing a low-zoom tile with and without
// it.
//
// The path is validated here rather than at first use so a typo stops the run
// before the first tile instead of quietly producing an empty world.
func naturalEarthConfig() (renderer.NaturalEarthConfig, error) {
	explicit := viper.IsSet(naturalEarthEnabledKey)
	if explicit && !viper.GetBool(naturalEarthEnabledKey) {
		return renderer.NaturalEarthConfig{}, nil
	}

	cfg := renderer.NaturalEarthConfig{
		Dir:     viper.GetString(naturalEarthDirKey),
		MaxZoom: viper.GetInt(naturalEarthMaxZoomKey),
	}

	// An explicit `enabled: true` with no directory is a configuration error,
	// not a disabled config. Treating it as disabled would send z0-5 to
	// Overpass — continent-scale queries — behind a setting that says the
	// opposite. Only the *absent* key means "off by default".
	if explicit && cfg.Dir == "" {
		return renderer.NaturalEarthConfig{}, fmt.Errorf(
			"invalid natural-earth configuration: %s is true but %s is empty (run `just fetch-natural-earth`)",
			naturalEarthEnabledKey, naturalEarthDirKey)
	}

	if err := cfg.Validate(); err != nil {
		return renderer.NaturalEarthConfig{}, fmt.Errorf("invalid natural-earth configuration: %w", err)
	}

	return cfg, nil
}
