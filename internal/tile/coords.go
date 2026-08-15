package tile

import (
	"errors"
	"fmt"
	"iter"

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

// WorldBounds is the WGS84 extent addressable by the Web Mercator tile grid.
// Longitudes are the full domain; latitudes stop at the Mercator cutoff, beyond
// which the projection runs to infinity.
const (
	MinLon = -180.0
	MaxLon = 180.0
	MinLat = -85.05112878
	MaxLat = 85.05112878
)

// clampToWorld pulls a point into the addressable Web Mercator extent.
//
// maptile.At snaps latitude to the Mercator cutoff but leaves longitude alone,
// so a point at lon=-181 wraps around to a large unsigned X that then looks like
// the *easternmost* column after the grid clamp below. Clipping the geographic
// coordinate first keeps an out-of-world corner attached to the edge it is
// actually nearest to. Callers that must reject such input outright (the CLI's
// bbox parser) validate before ever getting here.
func clampToWorld(p orb.Point) orb.Point {
	lon, lat := p[0], p[1]
	switch {
	case lon < MinLon:
		lon = MinLon
	case lon > MaxLon:
		lon = MaxLon
	}
	switch {
	case lat < MinLat:
		lat = MinLat
	case lat > MaxLat:
		lat = MaxLat
	}
	return orb.Point{lon, lat}
}

// BBoxTileBounds is the inclusive x/y range a bounding box covers at one zoom
// level.
type BBoxTileBounds struct {
	MinX uint32
	MaxX uint32
	MinY uint32
	MaxY uint32
}

// Contains reports whether a tile column and row lie inside the range.
func (b BBoxTileBounds) Contains(x, y uint32) bool {
	return x >= b.MinX && x <= b.MaxX && y >= b.MinY && y <= b.MaxY
}

// tileIndexRange returns the inclusive x/y tile index range covering two corner
// points at one zoom, ordered and clamped to the 2^z grid.
//
// The clamp is what makes a whole-world bbox usable. maptile.At maps lon=+180 to
// x=2^z — one column past the last real tile — and lat=-85.05 likewise to
// y=2^z, so `--bbox -180,-85,180,85` enumerated z0_x1_y0 and z1_x2_y0: tiles
// that Coords.Validate rejects and that no upstream can answer. Everything that
// answers a question about a bbox goes through here — enumeration, counting and
// the membership test alike — so the three cannot disagree.
func tileIndexRange(minPoint, maxPoint orb.Point, z int) BBoxTileBounds {
	zoom := maptile.Zoom(z)
	minTile := maptile.At(clampToWorld(minPoint), zoom)
	maxTile := maptile.At(clampToWorld(maxPoint), zoom)

	// Ensure min/max are correctly ordered (Y grows southwards, so the
	// northern corner yields the smaller row).
	minX, maxX := minTile.X, maxTile.X
	if minX > maxX {
		minX, maxX = maxX, minX
	}

	minY, maxY := minTile.Y, maxTile.Y
	if minY > maxY {
		minY, maxY = maxY, minY
	}

	// Safe: z is bounded by MaxZoom (22) at every call site, so the shift
	// cannot overflow uint32.
	last := uint32(1)<<uint32(z) - 1
	if maxX > last {
		maxX = last
	}
	if maxY > last {
		maxY = last
	}
	if minX > last {
		minX = last
	}
	if minY > last {
		minY = last
	}

	return BBoxTileBounds{MinX: minX, MaxX: maxX, MinY: minY, MaxY: maxY}
}

// BBoxTileBoundsAt returns the inclusive tile range a bounding box covers at one
// zoom level.
//
// It exists so that "is this tile inside the box" can be answered without
// enumerating the box. A caller that already holds the tiles it cares about —
// `purge` holds the ones that exist — would otherwise pay 4^z for a question
// about a handful of tiles, and at z22 that is not a slow answer but no answer
// at all. Because it shares tileIndexRange with TilesInBBox, the membership test
// accepts exactly the tiles enumeration would have produced.
func BBoxTileBoundsAt(bbox [4]float64, z int) BBoxTileBounds {
	return tileIndexRange(orb.Point{bbox[0], bbox[1]}, orb.Point{bbox[2], bbox[3]}, z)
}

// TilesInBBox returns all tile coordinates within a bounding box across a zoom range.
// bbox: [minLon, minLat, maxLon, maxLat] in WGS84
// Calculates correct tile coordinates at each zoom level independently.
//
// It materialises the whole list, which for a country-sized bbox is hundreds of
// thousands of entries. Callers that only walk the list once should take
// TilesInBBoxSeq instead; this stays for the callers that genuinely need random
// access (band grouping, tests).
func TilesInBBox(bbox [4]float64, zoomMin, zoomMax int) []Coords {
	tiles := make([]Coords, 0, TileCount(bbox, zoomMin, zoomMax))
	for c := range TilesInBBoxSeq(bbox, zoomMin, zoomMax) {
		tiles = append(tiles, c)
	}
	return tiles
}

// TilesInBBoxSeq yields the same coordinates as TilesInBBox, in the same order,
// without holding them all in memory.
//
// The order is part of the contract: zoom, then x, then y. A batch run
// checkpoints by position in this sequence, so changing the order would
// invalidate every checkpoint written by an older build — and, worse, would do
// so silently, since a resumed run would skip a prefix of a different set of
// tiles.
//
// It derives its per-zoom index range from BBoxTileBoundsAt, the same helper
// TilesInBBox, TileCount and the purge membership test use, so the streaming
// path inherits the world clamp instead of re-deriving — and possibly
// disagreeing about — the grid.
func TilesInBBoxSeq(bbox [4]float64, zoomMin, zoomMax int) iter.Seq[Coords] {
	return func(yield func(Coords) bool) {
		for z := zoomMin; z <= zoomMax; z++ {
			b := BBoxTileBoundsAt(bbox, z)

			// Generate all tiles at this zoom level
			for x := b.MinX; x <= b.MaxX; x++ {
				for y := b.MinY; y <= b.MaxY; y++ {
					if !yield(NewCoords(uint32(z), x, y)) {
						return
					}
				}
			}
		}
	}
}

// TileCount returns the number of tiles in a bounding box across a zoom range.
// This is useful for progress estimation without allocating the full tile list.
func TileCount(bbox [4]float64, zoomMin, zoomMax int) int {
	count := 0
	for z := zoomMin; z <= zoomMax; z++ {
		b := BBoxTileBoundsAt(bbox, z)

		xCount := int(b.MaxX - b.MinX + 1)
		yCount := int(b.MaxY - b.MinY + 1)
		count += xCount * yCount
	}

	return count
}
