package datasource

import (
	"testing"

	"github.com/cwbudde/go-overpass"
	"github.com/paulmach/orb"
)

// TestMultipolygonAssembly tests that multipolygon relations are properly assembled
// from their member ways, and that member ways are not rendered individually.
func TestMultipolygonAssembly(t *testing.T) {
	// Create a simple multipolygon: a lake with an island
	// Outer ring (counter-clockwise): lake boundary
	// Inner ring (clockwise): island

	// Outer ring way (open - part of multipolygon)
	outerWay := &overpass.Way{
		Meta: overpass.Meta{
			ID: 1001,
			Tags: map[string]string{
				"natural": "water",
			},
		},
		Geometry: []overpass.Point{
			{Lat: 52.0, Lon: 9.0},
			{Lat: 52.0, Lon: 9.1},
			{Lat: 52.1, Lon: 9.1},
			{Lat: 52.1, Lon: 9.0},
			{Lat: 52.0, Lon: 9.0}, // Closed
		},
	}

	// Inner ring way (island - open, part of multipolygon)
	innerWay := &overpass.Way{
		Meta: overpass.Meta{
			ID:   1002,
			Tags: map[string]string{
				// Island doesn't need natural=water tag
			},
		},
		Geometry: []overpass.Point{
			{Lat: 52.04, Lon: 9.04},
			{Lat: 52.04, Lon: 9.06},
			{Lat: 52.06, Lon: 9.06},
			{Lat: 52.06, Lon: 9.04},
			{Lat: 52.04, Lon: 9.04}, // Closed
		},
	}

	// Multipolygon relation
	relation := &overpass.Relation{
		Meta: overpass.Meta{
			ID: 2001,
			Tags: map[string]string{
				"type":    "multipolygon",
				"natural": "water",
			},
		},
		Members: []overpass.RelationMember{
			{Type: "way", Way: outerWay, Role: "outer"},
			{Type: "way", Way: innerWay, Role: "inner"},
		},
	}

	// Create Overpass result
	result := &overpass.Result{
		Ways: map[int64]*overpass.Way{
			1001: outerWay,
			1002: innerWay,
		},
		Relations: map[int64]*overpass.Relation{
			2001: relation,
		},
	}

	// Extract features
	features := ExtractFeaturesFromOverpassResult(result)

	// TEST 1: Should have exactly 1 water feature (the multipolygon, not the individual ways)
	if len(features.Water) != 1 {
		t.Errorf("Expected 1 water feature (multipolygon), got %d", len(features.Water))
	}

	// TEST 2: The water feature should be a Polygon or MultiPolygon, not a LineString
	if len(features.Water) > 0 {
		waterFeature := features.Water[0]

		switch geom := waterFeature.Geometry.(type) {
		case orb.Polygon:
			// TEST 3: Polygon should have 2 rings (outer + inner)
			if len(geom) != 2 {
				t.Errorf("Expected 2 rings (outer + inner), got %d", len(geom))
			}
		case orb.MultiPolygon:
			// MultiPolygon is also acceptable
			t.Logf("Got MultiPolygon with %d polygons", len(geom))
		case orb.LineString:
			t.Error("Water feature is LineString - multipolygon was not assembled!")
		default:
			t.Errorf("Expected Polygon or MultiPolygon, got %T", geom)
		}
	}

	// TEST 4: Feature ID should reference the relation, not a way
	if len(features.Water) > 0 {
		waterFeature := features.Water[0]
		expected := "relation/2001"
		if waterFeature.ID != expected {
			t.Errorf("Expected feature ID %s, got %s", expected, waterFeature.ID)
		}
	}
}

// TestStandaloneWaysNotInRelations tests that standalone water ways
// (not part of any relation) are still rendered as individual features.
func TestStandaloneWaysNotInRelations(t *testing.T) {
	// Standalone pond (closed polygon)
	standalonePond := &overpass.Way{
		Meta: overpass.Meta{
			ID: 3001,
			Tags: map[string]string{
				"natural": "water",
				"name":    "Small Pond",
			},
		},
		Geometry: []overpass.Point{
			{Lat: 52.2, Lon: 9.2},
			{Lat: 52.2, Lon: 9.3},
			{Lat: 52.3, Lon: 9.3},
			{Lat: 52.3, Lon: 9.2},
			{Lat: 52.2, Lon: 9.2}, // Closed
		},
	}

	// Create Overpass result (no relations)
	result := &overpass.Result{
		Ways: map[int64]*overpass.Way{
			3001: standalonePond,
		},
	}

	// Extract features
	features := ExtractFeaturesFromOverpassResult(result)

	// Should have 1 water feature
	if len(features.Water) != 1 {
		t.Errorf("Expected 1 water feature, got %d", len(features.Water))
	}

	// Should be a Polygon
	if len(features.Water) > 0 {
		waterFeature := features.Water[0]
		if _, ok := waterFeature.Geometry.(orb.Polygon); !ok {
			t.Errorf("Expected Polygon, got %T", waterFeature.Geometry)
		}
	}
}

// TestWaterwaysExcludedFromWaterFeatures tests that waterways (rivers/streams)
// are NOT included in water features since they are linear, not polygonal.
// Waterways would be forced closed by PolygonSymbolizer, creating incorrect rendering.
func TestWaterwaysExcludedFromWaterFeatures(t *testing.T) {
	// River segment (linear waterway)
	riverSegment := &overpass.Way{
		Meta: overpass.Meta{
			ID: 4001,
			Tags: map[string]string{
				"waterway": "river",
				"name":     "Test River",
			},
		},
		Geometry: []overpass.Point{
			{Lat: 52.0, Lon: 9.0},
			{Lat: 52.1, Lon: 9.1},
			{Lat: 52.2, Lon: 9.2},
		},
	}

	// Create Overpass result
	result := &overpass.Result{
		Ways: map[int64]*overpass.Way{
			4001: riverSegment,
		},
	}

	// Extract features
	features := ExtractFeaturesFromOverpassResult(result)

	// Waterways should NOT be in water features
	if len(features.Water) != 0 {
		t.Errorf("Expected 0 water features (waterways excluded), got %d", len(features.Water))
	}
}
