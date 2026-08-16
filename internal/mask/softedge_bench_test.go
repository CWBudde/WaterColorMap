package mask

import (
	"image"
	"math/rand"
	"testing"
)

// benchWidth is the padded metatile width a 256 tile is actually processed at.
const benchWidth = 384

// BenchmarkSoftEdgeRow compares the dispatched kernel against the portable one at
// the row width the renderer produces. On a host without AVX2, or under the purego
// tag, the two sub-benchmarks measure the same code.
func BenchmarkSoftEdgeRow(b *testing.B) {
	rng := rand.New(rand.NewSource(7))
	src, maskRow := randomRow(rng, benchWidth)
	dst := make([]byte, len(src))

	b.Run("scalar", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		for b.Loop() {
			softEdgeRowGo(dst, src, maskRow, 0.8, benchWidth)
		}
	})

	b.Run("dispatch", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		for b.Loop() {
			softEdgeRow(dst, src, maskRow, 0.8, benchWidth)
		}
	})
}

// BenchmarkApplySoftEdgeMaskInto measures the whole pass over a padded metatile,
// reusing its destination the way the pipeline does.
func BenchmarkApplySoftEdgeMaskInto(b *testing.B) {
	rng := rand.New(rand.NewSource(8))

	bounds := image.Rect(0, 0, benchWidth, benchWidth)
	base := image.NewNRGBA(bounds)
	msk := image.NewGray(bounds)
	dst := image.NewNRGBA(bounds)

	for i := range base.Pix {
		base.Pix[i] = byte(rng.Intn(256))
	}

	for i := range msk.Pix {
		msk.Pix[i] = byte(rng.Intn(256))
	}

	b.ResetTimer()

	for b.Loop() {
		ApplySoftEdgeMaskInto(base, msk, 0.8, dst)
	}
}
