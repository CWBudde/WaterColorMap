package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/spf13/viper"

	"github.com/cwbudde/watercolormap/internal/types"
)

// The regression this guards: newTileDataSource used to hardcode an empty
// endpoint, so `generate` always queried the public overpass-api.de and ignored
// the overpass.servers / overpass.endpoint config that `serve` has always read.
// A configured local instance went unused and every batch run took the public
// API's rate limits.
func TestNewTileDataSourceHonoursConfiguredServer(t *testing.T) {
	hannover := types.TileCoordinate{Zoom: 13, X: 4317, Y: 2692}

	tests := []struct {
		config func(endpoint string)
		name   string
	}{
		{func(endpoint string) {
			viper.Set("overpass.endpoint", endpoint)
		}, "overpass.endpoint"},
		{func(endpoint string) {
			viper.Set("overpass.servers", []map[string]interface{}{
				{"name": "test", "endpoint": endpoint, "workers": 1},
			})
		}, "overpass.servers, single entry"},
		{func(endpoint string) {
			viper.Set("overpass.servers", []map[string]interface{}{
				{
					"name": "regional", "endpoint": endpoint, "workers": 1,
					"coverage": map[string]interface{}{
						"min_lat": 51.3, "max_lat": 53.9, "min_lon": 6.6, "max_lon": 11.6,
					},
				},
			})
		}, "overpass.servers, routed by coverage"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var hits int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits++
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"version":0.6,"elements":[]}`))
			}))
			defer srv.Close()

			viper.Reset()
			t.Cleanup(viper.Reset)
			// Must not be reachable: if the datasource ignores the config again,
			// the request goes to the public API rather than to this test server.
			t.Setenv("WATERCOLORMAP_OVERPASS_ENDPOINT", "http://127.0.0.1:1/should-not-be-used")
			tt.config(srv.URL)

			ds, err := newTileDataSource("overpass")
			if err != nil {
				t.Fatalf("newTileDataSource: %v", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			// The fetch fails on the empty feature set; only the routing matters.
			_, _ = ds.FetchTileData(ctx, hannover)

			if hits == 0 {
				t.Error("the configured server received no request — the config was ignored")
			}
		})
	}
}

func TestNewTileDataSourceRejectsUnknownSource(t *testing.T) {
	if _, err := newTileDataSource("postgis"); err == nil {
		t.Error("expected an error for an unsupported data source")
	}
}
