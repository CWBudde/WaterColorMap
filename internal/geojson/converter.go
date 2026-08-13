package geojson

import (
	"encoding/json"
	"fmt"

	"github.com/cwbudde/watercolormap/internal/types"
	"github.com/paulmach/orb/geojson"
)

// LayerType represents the different map layers we render
type LayerType string

const (
	LayerWater     LayerType = "water"  // Polygonal water bodies (lakes, ponds)
	LayerRivers    LayerType = "rivers" // Linear waterways (rivers, streams, canals)
	LayerLand      LayerType = "land"
	LayerParks     LayerType = "parks"
	LayerUrban     LayerType = "urban"     // Urban landuse areas (residential/commercial/industrial/retail)
	LayerCivic     LayerType = "civic"     // Civic buildings (schools, hospitals, universities, libraries, town halls)
	LayerBuildings LayerType = "buildings" // Individual building footprints
	LayerRoads     LayerType = "roads"
	LayerRailroads LayerType = "railroads" // Railway lines (rail, light_rail, subway, tram)
	LayerHighways  LayerType = "highways"
	LayerPaper     LayerType = "paper"
)

// ToGeoJSON converts a slice of features to GeoJSON FeatureCollection
func ToGeoJSON(features []types.Feature) (*geojson.FeatureCollection, error) {
	fc := geojson.NewFeatureCollection()

	for _, f := range features {
		if f.Geometry == nil {
			continue
		}

		// Create GeoJSON feature
		geoFeature := geojson.NewFeature(f.Geometry)

		// Add properties
		if geoFeature.Properties == nil {
			geoFeature.Properties = make(map[string]interface{})
		}

		// Copy all properties from the feature
		for key, value := range f.Properties {
			geoFeature.Properties[key] = value
		}

		// Add OSM ID and name
		geoFeature.Properties["osm_id"] = f.ID
		if f.Name != "" {
			geoFeature.Properties["name"] = f.Name
		}

		// Add feature type
		geoFeature.Properties["feature_type"] = string(f.Type)

		fc.Append(geoFeature)
	}

	return fc, nil
}

// ToGeoJSONBytes converts features to GeoJSON bytes
func ToGeoJSONBytes(features []types.Feature) ([]byte, error) {
	fc, err := ToGeoJSON(features)
	if err != nil {
		return nil, fmt.Errorf("failed to convert to GeoJSON: %w", err)
	}

	data, err := json.MarshalIndent(fc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal GeoJSON: %w", err)
	}

	return data, nil
}

// GetLayerFeatures returns features for a specific layer from FeatureCollection
func GetLayerFeatures(fc types.FeatureCollection, layer LayerType) []types.Feature {
	switch layer {
	case LayerWater:
		return fc.Water
	case LayerRivers:
		return fc.Rivers
	case LayerParks:
		return fc.Parks
	case LayerUrban:
		// Return urban landuse areas
		return fc.Urban
	case LayerCivic:
		// Return civic buildings
		return fc.Civic
	case LayerBuildings:
		// Return individual building footprints
		return fc.Buildings
	case LayerRoads:
		return fc.Roads
	case LayerRailroads:
		return fc.Railroads
	case LayerHighways:
		// Highways/major roads are derived from the generic roads feature set.
		// We keep this as a view rather than adding a separate collection bucket.
		out := make([]types.Feature, 0, len(fc.Roads))
		for _, f := range fc.Roads {
			hw, _ := f.Properties["highway"].(string)
			switch hw {
			case "motorway", "motorway_link", "trunk", "trunk_link", "primary", "primary_link", "secondary", "secondary_link":
				out = append(out, f)
			}
		}
		return out
	case LayerLand:
		return fc.Land
	default:
		return nil
	}
}

// LayerCount returns the number of features in a layer
func LayerCount(fc types.FeatureCollection, layer LayerType) int {
	return len(GetLayerFeatures(fc, layer))
}

// LayerSummary returns a summary of features per layer
func LayerSummary(fc types.FeatureCollection) string {
	return fmt.Sprintf("Water: %d, Parks: %d, Urban: %d, Civic: %d, Buildings: %d, Roads: %d (Total: %d)",
		len(fc.Water), len(fc.Parks), len(fc.Urban), len(fc.Civic), len(fc.Buildings), len(fc.Roads), fc.Count())
}
