package mask

import (
	"math/rand"
	"testing"
)

// randomRow builds a pixel row and a mask row of w entries.
func randomRow(rng *rand.Rand, w int) (src, maskRow []byte) {
	src = make([]byte, 4*w)
	maskRow = make([]byte, w)
	for i := range src {
		src[i] = byte(rng.Intn(256))
	}
	for i := range maskRow {
		maskRow[i] = byte(rng.Intn(256))
	}

	return src, maskRow
}

// TestSoftEdgeRowMatchesReference is the differential test between the dispatched
// kernel - the AVX2 one where the CPU has it - and the portable reference.
//
// The widths deliberately straddle the eight-pixel block: everything below it goes
// entirely through the tail path, and everything above it exercises a tail of every
// possible length.
func TestSoftEdgeRowMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(1))

	strengths := []float64{0, 0.001, 0.25, 0.5, 0.7539, 1}
	widths := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 15, 16, 17, 31, 33, 64, 127, 256, 384}

	for _, strength := range strengths {
		for _, w := range widths {
			src, maskRow := randomRow(rng, w)

			want := make([]byte, len(src))
			softEdgeRowGo(want, src, maskRow, strength, w)

			got := make([]byte, len(src))
			softEdgeRow(got, src, maskRow, strength, w)

			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("strength %v width %d byte %d (pixel %d): got %d, want %d",
						strength, w, i, i/4, got[i], want[i])
				}
			}
		}
	}
}

// TestSoftEdgeRowInPlace checks that the kernel may write over its own input. The
// pass is called that way, and a kernel that covered its tail by repeating the last
// block - as the blur kernel does - would darken those pixels twice.
func TestSoftEdgeRowInPlace(t *testing.T) {
	rng := rand.New(rand.NewSource(2))

	for _, w := range []int{8, 11, 17, 100} {
		src, maskRow := randomRow(rng, w)

		want := make([]byte, len(src))
		softEdgeRowGo(want, src, maskRow, 0.6, w)

		got := make([]byte, len(src))
		copy(got, src)
		softEdgeRow(got, got, maskRow, 0.6, w)

		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("in place, width %d, byte %d: got %d, want %d", w, i, got[i], want[i])
			}
		}
	}
}

// TestSoftEdgeRowExhaustiveRGB runs every one of the 16.7M source colours through
// both paths. The mask level cycles so that the darkening factor varies across the
// sweep as well; the colour space is what the HSL round trip is sensitive to, and it
// is small enough to cover completely.
func TestSoftEdgeRowExhaustiveRGB(t *testing.T) {
	if testing.Short() {
		t.Skip("exhaustive colour sweep")
	}

	const w = 4096

	src := make([]byte, 4*w)
	maskRow := make([]byte, w)
	got := make([]byte, 4*w)
	want := make([]byte, 4*w)

	for _, strength := range []float64{0.35, 1} {
		colour := 0
		for colour < 1<<24 {
			for i := range w {
				src[4*i] = byte(colour)
				src[4*i+1] = byte(colour >> 8)
				src[4*i+2] = byte(colour >> 16)
				src[4*i+3] = byte(colour * 7)
				maskRow[i] = byte(colour*31 + i)
				colour++
			}

			softEdgeRowGo(want, src, maskRow, strength, w)
			softEdgeRow(got, src, maskRow, strength, w)

			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("strength %v, pixel rgb (%d,%d,%d) mask %d: byte %d got %d, want %d",
						strength, src[4*(i/4)], src[4*(i/4)+1], src[4*(i/4)+2], maskRow[i/4],
						i%4, got[i], want[i])
				}
			}
		}
	}
}

// TestSoftEdgeRowClipping pins the extremes: pure black and pure white at every mask
// level, where the lightness multiply and the clamp in hslToRGB are most likely to
// disagree between the two paths.
func TestSoftEdgeRowClipping(t *testing.T) {
	const w = 256

	src := make([]byte, 4*w)
	maskRow := make([]byte, w)

	for _, colour := range [][3]byte{{0, 0, 0}, {255, 255, 255}, {255, 0, 0}, {0, 255, 0}, {0, 0, 255}, {1, 254, 128}} {
		for i := range w {
			src[4*i], src[4*i+1], src[4*i+2] = colour[0], colour[1], colour[2]
			src[4*i+3] = byte(i)
			maskRow[i] = byte(i)
		}

		want := make([]byte, 4*w)
		softEdgeRowGo(want, src, maskRow, 1, w)

		got := make([]byte, 4*w)
		softEdgeRow(got, src, maskRow, 1, w)

		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("colour %v, byte %d: got %d, want %d", colour, i, got[i], want[i])
			}
		}
	}
}

// TestSoftEdgeRowPreservesAlpha states the one output invariant the kernel has
// independent of its reference: alpha is copied, never computed.
func TestSoftEdgeRowPreservesAlpha(t *testing.T) {
	const w = 64

	rng := rand.New(rand.NewSource(3))
	src, maskRow := randomRow(rng, w)

	dst := make([]byte, len(src))
	softEdgeRow(dst, src, maskRow, 0.9, w)

	for i := range w {
		if dst[4*i+3] != src[4*i+3] {
			t.Fatalf("pixel %d: alpha %d, want %d", i, dst[4*i+3], src[4*i+3])
		}
	}
}

// TestSoftEdgeDivisionMagic proves the two fixed-point divisions the assembly uses
// in place of a divide, over the whole range the kernel can reach.
//
// The kernel computes (l*darkening)/65025 as (n/255)/255, the first step by a
// widening multiply and the second by an add-shift identity. Both are exact only
// below a bound, and the bounds are what this test pins.
func TestSoftEdgeDivisionMagic(t *testing.T) {
	if testing.Short() {
		t.Skip("exhaustive division sweep")
	}

	// n = l * darkening, at most 255 * 65025.
	const maxProduct = 255 * 65025

	const magic = 16843010

	for n := range maxProduct + 1 {
		if got := uint32((uint64(n) * magic) >> 32); got != uint32(n/255) {
			t.Fatalf("widening multiply: %d/255 = %d, magic gives %d", n, n/255, got)
		}
	}

	// The add-shift identity runs on n/255 (at most 65025) and on t*S+127 (at
	// most 65152). It breaks at 65535, so the headroom is worth stating.
	for x := range 65536 {
		got := (x + 1 + (x >> 8)) >> 8
		if x <= 65152 && got != x/255 {
			t.Fatalf("add-shift: %d/255 = %d, identity gives %d", x, x/255, got)
		}
	}
}
