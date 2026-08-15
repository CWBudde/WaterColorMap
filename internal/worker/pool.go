// Package worker provides a parallel tile generation worker pool.
package worker

import (
	"context"
	"sync"
	"time"

	"github.com/cwbudde/watercolormap/internal/tile"
	"github.com/cwbudde/watercolormap/internal/types"
)

// Generator is the interface for tile generation.
// This matches the signature of pipeline.Generator.Generate.
type Generator interface {
	Generate(ctx context.Context, coords tile.Coords, force bool, suffix string) (path string, layersDir string, err error)
}

// DataGenerator is the optional half of Generator: one that can render from
// data the caller already has.
//
// Band fetching needs it — one Overpass query feeds a whole block of tiles —
// but a generator that does not implement it, or a task with no data attached,
// takes exactly the path it always did.
type DataGenerator interface {
	GenerateWithPrefetched(ctx context.Context, coords tile.Coords, force bool, suffix string, data *types.TileData) (path string, layersDir string, err error)
}

// Task represents a single tile generation task.
type Task struct {
	// Data, when non-nil, is the tile's OSM features, already fetched. The
	// worker then renders without touching the datasource. Nil means "fetch
	// it yourself", which is what every task was before band fetching.
	Data   *types.TileData
	Suffix string
	// Index is the task's position in the producer's enumeration. The pool does
	// not read it; it is carried through to Result so a streaming producer can
	// tell which tile a result belongs to without keeping a map of every tile it
	// emitted. Zero for producers that do not number their tasks.
	Index  int
	Coords tile.Coords
	Force  bool
}

// Result represents the outcome of a tile generation task.
type Result struct {
	Err     error
	Path    string
	Task    Task
	Elapsed time.Duration
}

// ProgressFunc is called after each task completes.
type ProgressFunc func(completed, total, failed int)

// ResultFunc consumes one result as it arrives.
type ResultFunc func(Result)

// Config configures the worker pool.
type Config struct {
	Generator  Generator
	OnProgress ProgressFunc
	// OnResult, when set, receives every result of a RunStream as it arrives,
	// and RunStream then retains none of them: it returns nil. A country-sized
	// run produces hundreds of thousands of results, and a caller that only
	// counts them has no reason to hold them all.
	//
	// It is called from the pool's single collector goroutine, in completion
	// order, before OnProgress for the same result — so an implementation needs
	// no locking of its own, but must not block for long.
	//
	// Run ignores it: Run's callers rely on len(results) == len(tasks).
	OnResult ResultFunc
	Workers  int
}

// Pool manages parallel tile generation.
type Pool struct {
	generator  Generator
	onProgress ProgressFunc
	onResult   ResultFunc
	workers    int
}

// New creates a new worker pool.
func New(cfg Config) *Pool {
	workers := cfg.Workers
	if workers <= 0 {
		workers = 1
	}

	return &Pool{
		workers:    workers,
		generator:  cfg.Generator,
		onProgress: cfg.OnProgress,
		onResult:   cfg.OnResult,
	}
}

// Run executes all tasks and returns results.
// Tasks are processed in parallel by the configured number of workers.
// The function blocks until all tasks complete or the context is cancelled.
//
// Config.OnResult is deliberately ignored here: Run's contract below is that
// every task yields a result in the returned slice, and callers count failures
// against nothing else.
func (p *Pool) Run(ctx context.Context, tasks []Task) []Result {
	if len(tasks) == 0 {
		return nil
	}

	// taskCh is buffered to len(tasks), so no send blocks and no task is
	// dropped: every task reaches a worker, which emits either a real result
	// or one carrying ctx.Err() (Canceled or DeadlineExceeded).
	// len(results) == len(tasks) always, which is the invariant callers count
	// failures against.
	taskCh := make(chan Task, len(tasks))
	for _, task := range tasks {
		taskCh <- task
	}
	close(taskCh)

	return p.runStream(ctx, taskCh, len(tasks), nil)
}

// RunStream is Run over a channel the caller feeds, for producers that do not
// have the whole task list up front.
//
// Band fetching is the reason: its tasks only exist once a band has been
// fetched and sliced, and materialising them all first would hold every band's
// geometry alive at once. total is passed explicitly because a streamed run
// cannot count its tasks in advance, and progress reporting needs a
// denominator.
//
// The caller must close tasks. One result is emitted per task received, so the
// len(results) == len(tasks) invariant holds exactly as it does for Run —
// unless Config.OnResult is set, in which case each result goes to that
// callback instead and the returned slice is nil. The invariant is then the
// callback's: it is called exactly once per task received.
func (p *Pool) RunStream(ctx context.Context, tasks <-chan Task, total int) []Result {
	return p.runStream(ctx, tasks, total, p.onResult)
}

// runStream is the body of both Run and RunStream. onResult nil means "collect
// the results and return them"; Run always passes nil, which is what keeps its
// contract independent of Config.OnResult.
func (p *Pool) runStream(ctx context.Context, tasks <-chan Task, total int, onResult ResultFunc) []Result {
	if tasks == nil {
		return nil
	}

	taskCh := tasks
	resultCh := make(chan Result, p.workers)

	// Track progress
	var (
		completed int
		failed    int
		mu        sync.Mutex
	)

	// Start workers
	var wg sync.WaitGroup
	for i := 0; i < p.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.worker(ctx, taskCh, resultCh)
		}()
	}

	// Collect results in a separate goroutine. With a callback there is nothing
	// to collect, so nothing is allocated either: `total` is the whole run, and
	// pre-allocating it is exactly the cost this exists to avoid.
	var results []Result
	if onResult == nil {
		results = make([]Result, 0, total)
	}
	done := make(chan struct{})

	go func() {
		for result := range resultCh {
			if onResult != nil {
				onResult(result)
			} else {
				results = append(results, result)
			}

			// Update progress
			mu.Lock()
			completed++
			if result.Err != nil {
				failed++
			}
			c, f := completed, failed
			mu.Unlock()

			if p.onProgress != nil {
				p.onProgress(c, total, f)
			}
		}
		close(done)
	}()

	// Wait for workers to finish
	wg.Wait()
	close(resultCh)

	// Wait for result collection to finish
	<-done

	return results
}

// generate renders one task, using its prefetched data when there is any and
// the generator can take it. Every other combination falls through to the
// original path, so nothing changes for a task that carries no data.
func (p *Pool) generate(ctx context.Context, task Task) (string, string, error) {
	if task.Data != nil {
		if dg, ok := p.generator.(DataGenerator); ok {
			return dg.GenerateWithPrefetched(ctx, task.Coords, task.Force, task.Suffix, task.Data)
		}
	}
	return p.generator.Generate(ctx, task.Coords, task.Force, task.Suffix)
}

// worker processes tasks from the task channel and sends results to the result channel.
func (p *Pool) worker(ctx context.Context, tasks <-chan Task, results chan<- Result) {
	for task := range tasks {
		select {
		case <-ctx.Done():
			// Send cancellation result
			results <- Result{
				Task: task,
				Err:  ctx.Err(),
			}
			continue
		default:
		}

		start := time.Now()
		path, _, err := p.generate(ctx, task)
		elapsed := time.Since(start)

		results <- Result{
			Task:    task,
			Path:    path,
			Err:     err,
			Elapsed: elapsed,
		}
	}
}
