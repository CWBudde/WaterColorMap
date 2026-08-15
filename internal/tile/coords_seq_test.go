package tile

import "testing"

// TestTilesInBBoxSeqMatchesSlice: the streaming enumeration and the slice one
// are the same enumeration, in the same order. A batch run checkpoints by
// position in the sequence, so an order difference would be a resume that skips
// tiles nobody rendered.
func TestTilesInBBoxSeqMatchesSlice(t *testing.T) {
	cases := []struct {
		name    string
		bbox    [4]float64
		zoomMin int
		zoomMax int
	}{
		{"single zoom", [4]float64{9.7, 52.3, 9.9, 52.4}, 13, 13},
		{"multi zoom", [4]float64{9.7, 52.3, 9.9, 52.4}, 10, 14},
		{"whole world", [4]float64{-180, -85, 180, 85}, 0, 3},
		{"empty range", [4]float64{9.7, 52.3, 9.9, 52.4}, 5, 4},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := TilesInBBox(tc.bbox, tc.zoomMin, tc.zoomMax)

			var got []Coords
			for c := range TilesInBBoxSeq(tc.bbox, tc.zoomMin, tc.zoomMax) {
				got = append(got, c)
			}

			if len(got) != len(want) {
				t.Fatalf("sequence yielded %d tiles, slice has %d", len(got), len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("tile %d: sequence gave %s, slice gave %s", i, got[i], want[i])
				}
			}
			if count := TileCount(tc.bbox, tc.zoomMin, tc.zoomMax); count != len(want) {
				t.Errorf("TileCount = %d, enumeration yielded %d", count, len(want))
			}
		})
	}
}

// TestTilesInBBoxSeqOrderIsTheDocumentedContract pins the enumeration order
// against a literal, not against another function.
//
// TestTilesInBBoxSeqMatchesSlice cannot do this job: TilesInBBox is itself built
// by collecting TilesInBBoxSeq (coords.go), so any future reorder moves both
// sides together and that test keeps passing. Batch runs checkpoint by position
// in this sequence, so a silent reorder would make every existing checkpoint
// resume into a different set of tiles — the failure this literal exists to
// catch.
//
// The bbox is chosen to exercise all three transitions the order is made of:
// y within an x column, x within a zoom, and zoom to zoom.
func TestTilesInBBoxSeqOrderIsTheDocumentedContract(t *testing.T) {
	want := []Coords{
		// zoom 1: x ascending, y ascending within each x.
		NewCoords(1, 0, 0), NewCoords(1, 0, 1),
		NewCoords(1, 1, 0), NewCoords(1, 1, 1),
		// zoom 2 follows the whole of zoom 1.
		NewCoords(2, 1, 1), NewCoords(2, 1, 2),
		NewCoords(2, 2, 1), NewCoords(2, 2, 2),
	}

	var got []Coords
	for c := range TilesInBBoxSeq([4]float64{-10, -10, 10, 10}, 1, 2) {
		got = append(got, c)
	}

	if len(got) != len(want) {
		t.Fatalf("enumeration yielded %d tiles, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tile %d: got %s, want %s. The enumeration order is a compatibility "+
				"contract — changing it invalidates every checkpoint written by an older build",
				i, got[i].String(), want[i].String())
		}
	}
}

// TestTilesInBBoxSeqStopsEarly: a consumer that breaks out must not leave the
// producer running.
func TestTilesInBBoxSeqStopsEarly(t *testing.T) {
	bbox := [4]float64{9.7, 52.3, 9.9, 52.4}

	n := 0
	for range TilesInBBoxSeq(bbox, 10, 14) {
		n++
		if n == 3 {
			break
		}
	}
	if n != 3 {
		t.Errorf("consumed %d tiles after breaking at 3", n)
	}
}
