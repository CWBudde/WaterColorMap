package tile

import (
	"errors"
	"fmt"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/maptile"

	"github.com/cwbudde/watercolormap/internal/geo"
)

// Coords represents a tile coordinate in the Web Mercator tile system (z/x/y)
type Coords struct {
	Z uint32 // Zoom level (0-MaxZoom when validated; see Validate)
	X uint32 // X coordinate (column)
	Y uint32 // Y coordinate (row)
}

// String returns the tile coordinate as a string in format "z{zoom}_x{x}_y{y}"
func (c Coords) String() string {
	return fmt.Sprintf("z%d_x%d_y%d", c.Z, c.X, c.Y)
}

// Path returns the file path for this tile
func (c Coords) Path(extension string) string {
	return c.FileName("", extension)
}

// FileName returns the flat-layout file name for this tile:
// "z{z}_x{x}_y{y}{suffix}.{extension}", where suffix is "" or "@2x".
//
// The suffix sits between the coordinates and the extension, which is why Path
// could not simply be reused once the extension stopped always being "png".
// Both the pipeline (writing) and the tile server (reading, and building
// on-demand paths) go through here, so the two cannot drift.
func (c Coords) FileName(suffix, extension string) string {
	return fmt.Sprintf("%s%s.%s", c.String(), suffix, extension)
}

// Tile returns the maptile.Tile for this coordinate
func (c Coords) Tile() maptile.Tile {
	return maptile.New(c.X, c.Y, maptile.Zoom(c.Z))
}

// Bounds returns the geographic bounding box for this tile in WGS84 (EPSG:4326)
// Returns [minLon, minLat, maxLon, maxLat]
func (c Coords) Bounds() [4]float64 {
	tile := c.Tile()
	bound := tile.Bound()

	return [4]float64{
		bound.Min.Lon(), // minLon
		bound.Min.Lat(), // minLat
		bound.Max.Lon(), // maxLon
		bound.Max.Lat(), // maxLat
	}
}

// BoundsMercator returns the bounding box in Web Mercator projection (EPSG:3857)
// Returns [minX, minY, maxX, maxY] in meters
func (c Coords) BoundsMercator() [4]float64 {
	bounds := c.Bounds()
	minLon, minLat := bounds[0], bounds[1]
	maxLon, maxLat := bounds[2], bounds[3]

	// Convert WGS84 to Web Mercator
	minX, minY := geo.LonLatToMercator(minLon, minLat)
	maxX, maxY := geo.LonLatToMercator(maxLon, maxLat)

	return [4]float64{minX, minY, maxX, maxY}
}

// Center returns the center point of the tile in WGS84 (lon, lat)
func (c Coords) Center() (float64, float64) {
	bounds := c.Bounds()
	lon := (bounds[0] + bounds[2]) / 2.0
	lat := (bounds[1] + bounds[3]) / 2.0
	return lon, lat
}

// CenterMercator returns the center point in Web Mercator (x, y) in meters
func (c Coords) CenterMercator() (float64, float64) {
	lon, lat := c.Center()
	return geo.LonLatToMercator(lon, lat)
}

// NewCoords creates a new Coords from zoom, x, y values
func NewCoords(z, x, y uint32) Coords {
	return Coords{Z: z, X: x, Y: y}
}

// MaxZoom is the highest zoom level accepted by ParseCoords. This is an
// application resource policy, not a limit of the Web Mercator tile scheme:
// z=23 and beyond are perfectly well-defined grids, but this renderer has no
// data at that detail, so serving them would only burn fetch and render time.
const MaxZoom = 22

// ErrCoordsFormat indicates the input did not have the form "z{z}_x{x}_y{y}".
var ErrCoordsFormat = errors.New("invalid tile coordinate format")

// ErrCoordsOutOfRange indicates a well-formed coordinate that cannot exist:
// a zoom above MaxZoom, or an x/y outside the 2^z grid for its zoom.
var ErrCoordsOutOfRange = errors.New("tile coordinate out of range")

// Validate reports whether c addresses a tile that can actually exist.
func (c Coords) Validate() error {
	if c.Z > MaxZoom {
		return fmt.Errorf("%w: zoom %d exceeds max %d", ErrCoordsOutOfRange, c.Z, MaxZoom)
	}
	// Safe: Z <= MaxZoom (22), so the shift cannot overflow uint32.
	limit := uint32(1) << c.Z
	if c.X >= limit || c.Y >= limit {
		return fmt.Errorf("%w: x=%d y=%d outside 0..%d at zoom %d", ErrCoordsOutOfRange, c.X, c.Y, limit-1, c.Z)
	}
	return nil
}

// ParseCoords parses a tile string like "z13_x4297_y2754" into Coords.
//
// The result is validated: coordinates that cannot exist are rejected with
// ErrCoordsOutOfRange rather than being handed to the render pipeline, where
// they would trigger an unbounded upstream fetch for a tile nobody can see.
// Malformed input yields ErrCoordsFormat.
func ParseCoords(s string) (Coords, error) {
	var c Coords
	if _, err := fmt.Sscanf(s, "z%d_x%d_y%d", &c.Z, &c.X, &c.Y); err != nil {
		return Coords{}, fmt.Errorf("%w: %s", ErrCoordsFormat, s)
	}
	// Sscanf stops at the last verb and ignores whatever follows, so
	// "z13_x1_y2JUNK" parses cleanly. Round-tripping rejects the remainder,
	// and also rejects zero-padded or "+"-prefixed numbers that would
	// otherwise alias to the same tile under a different cache key.
	if c.String() != s {
		return Coords{}, fmt.Errorf("%w: %s", ErrCoordsFormat, s)
	}
	if err := c.Validate(); err != nil {
		return Coords{}, err
	}
	return c, nil
}

// TileRange represents a range of tiles to render
type TileRange struct {
	MinZ, MaxZ uint32 // Zoom range
	MinX, MaxX uint32 // X range
	MinY, MaxY uint32 // Y range
}

// ForEach calls the given function for each tile in the range
func (r TileRange) ForEach(fn func(Coords)) {
	for z := r.MinZ; z <= r.MaxZ; z++ {
		for x := r.MinX; x <= r.MaxX; x++ {
			for y := r.MinY; y <= r.MaxY; y++ {
				fn(NewCoords(z, x, y))
			}
		}
	}
}

// Count returns the total number of tiles in this range
func (r TileRange) Count() int {
	count := 0
	for z := r.MinZ; z <= r.MaxZ; z++ {
		xCount := r.MaxX - r.MinX + 1
		yCount := r.MaxY - r.MinY + 1
		count += int(xCount * yCount)
	}
	return count
}

// TileRangeFromBounds creates a TileRange covering a geographic bounding box
// bounds: [minLon, minLat, maxLon, maxLat] in WGS84
// NOTE: This function is deprecated for multi-zoom ranges. Use TilesInBBox instead,
// as this function calculates X/Y only at minZ and applies it to all zoom levels.
func TileRangeFromBounds(minZ, maxZ uint32, bounds [4]float64) TileRange {
	minLon, minLat, maxLon, maxLat := bounds[0], bounds[1], bounds[2], bounds[3]

	// Calculate tile coordinates for the bounds at each zoom level
	// For simplicity, we'll use the first zoom level
	minPoint := orb.Point{minLon, minLat}
	maxPoint := orb.Point{maxLon, maxLat}

	minTile := maptile.At(minPoint, maptile.Zoom(minZ))
	maxTile := maptile.At(maxPoint, maptile.Zoom(minZ))

	// Ensure min/max are correctly ordered
	minX, maxX := minTile.X, maxTile.X
	if minX > maxX {
		minX, maxX = maxX, minX
	}

	minY, maxY := minTile.Y, maxTile.Y
	if minY > maxY {
		minY, maxY = maxY, minY
	}

	return TileRange{
		MinZ: minZ,
		MaxZ: maxZ,
		MinX: minX,
		MaxX: maxX,
		MinY: minY,
		MaxY: maxY,
	}
}

// TilesInBBox returns all tile coordinates within a bounding box across a zoom range.
// bbox: [minLon, minLat, maxLon, maxLat] in WGS84
// Calculates correct tile coordinates at each zoom level independently.
func TilesInBBox(bbox [4]float64, zoomMin, zoomMax int) []Coords {
	minLon, minLat, maxLon, maxLat := bbox[0], bbox[1], bbox[2], bbox[3]

	// Pre-allocate with estimated capacity
	estimatedCount := TileCount(bbox, zoomMin, zoomMax)
	tiles := make([]Coords, 0, estimatedCount)

	minPoint := orb.Point{minLon, minLat}
	maxPoint := orb.Point{maxLon, maxLat}

	for z := zoomMin; z <= zoomMax; z++ {
		zoom := maptile.Zoom(z)

		// Get tile coordinates at this zoom level
		minTile := maptile.At(minPoint, zoom)
		maxTile := maptile.At(maxPoint, zoom)

		// Ensure min/max are correctly ordered (Y is inverted in TMS)
		minX, maxX := minTile.X, maxTile.X
		if minX > maxX {
			minX, maxX = maxX, minX
		}

		minY, maxY := minTile.Y, maxTile.Y
		if minY > maxY {
			minY, maxY = maxY, minY
		}

		// Generate all tiles at this zoom level
		for x := minX; x <= maxX; x++ {
			for y := minY; y <= maxY; y++ {
				tiles = append(tiles, NewCoords(uint32(z), x, y))
			}
		}
	}

	return tiles
}

// TileCount returns the number of tiles in a bounding box across a zoom range.
// This is useful for progress estimation without allocating the full tile list.
func TileCount(bbox [4]float64, zoomMin, zoomMax int) int {
	minLon, minLat, maxLon, maxLat := bbox[0], bbox[1], bbox[2], bbox[3]
	minPoint := orb.Point{minLon, minLat}
	maxPoint := orb.Point{maxLon, maxLat}

	count := 0
	for z := zoomMin; z <= zoomMax; z++ {
		zoom := maptile.Zoom(z)

		minTile := maptile.At(minPoint, zoom)
		maxTile := maptile.At(maxPoint, zoom)

		minX, maxX := minTile.X, maxTile.X
		if minX > maxX {
			minX, maxX = maxX, minX
		}

		minY, maxY := minTile.Y, maxTile.Y
		if minY > maxY {
			minY, maxY = maxY, minY
		}

		xCount := int(maxX - minX + 1)
		yCount := int(maxY - minY + 1)
		count += xCount * yCount
	}

	return count
}
