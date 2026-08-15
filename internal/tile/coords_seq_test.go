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
