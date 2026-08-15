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
	if viper.IsSet(naturalEarthEnabledKey) && !viper.GetBool(naturalEarthEnabledKey) {
		return renderer.NaturalEarthConfig{}, nil
	}

	cfg := renderer.NaturalEarthConfig{
		Dir:     viper.GetString(naturalEarthDirKey),
		MaxZoom: viper.GetInt(naturalEarthMaxZoomKey),
	}

	if err := cfg.Validate(); err != nil {
		return renderer.NaturalEarthConfig{}, fmt.Errorf("invalid natural-earth configuration: %w", err)
	}

	return cfg, nil
}
