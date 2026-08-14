//go:build amd64 && !purego

package blurkernel

import (
	"math/rand"
	"testing"

	"golang.org/x/sys/cpu"

	blurasm "github.com/cwbudde/watercolormap/internal/mask/blurkernel/asm/amd64"
)

// requireAVX2 skips when the host cannot run the kernel. Dispatch checks the
// feature bit before calling in, but these tests call in directly and would
// fault instead.
func requireAVX2(tb testing.TB) {
	tb.Helper()
	if !cpu.X86.HasAVX2 {
		tb.Skip("host CPU lacks AVX2")
	}
}

// TestConvColsRowAVX2MatchesGo pins the assembly to the portable
// implementation. The two must agree bit for bit, not approximately: the same
// code path renders tiles on both, and the golden comparison allows no drift.
func TestConvColsRowAVX2MatchesGo(t *testing.T) {
	requireAVX2(t)

	rng := rand.New(rand.NewSource(1))

	// Widths straddle the 8-column block size so the ragged-tail path, which
	// repeats the last full block, is exercised in both directions.
	widths := []int{8, 9, 15, 16, 17, 23, 24, 31, 32, 33, 64, 100, 256, 257, 384}

	for _, w := range widths {
		for radius := 0; radius <= maxConvRadius; radius++ {
			const h = 24
			src := make([]byte, w*h)
			for i := range src {
				src[i] = byte(rng.Intn(256))
			}

			taps := GaussianTaps(max(float64(radius), 1)/3, max(radius, 1))
			if taps == nil {
				continue
			}
			// GaussianTaps may trim trailing zero weights, so use its radius.
			r := len(taps) - 1

			taps32 := make([]uint32, len(taps))
			for i, tap := range taps {
				taps32[i] = uint32(tap)
			}

			aboveOff := make([]int32, r+1)
			belowOff := make([]int32, r+1)
			acc := make([]uint32, w)

			for y := range h {
				for k := range r + 1 {
					aboveOff[k] = int32(clampRow(y-k, h) * w)
					belowOff[k] = int32(clampRow(y+k, h) * w)
				}

				want := make([]byte, w)
				convColsRowGo(want, src, aboveOff, belowOff, taps32, acc, w)

				got := make([]byte, w)
				blurasm.ConvColsRowAVX2(&got[0], &src[0], &aboveOff[0], &belowOff[0], &taps32[0], r, w)

				for x := range w {
					if got[x] != want[x] {
						t.Fatalf("w=%d radius=%d y=%d x=%d: asm=%d, go=%d",
							w, r, y, x, got[x], want[x])
					}
				}
			}
		}
	}
}

// TestConvColsAVX2MatchesPortable runs the whole column pass through dispatch
// and compares it against the portable path, so a dispatch mistake (wrong
// width guard, stale feature check) fails here even if the kernel is correct.
func TestConvColsAVX2MatchesPortable(t *testing.T) {
	requireAVX2(t)

	rng := rand.New(rand.NewSource(2))

	for _, size := range [][2]int{{8, 8}, {17, 5}, {64, 64}, {256, 256}, {257, 33}, {384, 384}} {
		w, h := size[0], size[1]
		stride := w + 5
		src := make([]byte, h*stride)
		for i := range src {
			src[i] = byte(rng.Intn(256))
		}

		for _, sigma := range []float64{0.35, 0.5, 0.9, 1.2, 2.0, 2.6} {
			plan := PlanFor(sigma)
			if plan.Mode != ModeConv {
				continue
			}

			var s Scratch
			s.Ensure(w, plan.Radius())

			got := make([]byte, len(src))
			ConvCols(got, src, w, h, stride, stride, plan.Taps, &s)

			want := make([]byte, len(src))
			s.ensureTaps(plan.Taps)
			radius := plan.Radius()
			above := make([]int32, radius+1)
			below := make([]int32, radius+1)
			acc := make([]uint32, w)
			for y := range h {
				for k := range radius + 1 {
					above[k] = int32(clampRow(y-k, h) * stride)
					below[k] = int32(clampRow(y+k, h) * stride)
				}
				convColsRowGo(want[y*stride:][:w], src, above, below, s.taps32, acc, w)
			}

			for y := range h {
				for x := range w {
					if got[y*stride+x] != want[y*stride+x] {
						t.Fatalf("w=%d h=%d sigma=%.2f at (%d,%d): dispatch=%d, portable=%d",
							w, h, sigma, x, y, got[y*stride+x], want[y*stride+x])
					}
				}
			}
		}
	}
}
