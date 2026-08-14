package mask

import (
	"image"
	"sync"

	"github.com/cwbudde/watercolormap/internal/mask/blurkernel"
)

// blurContextPool recycles the scratch buffers a blur needs. Tile generation
// runs a dozen or more blurs per tile, so allocating fresh buffers per call
// showed up plainly in the allocation profile.
var blurContextPool sync.Pool

// blurContext holds the reusable buffers for a blur. It is not part of the API:
// the pool below hands one to every call, so callers get the reuse without
// having to thread anything through.
type blurContext struct {
	// temp is the intermediate image between the row and column passes.
	temp    *image.Gray
	scratch blurkernel.Scratch
}

func (c *blurContext) ensure(width, height, radius int) {
	c.scratch.Ensure(width, radius)
	if c.temp == nil || c.temp.Bounds().Dx() < width || c.temp.Bounds().Dy() < height {
		c.temp = image.NewGray(image.Rect(0, 0, width, height))
	}
}

func acquireBlurContext(width, height, radius int) *blurContext {
	c, ok := blurContextPool.Get().(*blurContext)
	if !ok || c == nil {
		c = &blurContext{}
	}
	c.ensure(width, height, radius)
	return c
}

func releaseBlurContext(c *blurContext) {
	blurContextPool.Put(c)
}

// BoxBlurSigma blurs a mask with a Gaussian of the given sigma and returns a
// new image. Prefer BoxBlurSigmaInto on hot paths, which reuses buffers.
//
// The name is historical: small sigmas are convolved directly rather than
// approximated with boxes, because a 3-pass box blur degenerates to identity
// below sigma 0.8. See blurkernel.PlanFor.
func BoxBlurSigma(m *image.Gray, sigma float32) *image.Gray {
	dst := image.NewGray(m.Bounds())
	BoxBlurSigmaInto(dst, m, sigma)
	return dst
}

// BoxBlurSigmaInto blurs src into dst, which must have the same bounds. Passing
// the same image for both blurs in place, which is supported; passing two
// different images that share pixels is not.
func BoxBlurSigmaInto(dst, src *image.Gray, sigma float32) {
	plan := blurkernel.PlanFor(float64(sigma))
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()

	if plan.Mode == blurkernel.ModeNone || w == 0 || h == 0 {
		if dst != src {
			copyGray(dst, src, w, h)
		}
		return
	}

	ctx := acquireBlurContext(w, h, plan.Radius())
	defer releaseBlurContext(ctx)

	blurInto(dst, src, w, h, plan, ctx)
}

// blurInto runs the plan's passes, ping-ponging between ctx.temp and dst so
// that only one intermediate buffer is ever needed.
//
// Each pass reads whatever the previous one wrote and writes to whichever of
// dst and temp is not currently the input. src is only ever read, so blurring
// in place (dst == src) works: the first pass lands in temp and the sequence
// alternates from there.
func blurInto(dst, src *image.Gray, w, h int, plan blurkernel.Plan, ctx *blurContext) {
	temp := ctx.temp
	s := &ctx.scratch

	in := src
	target := func() *image.Gray {
		if in == dst {
			return temp
		}
		return dst
	}

	switch plan.Mode {
	case blurkernel.ModeConv:
		out := target()
		blurkernel.ConvRows(out.Pix, in.Pix, w, h, out.Stride, in.Stride, plan.Taps, s)
		in = out
		out = target()
		blurkernel.ConvCols(out.Pix, in.Pix, w, h, out.Stride, in.Stride, plan.Taps, s)
		in = out

	case blurkernel.ModeBox:
		for _, radius := range plan.Radii {
			if radius == 0 {
				continue
			}
			s.Ensure(w, radius)

			out := target()
			blurkernel.BoxRows(out.Pix, in.Pix, w, h, out.Stride, in.Stride, radius, s)
			in = out

			out = target()
			blurkernel.BoxCols(out.Pix, in.Pix, w, h, out.Stride, in.Stride, radius, s)
			in = out
		}

	case blurkernel.ModeNone:
	}

	if in != dst {
		copyGray(dst, in, w, h)
	}
}

// copyGray copies a w x h region from src into dst. The size is explicit
// because a pooled scratch image is often larger than the image being blurred,
// so neither side's bounds can be trusted as the region to copy.
func copyGray(dst, src *image.Gray, w, h int) {
	for y := range h {
		copy(dst.Pix[y*dst.Stride:][:w], src.Pix[y*src.Stride:][:w])
	}
}

// BoxBlur applies a single box blur pass of the given radius in each direction.
func BoxBlur(m *image.Gray, radius int) *image.Gray {
	bounds := m.Bounds()
	dst := image.NewGray(bounds)
	w, h := bounds.Dx(), bounds.Dy()

	// The kernels assume at least one pixel in each direction: the row pass
	// replicates row[0] into the pad, and the column pass clamps row indices
	// against h-1. Both fault on a degenerate image.
	if radius < 1 || w == 0 || h == 0 {
		copyGray(dst, m, w, h)
		return dst
	}

	ctx := acquireBlurContext(w, h, radius)
	defer releaseBlurContext(ctx)

	blurkernel.BoxRows(ctx.temp.Pix, m.Pix, w, h, ctx.temp.Stride, m.Stride, radius, &ctx.scratch)
	blurkernel.BoxCols(dst.Pix, ctx.temp.Pix, w, h, dst.Stride, ctx.temp.Stride, radius, &ctx.scratch)
	return dst
}

// AntialiasEdges applies a light blur to soften sharp mask edges.
func AntialiasEdges(m *image.Gray, sigma float32) *image.Gray {
	return BoxBlurSigma(m, sigma)
}
