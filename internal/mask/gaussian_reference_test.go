package mask

import (
	"image"

	"github.com/disintegration/gift"
)

// GaussianBlur is a true Gaussian blur, kept as the reference the fast kernels
// are measured against in benchmarks and quality tests.
//
// It lives in a test file on purpose. Nothing in the render path uses it any
// more, and keeping it out of the production build keeps the gift dependency
// out of the js/wasm binary that ships to the browser playground.
func GaussianBlur(m *image.Gray, sigma float32) *image.Gray {
	g := gift.New(gift.GaussianBlur(sigma))
	dst := image.NewGray(g.Bounds(m.Bounds()))
	g.Draw(dst, m)
	return dst
}
