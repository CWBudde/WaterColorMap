//go:build amd64 && !purego

package mask

import (
	"golang.org/x/sys/cpu"

	maskasm "github.com/cwbudde/watercolormap/internal/mask/asm/amd64"
)

// useAVX2 is resolved once. The kernel below is reached per image row, so
// re-reading the feature bits there would cost more than the check is worth.
var useAVX2 = cpu.X86.HasAVX2

// softEdgeBlock is the number of pixels the assembly kernel handles per iteration.
const softEdgeBlock = 8

// softEdgeRow runs one row of the soft-edge pass, using the AVX2 kernel for as many
// whole blocks as the row holds and the portable loop for the ragged tail.
//
// The tail is left to Go rather than covered by repeating the final block, the way
// the blur kernel does it: this pass writes its output, so re-running a block that
// overlaps an already-written one would darken those pixels twice whenever dst and
// src alias.
func softEdgeRow(dst, src, maskRow []byte, strength float64, w int) {
	if useAVX2 && w >= softEdgeBlock {
		n := w &^ (softEdgeBlock - 1)
		maskasm.SoftEdgeRowAVX2(&dst[0], &src[0], &maskRow[0], strength, n)

		if n < w {
			softEdgeRowGo(dst[4*n:], src[4*n:], maskRow[n:], strength, w-n)
		}

		return
	}

	softEdgeRowGo(dst, src, maskRow, strength, w)
}
