package mask

import (
	"image"
	"image/color"
	"strconv"
	"testing"
)

// blurBenchSizes covers both the nominal tile size and the padded metatile size
// that production actually blurs. RequiredPaddingPx floors padding at 64px, so a
// 256px tile is blurred as a 384px image and the padded case is the realistic one.
var blurBenchSizes = []int{256, 384}

// blurBenchSigmas are the sigmas DefaultParams actually produces: 1.41 antialias
// and rivers, 2.45 for the other layer masks and the global blur, 7.48 for the
// land shade. ZoomAdjustedBlurSigma scales the global blur and antialias by 1.4
// at z<=11 and 0.7 at z>=14, so 0.99 and 3.43 are the ends of that range.
var blurBenchSigmas = []float32{0.99, 1.41, 2.45, 3.43, 7.48}

// benchMask builds a radial gradient mask, matching the shape of a real coverage
// mask closely enough for a blur benchmark (blur cost is data-independent anyway).
func benchMask(size int) *image.Gray {
	m := image.NewGray(image.Rect(0, 0, size, size))
	center := size / 2
	for y := range size {
		for x := range size {
			dx, dy := float64(x-center), float64(y-center)
			dist := (dx*dx + dy*dy) / float64(size*size)
			m.SetGray(x, y, color.Gray{Y: uint8(255 * (1.0 - dist))})
		}
	}
	return m
}

func fmtSigma(sigma float32) string {
	return strconv.FormatFloat(float64(sigma), 'g', -1, 32)
}

func BenchmarkBoxBlurSigma(b *testing.B) {
	for _, size := range blurBenchSizes {
		src := benchMask(size)
		for _, sigma := range blurBenchSigmas {
			b.Run(strconv.Itoa(size)+"/sigma="+fmtSigma(sigma), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					_ = BoxBlurSigma(src, sigma)
				}
			})
		}
	}
}

// BenchmarkGaussianBlur measures the gift-based reference blur that BoxBlurSigma
// replaced, so the speed comparison in the docs stays honest.
func BenchmarkGaussianBlur(b *testing.B) {
	for _, size := range blurBenchSizes {
		src := benchMask(size)
		for _, sigma := range blurBenchSigmas {
			b.Run(strconv.Itoa(size)+"/sigma="+fmtSigma(sigma), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					_ = GaussianBlur(src, sigma)
				}
			})
		}
	}
}
