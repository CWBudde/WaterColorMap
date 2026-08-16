//go:build !amd64 || purego

package blurkernel

// convColsRow is the portable dispatch. Every non-amd64 target reaches it,
// including js/wasm, which the browser playground builds for.
func convColsRow(out, src []byte, aboveOff, belowOff []int32, taps []uint32, acc []uint32, w int) {
	convColsRowGo(out, src, aboveOff, belowOff, taps, acc, w)
}

func boxAccum(acc []uint32, row []byte, w int) {
	boxAccumGo(acc, row, w)
}

func boxColsRow(out []byte, acc []uint32, add, sub []byte, half, recip uint32, w int) {
	boxColsRowGo(out, acc, add, sub, half, recip, w)
}
