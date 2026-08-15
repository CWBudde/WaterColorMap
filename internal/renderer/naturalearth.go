package renderer

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/cwbudde/watercolormap/internal/geojson"
)

// DefaultNaturalEarthMaxZoom is the last zoom served from Natural Earth.
//
// Above it OSM is the better source: by z6 a tile is a region rather than a
// continent, and Natural Earth's generalisation starts to show. Below it OSM is
// the wrong source twice over — a single z2 tile would ask Overpass for a
// quarter of the planet, and ungeneralised coastline at ~1px per 50km is detail
// nobody can see. See docs/zoom-levels.md.
const DefaultNaturalEarthMaxZoom = 5

// DefaultNaturalEarthMidScaleMinZoom is the first zoom served from the 50m
// datasets; below it the 110m set is used.
//
// This is the same trade the ocean pass makes at DefaultSimplifiedMaxZoom, one
// scale step down: 110m is visibly coarse by z3, and 50m is detail nobody can
// resolve at z0-2.
const DefaultNaturalEarthMidScaleMinZoom = 3

// naturalEarthDatasets maps a render layer to the Natural Earth dataset that
// feeds it, without the scale prefix.
//
// Only these three layers exist below z6. Every other layer — roads, highways,
// railroads, buildings, civic, parks — resolves to "" and therefore renders
// absent, which is the whole of "generalized rendering for low zooms": at world
// scale the map is coastline, lakes and rivers, and nothing else.
//
// The layers chosen are ones the pipeline already has, so nothing downstream
// changes: ocean is folded into water before masking (foldOceanIntoWater), and
// water and rivers keep their existing masks, textures and composite order.
var naturalEarthDatasets = map[geojson.LayerType]string{
	geojson.LayerOcean:  "ocean",
	geojson.LayerWater:  "lakes",
	geojson.LayerRivers: "rivers_lake_centerlines",
}

// NaturalEarthConfig points the low-zoom render passes at the Natural Earth
// shapefiles from https://www.naturalearthdata.com/.
//
// The zero value is disabled, which is what keeps every tile byte-identical to
// a build without it.
type NaturalEarthConfig struct {
	// Dir holds the unpacked ne_*m_* shapefiles. `just fetch-natural-earth`
	// puts them all in one flat directory, which is how the upstream zips
	// extract.
	Dir string

	// MaxZoom is the last zoom served from Natural Earth.
	// Zero means DefaultNaturalEarthMaxZoom.
	MaxZoom int
}

// Enabled reports whether a data directory is configured. A disabled config
// makes the low-zoom passes a no-op rather than an error: like ocean data,
// Natural Earth is optional.
func (c NaturalEarthConfig) Enabled() bool {
	return c.Dir != ""
}

// EffectiveMaxZoom is MaxZoom with the default applied — the last zoom actually
// served from Natural Earth. Exported so a caller can report the boundary
// rather than probe it one zoom at a time.
func (c NaturalEarthConfig) EffectiveMaxZoom() int {
	if c.MaxZoom == 0 {
		return DefaultNaturalEarthMaxZoom
	}
	return c.MaxZoom
}

// CoversZoom reports whether this zoom is served from Natural Earth instead of
// from Overpass. It is the single predicate the renderer and the pipeline both
// branch on, so the two cannot disagree about where a tile's data comes from.
func (c NaturalEarthConfig) CoversZoom(zoom int) bool {
	return c.Enabled() && zoom <= c.EffectiveMaxZoom()
}

// scaleForZoom returns the Natural Earth scale prefix for this zoom.
func (c NaturalEarthConfig) scaleForZoom(zoom int) string {
	if zoom < DefaultNaturalEarthMidScaleMinZoom {
		return "110m"
	}
	return "50m"
}

// ShapefileForLayer returns the shapefile backing this layer at this zoom, or
// "" when Natural Earth does not cover the zoom, does not carry the layer, or
// has not been downloaded.
//
// A missing file yields "" rather than an error, deliberately. The datasets are
// independent: a missing lakes file should cost the lakes, not the coastline.
// This mirrors OceanConfig's stance that a wrong-detail coastline beats an
// inverted one — Validate is what turns a mistyped directory into a startup
// failure.
//
// The path is made absolute. Mapnik resolves a relative datasource path against
// the directory of the XML it was loaded from, and LoadXML writes that XML to a
// temp file, so a relative path here would be looked up next to /tmp and the
// layer would silently vanish.
func (c NaturalEarthConfig) ShapefileForLayer(layer geojson.LayerType, zoom int) string {
	if !c.CoversZoom(zoom) {
		return ""
	}

	dataset, ok := naturalEarthDatasets[layer]
	if !ok {
		return ""
	}

	path := filepath.Join(c.Dir, fmt.Sprintf("ne_%s_%s.shp", c.scaleForZoom(zoom), dataset))
	if _, err := os.Stat(path); err != nil {
		return ""
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}

// Validate checks that the configured directory exists and holds at least one
// usable dataset. It is meant to run at startup: a mistyped path should stop
// the run before the first tile, not surface as a silently empty world several
// thousand tiles in.
func (c NaturalEarthConfig) Validate() error {
	if !c.Enabled() {
		return nil
	}

	if c.MaxZoom < 0 {
		return fmt.Errorf("natural-earth max-zoom must not be negative, got %d", c.MaxZoom)
	}

	info, err := os.Stat(c.Dir)
	if err != nil {
		return fmt.Errorf("natural-earth dir %q is not readable: %w (run `just fetch-natural-earth`)", c.Dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("natural-earth dir %q is not a directory", c.Dir)
	}

	// Requiring every dataset would make a partial download fatal, and requiring
	// none would let an empty directory through as a working configuration. One
	// dataset at one scale is the line: it proves the path points at a Natural
	// Earth download rather than at nothing.
	for _, layer := range []geojson.LayerType{geojson.LayerOcean, geojson.LayerWater, geojson.LayerRivers} {
		for _, zoom := range []int{0, DefaultNaturalEarthMidScaleMinZoom} {
			if c.ShapefileForLayer(layer, zoom) != "" {
				return nil
			}
		}
	}

	return fmt.Errorf("natural-earth dir %q holds no ne_*.shp datasets (run `just fetch-natural-earth`)", c.Dir)
}
