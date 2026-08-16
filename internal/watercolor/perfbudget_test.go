//go:build !race

// Performance budgets for the per-tile paint path (PLAN.md 5.11.8).
//
// The whole file is excluded under -race on purpose. The race detector adds its
// own allocations and multiplies wall time, so every number below would be
// measuring the detector rather than the pipeline.
//
// What is gated here and why is written up in
// docs/performance/performance-monitoring.md. The short version: allocation
// counts and allocated bytes are near-deterministic and make good hard gates;
// wall clock on this project's machines is not, so the only time budget is a
// catastrophe ceiling set two orders of magnitude above the measurement.

package watercolor

import (
	"image"
	"image/color"
	"math"
	"runtime"
	"runtime/debug"
	"testing"
	"time"

	"github.com/cwbudde/watercolormap/internal/geojson"
	"github.com/cwbudde/watercolormap/internal/mask"
)

// Budgets for one 256 px tile painted through all five production layers, i.e.
// the workload BenchmarkFullPipeline runs.
//
// Measured on main at 7ebf18b, i7-1255U / go1.26.5 / linux-amd64:
// exactly 22 allocations and 1 704 640 B per tile on every one of five runs,
// and 17.2-19.0 ms of wall time. The PLAN.md figures of "38 allocs, 2.2 MiB"
// predate 5.11.4 and include GC-cleared pool refills; see
// docs/performance/performance-monitoring.md § "Where the numbers came from".
const (
	// +6 allocations over the measured 22. Enough that a helper gaining one
	// small heap value does not fail the build, and far too little to hide a
	// per-pixel or per-row allocation, which costs thousands.
	tileAllocBudget = 28

	// +5% over the measured 1 704 640 B. Every buffer here is sized from the
	// metatile dimensions, so a real regression adds a whole image plane --
	// 147 KiB for a 384x384 gray one (+8.6%), 590 KiB for an NRGBA one -- and
	// clears this comfortably. The 5% absorbs size-class differences on other
	// architectures, which is all the variation this measurement has.
	tileBytesBudget = 1_790_000

	// A catastrophe ceiling, not a performance target: ~80x the ~19 ms
	// measured on an idle machine, checked against the *fastest* of the runs
	// (see measurePerRun).
	//
	// 80x looks absurd until the alternative is priced. The same fastest-run
	// figure reaches 37 ms when `just test` has twelve package binaries in
	// flight on this 12-thread laptop, and a 2-core GitHub runner doing the
	// same thing has no headroom left at 400 ms. A ceiling that fires on a
	// busy runner teaches everyone to ignore it, and then it catches nothing
	// at all. At 1.5 s it still catches what it is for -- an inner loop that
	// went quadratic, or a blur planner falling back to naive convolution --
	// because those are 10x to 1000x events, not 3x ones. Anything finer than
	// that is the benchmark workflow's job, and it does not gate.
	tileTimeBudget = 1500 * time.Millisecond

	// Runs per measurement. Allocations and bytes are averaged over them and
	// are exact at any count; the time budget takes the fastest of them, which
	// is what this many runs is really for. Total cost: about a fifth of a
	// second.
	budgetRuns = 10
)

// TestTilePaintBudget is the allocation gate 5.11.3 earned and 5.11.8 keeps.
//
// It is deliberately the *whole tile*, not a single kernel: the per-kernel
// zero-allocation assertions already live in internal/mask/into_test.go and
// TestPaintLayerAllocationBudget covers one layer. What was missing was a
// number for the unit the project actually ships.
func TestTilePaintBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("performance budget: skipped under -short")
	}

	paint := tileBudgetWorkload(t, 256, 13)

	allocs, bytes, elapsed := measurePerRun(budgetRuns, paint)

	t.Logf("per tile: %.0f allocs, %.0f B, %v (fastest of %d)", allocs, bytes, elapsed.Round(time.Microsecond), budgetRuns)

	if allocs > tileAllocBudget {
		t.Errorf("tile paint made %.0f allocations, budget is %d\n"+
			"See docs/performance/performance-monitoring.md before raising this.",
			allocs, tileAllocBudget)
	}

	if bytes > tileBytesBudget {
		t.Errorf("tile paint allocated %.0f B, budget is %d\n"+
			"See docs/performance/performance-monitoring.md before raising this.",
			bytes, tileBytesBudget)
	}

	if elapsed > tileTimeBudget {
		t.Errorf("tile paint took %v in its fastest of %d runs, catastrophe "+
			"ceiling is %v\nThis budget is ~80x the measured cost and is "+
			"checked against the fastest run. Exceeding it means an "+
			"algorithmic regression, not a slow machine.",
			elapsed.Round(time.Microsecond), budgetRuns, tileTimeBudget)
	}
}

// tileBudgetWorkload builds the same five-layer workload as
// BenchmarkFullPipeline and returns a closure that paints one tile.
//
// zoom is applied through ZoomAdjustedBlurSigma exactly as
// pipeline.Generator.watercolorParams does, so the per-zoom benchmark below
// measures what production would run at that zoom.
func tileBudgetWorkload(tb testing.TB, tileSize, zoom int) func() {
	tb.Helper()

	layers := []struct {
		img   image.Image
		layer geojson.LayerType
	}{
		{createComplexLayer(tileSize, color.NRGBA{R: 100, G: 150, B: 200, A: 255}), geojson.LayerWater},
		{createComplexLayer(tileSize, color.NRGBA{R: 220, G: 200, B: 170, A: 255}), geojson.LayerLand},
		{createComplexLayer(tileSize, color.NRGBA{R: 120, G: 180, B: 120, A: 255}), geojson.LayerParks},
		{createComplexLayer(tileSize, color.NRGBA{R: 255, G: 255, B: 255, A: 255}), geojson.LayerRoads},
		{createComplexLayer(tileSize, color.NRGBA{R: 255, G: 230, B: 120, A: 255}), geojson.LayerHighways},
	}

	textures := map[geojson.LayerType]image.Image{
		geojson.LayerLand:     benchSolidTexture(8, 8, color.NRGBA{R: 240, G: 235, B: 220, A: 255}),
		geojson.LayerWater:    benchSolidTexture(8, 8, color.NRGBA{R: 120, G: 150, B: 200, A: 255}),
		geojson.LayerParks:    benchSolidTexture(8, 8, color.NRGBA{R: 140, G: 180, B: 140, A: 255}),
		geojson.LayerRoads:    benchSolidTexture(8, 8, color.NRGBA{R: 255, G: 255, B: 255, A: 255}),
		geojson.LayerHighways: benchSolidTexture(8, 8, color.NRGBA{R: 255, G: 230, B: 120, A: 255}),
	}

	params := DefaultParams(tileSize, 42, textures)
	params.BlurSigma = ZoomAdjustedBlurSigma(params.BlurSigma, zoom)
	params.AntialiasSigma = ZoomAdjustedBlurSigma(params.AntialiasSigma, zoom)
	params.OffsetX = 0
	params.OffsetY = 0
	params.PerlinNoise = mask.GeneratePerlinNoiseWithOffset(
		tileSize, tileSize, params.NoiseScale, params.Seed, params.OffsetX, params.OffsetY,
	)

	return func() {
		for _, l := range layers {
			if _, err := PaintLayer(l.img, l.layer, params); err != nil {
				panic("PaintLayer: " + err.Error())
			}
		}
	}
}

// measurePerRun reports allocations and allocated bytes per run of f, plus the
// wall time of its *fastest* run.
//
// Fastest, not mean, and that is a deliberate choice about which number can be
// asserted on. A mean is dragged upward by whatever else the machine was doing:
// the same tile measures ~18 ms with the package alone and 33-46 ms when the
// full `go test ./... -v` suite is running twelve package binaries at once. The
// minimum over ten runs is the closest thing to an unloaded measurement that can
// be taken on a loaded machine, because a run can be delayed by contention but
// never finish faster than the work takes.
//
// testing.AllocsPerRun would cover the first of those but not the second, and
// testing.Benchmark obeys the process-wide -benchtime flag, which would let a
// caller turn a sub-second gate into a minute-long one.
//
// The garbage collector is switched off for the measurement, and that is what
// makes the result reproducible rather than merely stable-ish. Every scratch
// buffer in this pipeline comes from a sync.Pool, and a GC empties those pools;
// a collection landing mid-run therefore shows up as several megabyte
// allocations that have nothing to do with the code under test. It is why
// BenchmarkFullPipeline reports anywhere between 26 and 28 allocs/op on the
// same binary. Ten runs of a ~2 MB workload is ~20 MB of retained garbage,
// which is affordable; the collector is restored either way.
func measurePerRun(runs int, f func()) (allocs, bytes float64, elapsed time.Duration) {
	// Restore the collector and hand the rest of the package a clean heap. The
	// explicit collection is not tidiness: leaving ~20 MB of garbage and a
	// stale GC pacing behind would shift when the *next* test's collection
	// lands, and TestPaintLayerAllocationBudget in scratch_test.go measures
	// with testing.AllocsPerRun, which a pool-clearing GC does perturb.
	previous := debug.SetGCPercent(-1)
	defer func() {
		debug.SetGCPercent(previous)
		runtime.GC()
	}()

	// Warm every pooled buffer first, after the collector is already off. The
	// first call grows the scratch contexts, and counting that growth would
	// measure the pool rather than the tile.
	f()

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)

	fastest := time.Duration(math.MaxInt64)
	for range runs {
		start := time.Now()
		f()
		if d := time.Since(start); d < fastest {
			fastest = d
		}
	}

	runtime.ReadMemStats(&after)

	n := float64(runs)
	return float64(after.Mallocs-before.Mallocs) / n,
		float64(after.TotalAlloc-before.TotalAlloc) / n,
		fastest
}

// BenchmarkTileByZoom measures the paint stage at each of the three zoom tiers
// ZoomAdjustedBlurSigma defines (x1.4 at z<=11, x1.0 at z12-13, x0.7 at z>=14).
//
// Run it with `just bench-zoom`. The numbers it produces are written up in
// docs/performance/performance-monitoring.md § "Performance per zoom level";
// the point of the benchmark is that the claim there stays checkable.
func BenchmarkTileByZoom(b *testing.B) {
	for _, zoom := range []int{6, 11, 13, 14, 17} {
		b.Run(zoomName(zoom), func(b *testing.B) {
			paint := tileBudgetWorkload(b, 256, zoom)
			paint()

			b.ReportAllocs()
			b.ResetTimer()

			for b.Loop() {
				paint()
			}
		})
	}
}

func zoomName(zoom int) string {
	return "z" + string(rune('0'+zoom/10)) + string(rune('0'+zoom%10))
}
