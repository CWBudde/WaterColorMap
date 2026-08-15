package tile

import (
	"math"
	"testing"
)

func TestCoordsString(t *testing.T) {
	tests := []struct {
		expected string
		coords   Coords
	}{
		{coords: Coords{Z: 13, X: 4297, Y: 2754}, expected: "z13_x4297_y2754"},
		{coords: Coords{Z: 0, X: 0, Y: 0}, expected: "z0_x0_y0"},
		{coords: Coords{Z: 18, X: 12345, Y: 67890}, expected: "z18_x12345_y67890"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := tt.coords.String()
			if result != tt.expected {
				t.Errorf("String() = %s, want %s", result, tt.expected)
			}
		})
	}
}

func TestCoordsPath(t *testing.T) {
	coords := Coords{Z: 13, X: 4297, Y: 2754}

	tests := []struct {
		ext      string
		expected string
	}{
		{"png", "z13_x4297_y2754.png"},
		{"json", "z13_x4297_y2754.json"},
		{"xml", "z13_x4297_y2754.xml"},
	}

	for _, tt := range tests {
		t.Run(tt.ext, func(t *testing.T) {
			result := coords.Path(tt.ext)
			if result != tt.expected {
				t.Errorf("Path(%s) = %s, want %s", tt.ext, result, tt.expected)
			}
		})
	}
}

func TestCoordsBounds(t *testing.T) {
	// Test tile covering Hanover (z13_x4297_y2754)
	coords := Coords{Z: 13, X: 4297, Y: 2754}
	bounds := coords.Bounds()

	t.Logf("Tile %s bounds: [%.6f, %.6f, %.6f, %.6f]",
		coords.String(), bounds[0], bounds[1], bounds[2], bounds[3])

	// Verify bounds are in reasonable range for Germany/Europe
	// Should be somewhere in Central Europe
	if bounds[0] < -10.0 || bounds[0] > 40.0 {
		t.Errorf("minLon %.6f is outside expected range for Europe", bounds[0])
	}
	if bounds[1] < 35.0 || bounds[1] > 70.0 {
		t.Errorf("minLat %.6f is outside expected range for Europe", bounds[1])
	}

	// Verify bounds are ordered correctly
	if bounds[0] >= bounds[2] {
		t.Errorf("minLon >= maxLon: %.6f >= %.6f", bounds[0], bounds[2])
	}
	if bounds[1] >= bounds[3] {
		t.Errorf("minLat >= maxLat: %.6f >= %.6f", bounds[1], bounds[3])
	}
}

func TestCoordsBoundsMercator(t *testing.T) {
	coords := Coords{Z: 13, X: 4297, Y: 2754}
	mercBounds := coords.BoundsMercator()

	t.Logf("Tile %s Mercator bounds: [%.2f, %.2f, %.2f, %.2f]",
		coords.String(), mercBounds[0], mercBounds[1], mercBounds[2], mercBounds[3])

	// Verify bounds are ordered correctly
	if mercBounds[0] >= mercBounds[2] {
		t.Errorf("minX >= maxX: %.2f >= %.2f", mercBounds[0], mercBounds[2])
	}
	if mercBounds[1] >= mercBounds[3] {
		t.Errorf("minY >= maxY: %.2f >= %.2f", mercBounds[1], mercBounds[3])
	}

	// Web Mercator bounds should be in reasonable range
	// (roughly -20037508 to 20037508 meters)
	for i, val := range mercBounds {
		if math.Abs(val) > 20037508 {
			t.Errorf("Mercator coordinate[%d] = %.2f is out of valid range", i, val)
		}
	}
}

func TestCoordsCenter(t *testing.T) {
	coords := Coords{Z: 13, X: 4297, Y: 2754}
	lon, lat := coords.Center()

	t.Logf("Tile %s center: %.6f, %.6f", coords.String(), lon, lat)

	// Verify center is within bounds
	bounds := coords.Bounds()
	if lon < bounds[0] || lon > bounds[2] {
		t.Errorf("Center lon %.6f is outside bounds [%.6f, %.6f]", lon, bounds[0], bounds[2])
	}
	if lat < bounds[1] || lat > bounds[3] {
		t.Errorf("Center lat %.6f is outside bounds [%.6f, %.6f]", lat, bounds[1], bounds[3])
	}
}

func TestParseCoords(t *testing.T) {
	tests := []struct {
		input    string
		expected Coords
		wantErr  bool
	}{
		{"z13_x4297_y2754", Coords{Z: 13, X: 4297, Y: 2754}, false},
		{"z0_x0_y0", Coords{Z: 0, X: 0, Y: 0}, false},
		{"z18_x262143_y262143", Coords{Z: 18, X: 262143, Y: 262143}, false},
		{"z22_x4194303_y4194303", Coords{Z: 22, X: 4194303, Y: 4194303}, false},
		{"invalid", Coords{}, true},
		{"z13_x4297", Coords{}, true},
		{"13_4297_2754", Coords{}, true},
		// Sscanf ignores anything after the last verb without the round-trip check.
		{"z13_x1_y2JUNK", Coords{}, true},
		{"z013_x1_y2", Coords{}, true},
		// Out of range: zoom above MaxZoom, or x/y outside the 2^z grid.
		{"z23_x0_y0", Coords{}, true},
		{"z30_x999999999_y1", Coords{}, true},
		{"z0_x1_y0", Coords{}, true},
		{"z0_x0_y1", Coords{}, true},
		{"z1_x2_y0", Coords{}, true},
		{"z18_x262144_y0", Coords{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := ParseCoords(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseCoords(%s) expected error, got nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Errorf("ParseCoords(%s) unexpected error: %v", tt.input, err)
				return
			}
			if result != tt.expected {
				t.Errorf("ParseCoords(%s) = %+v, want %+v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestTileRange(t *testing.T) {
	// Test range covering a few tiles
	tr := TileRange{
		MinZ: 13, MaxZ: 13,
		MinX: 4297, MaxX: 4298,
		MinY: 2754, MaxY: 2755,
	}

	// Should have 4 tiles (2x2)
	expectedCount := 4
	if tr.Count() != expectedCount {
		t.Errorf("Count() = %d, want %d", tr.Count(), expectedCount)
	}

	// Test ForEach
	var visited []string
	tr.ForEach(func(c Coords) {
		visited = append(visited, c.String())
	})

	if len(visited) != expectedCount {
		t.Errorf("ForEach visited %d tiles, want %d", len(visited), expectedCount)
	}

	t.Logf("Visited tiles: %v", visited)
}

func TestTileRangeFromBounds(t *testing.T) {
	// Use the bounds from our test tile
	testTile := Coords{Z: 13, X: 4297, Y: 2754}
	bounds := testTile.Bounds()

	t.Logf("Test tile %s bounds: [%.6f, %.6f, %.6f, %.6f]",
		testTile.String(), bounds[0], bounds[1], bounds[2], bounds[3])

	tr := TileRangeFromBounds(13, 13, bounds)

	t.Logf("Tile range: z%d x[%d-%d] y[%d-%d]",
		tr.MinZ, tr.MinX, tr.MaxX, tr.MinY, tr.MaxY)
	t.Logf("Total tiles: %d", tr.Count())

	// Range should have at least one tile
	if tr.Count() == 0 {
		t.Errorf("Expected at least one tile in range, got 0")
	}

	// Verify the range makes sense
	if tr.MinX > tr.MaxX || tr.MinY > tr.MaxY {
		t.Errorf("Invalid tile range: x[%d-%d] y[%d-%d]", tr.MinX, tr.MaxX, tr.MinY, tr.MaxY)
	}
}

func TestTilesInBBox(t *testing.T) {
	// Hanover area bbox (small area for testing)
	bbox := [4]float64{9.7, 52.3, 9.8, 52.4} // minLon, minLat, maxLon, maxLat

	t.Run("single zoom level", func(t *testing.T) {
		tiles := TilesInBBox(bbox, 13, 13)
		if len(tiles) == 0 {
			t.Fatal("Expected at least one tile")
		}
		// All tiles should be at zoom 13
		for _, tile := range tiles {
			if tile.Z != 13 {
				t.Errorf("Expected zoom 13, got %d", tile.Z)
			}
		}
		t.Logf("Zoom 13: %d tiles", len(tiles))
	})

	t.Run("multiple zoom levels have correct counts", func(t *testing.T) {
		// At each higher zoom level, there should be roughly 4x more tiles
		tilesZ10 := TilesInBBox(bbox, 10, 10)
		tilesZ11 := TilesInBBox(bbox, 11, 11)
		tilesZ12 := TilesInBBox(bbox, 12, 12)

		t.Logf("Z10: %d tiles, Z11: %d tiles, Z12: %d tiles",
			len(tilesZ10), len(tilesZ11), len(tilesZ12))

		// At higher zoom, count should increase (not necessarily 4x due to bbox edge effects)
		if len(tilesZ11) < len(tilesZ10) {
			t.Errorf("Z11 should have >= tiles than Z10: %d < %d", len(tilesZ11), len(tilesZ10))
		}
		if len(tilesZ12) < len(tilesZ11) {
			t.Errorf("Z12 should have >= tiles than Z11: %d < %d", len(tilesZ12), len(tilesZ11))
		}
	})

	t.Run("combined zoom range", func(t *testing.T) {
		tiles := TilesInBBox(bbox, 10, 12)

		// Count tiles per zoom level
		zoomCounts := make(map[uint32]int)
		for _, tile := range tiles {
			zoomCounts[tile.Z]++
		}

		t.Logf("Total: %d tiles across zooms %v", len(tiles), zoomCounts)

		// Should have tiles at all requested zoom levels
		for z := uint32(10); z <= 12; z++ {
			if zoomCounts[z] == 0 {
				t.Errorf("Expected tiles at zoom %d, got none", z)
			}
		}
	})

	t.Run("tiles are unique", func(t *testing.T) {
		tiles := TilesInBBox(bbox, 10, 12)
		seen := make(map[string]bool)
		for _, tile := range tiles {
			key := tile.String()
			if seen[key] {
				t.Errorf("Duplicate tile: %s", key)
			}
			seen[key] = true
		}
	})
}

func TestTileCount(t *testing.T) {
	bbox := [4]float64{9.7, 52.3, 9.8, 52.4}

	// Count should match actual tiles returned
	tiles := TilesInBBox(bbox, 10, 12)
	count := TileCount(bbox, 10, 12)

	if count != len(tiles) {
		t.Errorf("TileCount() = %d, but TilesInBBox returned %d tiles", count, len(tiles))
	}
}

// TestTilesInBBoxWholeWorld pins the grid clamp.
//
// maptile.At maps lon=+180 to x=2^z and lat=-85.05 to y=2^z — one index past
// the last real tile — so a whole-world bbox used to enumerate z0_x1_y0 and
// z1_x2_y0. Those are tiles Coords.Validate rejects and no upstream can answer,
// and they only became reachable once batch generation accepted --zoom-min 0.
func TestTilesInBBoxWholeWorld(t *testing.T) {
	world := [4]float64{-180, -85, 180, 85}

	tiles := TilesInBBox(world, 0, 2)

	// 1 + 4 + 16: the whole grid at each zoom, and nothing beyond it.
	if len(tiles) != 21 {
		t.Errorf("TilesInBBox(world, 0, 2) returned %d tiles, want 21: %v", len(tiles), tiles)
	}
	for _, c := range tiles {
		if err := c.Validate(); err != nil {
			t.Errorf("enumerated an impossible tile %s: %v", c, err)
		}
	}

	// TileCount pre-sizes TilesInBBox, so the two must not drift apart.
	if count := TileCount(world, 0, 2); count != len(tiles) {
		t.Errorf("TileCount(world, 0, 2) = %d, but TilesInBBox returned %d", count, len(tiles))
	}
}

// TestTilesInBBoxOutOfWorldClipsToNearestEdge pins the geographic clamp.
//
// maptile.At snaps latitude to the Mercator cutoff but wraps longitude into an
// unsigned X, so a bbox west of -180 used to land on large X values that the
// grid clamp then pinned to the *easternmost* column. Clipping the corner points
// to [-180, 180] first keeps them attached to the edge they are nearest to.
func TestTilesInBBoxOutOfWorldClipsToNearestEdge(t *testing.T) {
	tests := []struct {
		name  string
		bbox  [4]float64
		wantX uint32
	}{
		{name: "west of the antimeridian", bbox: [4]float64{-181, -10, -180.5, 10}, wantX: 0},
		{name: "east of the antimeridian", bbox: [4]float64{180.5, -1, 182, 1}, wantX: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tiles := TilesInBBox(tt.bbox, 2, 2)
			if len(tiles) == 0 {
				t.Fatalf("TilesInBBox(%v, 2, 2) returned no tiles", tt.bbox)
			}
			for _, c := range tiles {
				if err := c.Validate(); err != nil {
					t.Errorf("enumerated an impossible tile %s: %v", c, err)
				}
				if c.X != tt.wantX {
					t.Errorf("tile %s: X = %d, want %d", c, c.X, tt.wantX)
				}
			}
			if count := TileCount(tt.bbox, 2, 2); count != len(tiles) {
				t.Errorf("TileCount = %d, but TilesInBBox returned %d", count, len(tiles))
			}
		})
	}
}
