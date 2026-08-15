package cmd

import (
	"fmt"

	"github.com/spf13/viper"

	"github.com/cwbudde/watercolormap/internal/renderer"
)

// Config keys for the ocean layer. The paths point at the processed OSM water
// polygons from https://osmdata.openstreetmap.de/data/water-polygons.html;
// `just fetch-water-polygons` puts them where the example config expects.
const (
	oceanEnabledKey           = "ocean.enabled"
	oceanShapefileKey         = "ocean.shapefile"
	oceanSimplifiedKey        = "ocean.simplified-shapefile"
	oceanSimplifiedMaxZoomKey = "ocean.simplified-max-zoom"
)

// oceanConfig reads the ocean block from viper.
//
// Ocean rendering is opt-in: with no config the returned value is disabled and
// the pipeline renders exactly as it did before 4.10. `ocean.enabled: false`
// switches it off again while leaving the paths in place, which is what you want
// when comparing tiles with and without ocean.
//
// Paths are validated here rather than at first use so a typo stops the run
// before the first Overpass request instead of quietly producing tan oceans.
func oceanConfig() (renderer.OceanConfig, error) {
	if viper.IsSet(oceanEnabledKey) && !viper.GetBool(oceanEnabledKey) {
		return renderer.OceanConfig{}, nil
	}

	cfg := renderer.OceanConfig{
		FullPath:          viper.GetString(oceanShapefileKey),
		SimplifiedPath:    viper.GetString(oceanSimplifiedKey),
		SimplifiedMaxZoom: viper.GetInt(oceanSimplifiedMaxZoomKey),
	}

	if err := cfg.Validate(); err != nil {
		return renderer.OceanConfig{}, fmt.Errorf("invalid ocean configuration: %w", err)
	}

	return cfg, nil
}
