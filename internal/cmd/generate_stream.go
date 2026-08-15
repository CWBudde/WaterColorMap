package cmd

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/spf13/viper"

	"github.com/cwbudde/watercolormap/internal/checkpoint"
	"github.com/cwbudde/watercolormap/internal/tile"
	"github.com/cwbudde/watercolormap/internal/worker"
)

// maxRetainedFailures is how many failed results a run keeps for logging.
//
// Every failure is counted, only the first few are kept: a run that fails
// wholesale (an Overpass outage, a bad style) would otherwise hold a Result per
// tile, which is the allocation this streaming path exists to remove. The
// tail is reported as a count, and the tiles themselves are still missing from
// the output, which is what a rerun looks at anyway.
const maxRetainedFailures = 50

// tileRunResult is what a batch run reports instead of a result per tile.
type tileRunResult struct {
	// cause explains unscheduled tiles: the context error of an interrupted
	// run, or a generic error when the producer stopped for another reason.
	cause error
	// summary is the progress tracker's one-line summary.
	summary string
	// failures holds the first maxRetainedFailures failed results, for logging.
	failures  []worker.Result
	completed int
	failed    int
	// unscheduled counts tiles the producer never emitted, i.e. tiles that were
	// never even attempted. They count as failures — see resultAggregator.
	unscheduled int
}

// failedCount is what --allow-failures and the run's exit status are judged on.
func (r *tileRunResult) failedCount() int {
	return r.failed + r.unscheduled
}

// logFailures logs the retained failures and returns the total failure count.
func (r *tileRunResult) logFailures(msg string) int {
	for _, res := range r.failures {
		logger.Error(msg, "coords", res.Task.Coords.String(), "suffix", res.Task.Suffix, "error", res.Err)
	}
	if r.failed > len(r.failures) {
		logger.Error(msg+" (further failures not listed individually)",
			"failed", r.failed, "listed", len(r.failures))
	}
	if r.unscheduled > 0 {
		logger.Error("Tiles were never scheduled", "count", r.unscheduled, "cause", r.cause)
	}
	return r.failedCount()
}

// resultAggregator turns a stream of results into counts. The worker pool calls
// add from a single goroutine, so it needs no locking.
type resultAggregator struct {
	run tileRunResult
}

func (a *resultAggregator) add(res worker.Result) {
	a.run.completed++
	if res.Err == nil {
		return
	}
	a.run.failed++
	if len(a.run.failures) < maxRetainedFailures {
		a.run.failures = append(a.run.failures, res)
	}
}

// aggregateResults collapses a materialised result slice into the same shape,
// for the banded path, which still works tile-list-at-a-time.
func aggregateResults(results []worker.Result, summary string) tileRunResult {
	var agg resultAggregator
	for _, res := range results {
		agg.add(res)
	}
	agg.run.summary = summary
	return agg.run
}

// runStreamingTilePool renders every tile of bbox without ever materialising
// the tile list or the result list.
//
// A producer goroutine walks tile.TilesInBBoxSeq and feeds a small channel;
// results are counted as they arrive and dropped. Peak memory is the worker
// count, not the tile count, which is what makes a country-sized run's
// bookkeeping constant rather than 317,618 Tasks plus 317,618 Results.
func runStreamingTilePool(ctx context.Context, gen worker.Generator, bbox [4]float64, opts *batchOptions, suffix string) tileRunResult {
	total := tile.TileCount(bbox, opts.zoomMin, opts.zoomMax)

	// A resumed run skips a prefix of the enumeration, so its denominator is
	// what is left, not what the whole range would have been.
	start := 0
	if opts.checkpoint != nil {
		start = opts.checkpoint.Watermark()
	}
	remaining := total - start
	if remaining <= 0 {
		logger.Info("Checkpoint covers the whole range; nothing to render", "tiles", total)
		return tileRunResult{summary: fmt.Sprintf("0/0 tiles (all %d already checkpointed)", total)}
	}
	if start > 0 {
		logger.Info("Resuming from checkpoint", "skipped", start, "remaining", remaining, "tiles", total)
	}

	progress := worker.NewProgress(remaining, opts.showProgress)
	var agg resultAggregator

	pool := worker.New(worker.Config{
		Workers:    opts.workers,
		Generator:  gen,
		OnProgress: progress.Callback(),
		OnResult: func(res worker.Result) {
			agg.add(res)
			if opts.checkpoint != nil {
				if err := opts.checkpoint.Complete(res.Task.Index, res.Err == nil); err != nil {
					logger.Warn("Failed to write checkpoint", "error", err)
				}
			}
		},
	})

	// Two per worker: enough that no worker waits on the producer, small enough
	// that the queue is bookkeeping rather than the run's memory profile.
	taskCh := make(chan worker.Task, opts.workers*2)

	emitted := 0
	go func() {
		defer close(taskCh)

		index := 0
		for c := range tile.TilesInBBoxSeq(bbox, opts.zoomMin, opts.zoomMax) {
			// Fast-forward past the checkpointed prefix: pure arithmetic, no
			// filesystem I/O. Skip-existing still guards everything emitted.
			if index < start {
				index++
				continue
			}
			if !emitTask(ctx, taskCh, worker.Task{Coords: c, Force: opts.force, Suffix: suffix, Index: index}) {
				return
			}
			index++
			emitted++
		}
	}()

	pool.RunStream(ctx, taskCh, remaining)
	progress.Done()

	// The producer has finished — RunStream only returns once taskCh is closed
	// and drained — so `emitted` is stable and needs no synchronisation.
	run := agg.run
	run.summary = progress.Summary()
	run.unscheduled = remaining - emitted

	// Same guarantee reconcileBandResults gives the banded path: a run that
	// stopped early must never report success for tiles it never attempted.
	if run.unscheduled > 0 {
		run.cause = ctx.Err()
		if run.cause == nil {
			run.cause = errors.New("tile was never scheduled: enumeration stopped early")
		}
	}

	finishCheckpoint(opts.checkpoint, &run)
	return run
}

// finishCheckpoint saves or deletes the checkpoint once the run is over.
//
// Deleting is only correct when the whole range came out clean: with anything
// failed or unattempted, the file is what lets the next run pick up where this
// one stopped.
func finishCheckpoint(cp *checkpoint.Tracker, run *tileRunResult) {
	if cp == nil {
		return
	}

	if run.failedCount() == 0 {
		if err := cp.Remove(); err != nil {
			logger.Warn("Failed to remove checkpoint", "error", err)
		}
		return
	}

	if err := cp.Save(); err != nil {
		logger.Warn("Failed to write final checkpoint", "error", err)
		return
	}
	logger.Info("Checkpoint written", "path", cp.Path(), "watermark", cp.Watermark())
}

// checkpointPath resolves the configured checkpoint location, or "" when
// checkpointing is off. `--checkpoint` with no value (or `generate.checkpoint:
// auto` in config) means the default file inside the output directory.
func checkpointPath(opts *batchOptions) string {
	configured := viper.GetString("generate.checkpoint")
	switch configured {
	case "":
		return ""
	case checkpointAuto:
		return filepath.Join(opts.outputDir, checkpoint.FileName)
	default:
		return configured
	}
}

// setupCheckpoint builds the run's checkpoint tracker, loading any existing
// file that describes this exact run.
//
// A checkpoint from a different run is ignored loudly rather than reinterpreted:
// resuming one bbox's watermark into another bbox's enumeration would skip
// tiles nobody ever rendered, and skip-existing would not catch it because the
// tiles were never emitted to be checked. --force ignores it too, since force
// means render these again.
func setupCheckpoint(opts *batchOptions, bbox [4]float64) (*checkpoint.Tracker, error) {
	path := checkpointPath(opts)
	if path == "" {
		return nil, nil
	}

	// Band fetching reorders the enumeration wholesale, so an index-based
	// watermark would either be wrong or would need a frontier the size of the
	// run. Refusing beats resuming the wrong tiles.
	if opts.bandFetch {
		return nil, fmt.Errorf("--checkpoint cannot be combined with --band-fetch: " +
			"banded runs render tiles out of enumeration order, so there is no meaningful watermark")
	}

	key := checkpoint.RunKey{
		BBox:        bbox,
		ZoomMin:     opts.zoomMin,
		ZoomMax:     opts.zoomMax,
		Format:      opts.format,
		ImageFormat: opts.imageFormat.String(),
		// Batch runs render the base tiles only — `--hidpi` is rejected for a
		// bbox — so the suffix is always empty today. It is in the key anyway
		// because a suffixed pass would produce different files for the same
		// enumeration, and resuming across the two would be exactly the silent
		// skip the key exists to prevent.
		Suffix: "",
	}

	state, err := checkpoint.Load(path)
	if err != nil {
		// An unreadable checkpoint is not a reason to refuse to render; it is a
		// reason to start from the beginning.
		logger.Warn("Ignoring unreadable checkpoint; starting from the beginning", "path", path, "error", err)
		state = nil
	}

	switch {
	case state == nil:
	case opts.force:
		logger.Warn("Ignoring checkpoint because --force was given", "path", path, "watermark", state.Watermark)
		state = nil
	default:
		total := tile.TileCount(bbox, opts.zoomMin, opts.zoomMax)
		if ok, why := state.Resumable(key, total); !ok {
			logger.Warn("IGNORING CHECKPOINT: it does not describe this run; starting from the beginning",
				"path", path, "reason", why)
			state = nil
		}
	}

	return checkpoint.NewTracker(path, key, state, viper.GetInt("generate.checkpoint_interval")), nil
}
