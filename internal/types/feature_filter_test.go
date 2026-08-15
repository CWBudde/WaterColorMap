package types

import (
	"testing"

	"github.com/paulmach/orb"
)

func lineFeature(id string, coords ...orb.Point) Feature {
	return Feature{ID: id, Type: FeatureTypeRoad, Geometry: orb.LineString(coords)}
}

func ids(features []Feature) []string {
	out := make([]string, 0, len(features))
	for _, f := range features {
		out = append(out, f.ID)
	}
	return out
}

func equalIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestFilterByBounds(t *testing.T) {
	box := BoundingBox{MinLon: 0, MinLat: 0, MaxLon: 10, MaxLat: 10}

	tests := []struct {
		name  string
		input []Feature
		want  []string
	}{
		{
			name:  "inside is kept",
			input: []Feature{lineFeature("a", orb.Point{1, 1}, orb.Point{2, 2})},
			want:  []string{"a"},
		},
		{
			name:  "wholly outside is dropped",
			input: []Feature{lineFeature("a", orb.Point{20, 20}, orb.Point{21, 21})},
			want:  nil,
		},
		{
			// The reason band fetching is safe: a way crossing the edge is
			// exactly what per-tile fetching would also have returned, because
			// Overpass matches on intersection and returns unclipped geometry.
			name:  "straddling the edge is kept",
			input: []Feature{lineFeature("a", orb.Point{-5, 5}, orb.Point{5, 5})},
			want:  []string{"a"},
		},
		{
			name:  "touching the edge is kept",
			input: []Feature{lineFeature("a", orb.Point{10, 10}, orb.Point{12, 12})},
			want:  []string{"a"},
		},
		{
			name: "order is preserved",
			input: []Feature{
				lineFeature("a", orb.Point{1, 1}, orb.Point{2, 2}),
				lineFeature("far", orb.Point{50, 50}, orb.Point{51, 51}),
				lineFeature("b", orb.Point{3, 3}, orb.Point{4, 4}),
				lineFeature("c", orb.Point{5, 5}, orb.Point{6, 6}),
			},
			want: []string{"a", "b", "c"},
		},
		{
			// Untestable rather than absent; dropping data silently is the
			// worse of the two failures.
			name:  "nil geometry is kept",
			input: []Feature{{ID: "a", Type: FeatureTypeRoad}},
			want:  []string{"a"},
		},
		{
			name:  "empty input",
			input: nil,
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := FeatureCollection{Roads: tt.input}
			got := fc.FilterByBounds(box)
			if !equalIDs(ids(got.Roads), tt.want) {
				t.Errorf("kept %v, want %v", ids(got.Roads), tt.want)
			}
		})
	}
}

// TestFilterByBoundsEmptyLayerIsNil pins the property the renderer depends on:
// a layer filtered down to nothing must be indistinguishable from one that was
// never fetched, because the renderer skips an absent layer and paints a
// present-but-empty one.
func TestFilterByBoundsEmptyLayerIsNil(t *testing.T) {
	fc := FeatureCollection{
		Roads: []Feature{lineFeature("far", orb.Point{50, 50}, orb.Point{51, 51})},
	}
	got := fc.FilterByBounds(BoundingBox{MinLon: 0, MinLat: 0, MaxLon: 1, MaxLat: 1})
	if got.Roads != nil {
		t.Errorf("filtered-out layer = %v, want nil", got.Roads)
	}
}

// TestFilterByBoundsCoversEveryLayer guards against a new layer being added to
// FeatureCollection and quietly not being filtered — which would hand every
// tile the whole band's features for that one layer.
func TestFilterByBoundsCoversEveryLayer(t *testing.T) {
	near := lineFeature("near", orb.Point{1, 1}, orb.Point{2, 2})
	far := lineFeature("far", orb.Point{50, 50}, orb.Point{51, 51})
	both := []Feature{near, far}

	fc := FeatureCollection{
		Water: both, Rivers: both, Parks: both, Roads: both, Railroads: both,
		Buildings: both, Urban: both, Civic: both, Land: both,
	}

	got := fc.FilterByBounds(BoundingBox{MinLon: 0, MinLat: 0, MaxLon: 10, MaxLat: 10})

	layers := map[string][]Feature{
		"Water": got.Water, "Rivers": got.Rivers, "Parks": got.Parks,
		"Roads": got.Roads, "Railroads": got.Railroads, "Buildings": got.Buildings,
		"Urban": got.Urban, "Civic": got.Civic, "Land": got.Land,
	}
	for name, features := range layers {
		if len(features) != 1 || features[0].ID != "near" {
			t.Errorf("layer %s = %v, want [near] — is it missing from FilterByBounds?", name, ids(features))
		}
	}

	// And the total has to match, so a newly added layer shows up here too.
	if got.Count() != len(layers) {
		t.Errorf("Count() = %d, want %d — FeatureCollection gained a layer that FilterByBounds does not handle",
			got.Count(), len(layers))
	}
}

// TestCountIncludesRivers pins the fix for a long-standing omission: Count()
// left Rivers out, so a tile whose only features are waterways counted as
// empty.
func TestCountIncludesRivers(t *testing.T) {
	fc := FeatureCollection{Rivers: []Feature{lineFeature("r", orb.Point{1, 1}, orb.Point{2, 2})}}
	if got := fc.Count(); got != 1 {
		t.Errorf("Count() = %d, want 1 — Rivers is missing from the sum", got)
	}
}
