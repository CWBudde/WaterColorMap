package datasource

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The environment default is what points integration tests at a local Overpass:
// they build their datasource directly and never see config.yaml. The contract
// has three parts — env used when no endpoint is configured, public API when the
// env is unset, and an explicit endpoint always winning — so all three are
// pinned here.

func TestDefaultEndpoint_UsesEnvVar(t *testing.T) {
	t.Setenv(EndpointEnvVar, "http://localhost:12345/api/interpreter")

	if got := DefaultEndpoint(); got != "http://localhost:12345/api/interpreter" {
		t.Errorf("DefaultEndpoint() = %q, want the environment value", got)
	}
}

func TestDefaultEndpoint_FallsBackToPublic(t *testing.T) {
	t.Setenv(EndpointEnvVar, "")

	if got := DefaultEndpoint(); got != PublicEndpoint {
		t.Errorf("DefaultEndpoint() = %q, want %q", got, PublicEndpoint)
	}
}

// The env var must only supply the *default*. A caller that names an endpoint —
// an overpass.servers entry, or a direct constructor call — still wins, which is
// what keeps CI behaviour unchanged when the variable happens to be set.
func TestConstructorEndpointPrecedence(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		wantEnv  bool // true: the request must land on the env server
	}{
		{"empty endpoint uses the environment", "", true},
		{"explicit endpoint wins over the environment", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var envHits, explicitHits int
			envSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				envHits++
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"version":0.6,"elements":[]}`))
			}))
			defer envSrv.Close()
			explicitSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				explicitHits++
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"version":0.6,"elements":[]}`))
			}))
			defer explicitSrv.Close()

			t.Setenv(EndpointEnvVar, envSrv.URL)

			endpoint := tt.endpoint
			if !tt.wantEnv {
				endpoint = explicitSrv.URL
			}

			ds := NewOverpassDataSourceWithWorkers(endpoint, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			// The fetch itself is expected to fail: validateFeatureResponse
			// rejects a featureless z13 tile, and these stub servers return
			// none. That is irrelevant here — the question is only which host
			// the request was addressed to, and it had to complete the round
			// trip to be rejected at all.
			coord, bounds := testTile()
			_, _ = ds.FetchTileDataWithBounds(ctx, coord, bounds)

			if tt.wantEnv && envHits == 0 {
				t.Errorf("env server got %d requests, explicit got %d; wanted the env one", envHits, explicitHits)
			}
			if !tt.wantEnv && explicitHits == 0 {
				t.Errorf("explicit server got %d requests, env got %d; wanted the explicit one", explicitHits, envHits)
			}
		})
	}
}
