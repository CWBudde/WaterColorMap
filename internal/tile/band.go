package tile

import "sort"

// A Band is a square block of same-zoom tiles that can be fetched with one
// Overpass query.
//
// The saving comes from `out geom` returning unclipped geometry: a motorway
// crossing sixty-four tiles is transferred sixty-four times by per-tile
// fetching. One query for the whole block transfers it once.
//
// Every member shares the band's zoom, which is what makes this safe.
// buildTileQuery selects its rule set from the zoom alone, so every tile in a
// band produces identical query text apart from the bbox literal. (PLAN.md's
// note about the building rules not being monotone across z16 is a constraint
// on reusing a *parent's* data for its children at a different zoom. That is a
// different technique, and not this one.)
type Band struct {
	// Tiles are the members, in a deterministic order, and only ever tiles
	// that were actually asked for.
	Tiles []Coords
	// Key is the ancestor tile the members were grouped under. Its zoom is
	// Level levels above the members'.
	Key Coords
	// Level is how many zoom levels up Key sits: 0 means a single tile, 1 a
	// 2x2 block, 2 a 4x4 block.
	Level uint32
}

// Ancestor returns the tile that contains c, levels zoom levels up.
// Levels beyond c's zoom clamp at zoom 0.
func Ancestor(c Coords, levels uint32) Coords {
	if levels > c.Z {
		levels = c.Z
	}
	return Coords{
		Z: c.Z - levels,
		X: c.X >> levels,
		Y: c.Y >> levels,
	}
}

// GroupIntoBands partitions coords into bands of tiles sharing an ancestor
// `levels` levels up.
//
// Every input tile appears in exactly one band, and the result is fully
// deterministic — bands sorted by key, tiles sorted within each band — so a
// resumed or repeated run issues the same queries in the same order. Tiles of
// different zooms are grouped separately, since a band's query is built at its
// members' zoom.
//
// levels == 0 degenerates to one band per tile, which is exactly per-tile
// fetching and is what the adaptive split bottoms out at.
func GroupIntoBands(coords []Coords, levels uint32) []Band {
	if len(coords) == 0 {
		return nil
	}

	byKey := make(map[Coords][]Coords)
	for _, c := range coords {
		key := Ancestor(c, levels)
		byKey[key] = append(byKey[key], c)
	}

	bands := make([]Band, 0, len(byKey))
	for key, tiles := range byKey {
		sortCoords(tiles)
		bands = append(bands, Band{Key: key, Level: levels, Tiles: tiles})
	}

	sort.Slice(bands, func(i, j int) bool {
		return lessCoords(bands[i].Key, bands[j].Key)
	})
	return bands
}

// Split divides a band into the bands one level below it, covering exactly the
// same tiles. It returns nil at level 0, where the band is a single tile and
// there is nothing left to split.
//
// This is the size guard for band fetching. Rather than predicting how large a
// response will be — which depends on the data, not on the geometry — a band
// that fails for any reason is split and its quadrants retried independently,
// recursing down to ordinary per-tile fetches. The terminal case is exactly
// today's behaviour, so a tile that genuinely cannot be fetched fails with the
// same error it always did, attributed to itself and not to its fifteen
// neighbours.
func (b Band) Split() []Band {
	if b.Level == 0 {
		return nil
	}
	return GroupIntoBands(b.Tiles, b.Level-1)
}

func sortCoords(coords []Coords) {
	sort.Slice(coords, func(i, j int) bool { return lessCoords(coords[i], coords[j]) })
}

func lessCoords(a, b Coords) bool {
	if a.Z != b.Z {
		return a.Z < b.Z
	}
	if a.X != b.X {
		return a.X < b.X
	}
	return a.Y < b.Y
}
