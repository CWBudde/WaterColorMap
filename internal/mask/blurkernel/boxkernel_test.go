package blurkernel

import (
	"math/rand"
	"testing"
)

// boxKernelWidths straddle the eight-column block the assembly works in: below it
// everything falls to the portable loop, and 8 through 15 walk the assembly/Go
// handoff across every tail length from 0 to 7.
var boxKernelWidths = []int{
	0, 1, 2, 7,
	8, 9, 10, 11, 12, 13, 14, 15,
	16, 17, 31, 33, 64, 384,
}

// TestBoxAccumMatchesReference is the differential test for the priming kernel.
func TestBoxAccumMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(11))

	for _, w := range boxKernelWidths {
		row := make([]byte, w)
		for i := range row {
			row[i] = byte(rng.Intn(256))
		}

		// A non-zero starting accumulator catches a kernel that stores instead of
		// accumulating, which a zeroed one would hide.
		want := make([]uint32, w)
		got := make([]uint32, w)

		for i := range want {
			v := uint32(rng.Intn(1 << 20))
			want[i], got[i] = v, v
		}

		boxAccumGo(want, row, w)
		boxAccum(got, row, w)

		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("width %d, column %d: got %d, want %d", w, i, got[i], want[i])
			}
		}
	}
}

// TestBoxColsRowMatchesReference is the differential test for the slide-and-divide
// kernel, over both of its shapes: with a row to slide in, and without one, which is
// what the first output row of every pass uses.
func TestBoxColsRowMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(12))

	for _, n := range []int{1, 3, 9, 47, 95} {
		recip := BoxReciprocal(n)
		half := uint32(n / 2)

		for _, w := range boxKernelWidths {
			// The accumulator has to be a real window sum. Outside [0, n*255] the
			// two paths are entitled to disagree: BoxReciprocal is exact only over
			// that range, and past it the scalar path wraps its uint8 conversion
			// where the assembly saturates its pack. So the window is built from n
			// samples and the one being slid out is one of them.
			add := make([]byte, w)
			sub := make([]byte, w)
			base := make([]uint32, w)

			for i := range base {
				add[i] = byte(rng.Intn(256))
				sub[i] = byte(rng.Intn(256))

				sum := uint32(sub[i])
				for range n - 1 {
					sum += uint32(rng.Intn(256))
				}

				base[i] = sum
			}

			for _, slide := range []bool{false, true} {
				var addArg, subArg []byte
				if slide {
					addArg, subArg = add, sub
				}

				wantAcc := append([]uint32(nil), base...)
				gotAcc := append([]uint32(nil), base...)
				wantOut := make([]byte, w)
				gotOut := make([]byte, w)

				boxColsRowGo(wantOut, wantAcc, addArg, subArg, half, recip, w)
				boxColsRow(gotOut, gotAcc, addArg, subArg, half, recip, w)

				for i := range wantOut {
					if gotOut[i] != wantOut[i] {
						t.Fatalf("n=%d width %d slide=%v, column %d: out %d, want %d",
							n, w, slide, i, gotOut[i], wantOut[i])
					}

					if gotAcc[i] != wantAcc[i] {
						t.Fatalf("n=%d width %d slide=%v, column %d: acc %d, want %d",
							n, w, slide, i, gotAcc[i], wantAcc[i])
					}
				}
			}
		}
	}
}

// BenchmarkBoxColsRow compares the dispatched kernels against the portable ones at
// the row width the renderer produces. On a host without AVX2, or under the purego
// tag, the two sub-benchmarks measure the same code.
func BenchmarkBoxColsRow(b *testing.B) {
	const w = 384

	const n = 47

	rng := rand.New(rand.NewSource(13))
	add := make([]byte, w)
	sub := make([]byte, w)

	for i := range add {
		add[i] = byte(rng.Intn(256))
		sub[i] = byte(rng.Intn(256))
	}

	acc := make([]uint32, w)
	for i := range acc {
		acc[i] = uint32(rng.Intn(n * 255))
	}

	out := make([]byte, w)
	recip := BoxReciprocal(n)
	half := uint32(n / 2)

	b.Run("scalar", func(b *testing.B) {
		for b.Loop() {
			boxColsRowGo(out, acc, add, sub, half, recip, w)
		}
	})

	b.Run("dispatch", func(b *testing.B) {
		for b.Loop() {
			boxColsRow(out, acc, add, sub, half, recip, w)
		}
	})
}
