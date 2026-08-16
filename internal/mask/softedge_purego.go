//go:build !amd64 || purego

package mask

// softEdgeRow is the portable dispatch. Every non-amd64 target reaches it,
// including js/wasm, which the browser playground builds for.
func softEdgeRow(dst, src, maskRow []byte, strength float64, w int) {
	softEdgeRowGo(dst, src, maskRow, strength, w)
}
