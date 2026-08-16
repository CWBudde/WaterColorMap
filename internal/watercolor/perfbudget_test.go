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
// catastrophe ceiling set ~40x above the measurement.

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

// Budgets for one delivered 256 px tile painted through all five production
// layers -- which means a 384x384 padded metatile, because that is the buffer
// the generator actually paints (see tileBudgetWorkload).
//
// Measured on main at 636971a, i7-1255U / go1.26.5 / linux-amd64: exactly 22
// allocations and 3 834 560 B per tile, bit-identical on every one of ten
// runs, and 35-43 ms of wall time. The PLAN.md figures of "38 allocs, 2.2 MiB"
// predate 5.11.4, were taken on an unpadded 256px buffer, and include
// GC-cleared pool refills; see docs/performance/performance-monitoring.md
// § "Where the numbers came from".
const (
	// +6 allocations over the measured 22. Enough that a helper gaining one
	// small heap value does not fail the build, and far too little to hide a
	// per-pixel or per-row allocation, which costs thousands.
	tileAllocBudget = 28

	// +2% over the measured 3 834 560 B, and the tightness is the point. Every
	// buffer here is sized from the metatile dimensions, so the smallest real
	// regression is a whole extra image plane: 147 456 B for a 384x384 gray one
	// (+3.8%), 589 824 B for an NRGBA one (+15.4%). The budget has to sit below
	// the smaller of those or it gates nothing, which is why it is not the 5%
	// that a 256px measurement could afford.
	//
	// 2% is still ~75 KB of slack, and the measurement needs almost none: the
	// large buffers are exact multiples of the 8 KiB page (147 456 B is 18
	// pages, 589 824 B is 72), so they carry no size-class rounding to differ
	// over between architectures, and the handful of small allocations in the
	// remainder cannot move by tens of kilobytes.
	tileBytesBudget = 3_910_000

	// A catastrophe ceiling, not a performance target: ~40x the ~36 ms measured
	// on an idle machine, checked against the *fastest* of the runs (see
	// measurePerRun).
	//
	// 40x looks absurd until the alternative is priced. The same fastest-run
	// figure roughly doubles when `just test` has twelve package binaries in
	// flight on this 12-thread laptop, and a 2-core GitHub runner doing the
	// same thing has no headroom left at all. A ceiling that fires on a busy
	// runner teaches everyone to ignore it, and then it catches nothing at all.
	// At 3 s it still catches what it is for -- an inner loop that went
	// quadratic, or a blur planner falling back to naive convolution -- because
	// those are 10x to 1000x events, not 3x ones. Anything finer than that is
	// the benchmark workflow's job, and it does not gate.
	tileTimeBudget = 3 * time.Second

	// Runs per measurement. Allocations and bytes are averaged over them and
	// are exact at any count; the time budget takes the fastest of them, which
	// is what this many runs is really for. Total cost: about half a second.
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
			"ceiling is %v\nThis budget is ~40x the measured cost and is "+
			"checked against the fastest run. Exceeding it means an "+
			"algorithmic regression, not a slow machine.",
			elapsed.Round(time.Microsecond), budgetRuns, tileTimeBudget)
	}
}

// tileBudgetWorkload builds the five-layer workload of BenchmarkFullPipeline at
// the size production actually paints, and returns a closure that paints one
// tile.
//
// tileSize is the *delivered* tile size. What gets painted is the padded
// metatile pipeline.Generator.renderLayers works on -- tileSize + 2*padPx, i.e.
// 384x384 for the default 256px tile -- because every buffer in the paint path
// is sized from params.TileSize, and a budget taken at 256 would price 44% of
// the pixels production pays for. The four steps below (scale, zoom sigmas,
// padding, params.TileSize) are the same ones, in the same order, that
// watercolorParams and renderLayers perform; ScaleForTileSize is a no-op at 256
// but keeps the sequence honest at 512.
func tileBudgetWorkload(tb testing.TB, tileSize, zoom int) func() {
	tb.Helper()

	textures := map[geojson.LayerType]image.Image{
		geojson.LayerLand:     benchSolidTexture(8, 8, color.NRGBA{R: 240, G: 235, B: 220, A: 255}),
		geojson.LayerWater:    benchSolidTexture(8, 8, color.NRGBA{R: 120, G: 150, B: 200, A: 255}),
		geojson.LayerParks:    benchSolidTexture(8, 8, color.NRGBA{R: 140, G: 180, B: 140, A: 255}),
		geojson.LayerRoads:    benchSolidTexture(8, 8, color.NRGBA{R: 255, G: 255, B: 255, A: 255}),
		geojson.LayerHighways: benchSolidTexture(8, 8, color.NRGBA{R: 255, G: 230, B: 120, A: 255}),
	}

	params := DefaultParams(tileSize, 42, textures)
	params.ApplyScale(ScaleForTileSize(tileSize))
	params.BlurSigma = ZoomAdjustedBlurSigma(params.BlurSigma, zoom)
	params.AntialiasSigma = ZoomAdjustedBlurSigma(params.AntialiasSigma, zoom)

	padPx := min(RequiredPaddingPx(params), tileSize)
	metatileSize := tileSize + 2*padPx
	params.TileSize = metatileSize

	layers := []struct {
		img   image.Image
		layer geojson.LayerType
	}{
		{createComplexLayer(metatileSize, color.NRGBA{R: 100, G: 150, B: 200, A: 255}), geojson.LayerWater},
		{createComplexLayer(metatileSize, color.NRGBA{R: 220, G: 200, B: 170, A: 255}), geojson.LayerLand},
		{createComplexLayer(metatileSize, color.NRGBA{R: 120, G: 180, B: 120, A: 255}), geojson.LayerParks},
		{createComplexLayer(metatileSize, color.NRGBA{R: 255, G: 255, B: 255, A: 255}), geojson.LayerRoads},
		{createComplexLayer(metatileSize, color.NRGBA{R: 255, G: 230, B: 120, A: 255}), geojson.LayerHighways},
	}

	// Offsets are the metatile's world origin in production; tile (0,0) makes
	// them -padPx, which is a valid noise origin and keeps the workload
	// reproducible.
	params.OffsetX = -padPx
	params.OffsetY = -padPx
	params.PerlinNoise = mask.GeneratePerlinNoiseWithOffset(
		metatileSize, metatileSize, params.NoiseScale, params.Seed, params.OffsetX, params.OffsetY,
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
// the same tile measures ~36 ms with the package alone and roughly twice that
// when the full `go test ./...` suite has twelve package binaries in flight. The
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
// same binary. Ten runs of a ~3.8 MB workload is ~38 MB of retained garbage,
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

	// One P for the duration, and this is about sync.Pool, not about
	// parallelism -- the paint path is single-goroutine, so restricting the
	// scheduler cannot change the work being measured.
	//
	// sync.Pool shards per P. A goroutine that runs for tens of milliseconds is
	// preempted by sysmon and can resume on a different P, whose shard is empty;
	// the next Get then misses and allocates a fresh megabyte-sized buffer. That
	// is exactly what a 384x384 metatile run does: measured with the default 12
	// Ps this test reported 22 allocs / 3.83 MB on most runs and 29 allocs /
	// 4.87 MB when a migration happened to land badly. With a single P there is
	// a single shard, no migration, and the figures are bit-identical run to
	// run. No test in this package calls t.Parallel(), so nothing else is
	// running concurrently to be slowed down by this.
	previousProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previousProcs)

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
