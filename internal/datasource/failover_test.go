package datasource

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cwbudde/go-overpass"

	"github.com/cwbudde/watercolormap/internal/types"
)

// failoverUpstream is a fake Overpass endpoint with a settable reply.
type failoverUpstream struct {
	server *httptest.Server
	name   string
	calls  atomic.Int32
}

// newFailoverUpstream starts an endpoint whose handler is respond. The handler
// receives the 1-based call number so a server can fail once and then recover.
func newFailoverUpstream(t *testing.T, name string, respond func(n int, w http.ResponseWriter)) *failoverUpstream {
	t.Helper()

	up := &failoverUpstream{name: name}
	up.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		respond(int(up.calls.Add(1)), w)
	}))
	t.Cleanup(up.server.Close)
	return up
}

func respondOK(_ int, w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, sampleResponse) //nolint:errcheck // test server
}

func respond500(_ int, w http.ResponseWriter) {
	http.Error(w, "upstream exploded", http.StatusInternalServerError)
}

// serverCfg builds a ServerConfig with retry disabled, so a test's request
// count is exactly the number of failover attempts and nothing else.
func serverCfg(up *failoverUpstream, coverage *types.BoundingBox) ServerConfig {
	return ServerConfig{
		Endpoint:    up.server.URL,
		Workers:     1,
		Name:        up.name,
		Coverage:    coverage,
		RetryConfig: &overpass.RetryConfig{MaxRetries: 0},
		HTTPClient:  &http.Client{Timeout: 10 * time.Second},
	}
}

// hannoverBox covers the tile every test below asks for.
var hannoverBox = &types.BoundingBox{MinLat: 51.3, MaxLat: 53.9, MinLon: 6.6, MaxLon: 11.6}

func hannoverTile() types.TileCoordinate {
	return types.TileCoordinate{Zoom: 13, X: 4321, Y: 2718}
}

func fetchMulti(t *testing.T, mds *MultiOverpassDataSource) (*types.TileData, error) {
	t.Helper()
	return mds.FetchTileDataWithBounds(context.Background(), hannoverTile(), goldenQueryBounds)
}

// TestFailoverToNextServer is the headline behaviour. Before this, one flaky
// regional container failed every tile inside its coverage box without ever
// trying the nil-coverage fallback.
func TestFailoverToNextServer(t *testing.T) {
	regional := newFailoverUpstream(t, "Regional", respond500)
	fallback := newFailoverUpstream(t, "Public", respondOK)

	mds := NewMultiOverpassDataSource(
		serverCfg(regional, hannoverBox),
		serverCfg(fallback, nil),
	)

	data, err := fetchMulti(t, mds)
	if err != nil {
		t.Fatalf("fetch should have fallen back to the healthy server: %v", err)
	}
	if data == nil {
		t.Fatal("fetch returned no data and no error")
	}
	if got := regional.calls.Load(); got != 1 {
		t.Errorf("regional server saw %d requests, want 1", got)
	}
	if got := fallback.calls.Load(); got != 1 {
		t.Errorf("fallback server saw %d requests, want 1", got)
	}
}

// respondEmpty is a 200 carrying no elements — a legitimate open-sea answer, and
// also the exact shape of a silent Overpass failure over land.
func respondEmpty(_ int, w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"version":0.6,"generator":"Overpass API","elements":[]}`) //nolint:errcheck // test server
}

// TestFailoverOnHungServer covers the distinction shouldTryNextServer draws on
// ctx rather than on the error: a server that never answers trips the client's
// own timeout, which produces a context.DeadlineExceeded-shaped error while the
// caller is still waiting. Reading that as "caller gone" abandoned failover in
// precisely the outage it exists for.
func TestFailoverOnHungServer(t *testing.T) {
	hang := make(chan struct{})

	hung := newFailoverUpstream(t, "Hung", func(_ int, _ http.ResponseWriter) {
		<-hang
	})
	// Registered after the upstream so it runs *before* the server's own Close:
	// cleanups are LIFO, and httptest.Server.Close blocks on the in-flight
	// handler, which would deadlock against the still-open channel.
	t.Cleanup(func() { close(hang) })

	fallback := newFailoverUpstream(t, "Public", respondOK)

	hungCfg := serverCfg(hung, hannoverBox)
	hungCfg.HTTPClient = &http.Client{Timeout: 100 * time.Millisecond}

	mds := NewMultiOverpassDataSource(hungCfg, serverCfg(fallback, nil))

	data, err := fetchMulti(t, mds)
	if err != nil {
		t.Fatalf("a hung server must fail over, not abort the fetch: %v", err)
	}
	if data == nil {
		t.Fatal("fetch returned no data and no error")
	}
	if got := fallback.calls.Load(); got != 1 {
		t.Errorf("fallback server saw %d requests, want 1", got)
	}
}

// TestFailoverOnEmptyResponseWhenEmptyAllowed guards the interaction with ocean
// rendering. AllowEmptyResponses turns the empty mid-zoom response into a
// *success*, so accepting the first result would silently drop empty-response
// failover exactly where it matters: a regional server failing the
// 200-with-no-data way over land.
func TestFailoverOnEmptyResponseWhenEmptyAllowed(t *testing.T) {
	regional := newFailoverUpstream(t, "Regional", respondEmpty)
	fallback := newFailoverUpstream(t, "Public", respondOK)

	regionalCfg := serverCfg(regional, hannoverBox)
	regionalCfg.AllowEmptyResponses = true
	fallbackCfg := serverCfg(fallback, nil)
	fallbackCfg.AllowEmptyResponses = true

	mds := NewMultiOverpassDataSource(regionalCfg, fallbackCfg)

	data, err := fetchMulti(t, mds)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got := fallback.calls.Load(); got != 1 {
		t.Fatalf("fallback server saw %d requests, want 1", got)
	}
	if validateFeatureResponse(data.Features, hannoverTile().Zoom) != nil {
		t.Error("returned the empty response even though a later server had features")
	}
}

// TestEmptyResponseSurvivesWhenEveryServerIsEmpty is the ocean case: nothing is
// wrong, OSM simply does not map the sea. The tile must still come back so the
// ocean polygons render — an error here would be a hole in the map.
func TestEmptyResponseSurvivesWhenEveryServerIsEmpty(t *testing.T) {
	regional := newFailoverUpstream(t, "Regional", respondEmpty)
	fallback := newFailoverUpstream(t, "Public", respondEmpty)

	regionalCfg := serverCfg(regional, hannoverBox)
	regionalCfg.AllowEmptyResponses = true
	fallbackCfg := serverCfg(fallback, nil)
	fallbackCfg.AllowEmptyResponses = true

	mds := NewMultiOverpassDataSource(regionalCfg, fallbackCfg)

	data, err := fetchMulti(t, mds)
	if err != nil {
		t.Fatalf("an all-empty result is the ocean, not a failure: %v", err)
	}
	if data == nil {
		t.Fatal("fetch returned no data and no error")
	}
}

// TestFailoverReportsEveryServerTried keeps the diagnostics useful: an operator
// needs to know that *both* servers failed, and how.
func TestFailoverReportsEveryServerTried(t *testing.T) {
	regional := newFailoverUpstream(t, "Regional", respond500)
	fallback := newFailoverUpstream(t, "Public", respond500)

	mds := NewMultiOverpassDataSource(
		serverCfg(regional, hannoverBox),
		serverCfg(fallback, nil),
	)

	_, err := fetchMulti(t, mds)
	if err == nil {
		t.Fatal("expected an error when every server fails")
	}
	for _, want := range []string{"[Regional]", "[Public]"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %s, got: %v", want, err)
		}
	}
	if got := fallback.calls.Load(); got != 1 {
		t.Errorf("fallback server saw %d requests, want 1", got)
	}
}

// TestHappyPathDoesNotTouchTheFallback pins that failover did not turn routing
// into fan-out: a working regional server must still be the only one asked.
func TestHappyPathDoesNotTouchTheFallback(t *testing.T) {
	regional := newFailoverUpstream(t, "Regional", respondOK)
	fallback := newFailoverUpstream(t, "Public", respondOK)

	mds := NewMultiOverpassDataSource(
		serverCfg(regional, hannoverBox),
		serverCfg(fallback, nil),
	)

	if _, err := fetchMulti(t, mds); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got := regional.calls.Load(); got != 1 {
		t.Errorf("regional server saw %d requests, want 1", got)
	}
	if got := fallback.calls.Load(); got != 0 {
		t.Errorf("fallback server saw %d requests, want 0 — routing must not fan out", got)
	}
}

// TestNoFailoverOnCancelledContext: the caller is gone, so a second server
// cannot help and trying one only holds the process open past shutdown.
func TestNoFailoverOnCancelledContext(t *testing.T) {
	regional := newFailoverUpstream(t, "Regional", respondOK)
	fallback := newFailoverUpstream(t, "Public", respondOK)

	mds := NewMultiOverpassDataSource(
		serverCfg(regional, hannoverBox),
		serverCfg(fallback, nil),
	)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := mds.FetchTileDataWithBounds(ctx, hannoverTile(), goldenQueryBounds); err == nil {
		t.Fatal("expected the cancelled context to fail the fetch")
	}
	if got := fallback.calls.Load(); got != 0 {
		t.Errorf("fallback server saw %d requests, want 0 on a cancelled context", got)
	}
}

// TestNoFailoverOnOversizedResponse: the body is a property of the data and the
// configured cap, not of the server, so the next one would return the same.
func TestNoFailoverOnOversizedResponse(t *testing.T) {
	big := func(_ int, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"elements":[`) //nolint:errcheck // test server
		for i := 0; i < 200; i++ {
			fmt.Fprintf(w, `{"type":"node","id":%d,"lat":52.3,"lon":9.7},`, i)
		}
		io.WriteString(w, `{"type":"node","id":999,"lat":52.3,"lon":9.7}]}`) //nolint:errcheck // test server
	}

	regional := newFailoverUpstream(t, "Regional", big)
	fallback := newFailoverUpstream(t, "Public", respondOK)

	regionalCfg := serverCfg(regional, hannoverBox)
	regionalCfg.MaxResponseBytes = 64 // far below what the handler writes

	mds := NewMultiOverpassDataSource(regionalCfg, serverCfg(fallback, nil))

	_, err := fetchMulti(t, mds)
	if err == nil {
		t.Fatal("expected the oversized response to fail the fetch")
	}
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Errorf("expected ErrResponseTooLarge, got: %v", err)
	}
	if got := fallback.calls.Load(); got != 0 {
		t.Errorf("fallback server saw %d requests, want 0 for an oversized body", got)
	}
}

// TestFailoverSkipsServersOutsideCoverage: failover must not widen routing.
// A server whose coverage excludes the tile is not a candidate at all.
func TestFailoverSkipsServersOutsideCoverage(t *testing.T) {
	elsewhere := newFailoverUpstream(t, "Elsewhere", respondOK)
	regional := newFailoverUpstream(t, "Regional", respondOK)

	// Cape Town — nowhere near the Hannover tile under test.
	farAway := &types.BoundingBox{MinLat: -34.0, MaxLat: -33.0, MinLon: 18.0, MaxLon: 19.0}

	mds := NewMultiOverpassDataSource(
		serverCfg(elsewhere, farAway),
		serverCfg(regional, hannoverBox),
	)

	if _, err := fetchMulti(t, mds); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got := elsewhere.calls.Load(); got != 0 {
		t.Errorf("out-of-coverage server saw %d requests, want 0", got)
	}
	if got := regional.calls.Load(); got != 1 {
		t.Errorf("regional server saw %d requests, want 1", got)
	}
}

func TestNoServerConfiguredForTile(t *testing.T) {
	farAway := &types.BoundingBox{MinLat: -34.0, MaxLat: -33.0, MinLon: 18.0, MaxLon: 19.0}
	elsewhere := newFailoverUpstream(t, "Elsewhere", respondOK)

	mds := NewMultiOverpassDataSource(serverCfg(elsewhere, farAway))

	_, err := fetchMulti(t, mds)
	if err == nil {
		t.Fatal("expected an error when no server covers the tile")
	}
	if !strings.Contains(err.Error(), "no overpass server configured") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestShouldTryNextServer(t *testing.T) {
	// A live caller context: nothing here should be treated as "caller gone".
	tests := []struct {
		err  error
		name string
		want bool
	}{
		{nil, "no error", false},
		{ErrResponseTooLarge, "oversized response", false},
		{fmt.Errorf("[Regional] %w", ErrResponseTooLarge), "wrapped oversized response", false},
		{ErrEmptyOverpassResponse, "empty response", true},
		{errors.New("overpass engine error: 504"), "gateway timeout", true},
		{errors.New("connection refused"), "transport failure", true},

		// The distinction the classification exists for: an http.Client.Timeout
		// error satisfies errors.Is(err, context.DeadlineExceeded) while the
		// caller's context is untouched. That is a hung server, which is exactly
		// what the next server should be asked about.
		{context.DeadlineExceeded, "per-request timeout, caller still waiting", true},
		{fmt.Errorf("Get %q: %w", "http://overpass", context.DeadlineExceeded), "wrapped client timeout", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldTryNextServer(t.Context(), tt.err); got != tt.want {
				t.Errorf("shouldTryNextServer(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestShouldTryNextServerCallerGone pins the other half: once the *caller's*
// context is done, no error is worth another server.
func TestShouldTryNextServerCallerGone(t *testing.T) {
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	for _, err := range []error{
		context.Canceled,
		context.DeadlineExceeded,
		errors.New("connection refused"),
		ErrEmptyOverpassResponse,
	} {
		if shouldTryNextServer(cancelled, err) {
			t.Errorf("shouldTryNextServer(cancelled ctx, %v) = true, want false", err)
		}
	}
}

func TestContains(t *testing.T) {
	outer := types.BoundingBox{MinLat: 50, MaxLat: 54, MinLon: 6, MaxLon: 12}

	tests := []struct {
		name  string
		inner types.BoundingBox
		want  bool
	}{
		{"fully inside", types.BoundingBox{MinLat: 51, MaxLat: 53, MinLon: 7, MaxLon: 11}, true},
		{"identical", outer, true},
		{"partly outside", types.BoundingBox{MinLat: 51, MaxLat: 55, MinLon: 7, MaxLon: 11}, false},
		{"disjoint", types.BoundingBox{MinLat: -34, MaxLat: -33, MinLon: 18, MaxLon: 19}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := contains(outer, tt.inner); got != tt.want {
				t.Errorf("contains() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestWarnUnreachableCoverage covers the construction-time diagnostic. Routing
// takes servers in order, so a coverage box inside an earlier one is shadowed —
// a configuration mistake whose only symptom is quietly paying the public API's
// rate limits for a region you built a local instance for.
func TestWarnUnreachableCoverage(t *testing.T) {
	germany := &types.BoundingBox{MinLat: 47, MaxLat: 55, MinLon: 6, MaxLon: 15}
	hannover := &types.BoundingBox{MinLat: 52, MaxLat: 53, MinLon: 9, MaxLon: 10}
	austria := &types.BoundingBox{MinLat: 46, MaxLat: 49, MinLon: 9, MaxLon: 17}

	tests := []struct {
		name     string
		coverage []*types.BoundingBox
		names    []string
		wantWarn []string
	}{
		{
			name:     "nested box listed after the box containing it",
			coverage: []*types.BoundingBox{germany, hannover},
			names:    []string{"Germany", "Hannover"},
			wantWarn: []string{"Hannover"},
		},
		{
			name:     "nested box listed first is fine",
			coverage: []*types.BoundingBox{hannover, germany},
			names:    []string{"Hannover", "Germany"},
			wantWarn: nil,
		},
		{
			name:     "merely overlapping boxes are legitimate",
			coverage: []*types.BoundingBox{germany, austria},
			names:    []string{"Germany", "Austria"},
			wantWarn: nil,
		},
		{
			name:     "fallback last is the documented layout",
			coverage: []*types.BoundingBox{hannover, nil},
			names:    []string{"Hannover", "Public"},
			wantWarn: nil,
		},
		{
			name:     "fallback first shadows everything after it",
			coverage: []*types.BoundingBox{nil, hannover},
			names:    []string{"Public", "Hannover"},
			wantWarn: []string{"Public", "Hannover"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf strings.Builder
			restore := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
			t.Cleanup(func() { slog.SetDefault(restore) })

			servers := make([]serverInstance, 0, len(tt.coverage))
			for i, cov := range tt.coverage {
				servers = append(servers, serverInstance{coverage: cov, name: tt.names[i]})
			}
			warnUnreachableCoverage(servers)

			got := buf.String()
			if len(tt.wantWarn) == 0 {
				if strings.Contains(got, "level=WARN") {
					t.Errorf("expected no warning, got: %s", got)
				}
				return
			}
			for _, want := range tt.wantWarn {
				if !strings.Contains(got, want) {
					t.Errorf("expected a warning mentioning %q, got: %s", want, got)
				}
			}
		})
	}
}

// TestEmptyResponseFailsOver covers the case the emptiness check exists for: a
// 200 with no data is what a silent Overpass failure looks like, so another
// server is exactly the right response to it.
func TestEmptyResponseFailsOver(t *testing.T) {
	empty := func(_ int, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"version":0.6,"elements":[]}`) //nolint:errcheck // test server
	}

	regional := newFailoverUpstream(t, "Regional", empty)
	fallback := newFailoverUpstream(t, "Public", respondOK)

	mds := NewMultiOverpassDataSource(
		serverCfg(regional, hannoverBox),
		serverCfg(fallback, nil),
	)

	if _, err := fetchMulti(t, mds); err != nil {
		t.Fatalf("an empty regional response should have fallen through: %v", err)
	}
	if got := fallback.calls.Load(); got != 1 {
		t.Errorf("fallback server saw %d requests, want 1", got)
	}
}
