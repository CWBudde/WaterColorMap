package tile

import (
	"fmt"
	"testing"
)

func TestAncestor(t *testing.T) {
	tests := []struct {
		name   string
		coords Coords
		levels uint32
		want   Coords
	}{
		{"zero levels is identity", Coords{Z: 14, X: 8635, Y: 5385}, 0, Coords{Z: 14, X: 8635, Y: 5385}},
		{"one level halves", Coords{Z: 14, X: 8635, Y: 5385}, 1, Coords{Z: 13, X: 4317, Y: 2692}},
		{"two levels quarter", Coords{Z: 14, X: 8635, Y: 5385}, 2, Coords{Z: 12, X: 2158, Y: 1346}},
		{"clamps at zoom 0", Coords{Z: 2, X: 3, Y: 3}, 5, Coords{Z: 0, X: 0, Y: 0}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Ancestor(tt.coords, tt.levels); got != tt.want {
				t.Errorf("Ancestor = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// block builds the size x size tile block whose corner is (x0, y0).
func block(z uint32, x0, y0 uint32, size uint32) []Coords {
	out := make([]Coords, 0, size*size)
	for dx := uint32(0); dx < size; dx++ {
		for dy := uint32(0); dy < size; dy++ {
			out = append(out, Coords{Z: z, X: x0 + dx, Y: y0 + dy})
		}
	}
	return out
}

// TestGroupIntoBandsIsTotal is the invariant that matters most: every tile that
// was asked for lands in exactly one band. A tile lost here is a tile that
// never renders.
func TestGroupIntoBandsIsTotal(t *testing.T) {
	for _, levels := range []uint32{0, 1, 2, 3} {
		t.Run(fmt.Sprintf("levels=%d", levels), func(t *testing.T) {
			coords := block(14, 8632, 5380, 8)

			seen := map[Coords]int{}
			for _, b := range GroupIntoBands(coords, levels) {
				for _, c := range b.Tiles {
					seen[c]++
				}
			}

			if len(seen) != len(coords) {
				t.Fatalf("banding covered %d tiles, want %d", len(seen), len(coords))
			}
			for _, c := range coords {
				if seen[c] != 1 {
					t.Errorf("tile %s appears %d times, want exactly 1", c.String(), seen[c])
				}
			}
		})
	}
}

func TestGroupIntoBandsSizes(t *testing.T) {
	coords := block(14, 8632, 5380, 4)

	tests := []struct {
		levels    uint32
		wantBands int
		wantEach  int
	}{
		{0, 16, 1}, // degenerates to per-tile
		{1, 4, 4},  // 2x2 blocks
		{2, 1, 16}, // one 4x4 block
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("levels=%d", tt.levels), func(t *testing.T) {
			bands := GroupIntoBands(coords, tt.levels)
			if len(bands) != tt.wantBands {
				t.Fatalf("got %d bands, want %d", len(bands), tt.wantBands)
			}
			for _, b := range bands {
				if len(b.Tiles) != tt.wantEach {
					t.Errorf("band %s holds %d tiles, want %d", b.Key.String(), len(b.Tiles), tt.wantEach)
				}
				if b.Level != tt.levels {
					t.Errorf("band level = %d, want %d", b.Level, tt.levels)
				}
			}
		})
	}
}

// TestGroupIntoBandsIsDeterministic: a resumed or repeated run must issue the
// same queries in the same order, or the response cache stops helping and the
// upstream sees a different access pattern every time.
func TestGroupIntoBandsIsDeterministic(t *testing.T) {
	coords := block(14, 8632, 5380, 4)

	first := GroupIntoBands(coords, 2)
	for i := 0; i < 5; i++ {
		again := GroupIntoBands(coords, 2)
		if len(again) != len(first) {
			t.Fatalf("band count varies between runs: %d then %d", len(first), len(again))
		}
		for j := range first {
			if again[j].Key != first[j].Key {
				t.Fatalf("band order varies between runs at %d: %s then %s",
					j, first[j].Key.String(), again[j].Key.String())
			}
			for k := range first[j].Tiles {
				if again[j].Tiles[k] != first[j].Tiles[k] {
					t.Fatalf("tile order varies between runs in band %s", first[j].Key.String())
				}
			}
		}
	}
}

// TestGroupIntoBandsSeparatesZooms: a band's query is built at its members'
// zoom, so tiles of different zooms can never share one.
func TestGroupIntoBandsSeparatesZooms(t *testing.T) {
	coords := []Coords{
		{Z: 13, X: 4317, Y: 2692},
		{Z: 14, X: 8634, Y: 5384},
		{Z: 14, X: 8635, Y: 5385},
	}

	for _, b := range GroupIntoBands(coords, 2) {
		z := b.Tiles[0].Z
		for _, c := range b.Tiles {
			if c.Z != z {
				t.Errorf("band %s mixes zooms %d and %d", b.Key.String(), z, c.Z)
			}
		}
	}
}

// TestBandSplitCoversTheSameTiles is what makes the adaptive size guard safe: a
// split may not lose a tile, or a band that is too large to fetch would take
// its members down with it.
func TestBandSplitCoversTheSameTiles(t *testing.T) {
	bands := GroupIntoBands(block(14, 8632, 5380, 4), 2)
	if len(bands) != 1 {
		t.Fatalf("expected one band, got %d", len(bands))
	}

	sub := bands[0].Split()
	if len(sub) != 4 {
		t.Fatalf("splitting a 4x4 band gave %d sub-bands, want 4", len(sub))
	}

	seen := map[Coords]int{}
	for _, b := range sub {
		if b.Level != 1 {
			t.Errorf("sub-band level = %d, want 1", b.Level)
		}
		for _, c := range b.Tiles {
			seen[c]++
		}
	}
	for _, c := range bands[0].Tiles {
		if seen[c] != 1 {
			t.Errorf("tile %s appears %d times after the split, want 1", c.String(), seen[c])
		}
	}
	if len(seen) != len(bands[0].Tiles) {
		t.Errorf("split covers %d tiles, want %d", len(seen), len(bands[0].Tiles))
	}
}

// TestBandSplitBottomsOut: the recursion has to terminate at a single tile,
// which is where the fallback becomes an ordinary per-tile fetch.
func TestBandSplitBottomsOut(t *testing.T) {
	b := Band{Key: Coords{Z: 14, X: 8635, Y: 5385}, Level: 0,
		Tiles: []Coords{{Z: 14, X: 8635, Y: 5385}}}

	if sub := b.Split(); sub != nil {
		t.Errorf("splitting a level-0 band gave %d sub-bands, want none", len(sub))
	}
}

// TestBandSplitTerminates walks the whole recursion down from level 3.
func TestBandSplitTerminates(t *testing.T) {
	queue := GroupIntoBands(block(14, 8632, 5376, 8), 3)
	total := 0

	for iterations := 0; len(queue) > 0; iterations++ {
		if iterations > 1000 {
			t.Fatal("splitting did not terminate")
		}
		b := queue[0]
		queue = queue[1:]

		sub := b.Split()
		if sub == nil {
			total += len(b.Tiles)
			continue
		}
		queue = append(queue, sub...)
	}

	if total != 64 {
		t.Errorf("splitting all the way down yielded %d tiles, want 64", total)
	}
}

func TestGroupIntoBandsEmpty(t *testing.T) {
	if got := GroupIntoBands(nil, 2); got != nil {
		t.Errorf("GroupIntoBands(nil) = %v, want nil", got)
	}
}

func BenchmarkGroupIntoBands(b *testing.B) {
	coords := block(14, 8632, 5376, 32) // 1024 tiles
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		GroupIntoBands(coords, 2)
	}
}
