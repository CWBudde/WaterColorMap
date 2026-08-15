package types

import "testing"

func TestBoundingBoxUnion(t *testing.T) {
	tests := []struct {
		name string
		a    BoundingBox
		b    BoundingBox
		want BoundingBox
	}{
		{
			name: "disjoint boxes",
			a:    BoundingBox{MinLon: 0, MinLat: 0, MaxLon: 1, MaxLat: 1},
			b:    BoundingBox{MinLon: 5, MinLat: 5, MaxLon: 6, MaxLat: 6},
			want: BoundingBox{MinLon: 0, MinLat: 0, MaxLon: 6, MaxLat: 6},
		},
		{
			name: "b inside a",
			a:    BoundingBox{MinLon: 0, MinLat: 0, MaxLon: 10, MaxLat: 10},
			b:    BoundingBox{MinLon: 2, MinLat: 2, MaxLon: 3, MaxLat: 3},
			want: BoundingBox{MinLon: 0, MinLat: 0, MaxLon: 10, MaxLat: 10},
		},
		{
			name: "adjacent tiles",
			a:    BoundingBox{MinLon: 9.0, MinLat: 52.0, MaxLon: 9.1, MaxLat: 52.1},
			b:    BoundingBox{MinLon: 9.1, MinLat: 52.0, MaxLon: 9.2, MaxLat: 52.1},
			want: BoundingBox{MinLon: 9.0, MinLat: 52.0, MaxLon: 9.2, MaxLat: 52.1},
		},
		{
			name: "negative coordinates",
			a:    BoundingBox{MinLon: -10, MinLat: -5, MaxLon: -8, MaxLat: -3},
			b:    BoundingBox{MinLon: -9, MinLat: -6, MaxLon: -7, MaxLat: -4},
			want: BoundingBox{MinLon: -10, MinLat: -6, MaxLon: -7, MaxLat: -3},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.a.Union(tt.b); got != tt.want {
				t.Errorf("Union = %+v, want %+v", got, tt.want)
			}
			// Union is symmetric; a band's bounds must not depend on the order
			// its member tiles happen to be visited in.
			if got := tt.b.Union(tt.a); got != tt.want {
				t.Errorf("Union is not symmetric: %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestBoundingBoxContains(t *testing.T) {
	outer := BoundingBox{MinLon: 6, MinLat: 50, MaxLon: 12, MaxLat: 54}

	tests := []struct {
		name  string
		inner BoundingBox
		want  bool
	}{
		{"strictly inside", BoundingBox{MinLon: 7, MinLat: 51, MaxLon: 11, MaxLat: 53}, true},
		{"identical", outer, true},
		{"shares an edge", BoundingBox{MinLon: 6, MinLat: 50, MaxLon: 8, MaxLat: 52}, true},
		{"overhangs east", BoundingBox{MinLon: 7, MinLat: 51, MaxLon: 13, MaxLat: 53}, false},
		{"overhangs north", BoundingBox{MinLon: 7, MinLat: 51, MaxLon: 11, MaxLat: 55}, false},
		{"disjoint", BoundingBox{MinLon: 18, MinLat: -34, MaxLon: 19, MaxLat: -33}, false},
		{"encloses outer", BoundingBox{MinLon: 0, MinLat: 40, MaxLon: 20, MaxLat: 60}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := outer.Contains(tt.inner); got != tt.want {
				t.Errorf("Contains = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBoundingBoxIntersects(t *testing.T) {
	b := BoundingBox{MinLon: 0, MinLat: 0, MaxLon: 10, MaxLat: 10}

	tests := []struct {
		name  string
		other BoundingBox
		want  bool
	}{
		{"overlapping", BoundingBox{MinLon: 5, MinLat: 5, MaxLon: 15, MaxLat: 15}, true},
		{"contained", BoundingBox{MinLon: 2, MinLat: 2, MaxLon: 3, MaxLat: 3}, true},
		{"touching edge", BoundingBox{MinLon: 10, MinLat: 0, MaxLon: 20, MaxLat: 10}, true},
		{"touching corner", BoundingBox{MinLon: 10, MinLat: 10, MaxLon: 20, MaxLat: 20}, true},
		{"disjoint in lon", BoundingBox{MinLon: 11, MinLat: 0, MaxLon: 20, MaxLat: 10}, false},
		{"disjoint in lat", BoundingBox{MinLon: 0, MinLat: 11, MaxLon: 10, MaxLat: 20}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := b.Intersects(tt.other); got != tt.want {
				t.Errorf("Intersects = %v, want %v", got, tt.want)
			}
			if got := tt.other.Intersects(b); got != tt.want {
				t.Errorf("Intersects is not symmetric: %v, want %v", got, tt.want)
			}
		})
	}
}
