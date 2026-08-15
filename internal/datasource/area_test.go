package datasource

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cwbudde/go-overpass"

	"github.com/cwbudde/watercolormap/internal/types"
)

// queryRecorder is a fake Overpass that keeps the query text it was sent.
type queryRecorder struct {
	server  *httptest.Server
	queries []string
	mu      sync.Mutex
}

func newQueryRecorder(t *testing.T, body string) *queryRecorder {
	t.Helper()

	rec := &queryRecorder{}
	rec.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err == nil {
			rec.mu.Lock()
			rec.queries = append(rec.queries, r.FormValue("data"))
			rec.mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body) //nolint:errcheck // test server
	}))
	t.Cleanup(rec.server.Close)
	return rec
}

func (q *queryRecorder) seen() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]string(nil), q.queries...)
}

func areaSource(t *testing.T, endpoint string) *OverpassDataSource {
	t.Helper()

	cfg := DefaultOverpassConfig()
	cfg.Endpoint = endpoint
	cfg.Workers = 1
	cfg.RetryConfig = &overpass.RetryConfig{MaxRetries: 0}
	cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	return NewOverpassDataSourceWithConfig(cfg)
}

// TestAreaFetchQueryMatchesTileFetch is the load-bearing compatibility test.
// FetchTileDataWithBounds was reimplemented on top of FetchAreaData, and if the
// emitted query text moved by even a byte, every response-cache entry would be
// invalidated and every golden query file would need regenerating.
//
// It also pins that the default server-side timeout is still 60, which is the
// part of the query most likely to drift now that it is a parameter.
func TestAreaFetchQueryMatchesTileFetch(t *testing.T) {
	up := newQueryRecorder(t, sampleResponse)
	ds := areaSource(t, up.server.URL)

	tile := types.TileCoordinate{Zoom: 13, X: 4321, Y: 2718}
	if _, err := ds.FetchTileDataWithBounds(context.Background(), tile, goldenQueryBounds); err != nil {
		t.Fatalf("tile fetch: %v", err)
	}
	if _, err := ds.FetchAreaData(context.Background(), tile.Zoom, goldenQueryBounds, AreaFetchOptions{}); err != nil {
		t.Fatalf("area fetch: %v", err)
	}

	queries := up.seen()
	if len(queries) != 2 {
		t.Fatalf("upstream saw %d queries, want 2", len(queries))
	}
	if queries[0] != queries[1] {
		t.Errorf("area fetch emits different query text than tile fetch.\n--- tile ---\n%s\n--- area ---\n%s",
			queries[0], queries[1])
	}
	if !strings.Contains(queries[0], "[out:json][timeout:60]") {
		t.Errorf("default query timeout is no longer 60; every golden query moved:\n%s", queries[0])
	}
}

// TestAreaFetchTimeoutOnlyChangesTheHeader: a band asks for a longer timeout,
// and that must be the only difference, or band queries would stop sharing
// anything with their per-tile equivalents.
func TestAreaFetchTimeoutOnlyChangesTheHeader(t *testing.T) {
	up := newQueryRecorder(t, sampleResponse)
	ds := areaSource(t, up.server.URL)

	ctx := context.Background()
	if _, err := ds.FetchAreaData(ctx, 13, goldenQueryBounds, AreaFetchOptions{}); err != nil {
		t.Fatalf("default fetch: %v", err)
	}
	if _, err := ds.FetchAreaData(ctx, 13, goldenQueryBounds, AreaFetchOptions{TimeoutSec: 180}); err != nil {
		t.Fatalf("long fetch: %v", err)
	}

	queries := up.seen()
	if len(queries) != 2 {
		t.Fatalf("upstream saw %d queries, want 2", len(queries))
	}

	normalised := strings.Replace(queries[1], "[timeout:180]", "[timeout:60]", 1)
	if normalised != queries[0] {
		t.Errorf("the timeout changed more than the header.\n--- default ---\n%s\n--- long ---\n%s",
			queries[0], queries[1])
	}
}

func TestAreaFetchZeroTimeoutUsesTheDefault(t *testing.T) {
	up := newQueryRecorder(t, sampleResponse)
	ds := areaSource(t, up.server.URL)

	if _, err := ds.FetchAreaData(context.Background(), 13, goldenQueryBounds, AreaFetchOptions{TimeoutSec: 0}); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if q := up.seen(); len(q) != 1 || !strings.Contains(q[0], "[timeout:60]") {
		t.Errorf("zero timeout did not fall back to the default: %v", q)
	}
}

// TestAreaFetchSkipsEmptyValidation: the zero-feature check is a per-tile
// policy, and a band must not fail sixteen tiles because the block happened to
// be empty. The caller re-checks each slice instead.
func TestAreaFetchSkipsEmptyValidation(t *testing.T) {
	const empty = `{"version":0.6,"elements":[]}`

	up := newQueryRecorder(t, empty)
	ds := areaSource(t, up.server.URL)

	// z12 is inside the window where an empty response is an error.
	if _, err := ds.FetchAreaData(context.Background(), 12, goldenQueryBounds, AreaFetchOptions{}); err == nil {
		t.Error("without SkipEmptyValidation an empty mid-zoom response should still fail")
	}
	if _, err := ds.FetchAreaData(context.Background(), 12, goldenQueryBounds,
		AreaFetchOptions{SkipEmptyValidation: true}); err != nil {
		t.Errorf("with SkipEmptyValidation an empty response should be returned, got: %v", err)
	}
}

// TestAreaFetchLeavesCoordinateUnset: an area is not a tile, and a fabricated
// coordinate would be a lie that something downstream could come to rely on.
func TestAreaFetchLeavesCoordinateUnset(t *testing.T) {
	up := newQueryRecorder(t, sampleResponse)
	ds := areaSource(t, up.server.URL)

	data, err := ds.FetchAreaData(context.Background(), 13, goldenQueryBounds, AreaFetchOptions{})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if data.Coordinate != (types.TileCoordinate{}) {
		t.Errorf("area fetch set a coordinate: %+v", data.Coordinate)
	}
	if data.Bounds != goldenQueryBounds {
		t.Errorf("bounds = %+v, want %+v", data.Bounds, goldenQueryBounds)
	}
}

// TestTileFetchStillSetsCoordinate: the wrapper has to keep filling it in.
func TestTileFetchStillSetsCoordinate(t *testing.T) {
	up := newQueryRecorder(t, sampleResponse)
	ds := areaSource(t, up.server.URL)

	tile := types.TileCoordinate{Zoom: 13, X: 4321, Y: 2718}
	data, err := ds.FetchTileDataWithBounds(context.Background(), tile, goldenQueryBounds)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if data.Coordinate != tile {
		t.Errorf("coordinate = %+v, want %+v", data.Coordinate, tile)
	}
}

// TestMultiServerAreaFetchRequiresContainment is the hazard banding introduces.
// Per-tile routing takes any server whose coverage merely overlaps; at band
// scale that could hand sixteen tiles to a server holding data for a corner of
// the block and nothing else.
func TestMultiServerAreaFetchRequiresContainment(t *testing.T) {
	regional := newQueryRecorder(t, sampleResponse)

	// Covers the eastern half of the area asked for below.
	coverage := &types.BoundingBox{MinLat: 52.0, MaxLat: 53.0, MinLon: 9.75, MaxLon: 11.0}
	mds := NewMultiOverpassDataSource(ServerConfig{
		Endpoint:    regional.server.URL,
		Workers:     1,
		Name:        "Regional",
		Coverage:    coverage,
		RetryConfig: &overpass.RetryConfig{MaxRetries: 0},
		HTTPClient:  &http.Client{Timeout: 10 * time.Second},
	})

	spanning := types.BoundingBox{MinLat: 52.3, MaxLat: 52.5, MinLon: 9.6, MaxLon: 9.9}
	_, err := mds.FetchAreaData(context.Background(), 13, spanning, AreaFetchOptions{})
	if !errors.Is(err, ErrAreaSpansServers) {
		t.Errorf("expected ErrAreaSpansServers for a partly-covered area, got: %v", err)
	}
	if got := regional.seen(); len(got) != 0 {
		t.Errorf("a partly-covering server was queried anyway (%d times)", len(got))
	}

	// Fully inside, so the same server answers.
	inside := types.BoundingBox{MinLat: 52.3, MaxLat: 52.5, MinLon: 9.8, MaxLon: 9.9}
	if _, err := mds.FetchAreaData(context.Background(), 13, inside, AreaFetchOptions{}); err != nil {
		t.Fatalf("a fully contained area should be answered: %v", err)
	}
	if got := regional.seen(); len(got) != 1 {
		t.Errorf("regional server saw %d queries, want 1", len(got))
	}
}

// TestMultiServerAreaFetchUsesNilCoverageFallback: a server with no coverage
// covers everything, band boxes included.
func TestMultiServerAreaFetchUsesNilCoverageFallback(t *testing.T) {
	regional := newQueryRecorder(t, sampleResponse)
	fallback := newQueryRecorder(t, sampleResponse)

	coverage := &types.BoundingBox{MinLat: 52.0, MaxLat: 53.0, MinLon: 9.75, MaxLon: 11.0}
	mds := NewMultiOverpassDataSource(
		ServerConfig{
			Endpoint: regional.server.URL, Workers: 1, Name: "Regional", Coverage: coverage,
			RetryConfig: &overpass.RetryConfig{MaxRetries: 0},
			HTTPClient:  &http.Client{Timeout: 10 * time.Second},
		},
		ServerConfig{
			Endpoint: fallback.server.URL, Workers: 1, Name: "Public",
			RetryConfig: &overpass.RetryConfig{MaxRetries: 0},
			HTTPClient:  &http.Client{Timeout: 10 * time.Second},
		},
	)

	spanning := types.BoundingBox{MinLat: 52.3, MaxLat: 52.5, MinLon: 9.6, MaxLon: 9.9}
	if _, err := mds.FetchAreaData(context.Background(), 13, spanning, AreaFetchOptions{}); err != nil {
		t.Fatalf("the nil-coverage fallback should answer: %v", err)
	}
	if got := regional.seen(); len(got) != 0 {
		t.Errorf("the partly-covering regional server was queried (%d times)", len(got))
	}
	if got := fallback.seen(); len(got) != 1 {
		t.Errorf("fallback saw %d queries, want 1", len(got))
	}
}

// TestAreaFetchRespectsResponseCap: a band is many tiles' worth of data, so the
// size cap is exactly what the adaptive split needs to fire on.
func TestAreaFetchRespectsResponseCap(t *testing.T) {
	up := newQueryRecorder(t, strings.Repeat(sampleResponse, 20))

	cfg := DefaultOverpassConfig()
	cfg.Endpoint = up.server.URL
	cfg.Workers = 1
	cfg.MaxResponseBytes = 64
	cfg.RetryConfig = &overpass.RetryConfig{MaxRetries: 0}
	cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	ds := NewOverpassDataSourceWithConfig(cfg)

	_, err := ds.FetchAreaData(context.Background(), 13, goldenQueryBounds, AreaFetchOptions{})
	if err == nil {
		t.Fatal("an oversized band response should fail")
	}
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Errorf("expected ErrResponseTooLarge, got: %v", err)
	}
}
