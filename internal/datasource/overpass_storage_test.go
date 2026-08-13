package datasource

import (
	"testing"

	"github.com/cwbudde/go-overpass"
	"github.com/cwbudde/watercolormap/internal/types"
)

// TestOverpassResultStorage verifies that raw Overpass response storage is opt-in
func TestOverpassResultStorage(t *testing.T) {
	// Create a mock result
	mockWay := &overpass.Way{}
	mockWay.Tags = map[string]string{"natural": "water"}

	mockResult := &overpass.Result{
		Ways: map[int64]*overpass.Way{
			123: mockWay,
		},
	}

	// Test 1: Default behavior (no storage)
	features := ExtractFeaturesFromOverpassResult(mockResult)
	tileData := &types.TileData{
		Features: features,
		Source:   "test",
	}

	if tileData.OverpassResult != nil {
		t.Error("OverpassResult should be nil by default")
	}

	// Test 2: With storage enabled
	tileDataWithStorage := &types.TileData{
		Features:       features,
		Source:         "test",
		OverpassResult: mockResult,
	}

	if tileDataWithStorage.OverpassResult == nil {
		t.Error("OverpassResult should not be nil when explicitly set")
	}

	if len(tileDataWithStorage.OverpassResult.Ways) != 1 {
		t.Errorf("Expected 1 way, got %d", len(tileDataWithStorage.OverpassResult.Ways))
	}
}

// TestWithRawResponseStorage verifies the fluent API for enabling raw response storage
func TestWithRawResponseStorage(t *testing.T) {
	ds := NewOverpassDataSource("")

	// Default should be false
	if ds.storeRawResponse {
		t.Error("storeRawResponse should be false by default")
	}

	// Enable storage
	ds = ds.WithRawResponseStorage(true)
	if !ds.storeRawResponse {
		t.Error("storeRawResponse should be true after enabling")
	}

	// Disable storage
	ds = ds.WithRawResponseStorage(false)
	if ds.storeRawResponse {
		t.Error("storeRawResponse should be false after disabling")
	}
}
