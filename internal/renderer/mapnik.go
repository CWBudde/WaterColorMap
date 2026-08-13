package renderer

// #cgo LDFLAGS: -lmapnik
// #cgo CXXFLAGS: -std=c++14
import "C"

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"os"

	"github.com/cwbudde/watercolormap/internal/types"
	mapnik "github.com/omniscale/go-mapnik/v2"
)

// MapnikRenderer wraps Mapnik for tile rendering
type MapnikRenderer struct {
	mapObject *mapnik.Map
	tileSize  int
}

func (r *MapnikRenderer) resetMapObject() {
	if r.mapObject != nil {
		r.mapObject.Free()
	}
	r.mapObject = mapnik.NewSized(r.tileSize, r.tileSize)
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
	minX, minY := latLonToWebMercator(bounds.MinLat, bounds.MinLon)
	maxX, maxY := latLonToWebMercator(bounds.MaxLat, bounds.MaxLon)

	// Set the map extent (bounding box)
	r.mapObject.ZoomTo(minX, minY, maxX, maxY)

	// Render to image (returns *image.NRGBA directly)
	img, err := r.mapObject.RenderImage(mapnik.RenderOpts{
		Format: "png32",
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
	minX, minY := latLonToWebMercator(bounds.MinLat, bounds.MinLon)
	maxX, maxY := latLonToWebMercator(bounds.MaxLat, bounds.MaxLon)

	// Set extent
	r.mapObject.ZoomTo(minX, minY, maxX, maxY)

	// Render directly to file
	if err := r.mapObject.RenderToFile(mapnik.RenderOpts{
		Format: "png32",
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

// latLonToWebMercator converts WGS84 lat/lon to Web Mercator (EPSG:3857) coordinates
func latLonToWebMercator(lat, lon float64) (float64, float64) {
	const earthRadius = 6378137.0 // meters

	// Longitude: simple linear conversion
	x := lon * earthRadius * (3.14159265359 / 180.0)

	// Latitude: Mercator projection formula
	latRad := lat * math.Pi / 180.0
	y := earthRadius * math.Log(math.Tan(math.Pi/4.0+latRad/2.0))

	return x, y
}

// RenderCurrentToFile renders using the current map state (SRS + extent already set).
func (r *MapnikRenderer) RenderCurrentToFile(outputPath string) error {
	if err := r.mapObject.RenderToFile(mapnik.RenderOpts{
		Format: "png32",
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

// SetBufferSize sets the buffer size around the tile (for label placement, etc.)
func (r *MapnikRenderer) SetBufferSize(pixels int) {
	r.mapObject.SetBufferSize(pixels)
}
