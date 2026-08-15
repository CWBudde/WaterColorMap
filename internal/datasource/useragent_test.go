package datasource

import (
	"context"
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

// uaUpstream is a fake Overpass endpoint that records the User-Agent of every
// request it serves.
type uaUpstream struct {
	server *httptest.Server
	agents []string
	mu     sync.Mutex
}

func newUAUpstream(t *testing.T) *uaUpstream {
	t.Helper()

	up := &uaUpstream{}
	up.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up.mu.Lock()
		up.agents = append(up.agents, r.Header.Get("User-Agent"))
		up.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, sampleResponse) //nolint:errcheck // test server
	}))
	t.Cleanup(up.server.Close)
	return up
}

func (u *uaUpstream) seen() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.agents...)
}

// uaSource builds a datasource against endpoint with the given UserAgent.
func uaSource(t *testing.T, endpoint, ua string, cache *ResponseCache) *OverpassDataSource {
	t.Helper()

	cfg := DefaultOverpassConfig()
	cfg.Endpoint = endpoint
	cfg.Workers = 1
	cfg.UserAgent = ua
	cfg.Cache = cache
	cfg.RetryConfig = &overpass.RetryConfig{MaxRetries: 0}
	cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}

	return NewOverpassDataSourceWithConfig(cfg)
}

// TestDefaultUserAgentIsSent is the headline behaviour. The public API rejects
// Go's default UA with 406, so "some UA that is not Go-http-client" is the
// whole point of the change.
func TestDefaultUserAgentIsSent(t *testing.T) {
	up := newUAUpstream(t)
	ds := uaSource(t, up.server.URL, "", nil)

	if _, err := fetch(t, ds); err != nil {
		t.Fatalf("fetch: %v", err)
	}

	got := up.seen()
	if len(got) != 1 {
		t.Fatalf("upstream saw %d requests, want 1", len(got))
	}
	if got[0] != DefaultUserAgent {
		t.Errorf("User-Agent = %q, want %q", got[0], DefaultUserAgent)
	}
	if strings.HasPrefix(got[0], "Go-http-client") {
		t.Errorf("User-Agent is still the Go default: %q", got[0])
	}
}

// TestDefaultUserAgentIsUsable guards the constant itself. An empty or
// anonymous UA is exactly what overpass-api.de refuses, and its usage policy
// asks for a contactable identifier.
func TestDefaultUserAgentIsUsable(t *testing.T) {
	if DefaultUserAgent == "" {
		t.Fatal("DefaultUserAgent is empty")
	}
	if strings.Contains(DefaultUserAgent, "Go-http-client") {
		t.Errorf("DefaultUserAgent must not look like the Go default: %q", DefaultUserAgent)
	}
	if !strings.Contains(DefaultUserAgent, "http") {
		t.Errorf("DefaultUserAgent should carry a contact URL: %q", DefaultUserAgent)
	}
}

func TestConfiguredUserAgentOverridesDefault(t *testing.T) {
	const custom = "MyRenderer/2.0 (+https://example.invalid)"

	up := newUAUpstream(t)
	ds := uaSource(t, up.server.URL, custom, nil)

	if _, err := fetch(t, ds); err != nil {
		t.Fatalf("fetch: %v", err)
	}

	got := up.seen()
	if len(got) != 1 || got[0] != custom {
		t.Errorf("User-Agent = %v, want [%q]", got, custom)
	}
}

// TestUserAgentTransportLeavesExistingHeader covers the branch that matters for
// any future caller which sets its own UA on the request.
func TestUserAgentTransportLeavesExistingHeader(t *testing.T) {
	up := newUAUpstream(t)

	client := withUserAgent(&http.Client{Timeout: 10 * time.Second}, DefaultUserAgent)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, up.server.URL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("User-Agent", "Preset/1.0")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close() //nolint:errcheck // test

	got := up.seen()
	if len(got) != 1 || got[0] != "Preset/1.0" {
		t.Errorf("User-Agent = %v, want [\"Preset/1.0\"]", got)
	}
}

// TestUserAgentTransportDoesNotMutateRequest pins the RoundTripper contract:
// RoundTrip must not modify the request it is handed.
func TestUserAgentTransportDoesNotMutateRequest(t *testing.T) {
	up := newUAUpstream(t)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, up.server.URL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	transport := &userAgentTransport{userAgent: DefaultUserAgent}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	resp.Body.Close() //nolint:errcheck // test

	if ua := req.Header.Get("User-Agent"); ua != "" {
		t.Errorf("RoundTrip mutated the caller's request: User-Agent = %q", ua)
	}
	if got := up.seen(); len(got) != 1 || got[0] != DefaultUserAgent {
		t.Errorf("upstream saw %v, want [%q]", got, DefaultUserAgent)
	}
}

func TestWithUserAgentEmptyLeavesClientUnchanged(t *testing.T) {
	client := &http.Client{Timeout: time.Second}
	if got := withUserAgent(client, ""); got != client {
		t.Error("withUserAgent with an empty agent should return the same client")
	}
}

// TestUserAgentSurvivesCacheAndLimitTransports checks the composition order.
// It asserts on a cache *miss*, because a hit never reaches the network and so
// could never show a header either way.
func TestUserAgentSurvivesCacheAndLimitTransports(t *testing.T) {
	up := newUAUpstream(t)
	cache := testCache(t, nil)
	ds := uaSource(t, up.server.URL, "", cache)

	if _, err := fetch(t, ds); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	// The second fetch is a hit and must not reach the upstream at all.
	if _, err := fetch(t, ds); err != nil {
		t.Fatalf("second fetch: %v", err)
	}

	got := up.seen()
	if len(got) != 1 {
		t.Fatalf("upstream saw %d requests, want 1 (the miss)", len(got))
	}
	if got[0] != DefaultUserAgent {
		t.Errorf("User-Agent on the miss = %q, want %q", got[0], DefaultUserAgent)
	}
}

// TestMultiServerAppliesUserAgentPerServer covers the path the transport was
// chosen for: MultiOverpassDataSource builds its own client per server, so a
// fix inside a single call site would have missed it.
func TestMultiServerAppliesUserAgentPerServer(t *testing.T) {
	regional := newUAUpstream(t)
	fallback := newUAUpstream(t)

	const regionalUA = "Regional/1.0 (+https://example.invalid)"

	coverage := &types.BoundingBox{MinLat: 52.0, MaxLat: 53.0, MinLon: 9.0, MaxLon: 10.0}
	mds := NewMultiOverpassDataSource(
		ServerConfig{
			Endpoint:  regional.server.URL,
			Workers:   1,
			Coverage:  coverage,
			Name:      "Regional",
			UserAgent: regionalUA,
		},
		ServerConfig{
			Endpoint: fallback.server.URL,
			Workers:  1,
			Name:     "Fallback",
		},
	)

	// Inside the regional coverage.
	inside := types.TileCoordinate{Zoom: 13, X: 4321, Y: 2718}
	if _, err := mds.FetchTileDataWithBounds(context.Background(), inside, goldenQueryBounds); err != nil {
		t.Fatalf("regional fetch: %v", err)
	}
	// Far outside it, so the nil-coverage fallback answers.
	outsideBounds := types.BoundingBox{MinLat: -34.0, MaxLat: -33.9, MinLon: 18.4, MaxLon: 18.5}
	outside := types.TileCoordinate{Zoom: 13, X: 1, Y: 1}
	if _, err := mds.FetchTileDataWithBounds(context.Background(), outside, outsideBounds); err != nil {
		t.Fatalf("fallback fetch: %v", err)
	}

	if got := regional.seen(); len(got) != 1 || got[0] != regionalUA {
		t.Errorf("regional server saw %v, want [%q]", got, regionalUA)
	}
	if got := fallback.seen(); len(got) != 1 || got[0] != DefaultUserAgent {
		t.Errorf("fallback server saw %v, want [%q]", got, DefaultUserAgent)
	}
}
