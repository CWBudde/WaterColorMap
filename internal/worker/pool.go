// Package worker provides a parallel tile generation worker pool.
package worker

import (
	"context"
	"sync"
	"time"

	"github.com/cwbudde/watercolormap/internal/tile"
)

// Generator is the interface for tile generation.
// This matches the signature of pipeline.Generator.Generate.
type Generator interface {
	Generate(ctx context.Context, coords tile.Coords, force bool, suffix string) (path string, layersDir string, err error)
}

// Task represents a single tile generation task.
type Task struct {
	Suffix string
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

// Config configures the worker pool.
type Config struct {
	Generator  Generator
	OnProgress ProgressFunc
	Workers    int
}

// Pool manages parallel tile generation.
type Pool struct {
	generator  Generator
	onProgress ProgressFunc
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
	}
}

// Run executes all tasks and returns results.
// Tasks are processed in parallel by the configured number of workers.
// The function blocks until all tasks complete or the context is cancelled.
func (p *Pool) Run(ctx context.Context, tasks []Task) []Result {
	if len(tasks) == 0 {
		return nil
	}

	// Create channels
	taskCh := make(chan Task, len(tasks))
	resultCh := make(chan Result, len(tasks))

	// Feed tasks. taskCh is buffered to len(tasks), so no send blocks and no
	// task is dropped: every task reaches a worker, which emits either a real
	// result or one carrying ctx.Err() (Canceled or DeadlineExceeded).
	// len(results) == len(tasks) always, which is the invariant callers count
	// failures against.
	for _, task := range tasks {
		taskCh <- task
	}
	close(taskCh)

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

	// Collect results in a separate goroutine
	results := make([]Result, 0, len(tasks))
	done := make(chan struct{})

	go func() {
		for result := range resultCh {
			results = append(results, result)

			// Update progress
			mu.Lock()
			completed++
			if result.Err != nil {
				failed++
			}
			c, f := completed, failed
			mu.Unlock()

			if p.onProgress != nil {
				p.onProgress(c, len(tasks), f)
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
		path, _, err := p.generator.Generate(ctx, task.Coords, task.Force, task.Suffix)
		elapsed := time.Since(start)

		results <- Result{
			Task:    task,
			Path:    path,
			Err:     err,
			Elapsed: elapsed,
		}
	}
}
