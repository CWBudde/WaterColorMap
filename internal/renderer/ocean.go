package renderer

import (
	"fmt"
	"os"
	"path/filepath"
)

// DefaultSimplifiedMaxZoom is the highest zoom served from the simplified water
// polygons. Above it the simplified coastline is visibly coarse, below it the
// full set costs I/O for detail nobody can see.
const DefaultSimplifiedMaxZoom = 9

// OceanConfig points the ocean render pass at the processed OSM water polygons
// from https://osmdata.openstreetmap.de/data/water-polygons.html.
//
// OSM itself has no ocean polygons — the sea is the absence of land — so the
// open sea has to come from this external dataset. The zero value is disabled,
// which is what keeps every inland tile byte-identical to a build without it.
type OceanConfig struct {
	// FullPath is the .shp of water-polygons-split-3857, used from
	// SimplifiedMaxZoom+1 upwards.
	FullPath string

	// SimplifiedPath is the .shp of simplified-water-polygons-split-3857, used
	// up to and including SimplifiedMaxZoom.
	SimplifiedPath string

	// SimplifiedMaxZoom is the last zoom served from SimplifiedPath.
	// Zero means DefaultSimplifiedMaxZoom.
	SimplifiedMaxZoom int
}

// Enabled reports whether any shapefile is configured. A disabled config makes
// the ocean pass a no-op rather than an error: ocean data is optional.
func (c OceanConfig) Enabled() bool {
	return c.FullPath != "" || c.SimplifiedPath != ""
}

func (c OceanConfig) simplifiedMaxZoom() int {
	if c.SimplifiedMaxZoom == 0 {
		return DefaultSimplifiedMaxZoom
	}
	return c.SimplifiedMaxZoom
}

// ShapefileForZoom returns the shapefile to render at this zoom, or "" when
// ocean rendering is off. Either dataset stands in for the other when only one
// is configured — a wrong-detail coastline beats an inverted one.
//
// The path is made absolute. Mapnik resolves a relative datasource path against
// the directory of the XML it was loaded from, and LoadXML writes that XML to a
// temp file, so a relative path here would be looked up next to /tmp and the
// ocean would silently vanish.
func (c OceanConfig) ShapefileForZoom(zoom int) string {
	path := c.shapefileForZoom(zoom)
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}

func (c OceanConfig) shapefileForZoom(zoom int) string {
	if zoom <= c.simplifiedMaxZoom() {
		if c.SimplifiedPath != "" {
			return c.SimplifiedPath
		}
		return c.FullPath
	}
	if c.FullPath != "" {
		return c.FullPath
	}
	return c.SimplifiedPath
}

// Validate checks that the configured shapefiles exist. It is meant to run at
// startup: a mistyped path should stop the run before the first Overpass
// request, not surface as a silently oceanless tile several thousand tiles in.
func (c OceanConfig) Validate() error {
	if c.SimplifiedMaxZoom < 0 {
		return fmt.Errorf("ocean simplified-max-zoom must not be negative, got %d", c.SimplifiedMaxZoom)
	}

	for _, p := range []struct {
		path string
		key  string
	}{
		{c.FullPath, "shapefile"},
		{c.SimplifiedPath, "simplified-shapefile"},
	} {
		if p.path == "" {
			continue
		}
		if _, err := os.Stat(p.path); err != nil {
			return fmt.Errorf("ocean %s %q is not readable: %w (run `just fetch-water-polygons`)", p.key, p.path, err)
		}
	}

	return nil
}
