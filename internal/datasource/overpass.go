package datasource

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/cwbudde/go-overpass"

	"github.com/cwbudde/watercolormap/internal/types"
)

// OverpassConfig contains configuration for the Overpass API client.
//
// Fields are ordered for struct alignment, not for reading order.
type OverpassConfig struct {
	// RetryConfig configures retry behavior with exponential backoff
	RetryConfig *overpass.RetryConfig
	// HTTPClient allows custom HTTP client (default: a client with Timeout set)
	HTTPClient *http.Client
	// Endpoint is the Overpass API URL (default: https://overpass-api.de/api/interpreter)
	Endpoint string
	// MaxResponseBytes caps a single Overpass response body. Zero means
	// "unset" and is defaulted to DefaultMaxResponseBytes; a negative value
	// is an explicit opt-out that disables the cap.
	MaxResponseBytes int64
	// Workers controls parallelism (default: 2 for public API, increase for private instances)
	Workers int
}

// PublicEndpoint is the public Overpass API, used whenever no endpoint is
// configured and WATERCOLORMAP_OVERPASS_ENDPOINT is unset.
const PublicEndpoint = "https://overpass-api.de/api/interpreter"

// EndpointEnvVar overrides the endpoint used when a caller passes none.
//
// The CLI resolves its endpoint from config.yaml, but the integration tests
// construct datasources directly and would otherwise always hit the public API —
// which is slow, rate-limited, and currently answers 406. Point this at a local
// instance to run them against it. See docs/local-overpass.md.
const EndpointEnvVar = "WATERCOLORMAP_OVERPASS_ENDPOINT"

// DefaultEndpoint returns the endpoint used when a caller configures none:
// WATERCOLORMAP_OVERPASS_ENDPOINT if set, otherwise PublicEndpoint.
func DefaultEndpoint() string {
	if ep := os.Getenv(EndpointEnvVar); ep != "" {
		return ep
	}
	return PublicEndpoint
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
		Endpoint:         DefaultEndpoint(),
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
	client              overpass.Client
	storeRawResponse    bool // If true, stores raw Overpass response in TileData (for debugging)
	clipGeomToBbox      bool // If true, uses "out geom(bbox)" - DO NOT USE (known Overpass API bug)
	allowEmptyResponses bool // If true, an empty mid-zoom response is a warning, not an error
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
		cfg.Endpoint = DefaultEndpoint()
	}
	if cfg.Workers < 1 {
		cfg.Workers = 2
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = defaultHTTPClient()
	}
	// An omitted cap must not mean "unbounded": callers that build an
	// OverpassConfig literal (the multi-server path does) would otherwise lose
	// the OOM protection silently. Opting out stays possible via a negative value.
	if cfg.MaxResponseBytes == 0 {
		cfg.MaxResponseBytes = DefaultMaxResponseBytes
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

// WithEmptyResponsesAllowed stops treating an empty mid-zoom Overpass response
// as an error. Enable it whenever ocean rendering is configured.
//
// The emptiness check exists to catch silent Overpass failures — a 200 with no
// data — which are otherwise indistinguishable from success. An open-ocean tile
// is exactly that same shape: OSM does not map the sea, so Overpass legitimately
// returns nothing, and the check fails the tile before the ocean polygons ever
// get a chance to render. Telling the two apart would need the water-polygon
// geometry here in Go, which is precisely what the shapefile approach avoids.
// So with ocean data configured we trade the silent-failure detection for
// correct ocean tiles, and log instead of erroring.
func (ds *OverpassDataSource) WithEmptyResponsesAllowed(enabled bool) *OverpassDataSource {
	ds.allowEmptyResponses = enabled
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
		if !ds.allowEmptyResponses {
			return nil, err
		}
		// Ocean rendering is configured, so an empty tile is most likely open
		// sea and must still render. See WithEmptyResponsesAllowed.
		slog.Warn("Overpass returned no features; continuing because ocean rendering is configured",
			"zoom", tile.Zoom, "x", tile.X, "y", tile.Y, "err", err)
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

	// Build zoom-dependent query parts. The layer order is part of the emitted
	// query text, so it is fixed here rather than derived from a map.
	var queryParts []string
	for _, layer := range featureLayers {
		queryParts = append(queryParts, renderRules(layer.rules, bbox, zoom)...)
	}

	// Build final query
	query := "[out:json][timeout:60];\n(\n"
	for _, part := range queryParts {
		query += "  " + part + "\n"
	}
	query += ");\n" + outputMode

	return query
}

// featureRule describes one Overpass element filter and the zoom window in
// which it applies. The rule renders one query line per entry in elems, in
// order, so `way` before `relation` stays byte-stable.
//
// Fields are ordered for struct alignment, not for reading order.
type featureRule struct {
	// filter is the tag selector appended to the element, e.g. `["highway"]`.
	filter string
	// elems lists the Overpass element types to emit, e.g. "way", "relation".
	elems []string
	// minZoom is the lowest zoom at which the rule applies (0 == from z0).
	minZoom int
	// maxZoom is the highest zoom at which the rule applies (0 == unbounded).
	maxZoom int
}

// appliesAt reports whether the rule is active at the given zoom level.
func (r featureRule) appliesAt(zoom int) bool {
	if zoom < r.minZoom {
		return false
	}
	return r.maxZoom == 0 || zoom <= r.maxZoom
}

// featureLayer groups the rules of one thematic layer. Layers are concatenated
// in declaration order, which determines the order of the emitted query lines.
type featureLayer struct {
	name  string
	rules []featureRule
}

// renderRules turns the rules that apply at zoom into Overpass query lines.
func renderRules(rules []featureRule, bbox string, zoom int) []string {
	var parts []string
	for _, rule := range rules {
		if !rule.appliesAt(zoom) {
			continue
		}
		for _, elem := range rule.elems {
			parts = append(parts, fmt.Sprintf("%s%s(%s);", elem, rule.filter, bbox))
		}
	}
	return parts
}

var (
	wayOnly     = []string{"way"}
	wayRelation = []string{"way", "relation"}
)

// featureLayers is the ordered set of layers assembled into every tile query.
var featureLayers = []featureLayer{
	{name: "water", rules: waterRules},
	{name: "parks", rules: parksRules},
	{name: "roads", rules: roadsRules},
	{name: "railroads", rules: railroadsRules},
	{name: "buildings", rules: buildingsRules},
}

// waterRules covers water bodies and waterways.
// Zoom-based filtering:
//   - All zooms: Coastlines + large water bodies
//   - z10-11: + major rivers
//   - z12-15: + rivers/canals (streams excluded, they render too narrow)
//   - z16+: All waterways
//
// NOTE: OSM does NOT include ocean polygons in raw data. Ocean is represented
// as "absence of land". This causes ocean tiles to render as land (tan background).
// See PLAN.md section 4.10 for ocean rendering solutions (water polygons or synthesis).
var waterRules = []featureRule{
	{elems: wayOnly, filter: `["natural"="water"]`},
	{elems: wayOnly, filter: `["natural"="coastline"]`},
	{elems: []string{"relation"}, filter: `["natural"="water"]`},

	{elems: wayRelation, filter: `["waterway"="river"]`, minZoom: 10, maxZoom: 11},
	{elems: wayRelation, filter: `["waterway"~"river|canal"]`, minZoom: 12, maxZoom: 15},
	{elems: wayRelation, filter: `["waterway"]`, minZoom: 16},
}

// parksRules covers parks and other green spaces.
// Zoom-based filtering:
//   - All zooms: Large forests and woods (major geographic features)
//   - z8+: + parks, nature reserves, heath
//   - z10+: + grass
//   - z14+: + gardens, orchards, vineyards
//   - z16+: + playgrounds, allotments
var parksRules = []featureRule{
	// Forests and woods - always included (major geographic features like water).
	// Relations too, for complete coverage of large forest areas.
	{elems: wayRelation, filter: `["landuse"="forest"]`},
	{elems: wayRelation, filter: `["natural"="wood"]`},

	{elems: wayRelation, filter: `["leisure"="park"]`, minZoom: 8},
	{elems: wayRelation, filter: `["leisure"="nature_reserve"]`, minZoom: 8},
	{elems: wayRelation, filter: `["natural"="heath"]`, minZoom: 8},

	// landuse=grass only, not natural=grassland or meadow/farmland.
	{elems: wayOnly, filter: `["landuse"="grass"]`, minZoom: 10},

	{elems: wayOnly, filter: `["leisure"="garden"]`, minZoom: 14},
	{elems: wayOnly, filter: `["landuse"="orchard"]`, minZoom: 14},
	{elems: wayOnly, filter: `["landuse"="vineyard"]`, minZoom: 14},

	{elems: wayOnly, filter: `["leisure"="playground"]`, minZoom: 16},
	{elems: wayOnly, filter: `["landuse"="allotments"]`, minZoom: 16},
}

// roadsRules covers the highway network. The zoom windows are exclusive: each
// zoom level matches exactly one rule, and each regex is a superset of the
// previous one.
// Zoom-based filtering:
//   - z<5: No roads
//   - z5-7: Motorway only
//   - z8-11: + trunk, primary
//   - z12-13: + secondary, tertiary
//   - z14-15: + residential, unclassified, living_street
//   - z16+: All roads
//
// Including primary from z8 is intentional: an older description claimed
// "motorway + trunk" for z8-9, but the shipped regex has always matched primary
// too, and the goldens pin that behaviour — do not "correct" it back.
var roadsRules = []featureRule{
	{elems: wayOnly, filter: `["highway"~"motorway|motorway_link"]`, minZoom: 5, maxZoom: 7},
	{
		elems:   wayOnly,
		filter:  `["highway"~"motorway|motorway_link|trunk|trunk_link|primary|primary_link"]`,
		minZoom: 8,
		maxZoom: 11,
	},
	{
		elems: wayOnly,
		filter: `["highway"~"motorway|motorway_link|trunk|trunk_link|primary|primary_link|` +
			`secondary|secondary_link|tertiary|tertiary_link"]`,
		minZoom: 12,
		maxZoom: 13,
	},
	// z14-15: Major + residential (no service, track, path, footway, etc.)
	{
		elems: wayOnly,
		filter: `["highway"~"motorway|motorway_link|trunk|trunk_link|primary|primary_link|` +
			`secondary|secondary_link|tertiary|tertiary_link|residential|unclassified|living_street"]`,
		minZoom: 14,
		maxZoom: 15,
	},
	{elems: wayOnly, filter: `["highway"]`, minZoom: 16},
}

// railroadsRules covers railway lines. The zoom windows are exclusive.
// Zoom-based filtering:
//   - z<9: No railroads
//   - z9-15: Main rail lines only (major railway tracks)
//   - z16: + light_rail
//   - z17+: + subway, tram
var railroadsRules = []featureRule{
	{elems: wayOnly, filter: `["railway"="rail"]`, minZoom: 9, maxZoom: 15},
	{elems: wayOnly, filter: `["railway"~"rail|light_rail"]`, minZoom: 16, maxZoom: 16},
	{elems: wayOnly, filter: `["railway"~"rail|light_rail|subway|tram"]`, minZoom: 17},
}

// buildingsRules covers buildings and urban areas.
// Zoom-based filtering:
//   - z<11: Nothing
//   - z11-15: Urban landuse areas (residential, commercial, industrial, retail)
//   - z14-15: + civic amenities (schools, hospitals, universities, ...)
//   - z16+: Individual building footprints + civic amenities (landuse areas drop out)
//
// Campuses (schools, hospitals, universities) are frequently mapped as
// multipolygon relations, so relations are queried alongside ways.
var buildingsRules = []featureRule{
	{elems: wayRelation, filter: `["landuse"="residential"]`, minZoom: 11, maxZoom: 15},
	{elems: wayRelation, filter: `["landuse"="commercial"]`, minZoom: 11, maxZoom: 15},
	{elems: wayRelation, filter: `["landuse"="industrial"]`, minZoom: 11, maxZoom: 15},
	{elems: wayRelation, filter: `["landuse"="retail"]`, minZoom: 11, maxZoom: 15},

	{elems: wayRelation, filter: `["amenity"="school"]`, minZoom: 14},
	{elems: wayRelation, filter: `["amenity"="hospital"]`, minZoom: 14},
	{elems: wayRelation, filter: `["amenity"="university"]`, minZoom: 14},
	{elems: wayRelation, filter: `["amenity"="college"]`, minZoom: 14},
	{elems: wayRelation, filter: `["amenity"="library"]`, minZoom: 14},
	{elems: wayRelation, filter: `["amenity"="town_hall"]`, minZoom: 14},
	{elems: wayRelation, filter: `["leisure"="stadium"]`, minZoom: 14},

	{elems: wayOnly, filter: `["building"]`, minZoom: 16},
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
//
// Fields are ordered for struct alignment, not for reading order.
type ServerConfig struct {
	// RetryConfig configures retry behavior
	RetryConfig *overpass.RetryConfig
	// HTTPClient allows custom HTTP client
	HTTPClient *http.Client
	// Coverage defines the geographic area this server covers (nil = covers everything)
	Coverage *types.BoundingBox
	// Endpoint is the Overpass API URL
	Endpoint string
	// Name is an optional human-readable name for logging (e.g., "Niedersachsen", "Public")
	Name string
	// MaxResponseBytes caps a single Overpass response body. Zero means
	// "unset" and is defaulted to DefaultMaxResponseBytes; a negative value
	// disables the cap.
	MaxResponseBytes int64
	// Workers controls parallelism for this server
	Workers int
	// AllowEmptyResponses stops treating an empty mid-zoom response as an error.
	// See OverpassDataSource.WithEmptyResponsesAllowed.
	AllowEmptyResponses bool
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
			Endpoint:         cfg.Endpoint,
			Workers:          cfg.Workers,
			RetryConfig:      cfg.RetryConfig,
			HTTPClient:       cfg.HTTPClient,
			MaxResponseBytes: cfg.MaxResponseBytes,
		}

		// Apply defaults if needed
		if ovConfig.Endpoint == "" {
			ovConfig.Endpoint = DefaultEndpoint()
		}
		if ovConfig.Workers < 1 {
			ovConfig.Workers = 2
		}
		if ovConfig.RetryConfig == nil {
			defaultRetry := overpass.DefaultRetryConfig()
			ovConfig.RetryConfig = &defaultRetry
		}

		servers = append(servers, serverInstance{
			datasource: NewOverpassDataSourceWithConfig(ovConfig).
				WithEmptyResponsesAllowed(cfg.AllowEmptyResponses),
			coverage: cfg.Coverage,
			name:     cfg.Name,
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
		len(features.Roads) + len(features.Railroads) + len(features.Buildings) +
		len(features.Urban) + len(features.Civic)

	// At zoom 8-13, if we have ZERO features of any kind, it's suspicious.
	// Real land tiles should have at least forests, parks, water, or roads.
	// We skip z5-7 because those tiles are huge and often legitimately empty (ocean),
	// plus explicit Overpass errors (429, 504) are already handled by retry logic.
	if zoom >= 8 && zoom <= 13 && totalFeatures == 0 {
		return fmt.Errorf("%w: zoom %d tile has no features (expected roads/forests/water)", ErrEmptyOverpassResponse, zoom)
	}

	return nil
}
