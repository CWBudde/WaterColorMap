package datasource

import (
	"context"
	"testing"
	"time"

	"github.com/cwbudde/watercolormap/internal/types"
)

// Helper functions to reduce cyclomatic complexity

func logSampleFeatures(t *testing.T, data *types.TileData) {
	t.Log("\nSample water features:")
	for i, f := range data.Features.Water {
		if i >= 3 {
			break
		}
		t.Logf("  - %s: %s (type: %T)", f.ID, f.Name, f.Geometry)
	}

	t.Log("\nSample parks:")
	for i, f := range data.Features.Parks {
		if i >= 3 {
			break
		}
		t.Logf("  - %s: %s (type: %T)", f.ID, f.Name, f.Geometry)
	}

	t.Log("\nSample roads:")
	for i, f := range data.Features.Roads {
		if i >= 5 {
			break
		}
		name := f.Name
		if name == "" {
			name = "(unnamed)"
		}
		t.Logf("  - %s: %s (type: %T)", f.ID, name, f.Geometry)
	}
}

func verifyHanoverFeatures(t *testing.T, counts map[string]int) {
	if counts["total"] == 0 {
		t.Error("Expected to find features, but got none")
	}
	if counts["water"] == 0 {
		t.Error("Expected to find water features (Leine river) in Hanover tile")
	}
	if counts["parks"] == 0 {
		t.Error("Expected to find parks in Hanover tile")
	}
	if counts["roads"] == 0 {
		t.Error("Expected to find roads in Hanover tile")
	}
}

// TestFetchHanoverTile tests fetching data for a tile covering central Hanover
// Tile z13_x4297_y2754 covers downtown Hanover including:
// - Leine river (water)
// - Maschpark, Stadtpark (parks)
// - Major roads and streets
func TestFetchHanoverTile(t *testing.T) {
	requireIntegration(t)

	// Create datasource
	ds := NewOverpassDataSource("")
	defer ds.Close()

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Hanover tile coordinates
	tile := types.TileCoordinate{
		Zoom: 13,
		X:    4317,
		Y:    2692,
	}

	t.Logf("Fetching tile: %s", tile.String())

	// Calculate bounds for reference
	bounds := types.TileToBounds(tile)
	centerLat, centerLon := bounds.Center()
	t.Logf("Bounding box: %s", bounds.String())
	t.Logf("Center: %.6f, %.6f", centerLat, centerLon)

	// Fetch tile data
	start := time.Now()
	data, err := ds.FetchTileData(ctx, tile)
	duration := time.Since(start)

	if err != nil {
		t.Fatalf("Failed to fetch tile data: %v", err)
	}

	t.Logf("Fetch completed in %v", duration)
	t.Logf("Data source: %s", data.Source)
	t.Logf("Fetched at: %s", data.FetchedAt.Format(time.RFC3339))

	// Verify we got data
	counts := data.Features.FeatureCounts()
	t.Logf("\nFeature counts:")
	t.Logf("  Water: %d", counts["water"])
	t.Logf("  Parks: %d", counts["parks"])
	t.Logf("  Roads: %d", counts["roads"])
	t.Logf("  Buildings: %d", counts["buildings"])
	t.Logf("  Civic: %d", counts["urban"])
	t.Logf("  Total: %d", counts["total"])

	// Assertions
	verifyHanoverFeatures(t, counts)

	// Print sample features
	logSampleFeatures(t, data)

	// Test caching
	t.Log("\n--- Testing cache ---")
	t.Logf("Cache size before: %d", ds.CacheSize())

	// Fetch same tile again (should be cached)
	start2 := time.Now()
	data2, err := ds.FetchTileData(ctx, tile)
	duration2 := time.Since(start2)

	if err != nil {
		t.Fatalf("Failed to fetch tile data (cached): %v", err)
	}

	t.Logf("Cached fetch completed in %v", duration2)
	t.Logf("Cache size after: %d", ds.CacheSize())

	// Cached request should be much faster
	if duration2 > duration/2 {
		t.Logf("Warning: Cached request (%v) not significantly faster than original (%v)", duration2, duration)
	} else {
		t.Logf("Cache working: %v vs %v (%.1fx faster)", duration2, duration, float64(duration)/float64(duration2))
	}

	// Verify cached data matches
	counts2 := data2.Features.FeatureCounts()
	if counts2["total"] != counts["total"] {
		t.Errorf("Cached data mismatch: got %d features, expected %d", counts2["total"], counts["total"])
	}
}

// TestEilenriedeForests tests fetching forests around Eilenriede at zoom 15
func TestEilenriedeForests(t *testing.T) {
	requireIntegration(t)

	ds := NewOverpassDataSource("")
	defer ds.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Test the specific Eilenriede tile user reported
	tile := types.TileCoordinate{Zoom: 15, X: 17272, Y: 10766}
	t.Logf("Tile: %s", tile.String())

	data, err := ds.FetchTileData(ctx, tile)
	if err != nil {
		t.Fatalf("Failed to fetch tile: %v", err)
	}

	// Count forests
	forestCount := 0
	for _, p := range data.Features.Parks {
		if p.Properties["landuse"] == "forest" || p.Properties["natural"] == "wood" {
			forestCount++
			t.Logf("Forest: %s, bounds: %v", p.ID, p.Geometry.Bound())
		}
	}

	if forestCount == 0 {
		t.Error("Expected forests in Eilenriede tile z15_x17272_y10766")
	}
	t.Logf("Total parks: %d, Forests: %d", len(data.Features.Parks), forestCount)
}

// TestTileCoordinateConversion tests the tile coordinate to bounding box conversion
func TestTileCoordinateConversion(t *testing.T) {
	tests := []struct {
		name        string
		tile        types.TileCoordinate
		expectedLat float64 // Approximate center latitude
		expectedLon float64 // Approximate center longitude
		deltaLat    float64 // Acceptable error
		deltaLon    float64 // Acceptable error
	}{
		{
			name:        "Hanover tile z13",
			tile:        types.TileCoordinate{Zoom: 13, X: 4317, Y: 2692},
			expectedLat: 52.3737, // Central Hanover latitude
			expectedLon: 9.7339,  // Central Hanover longitude
			deltaLat:    0.02,    // ~2km tolerance
			deltaLon:    0.02,
		},
		{
			name:        "World tile z0",
			tile:        types.TileCoordinate{Zoom: 0, X: 0, Y: 0},
			expectedLat: 0.0,
			expectedLon: 0.0,
			deltaLat:    85.0, // Entire world
			deltaLon:    180.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bounds := types.TileToBounds(tt.tile)
			centerLat, centerLon := bounds.Center()

			t.Logf("Tile: %s", tt.tile.String())
			t.Logf("Bounds: %s", bounds.String())
			t.Logf("Center: %.6f, %.6f", centerLat, centerLon)
			t.Logf("Size: %.6f° x %.6f°", bounds.Width(), bounds.Height())

			// Check center is within expected range
			if centerLat < tt.expectedLat-tt.deltaLat || centerLat > tt.expectedLat+tt.deltaLat {
				t.Errorf("Center latitude %.6f not in range [%.6f, %.6f]",
					centerLat, tt.expectedLat-tt.deltaLat, tt.expectedLat+tt.deltaLat)
			}

			if centerLon < tt.expectedLon-tt.deltaLon || centerLon > tt.expectedLon+tt.deltaLon {
				t.Errorf("Center longitude %.6f not in range [%.6f, %.6f]",
					centerLon, tt.expectedLon-tt.deltaLon, tt.expectedLon+tt.deltaLon)
			}
		})
	}
}

// TestMultipleTiles tests fetching multiple adjacent tiles
func TestMultipleTiles(t *testing.T) {
	requireIntegration(t)

	ds := NewOverpassDataSource("")
	defer ds.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Test adjacent tiles around Hanover
	tiles := []types.TileCoordinate{
		{Zoom: 13, X: 4317, Y: 2692}, // Center
		{Zoom: 13, X: 4318, Y: 2692}, // East
		{Zoom: 13, X: 4317, Y: 2693}, // South
	}

	for _, tile := range tiles {
		t.Run(tile.String(), func(t *testing.T) {
			data, err := ds.FetchTileData(ctx, tile)
			if err != nil {
				t.Fatalf("Failed to fetch tile %s: %v", tile.String(), err)
			}

			counts := data.Features.FeatureCounts()
			t.Logf("Tile %s: %d total features", tile.String(), counts["total"])

			if counts["total"] == 0 {
				t.Errorf("Expected features in tile %s", tile.String())
			}
		})
	}

	t.Logf("\nFinal cache size: %d", ds.CacheSize())
}

// TestDataSourceConfiguration tests different configuration options
func TestDataSourceConfiguration(t *testing.T) {
	// Test with default endpoint
	ds1 := NewOverpassDataSource("")
	if ds1 == nil {
		t.Fatal("Failed to create data source with default endpoint")
	}
	ds1.Close()

	// Test with custom endpoint
	ds2 := NewOverpassDataSource("https://overpass.kumi.systems/api/interpreter")
	if ds2 == nil {
		t.Fatal("Failed to create data source with custom endpoint")
	}
	ds2.Close()

	t.Log("Data source configuration tests passed")
}

// TestCacheOperations tests cache manipulation
func TestCacheOperations(t *testing.T) {
	ds := NewOverpassDataSource("")
	defer ds.Close()

	// Initial state
	if size := ds.CacheSize(); size != 0 {
		t.Errorf("Expected empty cache, got size %d", size)
	}

	// Clear empty cache (should not panic)
	ds.ClearCache()

	// Note: We can't test actual caching without making API calls,
	// which we avoid in unit tests

	t.Log("Cache operations tests passed")
}
