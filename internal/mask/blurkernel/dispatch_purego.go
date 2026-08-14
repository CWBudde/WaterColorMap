//go:build !amd64 || purego

package blurkernel

// convColsRow is the portable dispatch. Every non-amd64 target reaches it,
// including js/wasm, which the browser playground builds for.
func convColsRow(out, src []byte, aboveOff, belowOff []int32, taps []uint32, acc []uint32, w int) {
	convColsRowGo(out, src, aboveOff, belowOff, taps, acc, w)
}
