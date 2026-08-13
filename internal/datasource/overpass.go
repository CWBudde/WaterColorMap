package datasource

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/cwbudde/go-overpass"
	"github.com/cwbudde/watercolormap/internal/types"
)

// OverpassConfig contains configuration for the Overpass API client.
type OverpassConfig struct {
	// Endpoint is the Overpass API URL (default: https://overpass-api.de/api/interpreter)
	Endpoint string
	// Workers controls parallelism (default: 2 for public API, increase for private instances)
	Workers int
	// RetryConfig configures retry behavior with exponential backoff
	RetryConfig *overpass.RetryConfig
	// HTTPClient allows custom HTTP client (default: a client with Timeout set)
	HTTPClient *http.Client
	// MaxResponseBytes caps a single Overpass response body
	// (default: DefaultMaxResponseBytes). Zero or less disables the cap.
	MaxResponseBytes int64
}

// defaultHTTPTimeout bounds a single Overpass request. http.DefaultClient has
// no timeout at all, so a hung upstream previously pinned a fetch worker
// indefinitely — and with only two workers by default, two hung requests
// stalled all tile generation.
const defaultHTTPTimeout = 3 * time.Minute

func defaultHTTPClient() *http.Client {
	return &http.Client{Timeout: defaultHTTPTimeout}
}

// DefaultOverpassConfig returns sensible defaults for public Overpass API.
func DefaultOverpassConfig() OverpassConfig {
	retryConfig := overpass.DefaultRetryConfig()
	return OverpassConfig{
		Endpoint:         "https://overpass-api.de/api/interpreter",
		Workers:          2,
		RetryConfig:      &retryConfig,
		HTTPClient:       defaultHTTPClient(),
		MaxResponseBytes: DefaultMaxResponseBytes,
	}
}

// PrivateInstanceConfig returns config optimized for a private Overpass instance.
// Uses more aggressive retries and higher parallelism.
func PrivateInstanceConfig(endpoint string) OverpassConfig {
	return OverpassConfig{
		Endpoint: endpoint,
		Workers:  10, // Higher parallelism for private instance
		RetryConfig: &overpass.RetryConfig{
			MaxRetries:        5,
			InitialBackoff:    500 * time.Millisecond,
			MaxBackoff:        10 * time.Second,
			BackoffMultiplier: 1.5,
			Jitter:            true, // Prevents thundering herd
		},
		HTTPClient:       defaultHTTPClient(),
		MaxResponseBytes: DefaultMaxResponseBytes,
	}
}

// OverpassDataSource fetches OSM data from Overpass API
type OverpassDataSource struct {
	client           overpass.Client
	storeRawResponse bool // If true, stores raw Overpass response in TileData (for debugging)
	clipGeomToBbox   bool // If true, uses "out geom(bbox)" - DO NOT USE (known Overpass API bug)
}

// NewOverpassDataSource creates a new Overpass data source with default settings.
// Use NewOverpassDataSourceWithWorkers for configurable parallelism.
func NewOverpassDataSource(endpoint string) *OverpassDataSource {
	return NewOverpassDataSourceWithWorkers(endpoint, 2)
}

// NewOverpassDataSourceWithWorkers creates a new Overpass data source with configurable parallelism.
// workers controls how many parallel requests can be made to the Overpass API.
// For the public overpass-api.de, 2-4 workers is reasonable; for a local instance, use more.
func NewOverpassDataSourceWithWorkers(endpoint string, workers int) *OverpassDataSource {
	cfg := DefaultOverpassConfig()
	if endpoint != "" {
		cfg.Endpoint = endpoint
	}
	if workers > 0 {
		cfg.Workers = workers
	}
	return NewOverpassDataSourceWithConfig(cfg)
}

// NewOverpassDataSourceWithConfig creates a new Overpass data source with full configuration.
// This is the recommended way to create a datasource with retry support.
func NewOverpassDataSourceWithConfig(cfg OverpassConfig) *OverpassDataSource {
	if cfg.Endpoint == "" {
		cfg.Endpoint = "https://overpass-api.de/api/interpreter"
	}
	if cfg.Workers < 1 {
		cfg.Workers = 2
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = defaultHTTPClient()
	}
	httpClient := withResponseLimit(cfg.HTTPClient, cfg.MaxResponseBytes)

	var client overpass.Client
	if cfg.RetryConfig != nil {
		// Use retry-enabled client for resilience
		client = overpass.NewWithRetry(
			cfg.Endpoint,
			cfg.Workers,
			httpClient,
			*cfg.RetryConfig,
		)
	} else {
		// Fall back to non-retry client
		client = overpass.NewWithSettings(
			cfg.Endpoint,
			cfg.Workers,
			httpClient,
		)
	}

	return &OverpassDataSource{
		client:           client,
		storeRawResponse: false, // Don't store raw response by default (saves memory)
		clipGeomToBbox:   false, // Don't clip geometry (prevents artifacts from Overpass bug)
	}
}

// WithRawResponseStorage enables storing the raw Overpass API response in TileData.
// This is useful for debugging but increases memory usage. Should only be used in tests.
func (ds *OverpassDataSource) WithRawResponseStorage(enabled bool) *OverpassDataSource {
	ds.storeRawResponse = enabled
	return ds
}

// WithGeometryClipping enables clipping geometry to bbox in Overpass query.
//
// WARNING: DO NOT USE IN PRODUCTION. This has a known Overpass API bug.
//
// When enabled, uses "out geom(bbox)" which should clip geometry to the bbox boundary.
// However, the Overpass API has a known regression (https://github.com/drolbr/Overpass-API/issues/417)
// where this returns malformed/wrapped geometry for ways not fully contained in the bbox.
// Visual testing confirmed severe rendering artifacts (distorted/wrapped polygons).
//
// This method is kept for potential future use if the Overpass API bug is fixed.
// Default is disabled (false).
func (ds *OverpassDataSource) WithGeometryClipping(enabled bool) *OverpassDataSource {
	ds.clipGeomToBbox = enabled
	return ds
}

// FetchTileData fetches all OSM features for a tile
func (ds *OverpassDataSource) FetchTileData(ctx context.Context, tile types.TileCoordinate) (*types.TileData, error) {
	return ds.FetchTileDataWithBounds(ctx, tile, types.TileToBounds(tile))
}

// FetchTileDataWithBounds fetches OSM features using an explicit bounding box.
// This is useful for "metatile" rendering where we need data slightly outside
// the tile bounds (e.g. to support post-processing blurs without seams).
func (ds *OverpassDataSource) FetchTileDataWithBounds(ctx context.Context, tile types.TileCoordinate, bounds types.BoundingBox) (*types.TileData, error) {
	// Build Overpass QL query with zoom-based filtering
	query := ds.buildTileQuery(bounds, tile.Zoom)

	// Execute the query under the caller's context, so a cancelled or
	// timed-out request actually aborts the in-flight Overpass fetch instead
	// of pinning a fetch worker until the upstream answers.
	result, err := ds.client.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("overpass query failed: %w", err)
	}

	// Convert to feature collection
	features := ExtractFeaturesFromOverpassResult(&result)

	// Validate that we got expected data based on zoom level.
	// At zoom 5-13, we should always have roads/highways in any tile over land.
	// An empty response likely indicates Overpass timeout or incomplete data.
	if err := validateFeatureResponse(features, tile.Zoom); err != nil {
		return nil, err
	}

	tileData := &types.TileData{
		Coordinate: tile,
		Bounds:     bounds,
		Features:   features,
		FetchedAt:  time.Now(),
		Source:     "overpass-api",
	}

	// Only store raw response if explicitly requested (for debugging/tests)
	if ds.storeRawResponse {
		tileData.OverpassResult = &result
	}

	return tileData, nil
}

// buildTileQuery creates a comprehensive Overpass QL query for tile features.
// It fetches COMPLETE unclipped geometry for all ways that intersect the bounding box.
// Features are filtered based on zoom level to reduce data at lower zooms.
//
// IMPORTANT: Uses "out geom qt;" to return COMPLETE geometry (not clipped to bbox).
// This prevents polygon clipping artifacts at tile boundaries.
//
// WARNING: The clipGeomToBbox option is available but should NOT be used due to a known
// Overpass API bug (https://github.com/drolbr/Overpass-API/issues/417) where "out geom(bbox)"
// returns malformed/wrapped geometry for partially-included ways. Visual testing confirmed
// severe rendering artifacts (distorted/wrapped polygons). Only use if the Overpass bug is fixed.
//
// Output modifiers:
// - "geom" returns complete geometry for ways intersecting the bbox
// - "geom(bbox)" clips geometry to bbox (BROKEN - causes malformed geometry)
// - "qt" (quiet) omits metadata (version, changeset, timestamp, user, uid)
func (ds *OverpassDataSource) buildTileQuery(bounds types.BoundingBox, zoom int) string {
	// Build query with all feature types we need.
	// IMPORTANT: We use per-element bbox filters (south,west,north,east) instead of
	// the global [bbox:] setting. When using per-element bbox filters with "out geom",
	// Overpass returns the COMPLETE geometry of ways that intersect the bbox,
	// rather than clipping geometry to the bbox boundary.
	bbox := fmt.Sprintf("%.6f,%.6f,%.6f,%.6f", bounds.MinLat, bounds.MinLon, bounds.MaxLat, bounds.MaxLon)

	// Choose output mode based on clipping setting
	var outputMode string
	if ds.clipGeomToBbox {
		// WARNING: This produces malformed geometry due to Overpass API bug
		outputMode = fmt.Sprintf("out geom(%s) qt;", bbox)
	} else {
		outputMode = "out geom qt;"
	}

	// Build zoom-dependent query parts
	var queryParts []string

	// Water features (blues)
	queryParts = append(queryParts, ds.buildWaterQuery(bbox, zoom)...)

	// Parks/greens features
	queryParts = append(queryParts, ds.buildParksQuery(bbox, zoom)...)

	// Roads features
	queryParts = append(queryParts, ds.buildRoadsQuery(bbox, zoom)...)

	// Railroads features
	queryParts = append(queryParts, ds.buildRailroadsQuery(bbox, zoom)...)

	// Buildings and urban (only at higher zooms)
	queryParts = append(queryParts, ds.buildBuildingsQuery(bbox, zoom)...)

	// Build final query
	query := "[out:json][timeout:60];\n(\n"
	for _, part := range queryParts {
		query += "  " + part + "\n"
	}
	query += ");\n" + outputMode

	return query
}

// buildWaterQuery returns water-related query parts based on zoom level.
// Zoom-based filtering:
//   - All zooms: Coastlines + large water bodies
//   - z10-11: + major rivers
//   - z12-13: + rivers/streams/canals
//   - z14+: All waterways
func (ds *OverpassDataSource) buildWaterQuery(bbox string, zoom int) []string {
	var parts []string

	// Coastlines and water bodies - always include at all zoom levels
	// NOTE: OSM does NOT include ocean polygons in raw data. Ocean is represented
	// as "absence of land". This causes ocean tiles to render as land (tan background).
	// See PLAN.md section 4.10 for ocean rendering solutions (water polygons or synthesis).
	parts = append(parts,
		fmt.Sprintf(`way["natural"="water"](%s);`, bbox),
		fmt.Sprintf(`way["natural"="coastline"](%s);`, bbox),
		fmt.Sprintf(`relation["natural"="water"](%s);`, bbox),
	)

	// Rivers and waterways - progressively add detail
	// Streams excluded at zoom ≤15 as they render too narrow to be useful
	if zoom >= 10 {
		if zoom >= 16 {
			// z16+: All waterways including streams
			parts = append(parts,
				fmt.Sprintf(`way["waterway"](%s);`, bbox),
				fmt.Sprintf(`relation["waterway"](%s);`, bbox),
			)
		} else if zoom >= 12 {
			// z12-15: Rivers and canals only (no streams - too narrow)
			parts = append(parts,
				fmt.Sprintf(`way["waterway"~"river|canal"](%s);`, bbox),
				fmt.Sprintf(`relation["waterway"~"river|canal"](%s);`, bbox),
			)
		} else {
			// z10-11: Major rivers only
			parts = append(parts,
				fmt.Sprintf(`way["waterway"="river"](%s);`, bbox),
				fmt.Sprintf(`relation["waterway"="river"](%s);`, bbox),
			)
		}
	}

	return parts
}

// buildParksQuery returns parks/green space query parts based on zoom level.
// Zoom-based filtering:
//   - All zooms: Large forests and woods (major geographic features)
//   - z8-9: + parks
//   - z10-11: + meadows and grass
//   - z14-15: + gardens
//   - z16+: + playgrounds
func (ds *OverpassDataSource) buildParksQuery(bbox string, zoom int) []string {
	var parts []string

	// Forests and woods - always include at all zoom levels (major geographic features like water)
	// Include both ways and relations for complete coverage of large forest areas
	parts = append(parts,
		fmt.Sprintf(`way["landuse"="forest"](%s);`, bbox),
		fmt.Sprintf(`relation["landuse"="forest"](%s);`, bbox),
		fmt.Sprintf(`way["natural"="wood"](%s);`, bbox),
		fmt.Sprintf(`relation["natural"="wood"](%s);`, bbox),
	)

	if zoom >= 8 {
		// z8+: Add parks, nature reserves, and heath (like Lüneburger Heide)
		parts = append(parts,
			fmt.Sprintf(`way["leisure"="park"](%s);`, bbox),
			fmt.Sprintf(`relation["leisure"="park"](%s);`, bbox),
			fmt.Sprintf(`way["leisure"="nature_reserve"](%s);`, bbox),
			fmt.Sprintf(`relation["leisure"="nature_reserve"](%s);`, bbox),
			fmt.Sprintf(`way["natural"="heath"](%s);`, bbox),
			fmt.Sprintf(`relation["natural"="heath"](%s);`, bbox),
		)
	}

	if zoom >= 10 {
		// z10+: Add grass (landuse=grass only, not natural=grassland or meadow/farmland)
		parts = append(parts,
			fmt.Sprintf(`way["landuse"="grass"](%s);`, bbox),
			fmt.Sprintf(`way["natural"="heath"](%s);`, bbox),
		)
	}

	if zoom >= 14 {
		// z14+: Add gardens and orchards
		parts = append(parts,
			fmt.Sprintf(`way["leisure"="garden"](%s);`, bbox),
			fmt.Sprintf(`way["landuse"="orchard"](%s);`, bbox),
			fmt.Sprintf(`way["landuse"="vineyard"](%s);`, bbox),
		)
	}

	if zoom >= 16 {
		// z16+: Add playgrounds and allotments
		parts = append(parts,
			fmt.Sprintf(`way["leisure"="playground"](%s);`, bbox),
			fmt.Sprintf(`way["landuse"="allotments"](%s);`, bbox),
		)
	}

	return parts
}

// buildRoadsQuery returns road query parts based on zoom level.
// Zoom-based filtering:
//   - z<5: No roads
//   - z5-7: Motorway only
//   - z8-9: Motorway + trunk
//   - z10-11: + primary
//   - z12-13: + secondary, tertiary
//   - z14-15: + residential, unclassified
//   - z16+: All roads
func (ds *OverpassDataSource) buildRoadsQuery(bbox string, zoom int) []string {
	if zoom < 5 {
		// No roads at very low zooms
		return nil
	}

	if zoom < 8 {
		// z5-7: Motorway only for overview
		return []string{
			fmt.Sprintf(`way["highway"~"motorway|motorway_link"](%s);`, bbox),
		}
	}

	if zoom < 10 {
		// z8-9: Motorway + trunk + primary for visibility at low zoom
		return []string{
			fmt.Sprintf(`way["highway"~"motorway|motorway_link|trunk|trunk_link|primary|primary_link"](%s);`, bbox),
		}
	}

	var parts []string

	if zoom >= 16 {
		// z16+: All roads
		parts = append(parts, fmt.Sprintf(`way["highway"](%s);`, bbox))
	} else if zoom >= 14 {
		// z14-15: Major + residential (no service, track, path, footway, etc.)
		parts = append(parts,
			fmt.Sprintf(`way["highway"~"motorway|motorway_link|trunk|trunk_link|primary|primary_link|secondary|secondary_link|tertiary|tertiary_link|residential|unclassified|living_street"](%s);`, bbox),
		)
	} else if zoom >= 12 {
		// z12-13: Major roads + secondary/tertiary
		parts = append(parts,
			fmt.Sprintf(`way["highway"~"motorway|motorway_link|trunk|trunk_link|primary|primary_link|secondary|secondary_link|tertiary|tertiary_link"](%s);`, bbox),
		)
	} else {
		// z10-11: Major roads only
		parts = append(parts,
			fmt.Sprintf(`way["highway"~"motorway|motorway_link|trunk|trunk_link|primary|primary_link"](%s);`, bbox),
		)
	}

	return parts
}

// buildRailroadsQuery returns railroad query parts based on zoom level.
// Zoom-based filtering:
//   - z<9: No railroads
//   - z9-15: Main rail lines only (major railway tracks)
//   - z16: + light_rail
//   - z17+: + subway, tram
func (ds *OverpassDataSource) buildRailroadsQuery(bbox string, zoom int) []string {
	if zoom < 9 {
		// No railroads at very low zooms
		return nil
	}

	if zoom <= 15 {
		// z9-15: Main rail lines only (major railway tracks)
		return []string{
			fmt.Sprintf(`way["railway"="rail"](%s);`, bbox),
		}
	}

	if zoom == 16 {
		// z16: Main rail + light rail
		return []string{
			fmt.Sprintf(`way["railway"~"rail|light_rail"](%s);`, bbox),
		}
	}

	// z17+: All major railway types including subway and tram
	return []string{
		fmt.Sprintf(`way["railway"~"rail|light_rail|subway|tram"](%s);`, bbox),
	}
}

// buildBuildingsQuery returns building and urban area query parts based on zoom level.
// Zoom-based filtering:
//   - z<11: Nothing
//   - z11-13: Urban landuse areas (residential, commercial, industrial, retail)
//   - z14-15: Urban landuse areas + civic amenities (schools, hospitals, universities)
//   - z16+: Individual building footprints + civic amenities (replaces urban areas)
func (ds *OverpassDataSource) buildBuildingsQuery(bbox string, zoom int) []string {
	if zoom < 11 {
		// No buildings or urban areas at very low zooms
		return nil
	}

	var parts []string

	// Urban landuse areas - z11 through z15 only
	// At z16+, individual buildings replace landuse areas
	if zoom >= 11 && zoom <= 15 {
		parts = append(parts,
			fmt.Sprintf(`way["landuse"="residential"](%s);`, bbox),
			fmt.Sprintf(`relation["landuse"="residential"](%s);`, bbox),
			fmt.Sprintf(`way["landuse"="commercial"](%s);`, bbox),
			fmt.Sprintf(`relation["landuse"="commercial"](%s);`, bbox),
			fmt.Sprintf(`way["landuse"="industrial"](%s);`, bbox),
			fmt.Sprintf(`relation["landuse"="industrial"](%s);`, bbox),
			fmt.Sprintf(`way["landuse"="retail"](%s);`, bbox),
			fmt.Sprintf(`relation["landuse"="retail"](%s);`, bbox),
		)
	}

	// Civic buildings - z14+ (as areas at z14-15, as buildings at z16+)
	if zoom >= 14 {
		parts = append(parts,
			fmt.Sprintf(`way["amenity"="school"](%s);`, bbox),
			fmt.Sprintf(`way["amenity"="hospital"](%s);`, bbox),
			fmt.Sprintf(`way["amenity"="university"](%s);`, bbox),
			fmt.Sprintf(`way["amenity"="college"](%s);`, bbox),
			fmt.Sprintf(`way["amenity"="library"](%s);`, bbox),
			fmt.Sprintf(`way["amenity"="town_hall"](%s);`, bbox),
			fmt.Sprintf(`way["leisure"="stadium"](%s);`, bbox),
		)
	}

	// Individual buildings - from z16+ (replaces landuse areas)
	if zoom >= 16 {
		parts = append(parts, fmt.Sprintf(`way["building"](%s);`, bbox))
	}

	return parts
}

// Close cleans up resources (no-op for current version)
func (ds *OverpassDataSource) Close() error {
	return nil
}

// ClearCache is a no-op for current version (no cache support)
func (ds *OverpassDataSource) ClearCache() {
	// No cache in current version
}

// CacheSize returns 0 (no cache in current version)
func (ds *OverpassDataSource) CacheSize() int {
	return 0
}

// ServerConfig defines configuration for a single Overpass server with its coverage area.
type ServerConfig struct {
	// Endpoint is the Overpass API URL
	Endpoint string
	// Workers controls parallelism for this server
	Workers int
	// RetryConfig configures retry behavior
	RetryConfig *overpass.RetryConfig
	// HTTPClient allows custom HTTP client
	HTTPClient *http.Client
	// Coverage defines the geographic area this server covers (nil = covers everything)
	Coverage *types.BoundingBox
	// Name is an optional human-readable name for logging (e.g., "Niedersachsen", "Public")
	Name string
}

// MultiOverpassDataSource routes queries to different Overpass servers based on geography.
// It checks tile coordinates against coverage areas and delegates to the appropriate server.
type MultiOverpassDataSource struct {
	servers []serverInstance
}

type serverInstance struct {
	datasource *OverpassDataSource
	coverage   *types.BoundingBox
	name       string
}

// NewMultiOverpassDataSource creates a datasource that routes to multiple Overpass servers.
// Servers are checked in order; the first server whose coverage contains the tile is used.
// At least one server with nil coverage (default/fallback) should be provided.
//
// Example:
//
//	ds := NewMultiOverpassDataSource(
//	    ServerConfig{
//	        Endpoint: "http://localhost:12345/api/interpreter",
//	        Workers:  10,
//	        Coverage: &types.BoundingBox{MinLat: 51.3, MaxLat: 53.9, MinLon: 6.6, MaxLon: 11.6},
//	        Name:     "Niedersachsen",
//	    },
//	    ServerConfig{
//	        Endpoint: "https://overpass-api.de/api/interpreter",
//	        Workers:  2,
//	        Coverage: nil, // Fallback for rest of world
//	        Name:     "Public",
//	    },
//	)
func NewMultiOverpassDataSource(configs ...ServerConfig) *MultiOverpassDataSource {
	servers := make([]serverInstance, 0, len(configs))

	for _, cfg := range configs {
		// Build OverpassConfig from ServerConfig
		ovConfig := OverpassConfig{
			Endpoint:    cfg.Endpoint,
			Workers:     cfg.Workers,
			RetryConfig: cfg.RetryConfig,
			HTTPClient:  cfg.HTTPClient,
		}

		// Apply defaults if needed
		if ovConfig.Endpoint == "" {
			ovConfig.Endpoint = "https://overpass-api.de/api/interpreter"
		}
		if ovConfig.Workers < 1 {
			ovConfig.Workers = 2
		}
		if ovConfig.RetryConfig == nil {
			defaultRetry := overpass.DefaultRetryConfig()
			ovConfig.RetryConfig = &defaultRetry
		}

		servers = append(servers, serverInstance{
			datasource: NewOverpassDataSourceWithConfig(ovConfig),
			coverage:   cfg.Coverage,
			name:       cfg.Name,
		})
	}

	return &MultiOverpassDataSource{servers: servers}
}

// FetchTileData routes the query to the appropriate Overpass server based on tile location.
func (mds *MultiOverpassDataSource) FetchTileData(ctx context.Context, tile types.TileCoordinate) (*types.TileData, error) {
	bounds := types.TileToBounds(tile)
	return mds.FetchTileDataWithBounds(ctx, tile, bounds)
}

// FetchTileDataWithBounds routes the query to the appropriate Overpass server.
func (mds *MultiOverpassDataSource) FetchTileDataWithBounds(ctx context.Context, tile types.TileCoordinate, bounds types.BoundingBox) (*types.TileData, error) {
	// Find the first server whose coverage contains this tile
	for _, srv := range mds.servers {
		if srv.coverage == nil || intersects(bounds, *srv.coverage) {
			// Found a matching server - delegate to it
			data, err := srv.datasource.FetchTileDataWithBounds(ctx, tile, bounds)
			if err != nil {
				// Include server name in error for debugging
				return nil, fmt.Errorf("[%s] %w", srv.name, err)
			}
			return data, nil
		}
	}

	// No server matched (shouldn't happen if you have a nil-coverage fallback)
	return nil, fmt.Errorf("no overpass server configured for tile %s", tile)
}

// intersects checks if two bounding boxes overlap.
// Returns true if they share any geographic area.
func intersects(a, b types.BoundingBox) bool {
	// Boxes intersect if they overlap in both longitude and latitude
	lonOverlap := a.MinLon <= b.MaxLon && a.MaxLon >= b.MinLon
	latOverlap := a.MinLat <= b.MaxLat && a.MaxLat >= b.MinLat
	return lonOverlap && latOverlap
}

// Close cleans up all underlying datasources.
func (mds *MultiOverpassDataSource) Close() error {
	for _, srv := range mds.servers {
		if err := srv.datasource.Close(); err != nil {
			return err
		}
	}
	return nil
}

// ClearCache clears cache for all underlying datasources.
func (mds *MultiOverpassDataSource) ClearCache() {
	for _, srv := range mds.servers {
		srv.datasource.ClearCache()
	}
}

// CacheSize returns total cache size across all underlying datasources.
func (mds *MultiOverpassDataSource) CacheSize() int {
	total := 0
	for _, srv := range mds.servers {
		total += srv.datasource.CacheSize()
	}
	return total
}

// ErrEmptyOverpassResponse indicates Overpass returned no data when features were expected.
// This is a transient error that should trigger a retry.
var ErrEmptyOverpassResponse = fmt.Errorf("overpass returned empty response")

// validateFeatureResponse checks if the Overpass response contains expected data.
// An empty response at mid-zoom levels likely indicates a timeout or incomplete data.
//
// Zoom level expectations:
//   - z5-7: Skip validation - tiles are huge, many are ocean/empty, and Overpass
//     often rate-limits or times out. Errors are already caught by query failure.
//   - z8-13: Should have SOME features (roads, water, parks, forests)
//   - z14+: May legitimately have no features (e.g., empty forest/field tiles)
//
// We check for ANY features to detect silent Overpass failures that return
// success with empty data (as opposed to explicit 429/504 errors).
func validateFeatureResponse(features types.FeatureCollection, zoom int) error {
	// Count all features including rivers
	totalFeatures := len(features.Water) + len(features.Rivers) + len(features.Parks) +
		len(features.Roads) + len(features.Buildings) + len(features.Urban) + len(features.Civic)

	// At zoom 8-13, if we have ZERO features of any kind, it's suspicious.
	// Real land tiles should have at least forests, parks, water, or roads.
	// We skip z5-7 because those tiles are huge and often legitimately empty (ocean),
	// plus explicit Overpass errors (429, 504) are already handled by retry logic.
	if zoom >= 8 && zoom <= 13 && totalFeatures == 0 {
		return fmt.Errorf("%w: zoom %d tile has no features (expected roads/forests/water)", ErrEmptyOverpassResponse, zoom)
	}

	return nil
}
