package datasource

import (
	"encoding/json"
	"fmt"

	"github.com/cwbudde/go-overpass"
	"github.com/paulmach/orb"

	"github.com/cwbudde/watercolormap/internal/types"
)

// UnmarshalOverpassJSON decodes an Overpass API JSON response into an overpass.Result.
// This is used by the WASM playground (browser fetch + Go-side parsing).
func UnmarshalOverpassJSON(data []byte) (*overpass.Result, error) {
	var result overpass.Result
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal overpass json: %w", err)
	}
	return &result, nil
}

// ExtractFeaturesFromOverpassResult converts an Overpass result to WaterColorMap's FeatureCollection.
// It mirrors the logic used by OverpassDataSource.
func ExtractFeaturesFromOverpassResult(result *overpass.Result) types.FeatureCollection {
	var features types.FeatureCollection
	if result == nil {
		return features
	}

	memberWayIDs := collectMultipolygonMemberWayIDs(result.Relations)

	// Process ways (skip those that are multipolygon members)
	for _, way := range result.Ways {
		// Skip ways that are members of multipolygon relations
		if memberWayIDs[way.ID] {
			continue
		}

		feature := convertWayToFeature(way)
		if feature == nil {
			continue
		}

		appendWayFeature(&features, way.Tags, feature)
	}

	// Process relations (mainly for multipolygon water bodies and parks)
	for _, rel := range result.Relations {
		var feature *types.Feature

		// Handle multipolygon relations specially
		if rel.Tags["type"] == "multipolygon" {
			feature = convertMultipolygonRelationToFeature(rel)
		} else {
			feature = convertRelationToFeature(rel)
		}

		if feature == nil {
			continue
		}

		appendRelationFeature(&features, rel.Tags, feature)
	}

	return features
}

// collectMultipolygonMemberWayIDs returns the set of way IDs that are members of
// multipolygon relations, so those ways are not emitted a second time on their own.
//
// Only members with an embedded *Way are collected. go-overpass's RelationMember
// carries Type, Node, Way, Relation and Role but no reference ID, so a member the
// API returned by reference alone cannot be identified here and will not be
// deduplicated. In practice Overpass does not embed way geometry in relations, so
// this mainly catches test fixtures and APIs that do embed it.
func collectMultipolygonMemberWayIDs(relations map[int64]*overpass.Relation) map[int64]bool {
	memberWayIDs := make(map[int64]bool)
	for _, rel := range relations {
		if rel.Tags["type"] != "multipolygon" {
			continue
		}
		for _, member := range rel.Members {
			if member.Type != "way" {
				continue
			}
			if member.Way != nil {
				// Embedded way object (from test data or some APIs)
				memberWayIDs[member.Way.ID] = true
			}
			// Note: Real Overpass API doesn't embed way geometry in relations
			// Member ways must be looked up from result.Ways map during assembly
		}
	}
	return memberWayIDs
}

// appendWayFeature files a way-derived feature into the matching collection bucket.
func appendWayFeature(features *types.FeatureCollection, tags map[string]string, feature *types.Feature) {
	switch {
	case isWater(tags):
		features.Water = append(features.Water, *feature)
	case isRiver(tags):
		features.Rivers = append(features.Rivers, *feature)
	case isGreen(tags):
		features.Parks = append(features.Parks, *feature)
	case isRoad(tags):
		features.Roads = append(features.Roads, *feature)
	case isRailroad(tags):
		features.Railroads = append(features.Railroads, *feature)
	case isCivic(tags):
		// Check civic BEFORE building - civic buildings have both amenity=* and building=*
		features.Civic = append(features.Civic, *feature)
	case isBuilding(tags):
		features.Buildings = append(features.Buildings, *feature)
	case isUrban(tags):
		features.Urban = append(features.Urban, *feature)
	}
}

// appendRelationFeature files a relation-derived feature into the matching
// collection bucket. Relations support fewer categories than ways.
func appendRelationFeature(features *types.FeatureCollection, tags map[string]string, feature *types.Feature) {
	switch {
	case isWater(tags):
		features.Water = append(features.Water, *feature)
	case isRiver(tags):
		features.Rivers = append(features.Rivers, *feature)
	case isGreen(tags):
		features.Parks = append(features.Parks, *feature)
	case isCivic(tags):
		// Civic campuses (schools, hospitals, universities) are often mapped
		// as multipolygon relations - classify them before urban landuse.
		features.Civic = append(features.Civic, *feature)
	case isUrban(tags):
		features.Urban = append(features.Urban, *feature)
	}
}

func convertWayToFeature(way *overpass.Way) *types.Feature {
	if way == nil || len(way.Geometry) == 0 {
		return nil
	}

	var geometry orb.Geometry
	points := make(orb.LineString, len(way.Geometry))

	for i, point := range way.Geometry {
		points[i] = orb.Point{point.Lon, point.Lat}
	}

	if len(points) > 2 && points[0] == points[len(points)-1] {
		ring := orb.Ring(points)
		geometry = orb.Polygon{ring}
	} else {
		geometry = points
	}

	name := ""
	if n, ok := way.Tags["name"]; ok {
		name = n
	}

	featureType := categorizeByTags(way.Tags)

	return &types.Feature{
		ID:         fmt.Sprintf("way/%d", way.ID),
		Type:       featureType,
		Geometry:   geometry,
		Properties: convertTags(way.Tags),
		Name:       name,
	}
}

func convertRelationToFeature(rel *overpass.Relation) *types.Feature {
	if rel == nil {
		return nil
	}

	name := ""
	if n, ok := rel.Tags["name"]; ok {
		name = n
	}

	featureType := categorizeByTags(rel.Tags)

	return &types.Feature{
		ID:         fmt.Sprintf("relation/%d", rel.ID),
		Type:       featureType,
		Geometry:   orb.Point{},
		Properties: convertTags(rel.Tags),
		Name:       name,
	}
}

// convertMultipolygonRelationToFeature assembles a multipolygon relation from its member ways.
//
// Only ways embedded in the relation's members are usable: the go-overpass library
// does not expose member ref IDs, so ways living solely in result.Ways cannot be
// looked up and are skipped.
func convertMultipolygonRelationToFeature(rel *overpass.Relation) *types.Feature {
	if rel == nil {
		return nil
	}

	// Separate outer and inner rings
	var outerRings []orb.Ring
	var innerRings []orb.Ring

	for _, member := range rel.Members {
		if member.Type != "way" {
			continue
		}

		// Look up the way - try embedded object first, then fall back to map lookup
		var way *overpass.Way
		if member.Way != nil {
			// Embedded way object (from test data)
			way = member.Way
		}
		// Note: Real Overpass API doesn't embed member ways - they must be looked up
		// However, the go-overpass library doesn't expose the ref ID, so we can't look them up
		// Member ways that aren't in the result will be skipped

		if way == nil || len(way.Geometry) == 0 {
			continue
		}

		// Convert way geometry to ring
		points := make(orb.LineString, len(way.Geometry))
		for i, point := range way.Geometry {
			points[i] = orb.Point{point.Lon, point.Lat}
		}

		// Ensure ring is closed
		if len(points) > 0 && points[0] != points[len(points)-1] {
			points = append(points, points[0])
		}

		ring := orb.Ring(points)

		// Classify as outer or inner based on role
		if member.Role == "inner" {
			innerRings = append(innerRings, ring)
		} else {
			// Default to outer (role can be empty or "outer")
			outerRings = append(outerRings, ring)
		}
	}

	// Build geometry
	var geometry orb.Geometry
	if len(outerRings) == 0 {
		// No outer rings - can't build polygon
		return nil
	}

	if len(outerRings) == 1 {
		// Single polygon with potential inner rings
		rings := make([]orb.Ring, 0, 1+len(innerRings))
		rings = append(rings, outerRings[0])
		rings = append(rings, innerRings...)
		geometry = orb.Polygon(rings)
	} else {
		// Multiple outer rings - create MultiPolygon
		polygons := make(orb.MultiPolygon, len(outerRings))
		for i, outer := range outerRings {
			polygons[i] = orb.Polygon{outer}
		}
		// Note: For simplicity, we're not assigning inner rings to specific outer rings
		// A more sophisticated implementation would determine which inner rings belong to which outer rings
		geometry = polygons
	}

	name := ""
	if n, ok := rel.Tags["name"]; ok {
		name = n
	}

	featureType := categorizeByTags(rel.Tags)

	return &types.Feature{
		ID:         fmt.Sprintf("relation/%d", rel.ID),
		Type:       featureType,
		Geometry:   geometry,
		Properties: convertTags(rel.Tags),
		Name:       name,
	}
}

func categorizeByTags(tags map[string]string) types.FeatureType {
	if isWater(tags) {
		return types.FeatureTypeWater
	}
	if isGreen(tags) {
		return types.FeatureTypePark
	}
	if isRoad(tags) {
		return types.FeatureTypeRoad
	}
	if isRailroad(tags) {
		return types.FeatureTypeRailroad
	}
	if isCivic(tags) {
		// Check civic BEFORE building - civic buildings have both amenity=* and building=*
		return types.FeatureTypeCivic
	}
	if isBuilding(tags) {
		return types.FeatureTypeBuilding
	}
	if isUrban(tags) {
		return types.FeatureTypeUrban
	}
	return types.FeatureTypeUnknown
}

func isWater(tags map[string]string) bool {
	// Only include polygonal water bodies, not linear waterways
	// Waterways are now handled separately in isRiver()
	// NOTE: natural=sea and place=sea are NOT area polygons (they're points or don't exist)
	// Ocean tiles will not render correctly - see PLAN.md section 4.10
	return tags["natural"] == "water" ||
		tags["natural"] == "coastline"
}

func isRiver(tags map[string]string) bool {
	// Linear waterways: rivers, streams, canals
	// These will be rendered with LineSymbolizer to avoid polygon closing issues
	return tags["waterway"] != ""
}

func isGreen(tags map[string]string) bool {
	return tags["leisure"] == "park" ||
		tags["leisure"] == "garden" ||
		tags["leisure"] == "playground" ||
		tags["leisure"] == "nature_reserve" ||
		tags["landuse"] == "forest" ||
		tags["landuse"] == "grass" ||
		tags["landuse"] == "orchard" ||
		tags["landuse"] == "vineyard" ||
		tags["landuse"] == "allotments" ||
		tags["natural"] == "wood" ||
		tags["natural"] == "heath"
}

func isRoad(tags map[string]string) bool {
	return tags["highway"] != ""
}

func isRailroad(tags map[string]string) bool {
	railway := tags["railway"]
	return railway == "rail" ||
		railway == "light_rail" ||
		railway == "subway" ||
		railway == "tram"
}

func isBuilding(tags map[string]string) bool {
	return tags["building"] != ""
}

func isUrban(tags map[string]string) bool {
	// Urban areas are landuse zones (residential/commercial/industrial/retail)
	landuse := tags["landuse"]
	return landuse == "residential" ||
		landuse == "commercial" ||
		landuse == "industrial" ||
		landuse == "retail"
}

func isCivic(tags map[string]string) bool {
	// Civic buildings are amenities like schools, hospitals, universities, colleges, and stadiums
	amenity := tags["amenity"]
	leisure := tags["leisure"]
	return amenity == "school" ||
		amenity == "hospital" ||
		amenity == "university" ||
		amenity == "college" ||
		amenity == "library" ||
		amenity == "town_hall" ||
		leisure == "stadium"
}

// convertTags converts OSM tags to generic properties map
func convertTags(tags map[string]string) map[string]interface{} {
	props := make(map[string]interface{}, len(tags))
	for k, v := range tags {
		props[k] = v
	}
	return props
}
