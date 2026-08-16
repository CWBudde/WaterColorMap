//go:build amd64 && !purego

// Package amd64 holds the hand-written assembly blur kernels for x86-64.
//
// The !purego constraint lives here rather than in the .s files: with the
// declarations gone, the assembly has nothing to bind to and is simply not
// referenced, so `go test -tags purego` exercises the portable path without a
// second copy of the build tags to keep in sync.
package amd64

// ConvColsRowAVX2 computes one output row of a separable Gaussian column pass.
//
// It evaluates, for each column x in [0, w):
//
//	acc = src[aboveOff[0]+x] * taps[0]
//	      + sum over k in [1, radius] of
//	        (src[aboveOff[k]+x] + src[belowOff[k]+x]) * taps[k]
//	dst[x] = uint8((acc + 1<<15) >> 16)
//
// The offsets are byte offsets from src and are expected to be clamped to the
// image already, so this kernel has no edge handling. taps is the half-kernel
// widened to 32 bits, with taps[0] the centre weight.
//
// It requires w >= 8 and AVX2. Columns are processed eight at a time; a width
// that is not a multiple of eight is covered by repeating the final full block,
// which is safe because the computation is a pure function of src.
//
//go:noescape
func ConvColsRowAVX2(dst, src *byte, aboveOff, belowOff *int32, taps *uint32, radius, w int)

// BoxAccumAVX2 adds n source bytes into a column accumulator:
//
//	acc[x] += uint32(row[x])
//
// n must be a positive multiple of 8. Requires AVX2.
//
//go:noescape
func BoxAccumAVX2(acc *uint32, row *byte, n int)

// BoxColsRowAVX2 slides a box window down by one row and emits the output row:
//
//	acc[x] += uint32(add[x]) - uint32(sub[x])   // skipped when add is nil
//	out[x]  = uint8((uint64(acc[x]+half) * uint64(recip)) >> 32)
//
// A nil add leaves the accumulator alone, which is what the first output row of a
// pass needs; add and sub are either both set or both nil. recip is the fixed-point
// reciprocal of the window width from BoxReciprocal, so the shift reproduces the
// division exactly.
//
// n must be a positive multiple of 8. Requires AVX2.
//
//go:noescape
func BoxColsRowAVX2(out *byte, acc *uint32, add, sub *byte, half, recip uint32, n int)
