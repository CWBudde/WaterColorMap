package mask

import (
	"image"
	"math"
	"sync"
)

// distanceContextPool recycles DistanceContext buffers across calls to
// EuclideanDistanceTransform. The transform is called once per layer per tile
// (and once more per adaptive-noise layer), so allocating a fresh context each
// time dominated the allocation profile of tile generation.
var distanceContextPool sync.Pool

// DistanceContext holds reusable buffers for distance transform operations.
// Reusing these buffers across multiple calls significantly reduces allocations.
type DistanceContext struct {
	// Buffers for distanceTransform1D
	v []int     // parabola vertex positions
	z []float64 // intersection x-coordinates

	// Buffers for EuclideanDistanceTransform
	temp   []float64 // squared distances (flat 1D: y*width+x)
	isEdge []bool    // edge detection (flat 1D: y*width+x)
	rowBuf []float64 // row input/output buffer
	colBuf []float64 // column input/output buffer
}

// NewDistanceContext creates a context sized for images up to maxDim x maxDim.
func NewDistanceContext(maxDim int) *DistanceContext {
	return &DistanceContext{
		v:      make([]int, maxDim),
		z:      make([]float64, maxDim+1),
		temp:   make([]float64, maxDim*maxDim),
		isEdge: make([]bool, maxDim*maxDim),
		rowBuf: make([]float64, maxDim),
		colBuf: make([]float64, maxDim),
	}
}

// EnsureCapacity grows buffers if needed for the given dimensions.
func (c *DistanceContext) EnsureCapacity(width, height int) {
	maxDim := width
	if height > maxDim {
		maxDim = height
	}
	area := width * height

	if len(c.v) < maxDim {
		c.v = make([]int, maxDim)
	}
	if len(c.z) < maxDim+1 {
		c.z = make([]float64, maxDim+1)
	}
	if len(c.temp) < area {
		c.temp = make([]float64, area)
	}
	if len(c.isEdge) < area {
		c.isEdge = make([]bool, area)
	}
	if len(c.rowBuf) < width {
		c.rowBuf = make([]float64, width)
	}
	if len(c.colBuf) < height {
		c.colBuf = make([]float64, height)
	}
}

// EuclideanDistanceTransform computes the Euclidean distance from each "inside"
// pixel (value > 0) to the nearest boundary (value == 0) using the Felzenszwalb
// & Huttenlocher separable squared distance transform algorithm.
//
// Returns distances normalized to 0-255 range, where:
//   - 0 = at boundary (edge)
//   - 255 = maximum distance (center of feature)
//
// The maxDistance parameter caps the distance calculation for normalization.
// For example, maxDistance=50.0 means distances are normalized such that
// 50 pixels from the edge maps to 255.
//
// Algorithm: O(n) complexity using two separable 1D passes (horizontal, vertical)
// with parabola lower envelope method.
func EuclideanDistanceTransform(mask *image.Gray, maxDistance float64) *image.Gray {
	bounds := mask.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	// Borrow a context from the pool; EnsureCapacity (called by the WithContext
	// variant) grows the buffers when the pooled ones are too small. Oversized
	// buffers are harmless because every loop is bounded by width/height.
	// Nothing from the context escapes: the result image is freshly allocated.
	ctx, ok := distanceContextPool.Get().(*DistanceContext)
	if !ok || ctx == nil {
		ctx = NewDistanceContext(max(width, height))
	}
	defer distanceContextPool.Put(ctx)

	return EuclideanDistanceTransformWithContext(mask, maxDistance, ctx)
}

// EuclideanDistanceTransformWithContext is like EuclideanDistanceTransform but uses
// preallocated buffers from the provided context to avoid allocations.
func EuclideanDistanceTransformWithContext(mask *image.Gray, maxDistance float64, ctx *DistanceContext) *image.Gray {
	dst := image.NewGray(mask.Bounds())
	EuclideanDistanceTransformIntoWithContext(mask, maxDistance, ctx, dst)

	return dst
}

// EuclideanDistanceTransformIntoWithContext is EuclideanDistanceTransformWithContext
// writing into a caller-owned destination, which must have the same bounds as mask.
//
// Safe in place (dst == mask): the image is only read while the scratch buffers are
// filled, and the final normalisation reads and writes the same coordinate in the same
// iteration.
func EuclideanDistanceTransformIntoWithContext(
	mask *image.Gray, maxDistance float64, ctx *DistanceContext, dst *image.Gray,
) {
	bounds := mask.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	// Ensure context has enough capacity
	ctx.EnsureCapacity(width, height)

	infinity := maxDistance * maxDistance * 2.0

	// Use flat 1D slices from context
	temp := ctx.temp
	isEdge := ctx.isEdge

	// Neither buffer is cleared first: detectEdgePixels assigns every one of the
	// width*height entries it will later be read at, and temp is overwritten completely.
	detectEdgePixels(mask, isEdge, width, height)

	// Now initialize based on edge detection
	initDistanceField(mask, temp, isEdge, width, height, infinity)

	// Separable passes: rows then columns (complete Euclidean distance)
	distanceTransformRows(temp, ctx, width, height)
	distanceTransformColumns(temp, ctx, width, height)

	// Convert squared distances to distances and normalize to 0-255
	normalizeDistanceFieldInto(mask, temp, width, maxDistance, infinity, dst)
}

// detectEdgePixels marks every foreground pixel that has a 4-connected background
// neighbour. Every entry is assigned, background pixels included, so the caller does not
// have to clear the buffer first.
//
// The stencil reads three rows, so the row above and below are resolved once per row
// rather than once per pixel. Both are nil at the top and bottom edge, where the
// original bounded the neighbour check by y instead.
func detectEdgePixels(mask *image.Gray, isEdge []bool, width, height int) {
	bounds := mask.Bounds()

	for y := 0; y < height; y++ {
		row := grayRow(mask, bounds.Min.X, bounds.Min.Y+y, width)

		var above, below []uint8
		if y > 0 {
			above = grayRow(mask, bounds.Min.X, bounds.Min.Y+y-1, width)
		}
		if y < height-1 {
			below = grayRow(mask, bounds.Min.X, bounds.Min.Y+y+1, width)
		}

		out := isEdge[y*width:][:width]
		for x, val := range row {
			if val == 0 {
				out[x] = false

				continue
			}

			// Any 4-connected background neighbour makes this an edge pixel.
			out[x] = (x > 0 && row[x-1] == 0) ||
				(x < width-1 && row[x+1] == 0) ||
				(above != nil && above[x] == 0) ||
				(below != nil && below[x] == 0)
		}
	}
}

// initDistanceField seeds the squared-distance field: edge pixels at 0, everything else at infinity.
func initDistanceField(mask *image.Gray, temp []float64, isEdge []bool, width, height int, infinity float64) {
	bounds := mask.Bounds()

	for y := 0; y < height; y++ {
		row := grayRow(mask, bounds.Min.X, bounds.Min.Y+y, width)
		edges := isEdge[y*width:][:width]
		dst := temp[y*width:][:width]

		for x, val := range row {
			if val > 0 && edges[x] {
				dst[x] = 0.0 // Edge pixel - distance is 0

				continue
			}

			// Interior pixels need a distance computed; background pixels are outside the shape.
			dst[x] = infinity
		}
	}
}

// distanceTransformRows runs the horizontal 1D distance transform pass in place.
func distanceTransformRows(temp []float64, ctx *DistanceContext, width, height int) {
	rowBuf := ctx.rowBuf

	for y := 0; y < height; y++ {
		rowStart := y * width
		// Copy row to buffer
		for x := 0; x < width; x++ {
			rowBuf[x] = temp[rowStart+x]
		}
		// Transform in place using v and z buffers
		distanceTransform1DWithBuffers(rowBuf[:width], rowBuf[:width], ctx.v, ctx.z)
		// Copy back
		for x := 0; x < width; x++ {
			temp[rowStart+x] = rowBuf[x]
		}
	}
}

// distanceTransformColumns runs the vertical 1D distance transform pass in place.
func distanceTransformColumns(temp []float64, ctx *DistanceContext, width, height int) {
	colBuf := ctx.colBuf

	for x := 0; x < width; x++ {
		// Extract column to buffer
		for y := 0; y < height; y++ {
			colBuf[y] = temp[y*width+x]
		}
		// Transform in place
		distanceTransform1DWithBuffers(colBuf[:height], colBuf[:height], ctx.v, ctx.z)
		// Write back
		for y := 0; y < height; y++ {
			temp[y*width+x] = colBuf[y]
		}
	}
}

// normalizeDistanceFieldInto converts squared distances into a 0-255 gray image.
// Each pixel of mask is read and written at the same coordinate in the same iteration,
// so output may be mask itself.
func normalizeDistanceFieldInto(
	mask *image.Gray, temp []float64, width int, maxDistance, infinity float64, output *image.Gray,
) {
	bounds := mask.Bounds()
	maxDistSq := maxDistance * maxDistance

	// The distance field is indexed from the mask's origin; the output is indexed from
	// its own, and may be smaller than the mask, which SetGray used to absorb.
	r := writeRect(bounds, output.Bounds())
	w := r.Dx()
	if w == 0 {
		return
	}

	tempX := r.Min.X - bounds.Min.X

	for y := r.Min.Y; y < r.Max.Y; y++ {
		srcRow := grayRow(mask, r.Min.X, y, w)
		dstRow := grayRow(output, r.Min.X, y, w)
		tempRow := temp[(y-bounds.Min.Y)*width+tempX:][:w]

		for i, val := range srcRow {
			dstRow[i] = normalizedDistanceValue(val, tempRow[i], maxDistSq, maxDistance, infinity)
		}
	}
}

// normalizedDistanceValue maps a single pixel's squared distance to its gray output value.
func normalizedDistanceValue(val uint8, distSq, maxDistSq, maxDistance, infinity float64) uint8 {
	// Background pixels (outside shape) remain at 0
	if val == 0 {
		return 0
	}

	// Interior pixels: if still at infinity, clamp to maxDistance
	// (this happens when distance exceeds maxDistance)
	if distSq >= infinity/2 {
		return 255
	}

	// Clamp to maxDistance and normalize
	if distSq >= maxDistSq {
		return 255
	}

	dist := math.Sqrt(distSq)

	return uint8(255.0 * dist / maxDistance)
}

// distanceTransform1DWithBuffers computes the squared distance transform using provided buffers.
// v must have length >= n, z must have length >= n+1 where n = len(input).
// This avoids allocations when called repeatedly.
func distanceTransform1DWithBuffers(input []float64, output []float64, v []int, z []float64) {
	n := len(input)

	k := 0 // Index of rightmost parabola in lower envelope
	v[0] = 0
	z[0] = math.Inf(-1)
	z[1] = math.Inf(1)

	// Build lower envelope of parabolas
	for q := 1; q < n; q++ {
		// Compute intersection of parabola from q with rightmost parabola in envelope
		// Parabola from position i: f_i(x) = (x - i)^2 + input[i]
		// Find intersection s where f_v[k](s) = f_q(s)
		var s float64
		for k >= 0 {
			// Solve (s - v[k])^2 + input[v[k]] = (s - q)^2 + input[q]
			// Expands to: s = ((input[q] + q^2) - (input[v[k]] + v[k]^2)) / (2*(q - v[k]))
			s = ((input[q] + float64(q*q)) - (input[v[k]] + float64(v[k]*v[k]))) /
				(2.0 * float64(q-v[k]))

			if s <= z[k] {
				// Remove this parabola from envelope (it's completely dominated)
				k--
			} else {
				// This parabola stays in envelope
				break
			}
		}

		// Add parabola q to envelope
		k++
		v[k] = q
		z[k] = s
		z[k+1] = math.Inf(1)
	}

	// Sample the lower envelope to get output distances
	k = 0
	for q := 0; q < n; q++ {
		// Find which parabola is minimal at position q
		for z[k+1] < float64(q) {
			k++
		}
		// Compute squared distance: (q - v[k])^2 + input[v[k]]
		dx := float64(q - v[k])
		output[q] = dx*dx + input[v[k]]
	}
}

// DistanceToIntensity converts a distance mask to an intensity mask using
// a power curve falloff: I = pow(1 - D/R, gamma)
//
// Input: distMask with values 0-255 where 0=boundary, 255=max distance
// Output: intensity mask with values 0-255 where 0=max effect (edge), 255=no effect (center)
//
// The gamma parameter controls curve shape:
//   - gamma > 1: steeper falloff near edges (more concentrated darkening)
//   - gamma = 1: linear falloff
//   - gamma < 1: flatter falloff near edges (more diffuse darkening)
//
// The output is suitable for use with ApplySoftEdgeMask or similar edge darkening functions.
func DistanceToIntensity(distMask *image.Gray, gamma float64) *image.Gray {
	output := image.NewGray(distMask.Bounds())
	DistanceToIntensityInto(distMask, gamma, output)

	return output
}

// DistanceToIntensityInto is DistanceToIntensity writing into a caller-owned destination,
// which must have the same bounds as distMask. Safe in place: each pixel is read before
// it is written.
func DistanceToIntensityInto(distMask *image.Gray, gamma float64, output *image.Gray) {
	distanceToIntensityIntoRect(distMask, gamma, output, distMask.Bounds())
}

// distanceToIntensityIntoRect is DistanceToIntensityInto restricted to a rectangle, so
// that a destination larger than the region being computed keeps the rest of its pixels
// untouched instead of running the curve over them.
func distanceToIntensityIntoRect(distMask *image.Gray, gamma float64, output *image.Gray, bounds image.Rectangle) {
	r := writeRect(bounds, output.Bounds())
	w := r.Dx()

	for y := r.Min.Y; y < r.Max.Y; y++ {
		// A distance mask narrower than the rectangle read zero outside itself, which is
		// what GrayAt returns; a nil row falls back to it.
		srcRow := grayRow(distMask, r.Min.X, y, w)
		dstRow := grayRow(output, r.Min.X, y, w)

		for i := range dstRow {
			dist := uint8(0)
			if srcRow != nil {
				dist = srcRow[i]
			} else {
				dist = distMask.GrayAt(r.Min.X+i, y).Y
			}

			// Get normalized distance (0-255)
			distNorm := float64(dist) / 255.0

			// I = pow(1 - D/R, gamma)
			base := math.Max(0, 1.0-distNorm)
			intensity := math.Pow(base, gamma)

			// Convert intensity (0-1) to output (0-255)
			// Invert: 0 intensity = 255 output (no darkening at center)
			//         1 intensity = 0 output (max darkening at edge)
			dstRow[i] = uint8(255.0 * (1.0 - intensity))
		}
	}
}

// CreateDistanceEdgeMask is a high-level convenience function that combines
// distance transform and intensity mapping in a single call.
//
// It computes the Euclidean distance transform and applies a power curve falloff
// to create an edge mask suitable for edge darkening effects.
//
// Parameters:
//   - mask: Binary mask (0=outside/boundary, >0=inside)
//   - radius: Distance parameter in pixels (controls how far the effect extends)
//   - gamma: Power curve exponent (>1 for steeper falloff, <1 for gentler falloff)
//
// Returns: Grayscale mask where 0=max darkening (at edges), 255=no darkening (at center)
func CreateDistanceEdgeMask(mask *image.Gray, radius float64, gamma float64) *image.Gray {
	distMask := EuclideanDistanceTransform(mask, radius)
	return DistanceToIntensity(distMask, gamma)
}

// CreateDistanceEdgeMaskWithContext is like CreateDistanceEdgeMask but uses
// preallocated buffers from the provided context to avoid allocations.
func CreateDistanceEdgeMaskWithContext(mask *image.Gray, radius float64, gamma float64, ctx *DistanceContext) *image.Gray {
	dst := image.NewGray(mask.Bounds())
	CreateDistanceEdgeMaskIntoWithContext(mask, radius, gamma, ctx, dst)

	return dst
}

// CreateDistanceEdgeMaskIntoWithContext is CreateDistanceEdgeMaskWithContext writing into a
// caller-owned destination.
//
// It needs no intermediate image of its own: the distance field lands in dst and the
// intensity curve is then applied to it in place. Both passes are safe in place, but
// callers here always want their input mask preserved, so pass a distinct destination.
//
// Unlike the other *Into helpers, dst is allowed to be larger than mask - the watercolor
// processor keeps one tile-sized edge-mask buffer and paints masks of any size through it.
// Only mask's bounds are written, by both passes; the rest of dst is left exactly as it
// was, so a caller reusing a buffer that way has to clear it. Bounding both passes the
// same way is the point: the transform's writes are clipped to mask's bounds anyway, so an
// intensity pass running over all of dst would fold the previous layer's fringe back in.
func CreateDistanceEdgeMaskIntoWithContext(
	mask *image.Gray, radius float64, gamma float64, ctx *DistanceContext, dst *image.Gray,
) {
	EuclideanDistanceTransformIntoWithContext(mask, radius, ctx, dst)
	distanceToIntensityIntoRect(dst, gamma, dst, mask.Bounds())
}
