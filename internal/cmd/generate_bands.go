package cmd

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/spf13/viper"

	"github.com/cwbudde/watercolormap/internal/datasource"
	"github.com/cwbudde/watercolormap/internal/pipeline"
	"github.com/cwbudde/watercolormap/internal/tile"
	"github.com/cwbudde/watercolormap/internal/types"
	"github.com/cwbudde/watercolormap/internal/worker"
)

// areaDataSource is a datasource that can answer for an arbitrary box rather
// than a single tile. Declared on the consumer side, mirroring how
// pipeline.DataSource treats its optional bounds-aware half.
type areaDataSource interface {
	FetchAreaData(ctx context.Context, zoom int, bounds types.BoundingBox, opts datasource.AreaFetchOptions) (*types.TileData, error)
}

// bandOptions are the knobs of a banded run, already validated.
type bandOptions struct {
	level      uint32
	minZoom    int
	fetchAhead int
	timeoutSec int
}

// bandStats is what the run reports at the end. Without it, a run that quietly
// split every band back down to single tiles would look identical to one that
// worked.
type bandStats struct {
	mu               sync.Mutex
	bands            int
	fetches          int
	splits           int
	perTileFallbacks int
	emptyFallbacks   int
	skippedBands     int
}

func (s *bandStats) add(bands, fetches, splits, perTile, empty int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bands += bands
	s.fetches += fetches
	s.splits += splits
	s.perTileFallbacks += perTile
	s.emptyFallbacks += empty
}

// skipped counts a band whose tiles were all already rendered, so no query was
// issued for it. Reported separately: a resumed run showing few fetches and
// many skips is working, and should not look like one that failed to fetch.
func (s *bandStats) skipped() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skippedBands++
}

// bandTileGenerator is the slice of pipeline.Generator the scheduler needs.
// Narrow on purpose, so the tests can fake it without a Mapnik build.
type bandTileGenerator interface {
	BandFetchBounds(coords []tile.Coords) (types.BoundingBox, error)
	SliceForTile(band *types.TileData, coords tile.Coords) *types.TileData
	TileExists(coords tile.Coords, filenameSuffix string) bool
}

// runBandedTilePool renders coords by fetching Overpass data one band at a
// time instead of one tile at a time.
//
// `out geom` returns unclipped geometry, so a motorway crossing a 4x4 block is
// transferred sixteen times by per-tile fetching. One query for the block
// transfers it once, which matters because the fetch is ~71% of per-tile wall
// clock.
//
// The structure is a producer feeding the existing worker pool through
// RunStream: fetch a band, slice it into per-tile data, emit those tasks, drop
// the band. Only a bounded number of bands is ever live, and the slices share
// the band's geometry rather than copying it.
//
// Band-ordered scheduling is not optional. TilesInBBox emits z, then x, then y,
// so a 4x4 band's tiles are separated by several column-heights of task list;
// caching bands behind the ordinary slice-based Run would either hold every
// band alive or refetch each one repeatedly.
func runBandedTilePool(
	ctx context.Context,
	gen worker.Generator,
	bandGen bandTileGenerator,
	ds areaDataSource,
	coords []tile.Coords,
	opts *batchOptions,
	suffix string,
) ([]worker.Result, string) {
	band := opts.band

	// Tiles below the threshold are fetched per tile as before. At low zoom a
	// single tile already covers a huge area, so banding buys little and risks
	// a great deal.
	var banded, perTile []tile.Coords
	for _, c := range coords {
		if int(c.Z) >= band.minZoom {
			banded = append(banded, c)
		} else {
			perTile = append(perTile, c)
		}
	}

	bands := tile.GroupIntoBands(banded, band.level)
	stats := &bandStats{}
	stats.add(len(bands), 0, 0, len(perTile), 0)

	progress := worker.NewProgress(len(coords), opts.showProgress)
	pool := worker.New(worker.Config{
		Workers:    opts.workers,
		Generator:  gen,
		OnProgress: progress.Callback(),
	})

	// Capacity is the fetch-ahead budget: producers block once this many
	// tiles' data is queued, which is what bounds how much band geometry is
	// live at once.
	taskCh := make(chan worker.Task, max(1, band.fetchAhead)*tilesPerBand(band.level))

	go func() {
		defer close(taskCh)

		for _, c := range perTile {
			if !emitTask(ctx, taskCh, worker.Task{Coords: c, Force: opts.force, Suffix: suffix}) {
				return
			}
		}

		queue := append([]tile.Band(nil), bands...)
		for len(queue) > 0 {
			b := queue[0]
			queue = queue[1:]

			next, ok := fetchAndEmitBand(ctx, taskCh, bandGen, ds, b, opts, suffix, stats)
			if !ok {
				return
			}
			// Sub-bands go to the front, so a band's own tiles finish before
			// the next band starts and the data stays hot.
			queue = append(next, queue...)
		}
	}()

	results := reconcileBandResults(pool.RunStream(ctx, taskCh, len(coords)), coords, suffix, ctx.Err())
	progress.Done()

	stats.mu.Lock()
	logger.Info("Band fetching summary",
		"bands", stats.bands,
		"band_fetches", stats.fetches,
		"splits", stats.splits,
		"per_tile_fallbacks", stats.perTileFallbacks,
		"empty_slice_fallbacks", stats.emptyFallbacks,
		"bands_already_complete", stats.skippedBands,
		"tiles", len(coords),
	)
	stats.mu.Unlock()

	return results, progress.Summary()
}

// reconcileBandResults guarantees one result per requested tile, inventing a
// failure for any tile the producer never got as far as emitting.
//
// worker.Pool.Run gives callers len(results) == len(tasks) unconditionally, and
// runBatchGenerate counts failures from nothing else. The banded producer can
// stop early — a cancelled context is the ordinary way — which without this
// would leave those tiles with no result at all, so `failureError` would see
// zero failures, TileJSON would be written and the MBTiles metadata flushed,
// and an interrupted run would exit 0 having rendered part of a tileset. That
// is the same failure this project refuses elsewhere: a wrong success is far
// worse than a wrong failure, because nothing downstream ever revisits it.
//
// cause is the context's error, which is nil if the producer stopped for some
// other reason; a missing result is a failure either way, so an unexplained one
// still gets an error rather than passing silently.
func reconcileBandResults(results []worker.Result, coords []tile.Coords, suffix string, cause error) []worker.Result {
	if len(results) == len(coords) {
		return results
	}

	answered := make(map[tile.Coords]struct{}, len(results))
	for _, r := range results {
		answered[r.Task.Coords] = struct{}{}
	}

	if cause == nil {
		cause = errors.New("tile was never scheduled: band production stopped early")
	}

	for _, c := range coords {
		if _, ok := answered[c]; ok {
			continue
		}
		results = append(results, worker.Result{
			Task: worker.Task{Coords: c, Suffix: suffix},
			Err:  cause,
		})
	}
	return results
}

// fetchAndEmitBand fetches one band and emits its tiles' tasks. It returns the
// sub-bands to retry when the band could not be fetched as one query, and
// false when the context is done.
func fetchAndEmitBand(
	ctx context.Context,
	taskCh chan<- worker.Task,
	bandGen bandTileGenerator,
	ds areaDataSource,
	b tile.Band,
	opts *batchOptions,
	suffix string,
	stats *bandStats,
) ([]tile.Band, bool) {
	// A level-0 band is a single tile: emit it the ordinary way, with no data
	// attached, so it fetches and fails exactly as it always did. This is the
	// bottom of the split recursion and the reason a band failure is never
	// reported against sixteen tiles.
	if b.Level == 0 || len(b.Tiles) == 1 {
		return nil, emitPlainTasks(ctx, taskCh, b.Tiles, opts, suffix)
	}

	// Only tiles that still need rendering justify a fetch. The skip-existing
	// check otherwise happens inside the generator, i.e. *after* the band
	// query has already been paid for -- so a resumed run would re-fetch every
	// band it had already finished, and with the fetch at ~71% of wall clock
	// that is the whole cost of the run. Worse, against a flaky Overpass it
	// would also burn the retry and split paths for tiles nobody needs.
	//
	// Existing tiles are still emitted as ordinary tasks: the generator's own
	// check short-circuits them before any fetch, they log the skip, and --
	// the part that matters -- they still produce a result, so the caller's
	// one-result-per-tile accounting is unaffected.
	missing := tilesNeedingRender(bandGen, b.Tiles, opts.force, suffix)

	if len(missing) == 0 {
		stats.skipped()
		return nil, emitPlainTasks(ctx, taskCh, b.Tiles, opts, suffix)
	}

	// Bounds cover only the tiles that need data, so a band that is mostly
	// complete asks for correspondingly less.
	bounds, err := bandGen.BandFetchBounds(missing)
	if err != nil {
		return splitOrDrop(b, stats), true
	}

	stats.add(0, 1, 0, 0, 0)
	data, err := ds.FetchAreaData(ctx, int(b.Tiles[0].Z), bounds, datasource.AreaFetchOptions{
		TimeoutSec: opts.band.timeoutSec,
		// The zero-feature check is a per-tile policy. A non-empty band would
		// mask a genuinely empty member, and an empty band would fail every
		// member at once; each tile's slice is re-checked below instead.
		SkipEmptyValidation: true,
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, false
		}
		logger.Warn("Band fetch failed; splitting",
			"band", b.Key.String(), "tiles", len(b.Tiles), "err", err)
		return splitOrDrop(b, stats), true
	}

	needsData := make(map[tile.Coords]struct{}, len(missing))
	for _, c := range missing {
		needsData[c] = struct{}{}
	}

	for _, c := range b.Tiles {
		// An already-rendered tile is emitted plain: the data was never fetched
		// on its behalf and the generator will skip it before looking at any.
		if _, ok := needsData[c]; !ok {
			if !emitTask(ctx, taskCh, worker.Task{Coords: c, Force: opts.force, Suffix: suffix}) {
				return nil, false
			}
			continue
		}

		slice := bandGen.SliceForTile(data, c)

		// An empty slice at a zoom where emptiness is an error means either a
		// genuinely empty tile or a silent upstream failure, and only a real
		// per-tile fetch can tell those apart on the terms the rest of the
		// pipeline already agreed. Rare enough to cost nothing.
		if slice == nil || (emptinessIsCheckedAt(int(c.Z)) && slice.Features.Count() == 0) {
			stats.add(0, 0, 0, 0, 1)
			slice = nil
		}

		if !emitTask(ctx, taskCh, worker.Task{Coords: c, Force: opts.force, Suffix: suffix, Data: slice}) {
			return nil, false
		}
	}
	return nil, true
}

// emitPlainTasks emits tiles with no prefetched data attached, i.e. on exactly
// the path they took before band fetching existed. It reports false when the
// producer should stop.
func emitPlainTasks(ctx context.Context, taskCh chan<- worker.Task, coords []tile.Coords, opts *batchOptions, suffix string) bool {
	for _, c := range coords {
		if !emitTask(ctx, taskCh, worker.Task{Coords: c, Force: opts.force, Suffix: suffix}) {
			return false
		}
	}
	return true
}

// tilesNeedingRender returns the tiles a band actually has to fetch data for.
//
// With --force that is all of them: force means render these again, not
// reinterpret what is already there.
func tilesNeedingRender(bandGen bandTileGenerator, coords []tile.Coords, force bool, suffix string) []tile.Coords {
	if force {
		return coords
	}

	missing := make([]tile.Coords, 0, len(coords))
	for _, c := range coords {
		if !bandGen.TileExists(c, suffix) {
			missing = append(missing, c)
		}
	}
	return missing
}

// splitOrDrop returns the sub-bands of b, counting the split.
func splitOrDrop(b tile.Band, stats *bandStats) []tile.Band {
	sub := b.Split()
	stats.add(len(sub), 0, 1, 0, 0)
	return sub
}

// emitTask sends a task unless the context is done. It reports false when the
// producer should stop.
func emitTask(ctx context.Context, taskCh chan<- worker.Task, task worker.Task) bool {
	select {
	case taskCh <- task:
		return true
	case <-ctx.Done():
		return false
	}
}

// emptinessIsCheckedAt mirrors validateFeatureResponse's zoom window: below it
// tiles are legitimately huge and often empty, above it an empty tile is
// ordinary. Only in between does emptiness signal a failed fetch.
func emptinessIsCheckedAt(zoom int) bool {
	return zoom >= 8 && zoom <= 13
}

// tilesPerBand is 4^level, the number of tiles a full band holds.
func tilesPerBand(level uint32) int {
	return 1 << (2 * level)
}

// bandGeneratorFor returns the band-aware half of a pipeline generator.
func bandGeneratorFor(gen worker.Generator) (bandTileGenerator, bool) {
	g, ok := gen.(*pipeline.Generator)
	if !ok {
		return nil, false
	}
	return g, true
}

// bandFetchUnavailable explains why a run cannot use band fetching, or returns
// nil when it can. Erroring rather than silently falling back: a user who asked
// for band fetching and got per-tile fetching would see only an unexplained
// lack of speedup.
// gen may be nil, to check only the datasource before one exists.
func bandFetchUnavailable(ds pipeline.DataSource, gen worker.Generator) error {
	if _, ok := ds.(areaDataSource); !ok {
		return errors.New("--band-fetch needs an Overpass data source that can fetch by area; " +
			"the configured data source cannot answer for a bounding box")
	}
	if gen != nil {
		if _, ok := bandGeneratorFor(gen); !ok {
			return errors.New("--band-fetch needs the standard tile generator")
		}
	}
	return nil
}

// bandOptionsFromConfig reads and validates the band knobs.
func bandOptionsFromConfig() (bandOptions, error) {
	level := viper.GetInt("generate.band_level")
	if level < 0 || level > 4 {
		return bandOptions{}, fmt.Errorf(
			"invalid --band-level %d: must be 0-4 (a level-4 band is 256 tiles, which no Overpass will answer as one query)",
			level)
	}

	minZoom := viper.GetInt("generate.band_min_zoom")
	if minZoom < 0 || minZoom > int(tile.MaxZoom) {
		return bandOptions{}, fmt.Errorf("invalid --band-min-zoom %d: must be 0-%d", minZoom, tile.MaxZoom)
	}

	fetchAhead := viper.GetInt("generate.band_fetch_ahead")
	if fetchAhead < 1 {
		fetchAhead = 1
	}

	timeoutSec := viper.GetInt("generate.band_timeout")
	if timeoutSec <= 0 {
		timeoutSec = datasource.DefaultQueryTimeoutSec
	}

	return bandOptions{
		level:      uint32(level), //nolint:gosec // range-checked above
		minZoom:    minZoom,
		fetchAhead: fetchAhead,
		timeoutSec: timeoutSec,
	}, nil
}

// checkBandFetchUsable reports why a banded run cannot proceed, before the run
// starts. Extracted from runBatchGenerate to keep that function under the
// cyclomatic complexity budget.
func checkBandFetchUsable(opts *batchOptions, ds pipeline.DataSource) error {
	if !opts.bandFetch {
		return nil
	}
	return bandFetchUnavailable(ds, nil)
}
