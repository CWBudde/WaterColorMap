package datasource

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cwbudde/watercolormap/internal/types"
)

// emptyOverpassServer answers every query with a well-formed but featureless
// response — what the real API returns for an open-ocean tile, since OSM maps
// no ocean at all.
func emptyOverpassServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"version":0.6,"generator":"test","elements":[]}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestValidateFeatureResponseZoomBands(t *testing.T) {
	empty := types.FeatureCollection{}

	tests := []struct {
		name      string
		zoom      int
		wantError bool
	}{
		{name: "z7 is exempt: huge tiles are often legitimately empty", zoom: 7},
		{name: "z8 is the start of the checked band", zoom: 8, wantError: true},
		{name: "z13 is the end of the checked band", zoom: 13, wantError: true},
		{name: "z14 is exempt: an empty field tile is normal", zoom: 14},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateFeatureResponse(empty, tt.zoom)
			if tt.wantError && !errors.Is(err, ErrEmptyOverpassResponse) {
				t.Errorf("zoom %d: got %v, want ErrEmptyOverpassResponse", tt.zoom, err)
			}
			if !tt.wantError && err != nil {
				t.Errorf("zoom %d: got %v, want no error", tt.zoom, err)
			}
		})
	}
}

// An open-ocean tile at z8-13 returns nothing from Overpass. Without the opt-out
// the fetch fails there and the ocean polygons never get a chance to render —
// blocker #1 of PLAN.md 4.10.
func TestFetchTileDataEmptyResponseHonoursOceanOptOut(t *testing.T) {
	srv := emptyOverpassServer(t)

	// z9_x266_y164 is the North Sea tile named in PLAN.md 4.10; use a zoom
	// inside the validated band so the check actually fires.
	oceanTile := types.TileCoordinate{Zoom: 9, X: 266, Y: 164}
	if err := validateFeatureResponse(types.FeatureCollection{}, oceanTile.Zoom); err == nil {
		t.Fatal("test premise broken: z9 should be inside the validated band")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	t.Run("rejected by default", func(t *testing.T) {
		ds := NewOverpassDataSource(srv.URL)
		if _, err := ds.FetchTileData(ctx, oceanTile); !errors.Is(err, ErrEmptyOverpassResponse) {
			t.Errorf("got %v, want ErrEmptyOverpassResponse", err)
		}
	})

	t.Run("allowed when ocean rendering is configured", func(t *testing.T) {
		ds := NewOverpassDataSource(srv.URL).WithEmptyResponsesAllowed(true)
		data, err := ds.FetchTileData(ctx, oceanTile)
		if err != nil {
			t.Fatalf("an empty ocean tile must still fetch: %v", err)
		}
		if data == nil {
			t.Fatal("expected tile data")
		}
		if got := data.Features.Count(); got != 0 {
			t.Errorf("expected an empty feature collection, got %d features", got)
		}
	})
}
