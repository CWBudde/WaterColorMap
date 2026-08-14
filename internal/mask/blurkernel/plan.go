// Package blurkernel holds the low-level separable blur kernels used by the
// mask package.
//
// The kernels take flat byte slices with an explicit stride rather than
// *image.Gray, for two reasons: the assembly implementations bind to that shape
// directly, and it keeps the kernels usable for both the row and column passes
// without a second set of accessors.
//
// All kernels use clamp-to-edge (replicated border) sampling and round-to-nearest
// output, and all arithmetic is fixed point with FracBits fractional bits.
package blurkernel

import "math"

// FracBits is the fixed-point precision of the kernel weights. Weights for one
// pass sum to exactly 1<<FracBits, so a full-white input yields exactly 255 and
// no output clamping is needed. 16 bits keeps the accumulator inside uint32
// (255 << 16 per tap, times at most a few dozen taps) and matches what the AVX2
// path can do with a 16x16->32 multiply.
const FracBits = 16

// One is the fixed-point representation of 1.0.
const One = 1 << FracBits

// maxConvRadius bounds the direct-convolution path. Above it the 3-pass box
// approximation takes over: box cost is flat in radius while convolution cost
// grows with it, and by that width the box radii track sigma closely enough
// that the accuracy difference stops mattering.
//
// 12 covers sigma up to 4, which includes every sigma DefaultParams produces
// except the land shade (7.48). Measured on a 384px metatile with AVX2, sigma
// 3.43 costs 543µs convolved against 1100µs boxed, and the two cross over
// around sigma 7. Without the assembly the crossover is much lower, but this
// has to be one fixed number: if the choice varied with CPU features, two
// machines would render different pixels for the same tile.
const maxConvRadius = 12

// Mode selects which kernel family a Plan uses.
type Mode int

const (
	// ModeNone is an identity blur: sigma is too small to change anything.
	ModeNone Mode = iota
	// ModeConv is a direct separable Gaussian convolution.
	ModeConv
	// ModeBox is a 3-pass box blur approximating a Gaussian.
	ModeBox
)

func (m Mode) String() string {
	switch m {
	case ModeNone:
		return "none"
	case ModeConv:
		return "conv"
	case ModeBox:
		return "box"
	}
	return "unknown"
}

// Plan is the kernel configuration derived from a sigma. Deriving it once and
// reusing it for both passes keeps the sigma-to-kernel policy in a single place
// and out of the hot loops.
type Plan struct {
	// Taps holds the half-kernel for ModeConv: Taps[0] is the centre weight and
	// Taps[k] the weight at offset ±k, so the radius is len(Taps)-1.
	Taps []uint16
	// Radii holds the three box radii for ModeBox. A radius of 0 is an identity
	// pass and is skipped.
	Radii [3]int
	Mode  Mode
}

// Radius returns the widest distance the plan reads from any output pixel,
// which is what callers need to size padding.
func (p Plan) Radius() int {
	switch p.Mode {
	case ModeConv:
		return len(p.Taps) - 1
	case ModeBox:
		return p.Radii[0] + p.Radii[1] + p.Radii[2]
	case ModeNone:
		return 0
	}
	return 0
}

// PlanFor derives the kernel configuration for a Gaussian of the given sigma.
//
// Small sigmas use a direct convolution rather than the box approximation. A
// 3-pass box blur cannot represent sigma below ~0.8 at all — the narrowest
// non-trivial box is 3 wide, which on its own is already sigma 0.82 — so the
// classic boxesForGauss split degenerates to identity there.
//
// DefaultParams asks for 1.41 to 7.48, and ZoomAdjustedBlurSigma stretches the
// global blur and antialias across 0.99 to 3.43, so everything but the 7.48
// land shade lands on the direct path. That makes the convolution the common
// case and the box blur the exception — which is also why maxConvRadius below
// is set where it is.
func PlanFor(sigma float64) Plan {
	if sigma <= 0 || sigma < 0.05 {
		return Plan{Mode: ModeNone}
	}

	if radius := int(math.Ceil(3 * sigma)); radius <= maxConvRadius {
		taps := GaussianTaps(sigma, radius)
		if taps == nil {
			return Plan{Mode: ModeNone}
		}
		return Plan{Mode: ModeConv, Taps: taps}
	}

	return Plan{Mode: ModeBox, Radii: boxesForGauss(sigma)}
}

// GaussianTaps builds a fixed-point half-kernel for the given sigma, normalised
// so that the full kernel sums to exactly One.
//
// It returns nil when the kernel is an identity at this precision, which
// happens for sigma below roughly 0.25: every off-centre weight rounds to zero
// and the centre weight would need the unrepresentable value One. Callers must
// treat nil as "no blur to do".
func GaussianTaps(sigma float64, radius int) []uint16 {
	if radius < 1 {
		radius = 1
	}

	weights := make([]float64, radius+1)
	var total float64
	for k := range weights {
		weights[k] = math.Exp(-float64(k*k) / (2 * sigma * sigma))
		// Every tap but the centre appears on both sides of the kernel.
		if k == 0 {
			total += weights[k]
		} else {
			total += 2 * weights[k]
		}
	}

	taps := make([]uint16, radius+1)
	sum := 0
	for k := range taps {
		taps[k] = uint16(math.Round(weights[k] / total * One))
		if k == 0 {
			sum += int(taps[k])
		} else {
			sum += 2 * int(taps[k])
		}
	}

	// Trim tail weights that rounded away to nothing; they would only cost the
	// kernels work without changing any output.
	for len(taps) > 1 && taps[len(taps)-1] == 0 {
		taps = taps[:len(taps)-1]
	}

	// Push the rounding residual into the centre tap so the kernel sums to
	// exactly One and a full-white input maps back to exactly 255.
	centre := int(taps[0]) + One - sum
	if centre >= One {
		// Every side weight vanished, so the kernel is an identity and the
		// centre weight has no uint16 representation.
		return nil
	}
	taps[0] = uint16(centre)

	return taps
}

// boxesForGauss splits a Gaussian into three box radii (Kutskir's construction).
// Using three distinct radii rather than the same radius three times is what
// lets the approximation actually track sigma instead of quantising hard.
func boxesForGauss(sigma float64) [3]int {
	const n = 3

	variance := 12 * sigma * sigma
	wIdeal := math.Sqrt(variance/n + 1)

	wl := int(math.Floor(wIdeal))
	if wl%2 == 0 {
		wl--
	}
	if wl < 1 {
		wl = 1
	}
	wu := wl + 2

	mIdeal := (variance - float64(n*wl*wl) - float64(4*n*wl) - float64(3*n)) /
		float64(-4*wl-4)
	m := int(math.Round(mIdeal))

	var radii [3]int
	for i := range radii {
		size := wu
		if i < m {
			size = wl
		}
		radii[i] = (size - 1) / 2
	}
	return radii
}
