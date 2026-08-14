package blurkernel

// Scratch holds the reusable buffers the kernels need. Sizing it once per blur
// and threading it through every pass keeps the kernels allocation-free, which
// matters because a tile runs a dozen or more blurs.
type Scratch struct {
	// pad is a single row with its edges replicated, so the row passes can run
	// a branch-free window without clamping inside the loop.
	pad []byte
	// acc is the per-column accumulator for the column passes, one entry per
	// output pixel in a row.
	acc []uint32
	// aboveOff and belowOff hold, for the output row being worked on, the byte
	// offset of the source row each tap reads, already clamped to the image.
	aboveOff, belowOff []int32
	// taps32 is the half-kernel widened to 32 bits. The assembly broadcasts a
	// tap straight from this slice, which needs dword-aligned elements.
	taps32 []uint32
}

// ensureTaps widens the half-kernel and sizes the per-tap offset tables.
func (s *Scratch) ensureTaps(taps []uint16) {
	n := len(taps)
	if cap(s.taps32) < n {
		s.taps32 = make([]uint32, n)
		s.aboveOff = make([]int32, n)
		s.belowOff = make([]int32, n)
	}
	s.taps32 = s.taps32[:n]
	s.aboveOff = s.aboveOff[:n]
	s.belowOff = s.belowOff[:n]
	for i, tap := range taps {
		s.taps32[i] = uint32(tap)
	}
}

// Ensure grows the scratch buffers to fit a width-w row with the given radius.
func (s *Scratch) Ensure(w, radius int) {
	if len(s.acc) < w {
		s.acc = make([]uint32, w)
	}
	// The row window reads pad[x] through pad[x+2*radius] for x up to w-1.
	if need := w + 2*radius + 1; len(s.pad) < need {
		s.pad = make([]byte, need)
	}
}

// clampRow maps a possibly out-of-range row index onto the nearest edge row.
func clampRow(y, h int) int {
	if y < 0 {
		return 0
	}
	if y >= h {
		return h - 1
	}
	return y
}

// fillPad copies row into the scratch pad with its first and last pixel
// replicated radius times on each side.
func fillPad(pad, row []byte, w, radius int) {
	first, last := row[0], row[w-1]
	for i := range radius {
		pad[i] = first
	}
	copy(pad[radius:radius+w], row)
	for i := range radius + 1 {
		pad[radius+w+i] = last
	}
}

// ConvRows applies a direct separable Gaussian along each row.
//
// Edge handling is done by materialising a replicated-border copy of the row up
// front. That leaves the row pass with exactly the shape of the column pass —
// a weighted sum of samples at fixed offsets from a base pointer — so both
// share one kernel, and both get the assembly implementation. The offsets are
// byte distances within the padded row instead of whole rows.
func ConvRows(dst, src []byte, w, h, dstStride, srcStride int, taps []uint16, s *Scratch) {
	radius := len(taps) - 1
	s.ensureTaps(taps)

	// The window for output x spans pad[x] through pad[x+2*radius], centred on
	// pad[x+radius], so tap k reads at radius-k and radius+k.
	above := s.aboveOff[:radius+1]
	below := s.belowOff[:radius+1]
	for k := range radius + 1 {
		above[k] = int32(radius - k)
		below[k] = int32(radius + k)
	}

	pad := s.pad
	for y := range h {
		fillPad(pad, src[y*srcStride:][:w], w, radius)
		convColsRow(dst[y*dstStride:][:w], pad, above, below, s.taps32, s.acc[:w], w)
	}
}

// ConvCols applies a direct separable Gaussian down each column.
//
// Unlike a per-column walk, this accumulates a whole output row at a time: for
// each tap it sweeps one source row left to right into the accumulator. Every
// access is sequential, and the 2*radius+1 source rows in play stay resident in
// L1 for the whole output row.
//
// The per-row work is handed to convColsRow, which uses an assembly kernel when
// one is available. Row geometry — which source rows a given output row reads,
// after clamping — is resolved here and passed down as byte offsets, so the
// kernel itself has no edge logic.
//
// dst and src must not alias: an output row is written before later output rows
// read it as part of their window.
func ConvCols(dst, src []byte, w, h, dstStride, srcStride int, taps []uint16, s *Scratch) {
	radius := len(taps) - 1
	s.ensureTaps(taps)

	above := s.aboveOff[:radius+1]
	below := s.belowOff[:radius+1]

	for y := range h {
		for k := range radius + 1 {
			above[k] = int32(clampRow(y-k, h) * srcStride)
			below[k] = int32(clampRow(y+k, h) * srcStride)
		}
		convColsRow(dst[y*dstStride:][:w], src, above, below, s.taps32, s.acc[:w], w)
	}
}

// convColsRowGo is the portable implementation of one output row, and the
// reference the assembly kernels are tested against.
func convColsRowGo(out, src []byte, aboveOff, belowOff []int32, taps []uint32, acc []uint32, w int) {
	centre := src[aboveOff[0]:][:w]
	tap := taps[0]
	for x := range acc {
		acc[x] = uint32(centre[x]) * tap
	}

	for k := 1; k < len(taps); k++ {
		above := src[aboveOff[k]:][:w]
		below := src[belowOff[k]:][:w]
		tap := taps[k]
		for x := range acc {
			acc[x] += (uint32(above[x]) + uint32(below[x])) * tap
		}
	}

	for x := range acc {
		out[x] = uint8((acc[x] + One/2) >> FracBits)
	}
}

// boxShift is the fixed-point precision of the box-window reciprocal. It is
// wider than FracBits because the box divisor is a runtime value: with
// recip = ceil(2^k/n), the multiply reproduces the division exactly only while
// sum*(n-1) < 2^k, and 32 bits clears that for every window this renderer can
// produce by three orders of magnitude. TestBoxReciprocalExact proves it
// exhaustively rather than relying on the argument.
const boxShift = 32

// BoxReciprocal returns the fixed-point multiplier that replaces division by a
// window of n samples.
func BoxReciprocal(n int) uint32 {
	return uint32((1<<boxShift + uint64(n) - 1) / uint64(n))
}

// boxDiv divides an accumulated window sum by its width, rounding to nearest.
func boxDiv(sum, half uint32, recip uint32) uint8 {
	return uint8((uint64(sum+half) * uint64(recip)) >> boxShift)
}

// BoxRows applies a single box-blur pass along each row.
func BoxRows(dst, src []byte, w, h, dstStride, srcStride, radius int, s *Scratch) {
	n := 2*radius + 1
	recip := BoxReciprocal(n)
	half := uint32(n / 2)
	pad := s.pad

	for y := range h {
		row := src[y*srcStride:][:w]
		out := dst[y*dstStride:][:w]
		fillPad(pad, row, w, radius)

		var sum uint32
		for i := range n {
			sum += uint32(pad[i])
		}
		for x := range w {
			out[x] = boxDiv(sum, half, recip)
			sum += uint32(pad[x+n])
			sum -= uint32(pad[x])
		}
	}
}

// BoxCols applies a single box-blur pass down each column, sweeping row by row
// so that every memory access stays sequential.
func BoxCols(dst, src []byte, w, h, dstStride, srcStride, radius int, s *Scratch) {
	n := 2*radius + 1
	recip := BoxReciprocal(n)
	half := uint32(n / 2)
	acc := s.acc[:w]

	// Prime the window on row 0. Rows above the image clamp onto row 0, so it
	// contributes radius+1 times.
	clear(acc)
	for k := -radius; k <= radius; k++ {
		row := src[clampRow(k, h)*srcStride:][:w]
		for x := range acc {
			acc[x] += uint32(row[x])
		}
	}

	for y := range h {
		if y > 0 {
			add := src[clampRow(y+radius, h)*srcStride:][:w]
			sub := src[clampRow(y-radius-1, h)*srcStride:][:w]
			for x := range acc {
				acc[x] += uint32(add[x])
				acc[x] -= uint32(sub[x])
			}
		}
		out := dst[y*dstStride:][:w]
		for x := range acc {
			out[x] = boxDiv(acc[x], half, recip)
		}
	}
}
