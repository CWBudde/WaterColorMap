package renderer

// #cgo LDFLAGS: -lmapnik
// #cgo CXXFLAGS: -std=c++14
import "C"

import (
	"fmt"
	"image"
	"image/color"
	"os"

	mapnik "github.com/omniscale/go-mapnik/v2"

	"github.com/cwbudde/watercolormap/internal/geo"
	"github.com/cwbudde/watercolormap/internal/types"
)

// MapnikRenderer wraps Mapnik for tile rendering
type MapnikRenderer struct {
	mapObject *mapnik.Map
	// scaleFactor is Mapnik's hi-DPI multiplier. Zero means "unset"; go-mapnik
	// normalises that to 1.0, so a renderer that never calls SetScaleFactor
	// issues exactly the same Mapnik call it always has.
	scaleFactor float64
	// bufferSize is Mapnik's buffer around the render bounds, in device pixels.
	// It is kept here rather than only on the map object because resetMapObject
	// replaces that object between every layer pass; a value set once at
	// construction would be silently dropped by the first LoadStyle/LoadXML.
	bufferSize int
	tileSize   int
}

func (r *MapnikRenderer) resetMapObject() {
	if r.mapObject != nil {
		r.mapObject.Free()
	}
	r.mapObject = mapnik.NewSized(r.tileSize, r.tileSize)
	// The new map object starts with Mapnik's default buffer, so anything the
	// caller configured has to be re-applied here or it only ever applies to
	// the render before the first style load — which, in the multi-pass
	// renderer, is no render at all.
	if r.bufferSize > 0 {
		r.mapObject.SetBufferSize(r.bufferSize)
	}
}

// NewMapnikRenderer creates a new Mapnik renderer
func NewMapnikRenderer(styleFile string, tileSize int) (*MapnikRenderer, error) {
	// Initialize Mapnik (must be called once)
	if err := mapnik.RegisterDatasources("/usr/lib/mapnik/3.1/input"); err != nil {
		return nil, fmt.Errorf("failed to register datasources: %w", err)
	}

	// Create map object with specified size
	m := mapnik.NewSized(tileSize, tileSize)

	// Load style from XML file
	if styleFile != "" {
		if err := m.Load(styleFile); err != nil {
			return nil, fmt.Errorf("failed to load Mapnik style: %w", err)
		}
	}

	return &MapnikRenderer{
		mapObject: m,
		tileSize:  tileSize,
	}, nil
}

// RenderTile renders a tile from OSM data
func (r *MapnikRenderer) RenderTile(tile types.TileCoordinate, data *types.TileData) (image.Image, error) {
	// Calculate tile bounds
	bounds := types.TileToBounds(tile)

	// Set map projection to Web Mercator (EPSG:3857)
	r.mapObject.SetSRS("+proj=merc +a=6378137 +b=6378137 +lat_ts=0.0 +lon_0=0.0 +x_0=0.0 +y_0=0 +k=1.0 +units=m +nadgrids=@null +no_defs +over")

	// Convert lat/lon bounds to Web Mercator coordinates
	minX, minY := geo.LonLatToMercator(bounds.MinLon, bounds.MinLat)
	maxX, maxY := geo.LonLatToMercator(bounds.MaxLon, bounds.MaxLat)

	// Set the map extent (bounding box)
	r.mapObject.ZoomTo(minX, minY, maxX, maxY)

	// Render to image (returns *image.NRGBA directly)
	img, err := r.mapObject.RenderImage(mapnik.RenderOpts{
		Format:      "png32",
		ScaleFactor: r.scaleFactor,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to render tile: %w", err)
	}

	return img, nil
}

// RenderToFile renders a tile directly to a file
func (r *MapnikRenderer) RenderToFile(tile types.TileCoordinate, outputPath string) error {
	// Calculate tile bounds
	bounds := types.TileToBounds(tile)

	// Set map projection
	r.mapObject.SetSRS("+proj=merc +a=6378137 +b=6378137 +lat_ts=0.0 +lon_0=0.0 +x_0=0.0 +y_0=0 +k=1.0 +units=m +nadgrids=@null +no_defs +over")

	// Convert to Web Mercator
	minX, minY := geo.LonLatToMercator(bounds.MinLon, bounds.MinLat)
	maxX, maxY := geo.LonLatToMercator(bounds.MaxLon, bounds.MaxLat)

	// Set extent
	r.mapObject.ZoomTo(minX, minY, maxX, maxY)

	// Render directly to file
	if err := r.mapObject.RenderToFile(mapnik.RenderOpts{
		Format:      "png32",
		ScaleFactor: r.scaleFactor,
	}, outputPath); err != nil {
		return fmt.Errorf("failed to render to file: %w", err)
	}

	return nil
}

// Close releases Mapnik resources
func (r *MapnikRenderer) Close() error {
	if r.mapObject != nil {
		r.mapObject.Free()
		r.mapObject = nil
	}
	return nil
}

// RenderCurrentToFile renders using the current map state (SRS + extent already set).
func (r *MapnikRenderer) RenderCurrentToFile(outputPath string) error {
	if err := r.mapObject.RenderToFile(mapnik.RenderOpts{
		Format:      "png32",
		ScaleFactor: r.scaleFactor,
	}, outputPath); err != nil {
		return fmt.Errorf("failed to render to file: %w", err)
	}
	return nil
}

// SetBackgroundColor sets the map background color (hex string like "#f8f4e8")
func (r *MapnikRenderer) SetBackgroundColor(hexColor string) error {
	// Parse hex color string to color.NRGBA
	c, err := parseHexColor(hexColor)
	if err != nil {
		return fmt.Errorf("invalid color format: %w", err)
	}
	r.mapObject.SetBackgroundColor(c)
	return nil
}

// parseHexColor converts hex color string to color.NRGBA
func parseHexColor(s string) (color.NRGBA, error) {
	// Remove # prefix if present
	if len(s) > 0 && s[0] == '#' {
		s = s[1:]
	}

	// Parse RGB or RGBA
	var r, g, b, a uint8 = 0, 0, 0, 255

	switch len(s) {
	case 6: // RGB
		_, err := fmt.Sscanf(s, "%02x%02x%02x", &r, &g, &b)
		if err != nil {
			return color.NRGBA{}, err
		}
	case 8: // RGBA
		_, err := fmt.Sscanf(s, "%02x%02x%02x%02x", &r, &g, &b, &a)
		if err != nil {
			return color.NRGBA{}, err
		}
	default:
		return color.NRGBA{}, fmt.Errorf("invalid hex color length: %d", len(s))
	}

	return color.NRGBA{R: r, G: g, B: b, A: a}, nil
}

// LoadStyle loads a Mapnik XML style file
func (r *MapnikRenderer) LoadStyle(styleFile string) error {
	// Mapnik map instances can retain layers/styles across loads depending on the binding.
	// Reset the map object to ensure isolation between multi-pass layer renders.
	r.resetMapObject()
	if err := r.mapObject.Load(styleFile); err != nil {
		return fmt.Errorf("failed to load style: %w", err)
	}
	return nil
}

// LoadXML loads a Mapnik style from XML string
// It writes the XML to a temporary file and loads it
func (r *MapnikRenderer) LoadXML(xmlString string) error {
	// Reset the map object to avoid layer/style accumulation across multiple loads.
	r.resetMapObject()

	// Create temporary file for the XML
	tmpFile, err := os.CreateTemp("", "mapnik-style-*.xml")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() {
		os.Remove(tmpPath) // nolint:errcheck // Best-effort cleanup
	}()

	// Write XML to temp file
	if _, err := tmpFile.WriteString(xmlString); err != nil {
		tmpFile.Close() // nolint:errcheck // Already returning an error
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	// Load the style file
	if err := r.mapObject.Load(tmpPath); err != nil {
		return fmt.Errorf("failed to load XML: %w", err)
	}
	return nil
}

// SetBounds sets the map bounds in Web Mercator coordinates (minX, minY, maxX, maxY)
func (r *MapnikRenderer) SetBounds(minX, minY, maxX, maxY float64) error {
	// Set map projection to Web Mercator (EPSG:3857)
	r.mapObject.SetSRS("+proj=merc +a=6378137 +b=6378137 +lat_ts=0.0 +lon_0=0.0 +x_0=0.0 +y_0=0 +k=1.0 +units=m +nadgrids=@null +no_defs +over")

	// Set the map extent (bounding box)
	r.mapObject.ZoomTo(minX, minY, maxX, maxY)
	return nil
}

// SetBufferSize sets the buffer size around the tile (for label placement and
// for features whose geometry crosses the render bounds).
//
// The value is remembered on the renderer, not just pushed to the current map
// object, because LoadStyle/LoadXML reset that object between every layer pass.
func (r *MapnikRenderer) SetBufferSize(pixels int) {
	if pixels < 0 {
		return
	}
	r.bufferSize = pixels
	r.mapObject.SetBufferSize(pixels)
}

// SetScaleFactor sets Mapnik's hi-DPI scale factor, i.e. how many device pixels
// one "reference" pixel of the stylesheet is worth. Callers pass
// watercolor.ScaleForTileSize(baseTileSize); note that the renderer's own
// tileSize is the padded metatile size and must not be used to derive this.
//
// It does two things, and the second one matters more:
//
//  1. stroke-width, font sizes and marker sizes in assets/styles/layers/*.xml are
//     fixed device-pixel values, so without it a 512px @2x tile draws roads at the
//     same pixel width as the 256px tile covering the same ground.
//  2. Mapnik multiplies the scale denominator by this factor before evaluating
//     Min/MaxScaleDenominator filters. An @2x tile has half the denominator of the
//     @1x tile over the same extent, so without the correction it resolves a
//     different detail tier and can draw road classes the @1x tile omits entirely.
//
// Values <= 0 are ignored, leaving the field at its zero value, which go-mapnik
// normalises to 1.0.
func (r *MapnikRenderer) SetScaleFactor(s float64) {
	if s <= 0 {
		return
	}
	r.scaleFactor = s
}
