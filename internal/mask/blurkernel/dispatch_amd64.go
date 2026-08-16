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

// The box column kernels cannot borrow the convolution kernel's trick of covering a
// ragged tail by repeating the final block: they carry the accumulator forward, so a
// repeated block would add a row into it twice. The tail goes to the portable loop.

func boxAccum(acc []uint32, row []byte, w int) {
	if useAVX2 && w >= minAVX2Width {
		n := w &^ (minAVX2Width - 1)

		blurasm.BoxAccumAVX2(&acc[0], &row[0], n)

		if n < w {
			boxAccumGo(acc[n:], row[n:], w-n)
		}

		return
	}

	boxAccumGo(acc, row, w)
}

func boxColsRow(out []byte, acc []uint32, add, sub []byte, half, recip uint32, w int) {
	if useAVX2 && w >= minAVX2Width {
		n := w &^ (minAVX2Width - 1)

		var addPtr, subPtr *byte
		if add != nil {
			addPtr, subPtr = &add[0], &sub[0]
		}

		blurasm.BoxColsRowAVX2(&out[0], &acc[0], addPtr, subPtr, half, recip, n)

		if n < w {
			var addTail, subTail []byte
			if add != nil {
				addTail, subTail = add[n:], sub[n:]
			}

			boxColsRowGo(out[n:], acc[n:], addTail, subTail, half, recip, w-n)
		}

		return
	}

	boxColsRowGo(out, acc, add, sub, half, recip, w)
}
