//go:build amd64 && !purego

// Package amd64 holds the hand-written assembly mask kernels for x86-64.
//
// The !purego constraint lives here rather than in the .s files: with the
// declarations gone, the assembly has nothing to bind to and is simply not
// referenced, so `go test -tags purego` exercises the portable path without a
// second copy of the build tags to keep in sync.
package amd64

// SoftEdgeRowAVX2 applies one row of the soft-edge darkening pass.
//
// For each of the n pixels it computes, with m the mask level at that pixel:
//
//	darkening = 65025 - int(float64(65025-m*m) * strength)
//	h, s, l   = rgbToHSL(src.R, src.G, src.B)
//	dst.RGB   = hslToRGB(h, s, uint8(int(l)*darkening/65025))
//	dst.A     = src.A
//
// It is bit-identical to the portable `softEdgeRowGo`, including the truncating
// float64 multiply, which runs in double-precision lanes here for that reason.
//
// dst and src point at NRGBA pixels and may be the same buffer; mask points at n
// 8-bit levels. n must be a positive multiple of 8. Requires AVX2.
//
//go:noescape
func SoftEdgeRowAVX2(dst, src, mask *byte, strength float64, n int)
