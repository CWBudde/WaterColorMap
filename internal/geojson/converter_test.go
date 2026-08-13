package geojson

import (
	"encoding/json"
	"testing"

	"github.com/paulmach/orb"

	"github.com/cwbudde/watercolormap/internal/types"
)

func TestToGeoJSON(t *testing.T) {
	features := []types.Feature{
		{
			ID:       "way/12345",
			Type:     types.FeatureTypeWater,
			Geometry: orb.Polygon{{{9.73, 52.37}, {9.74, 52.37}, {9.74, 52.38}, {9.73, 52.38}, {9.73, 52.37}}},
			Properties: map[string]interface{}{
				"natural": "water",
			},
			Name: "Test Lake",
		},
		{
			ID:       "way/67890",
			Type:     types.FeatureTypeRoad,
			Geometry: orb.LineString{{9.73, 52.37}, {9.74, 52.37}, {9.75, 52.38}},
			Properties: map[string]interface{}{
				"highway": "primary",
			},
			Name: "Main Street",
		},
	}

	fc, err := ToGeoJSON(features)
	if err != nil {
		t.Fatalf("ToGeoJSON failed: %v", err)
	}

	if len(fc.Features) != 2 {
		t.Errorf("Expected 2 GeoJSON features, got %d", len(fc.Features))
	}

	// Verify first feature (polygon)
	if fc.Features[0].Geometry.GeoJSONType() != "Polygon" {
		t.Errorf("Expected Polygon, got %s", fc.Features[0].Geometry.GeoJSONType())
	}
	if fc.Features[0].Properties["natural"] != "water" {
		t.Errorf("Expected natural=water property")
	}
	if fc.Features[0].Properties["osm_id"] != "way/12345" {
		t.Errorf("Expected osm_id=way/12345")
	}
	if fc.Features[0].Properties["name"] != "Test Lake" {
		t.Errorf("Expected name=Test Lake")
	}

	// Verify second feature (linestring)
	if fc.Features[1].Geometry.GeoJSONType() != "LineString" {
		t.Errorf("Expected LineString, got %s", fc.Features[1].Geometry.GeoJSONType())
	}
	if fc.Features[1].Properties["highway"] != "primary" {
		t.Errorf("Expected highway=primary property")
	}

	t.Logf("Successfully converted %d features to GeoJSON", len(fc.Features))
}

func TestToGeoJSONBytes(t *testing.T) {
	features := []types.Feature{
		{
			ID:       "node/123",
			Type:     types.FeatureTypeWater,
			Geometry: orb.Point{9.73, 52.37},
			Properties: map[string]interface{}{
				"natural": "spring",
			},
		},
	}

	data, err := ToGeoJSONBytes(features)
	if err != nil {
		t.Fatalf("ToGeoJSONBytes failed: %v", err)
	}

	if len(data) == 0 {
		t.Error("Expected non-empty GeoJSON bytes")
	}

	// Verify it's valid JSON
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Errorf("Invalid JSON: %v", err)
	}

	// Verify it's a FeatureCollection
	if result["type"] != "FeatureCollection" {
		t.Errorf("Expected FeatureCollection type")
	}

	t.Logf("Generated GeoJSON: %d bytes", len(data))
}

func TestGetLayerFeatures(t *testing.T) {
	// Create a feature collection with various features
	fc := types.FeatureCollection{
		Water: []types.Feature{
			{ID: "water1", Type: types.FeatureTypeWater},
			{ID: "water2", Type: types.FeatureTypeWater},
		},
		Parks: []types.Feature{
			{ID: "park1", Type: types.FeatureTypePark},
		},
		Buildings: []types.Feature{
			{ID: "building1", Type: types.FeatureTypeBuilding},
			{ID: "building2", Type: types.FeatureTypeBuilding},
		},
		Urban: []types.Feature{
			{ID: "civic1", Type: types.FeatureTypeUrban},
		},
		Roads: []types.Feature{
			{ID: "road1", Type: types.FeatureTypeRoad},
			{ID: "road2", Type: types.FeatureTypeRoad},
			{ID: "road3", Type: types.FeatureTypeRoad},
		},
	}

	// Test water layer
	waterFeatures := GetLayerFeatures(fc, LayerWater)
	if len(waterFeatures) != 2 {
		t.Errorf("Expected 2 water features, got %d", len(waterFeatures))
	}

	// Test parks layer
	parkFeatures := GetLayerFeatures(fc, LayerParks)
	if len(parkFeatures) != 1 {
		t.Errorf("Expected 1 park feature, got %d", len(parkFeatures))
	}

	// Test urban layer (urban only, buildings are separate)
	urbanFeatures := GetLayerFeatures(fc, LayerUrban)
	if len(urbanFeatures) != 1 {
		t.Errorf("Expected 1 urban feature, got %d", len(urbanFeatures))
	}

	// Test buildings layer
	buildingFeatures := GetLayerFeatures(fc, LayerBuildings)
	if len(buildingFeatures) != 2 {
		t.Errorf("Expected 2 building features, got %d", len(buildingFeatures))
	}

	// Test roads layer
	roadFeatures := GetLayerFeatures(fc, LayerRoads)
	if len(roadFeatures) != 3 {
		t.Errorf("Expected 3 road features, got %d", len(roadFeatures))
	}

	t.Logf("Layer extraction: Water=%d, Parks=%d, Urban=%d, Buildings=%d, Roads=%d",
		len(waterFeatures), len(parkFeatures), len(urbanFeatures), len(buildingFeatures), len(roadFeatures))
}

func TestLayerCount(t *testing.T) {
	fc := types.FeatureCollection{
		Water:     make([]types.Feature, 5),
		Parks:     make([]types.Feature, 3),
		Buildings: make([]types.Feature, 10),
		Urban:     make([]types.Feature, 2),
		Roads:     make([]types.Feature, 7),
	}

	// Test LayerCount
	if LayerCount(fc, LayerWater) != 5 {
		t.Errorf("Expected 5 water features, got %d", LayerCount(fc, LayerWater))
	}
	if LayerCount(fc, LayerParks) != 3 {
		t.Errorf("Expected 3 park features, got %d", LayerCount(fc, LayerParks))
	}
	// Civic is separate from buildings now
	if LayerCount(fc, LayerUrban) != 2 {
		t.Errorf("Expected 2 urban features, got %d", LayerCount(fc, LayerUrban))
	}
	if LayerCount(fc, LayerBuildings) != 10 {
		t.Errorf("Expected 10 building features, got %d", LayerCount(fc, LayerBuildings))
	}

	t.Log("LayerCount tests passed")
}

func TestLayerSummary(t *testing.T) {
	fc := types.FeatureCollection{
		Water:     make([]types.Feature, 5),
		Parks:     make([]types.Feature, 3),
		Buildings: make([]types.Feature, 10),
		Urban:     make([]types.Feature, 2),
		Roads:     make([]types.Feature, 7),
	}

	summary := LayerSummary(fc)
	t.Logf("Summary: %s", summary)
	if summary == "" {
		t.Error("Expected non-empty summary")
	}
}

func TestEmptyFeatures(t *testing.T) {
	// Test with empty feature list
	features := []types.Feature{}

	fc, err := ToGeoJSON(features)
	if err != nil {
		t.Fatalf("ToGeoJSON failed on empty features: %v", err)
	}

	if len(fc.Features) != 0 {
		t.Errorf("Expected 0 GeoJSON features, got %d", len(fc.Features))
	}

	t.Log("Empty features handled correctly")
}

func TestNilGeometry(t *testing.T) {
	// Test with nil geometry (should be skipped)
	features := []types.Feature{
		{
			ID:       "invalid1",
			Type:     types.FeatureTypeWater,
			Geometry: nil, // nil geometry should be skipped
		},
		{
			ID:       "valid1",
			Type:     types.FeatureTypeWater,
			Geometry: orb.Point{9.73, 52.37},
		},
	}

	fc, err := ToGeoJSON(features)
	if err != nil {
		t.Fatalf("ToGeoJSON failed: %v", err)
	}

	// Should only have 1 feature (the valid one)
	if len(fc.Features) != 1 {
		t.Errorf("Expected 1 GeoJSON feature (nil geometry skipped), got %d", len(fc.Features))
	}

	if fc.Features[0].Properties["osm_id"] != "valid1" {
		t.Errorf("Expected valid feature to be included")
	}

	t.Log("Nil geometry correctly skipped")
}
