package types

import (
	"time"

	"github.com/cwbudde/go-overpass"
	"github.com/paulmach/orb"
)

// FeatureType represents the type of geographic feature
type FeatureType string

const (
	FeatureTypeWater    FeatureType = "water"
	FeatureTypePark     FeatureType = "park"
	FeatureTypeRoad     FeatureType = "road"
	FeatureTypeRailroad FeatureType = "railroad"
	FeatureTypeBuilding FeatureType = "building"
	FeatureTypeUrban    FeatureType = "urban"
	FeatureTypeCivic    FeatureType = "civic"
	FeatureTypeLand     FeatureType = "land"
	FeatureTypeUnknown  FeatureType = "unknown"
)

// Feature represents a geographic feature extracted from OSM
type Feature struct {
	ID         string                 // OSM element ID (e.g., "way/12345")
	Type       FeatureType            // Feature category
	Geometry   orb.Geometry           // Geometry (Point, LineString, Polygon, MultiPolygon)
	Properties map[string]interface{} // OSM tags and additional properties
	Name       string                 // Feature name (if available)
}

// FeatureCollection groups features by type
type FeatureCollection struct {
	Water     []Feature // Polygonal water bodies (lakes, ponds)
	Rivers    []Feature // Linear waterways (rivers, streams, canals)
	Parks     []Feature // Parks, forests, green spaces
	Roads     []Feature // Streets, highways
	Railroads []Feature // Railway lines (rail, light_rail, subway, tram)
	Buildings []Feature // Building footprints
	Urban     []Feature // Urban landuse areas (residential/commercial/industrial/retail)
	Civic     []Feature // Civic buildings (schools, hospitals, universities, libraries, town halls)
	Land      []Feature // Land polygons (background)
}

// TileData represents all data for a single tile
type TileData struct {
	FetchedAt      time.Time
	OverpassResult *overpass.Result
	Source         string
	Features       FeatureCollection
	Bounds         BoundingBox
	Coordinate     TileCoordinate
}

// Count returns the total number of features
func (fc FeatureCollection) Count() int {
	return len(fc.Water) + len(fc.Parks) + len(fc.Roads) + len(fc.Railroads) + len(fc.Buildings) + len(fc.Urban) + len(fc.Civic) + len(fc.Land)
}

// FeatureCounts returns a map of feature counts by type
func (fc FeatureCollection) FeatureCounts() map[string]int {
	return map[string]int{
		"water":     len(fc.Water),
		"parks":     len(fc.Parks),
		"roads":     len(fc.Roads),
		"railroads": len(fc.Railroads),
		"buildings": len(fc.Buildings),
		"urban":     len(fc.Urban),
		"civic":     len(fc.Civic),
		"land":      len(fc.Land),
		"total":     fc.Count(),
	}
}
