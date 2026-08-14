//go:build amd64 && !purego

package blurkernel

import (
	"golang.org/x/sys/cpu"

	blurasm "github.com/cwbudde/watercolormap/internal/mask/blurkernel/asm/amd64"
)

// useAVX2 is resolved once. The kernels below are reached per output row, so
// re-reading the feature bits there would cost more than the check is worth.
var useAVX2 = cpu.X86.HasAVX2

// minAVX2Width is the narrowest row the assembly handles. It processes eight
// columns at a time and covers a ragged tail by repeating the last full block,
// which needs at least one full block to exist.
const minAVX2Width = 8

func convColsRow(out, src []byte, aboveOff, belowOff []int32, taps []uint32, acc []uint32, w int) {
	if useAVX2 && w >= minAVX2Width {
		blurasm.ConvColsRowAVX2(&out[0], &src[0], &aboveOff[0], &belowOff[0], &taps[0], len(taps)-1, w)
		return
	}
	convColsRowGo(out, src, aboveOff, belowOff, taps, acc, w)
}
