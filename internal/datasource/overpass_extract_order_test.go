package datasource

import (
	"fmt"
	"testing"

	"github.com/cwbudde/go-overpass"

	"github.com/cwbudde/watercolormap/internal/types"
)

// orderFixture builds a result whose ways and relations land in several
// different buckets, with IDs deliberately not in insertion order so that a
// sort is observably doing something.
func orderFixture() *overpass.Result {
	square := func(lat, lon float64) []overpass.Point {
		return []overpass.Point{
			{Lat: lat, Lon: lon},
			{Lat: lat, Lon: lon + 0.01},
			{Lat: lat + 0.01, Lon: lon + 0.01},
			{Lat: lat + 0.01, Lon: lon},
			{Lat: lat, Lon: lon},
		}
	}
	way := func(id int64, tags map[string]string, lat, lon float64) *overpass.Way {
		return &overpass.Way{
			Meta:     overpass.Meta{ID: id, Tags: tags},
			Geometry: square(lat, lon),
		}
	}

	ways := map[int64]*overpass.Way{}
	// Several ways per bucket, so ordering within a bucket is testable.
	for i, id := range []int64{907, 12, 5001, 33, 480} {
		lat, lon := 52.0+float64(i)*0.02, 9.0+float64(i)*0.02
		ways[id] = way(id, map[string]string{"natural": "water"}, lat, lon)
	}
	for i, id := range []int64{88, 7702, 3, 1500} {
		lat, lon := 53.0+float64(i)*0.02, 10.0+float64(i)*0.02
		ways[id] = way(id, map[string]string{"highway": "residential"}, lat, lon)
	}
	for i, id := range []int64{640, 21, 9999} {
		lat, lon := 54.0+float64(i)*0.02, 11.0+float64(i)*0.02
		ways[id] = way(id, map[string]string{"building": "yes"}, lat, lon)
	}

	relations := map[int64]*overpass.Relation{}
	for _, id := range []int64{770, 14, 2200, 61} {
		relations[id] = &overpass.Relation{
			Meta: overpass.Meta{ID: id, Tags: map[string]string{"leisure": "park"}},
		}
	}

	return &overpass.Result{Ways: ways, Relations: relations}
}

// bucketIDs flattens a collection into one ID sequence per bucket.
func bucketIDs(fc types.FeatureCollection) map[string][]string {
	out := map[string][]string{}
	add := func(name string, features []types.Feature) {
		ids := make([]string, len(features))
		for i := range features {
			ids[i] = features[i].ID
		}
		out[name] = ids
	}
	add("water", fc.Water)
	add("rivers", fc.Rivers)
	add("parks", fc.Parks)
	add("roads", fc.Roads)
	add("railroads", fc.Railroads)
	add("buildings", fc.Buildings)
	add("urban", fc.Urban)
	add("civic", fc.Civic)
	return out
}

// TestExtractFeatureOrderIsStable pins the property the whole change exists for:
// feature order is draw order, so two extractions of the same result must emit
// the same sequence. Go randomises map iteration on every range, so repeating
// the extraction is a real test rather than a formality — before the sort, ten
// rounds over a fixture this size failed essentially every time.
func TestExtractFeatureOrderIsStable(t *testing.T) {
	result := orderFixture()

	want := bucketIDs(ExtractFeaturesFromOverpassResult(result))
	for round := range 10 {
		got := bucketIDs(ExtractFeaturesFromOverpassResult(result))
		for bucket, wantIDs := range want {
			gotIDs := got[bucket]
			if len(gotIDs) != len(wantIDs) {
				t.Fatalf("round %d bucket %s: got %d features, want %d",
					round, bucket, len(gotIDs), len(wantIDs))
			}
			for i := range wantIDs {
				if gotIDs[i] != wantIDs[i] {
					t.Fatalf("round %d bucket %s: order changed at %d: got %s, want %s\ngot:  %v\nwant: %v",
						round, bucket, i, gotIDs[i], wantIDs[i], gotIDs, wantIDs)
				}
			}
		}
	}
}

// TestExtractFeatureOrderIsByOSMID pins which order it is. The choice of OSM ID
// ascending is arbitrary as a painting order; naming it here is what stops a
// later refactor from swapping it for another one without noticing.
func TestExtractFeatureOrderIsByOSMID(t *testing.T) {
	features := ExtractFeaturesFromOverpassResult(orderFixture())

	cases := []struct {
		name string
		want []string
		got  []types.Feature
	}{
		{"water", idsOf("way", 12, 33, 480, 907, 5001), features.Water},
		{"roads", idsOf("way", 3, 88, 1500, 7702), features.Roads},
		{"buildings", idsOf("way", 21, 640, 9999), features.Buildings},
		{"parks", idsOf("relation", 14, 61, 770, 2200), features.Parks},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.got) != len(tc.want) {
				t.Fatalf("got %d features, want %d", len(tc.got), len(tc.want))
			}
			for i := range tc.want {
				if tc.got[i].ID != tc.want[i] {
					t.Errorf("feature %d: got %s, want %s", i, tc.got[i].ID, tc.want[i])
				}
			}
		})
	}
}

func idsOf(kind string, ids ...int64) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = fmt.Sprintf("%s/%d", kind, id)
	}
	return out
}
