package worker

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cwbudde/watercolormap/internal/tile"
	"github.com/cwbudde/watercolormap/internal/types"
)

// mockGenerator simulates tile generation for testing
type mockGenerator struct {
	failTiles map[string]bool
	delay     time.Duration
	callCount atomic.Int32
}

func (m *mockGenerator) Generate(ctx context.Context, coords tile.Coords, force bool, suffix string) (string, string, error) {
	m.callCount.Add(1)

	select {
	case <-ctx.Done():
		return "", "", ctx.Err()
	case <-time.After(m.delay):
	}

	if m.failTiles != nil && m.failTiles[coords.String()] {
		return "", "", errors.New("simulated failure")
	}

	return "/tmp/" + coords.String() + suffix + ".png", "", nil
}

func TestPool_BasicExecution(t *testing.T) {
	gen := &mockGenerator{delay: 10 * time.Millisecond}

	pool := New(Config{
		Workers:   2,
		Generator: gen,
	})

	tasks := []Task{
		{Coords: tile.NewCoords(13, 4297, 2754)},
		{Coords: tile.NewCoords(13, 4297, 2755)},
		{Coords: tile.NewCoords(13, 4298, 2754)},
	}

	results := pool.Run(context.Background(), tasks)

	if len(results) != len(tasks) {
		t.Errorf("Expected %d results, got %d", len(tasks), len(results))
	}

	for _, r := range results {
		if r.Err != nil {
			t.Errorf("Unexpected error for %s: %v", r.Task.Coords.String(), r.Err)
		}
		if r.Path == "" {
			t.Errorf("Expected path for %s, got empty", r.Task.Coords.String())
		}
	}

	if gen.callCount.Load() != int32(len(tasks)) {
		t.Errorf("Expected %d generator calls, got %d", len(tasks), gen.callCount.Load())
	}
}

func TestPool_Parallelism(t *testing.T) {
	// Use a longer delay to ensure parallelism is tested
	gen := &mockGenerator{delay: 50 * time.Millisecond}

	pool := New(Config{
		Workers:   4,
		Generator: gen,
	})

	tasks := make([]Task, 8)
	for i := range tasks {
		tasks[i] = Task{Coords: tile.NewCoords(13, 4297+uint32(i), 2754)}
	}

	start := time.Now()
	results := pool.Run(context.Background(), tasks)
	elapsed := time.Since(start)

	// With 4 workers and 8 tasks at 50ms each, should take ~100ms (2 batches)
	// Allow some margin for overhead
	maxExpected := 200 * time.Millisecond
	if elapsed > maxExpected {
		t.Errorf("Expected parallel execution in ~100ms, took %v", elapsed)
	}

	if len(results) != len(tasks) {
		t.Errorf("Expected %d results, got %d", len(tasks), len(results))
	}

	t.Logf("Processed %d tasks with %d workers in %v", len(tasks), 4, elapsed)
}

func TestPool_ErrorHandling(t *testing.T) {
	failTile := "z13_x4297_y2755"
	gen := &mockGenerator{
		delay:     10 * time.Millisecond,
		failTiles: map[string]bool{failTile: true},
	}

	pool := New(Config{
		Workers:   2,
		Generator: gen,
	})

	tasks := []Task{
		{Coords: tile.NewCoords(13, 4297, 2754)},
		{Coords: tile.NewCoords(13, 4297, 2755)}, // This one should fail
		{Coords: tile.NewCoords(13, 4298, 2754)},
	}

	results := pool.Run(context.Background(), tasks)

	// Should still get all results
	if len(results) != len(tasks) {
		t.Errorf("Expected %d results, got %d", len(tasks), len(results))
	}

	// Count successes and failures
	var successCount, failCount int
	for _, r := range results {
		if r.Err != nil {
			failCount++
			if r.Task.Coords.String() != failTile {
				t.Errorf("Unexpected failure for %s", r.Task.Coords.String())
			}
		} else {
			successCount++
		}
	}

	if successCount != 2 {
		t.Errorf("Expected 2 successes, got %d", successCount)
	}
	if failCount != 1 {
		t.Errorf("Expected 1 failure, got %d", failCount)
	}
}

// makeTasks builds n distinct tasks.
func makeTasks(n int) []Task {
	tasks := make([]Task, n)
	for i := range tasks {
		tasks[i] = Task{Coords: tile.NewCoords(13, 4297+uint32(i), 2754)}
	}

	return tasks
}

// assertOneResultPerTask verifies the core Run contract: exactly one Result per
// input task, no drops and no duplicates.
func assertOneResultPerTask(t *testing.T, tasks []Task, results []Result) {
	t.Helper()

	if len(results) != len(tasks) {
		t.Fatalf("Expected %d results, got %d", len(tasks), len(results))
	}

	seen := make(map[string]int, len(tasks))
	for _, r := range results {
		seen[r.Task.Coords.String()]++
	}

	for _, task := range tasks {
		key := task.Coords.String()
		if seen[key] != 1 {
			t.Errorf("Expected exactly 1 result for %s, got %d", key, seen[key])
		}
		delete(seen, key)
	}

	for key, count := range seen {
		t.Errorf("Unexpected result for unknown task %s (%d times)", key, count)
	}
}

func TestPool_Cancellation(t *testing.T) {
	gen := &mockGenerator{delay: 100 * time.Millisecond}

	pool := New(Config{
		Workers:   2,
		Generator: gen,
	})

	tasks := makeTasks(10)

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after a short time
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	results := pool.Run(ctx, tasks)
	elapsed := time.Since(start)

	// Should return early due to cancellation
	if elapsed > 200*time.Millisecond {
		t.Errorf("Expected early cancellation, took %v", elapsed)
	}

	// Cancellation must not change the number of results: every task yields
	// exactly one Result, successful or cancelled.
	assertOneResultPerTask(t, tasks, results)

	var cancelledCount int
	for _, r := range results {
		if r.Err != nil && errors.Is(r.Err, context.Canceled) {
			cancelledCount++
		}
	}

	if cancelledCount == 0 {
		t.Errorf("Expected at least one cancelled result, got none")
	}

	t.Logf("Completed with %d results (%d cancelled) in %v", len(results), cancelledCount, elapsed)
}

func TestPool_CancelledBeforeRun(t *testing.T) {
	tests := []struct {
		name      string
		workers   int
		taskCount int
	}{
		{name: "single worker", workers: 1, taskCount: 5},
		{name: "more workers than tasks", workers: 8, taskCount: 3},
		{name: "many tasks", workers: 4, taskCount: 64},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gen := &mockGenerator{delay: 10 * time.Millisecond}

			pool := New(Config{
				Workers:   tt.workers,
				Generator: gen,
			})

			tasks := makeTasks(tt.taskCount)

			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			results := pool.Run(ctx, tasks)

			assertOneResultPerTask(t, tasks, results)

			for _, r := range results {
				if !errors.Is(r.Err, context.Canceled) {
					t.Errorf("Expected context.Canceled for %s, got %v", r.Task.Coords.String(), r.Err)
				}
			}
		})
	}
}

func TestPool_ProgressCallback(t *testing.T) {
	gen := &mockGenerator{delay: 10 * time.Millisecond}

	var progressCalls atomic.Int32
	var lastCompleted, lastTotal int

	pool := New(Config{
		Workers:   2,
		Generator: gen,
		OnProgress: func(completed, total, failed int) {
			progressCalls.Add(1)
			lastCompleted = completed
			lastTotal = total
		},
	})

	tasks := []Task{
		{Coords: tile.NewCoords(13, 4297, 2754)},
		{Coords: tile.NewCoords(13, 4297, 2755)},
		{Coords: tile.NewCoords(13, 4298, 2754)},
	}

	pool.Run(context.Background(), tasks)

	// Should have received progress callbacks
	if progressCalls.Load() == 0 {
		t.Error("Expected progress callbacks, got none")
	}

	// Final callback should show all completed
	if lastCompleted != len(tasks) {
		t.Errorf("Expected lastCompleted=%d, got %d", len(tasks), lastCompleted)
	}
	if lastTotal != len(tasks) {
		t.Errorf("Expected lastTotal=%d, got %d", len(tasks), lastTotal)
	}
}

func TestPool_EmptyTasks(t *testing.T) {
	gen := &mockGenerator{}

	pool := New(Config{
		Workers:   2,
		Generator: gen,
	})

	results := pool.Run(context.Background(), nil)

	if len(results) != 0 {
		t.Errorf("Expected 0 results for empty tasks, got %d", len(results))
	}

	if gen.callCount.Load() != 0 {
		t.Errorf("Expected 0 generator calls for empty tasks, got %d", gen.callCount.Load())
	}
}

func TestPool_WithSuffix(t *testing.T) {
	gen := &mockGenerator{delay: 10 * time.Millisecond}

	pool := New(Config{
		Workers:   1,
		Generator: gen,
	})

	tasks := []Task{
		{Coords: tile.NewCoords(13, 4297, 2754), Suffix: "@2x"},
	}

	results := pool.Run(context.Background(), tasks)

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	// Path should include the suffix
	if results[0].Path != "/tmp/z13_x4297_y2754@2x.png" {
		t.Errorf("Expected path with @2x suffix, got %s", results[0].Path)
	}
}

// dataMockGenerator records which path each tile took: the prefetched-data one
// or the ordinary fetch-it-yourself one.
type dataMockGenerator struct {
	withData     []string
	withoutData  []string
	mu           sync.Mutex
	failWithData bool
}

func (m *dataMockGenerator) Generate(_ context.Context, coords tile.Coords, _ bool, suffix string) (string, string, error) {
	m.mu.Lock()
	m.withoutData = append(m.withoutData, coords.String())
	m.mu.Unlock()
	return "/tmp/" + coords.String() + suffix + ".png", "", nil
}

func (m *dataMockGenerator) GenerateWithPrefetched(_ context.Context, coords tile.Coords, _ bool, suffix string, data *types.TileData) (string, string, error) {
	if data == nil {
		return "", "", errors.New("GenerateWithPrefetched called with nil data")
	}
	m.mu.Lock()
	m.withData = append(m.withData, coords.String())
	m.mu.Unlock()
	if m.failWithData {
		return "", "", errors.New("simulated failure")
	}
	return "/tmp/" + coords.String() + suffix + ".png", "", nil
}

func (m *dataMockGenerator) seen() (with, without []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.withData...), append([]string(nil), m.withoutData...)
}

// TestPoolRoutesPrefetchedData: a task carrying data must skip the datasource,
// and one without must take exactly the path it always did.
func TestPoolRoutesPrefetchedData(t *testing.T) {
	gen := &dataMockGenerator{}
	pool := New(Config{Workers: 2, Generator: gen})

	tasks := []Task{
		{Coords: tile.Coords{Z: 13, X: 1, Y: 1}, Data: &types.TileData{}},
		{Coords: tile.Coords{Z: 13, X: 2, Y: 2}},
		{Coords: tile.Coords{Z: 13, X: 3, Y: 3}, Data: &types.TileData{}},
	}

	results := pool.Run(context.Background(), tasks)
	if len(results) != len(tasks) {
		t.Fatalf("got %d results, want %d", len(results), len(tasks))
	}
	for _, r := range results {
		if r.Err != nil {
			t.Errorf("tile %s failed: %v", r.Task.Coords.String(), r.Err)
		}
	}

	with, without := gen.seen()
	if len(with) != 2 {
		t.Errorf("%d tiles took the prefetched path, want 2 (%v)", len(with), with)
	}
	if len(without) != 1 {
		t.Errorf("%d tiles took the fetching path, want 1 (%v)", len(without), without)
	}
}

// TestPoolFallsBackWhenGeneratorCannotTakeData: a generator that does not
// implement DataGenerator must still render, not fail. This is what keeps every
// existing test fake working.
func TestPoolFallsBackWhenGeneratorCannotTakeData(t *testing.T) {
	gen := &mockGenerator{}
	pool := New(Config{Workers: 1, Generator: gen})

	results := pool.Run(context.Background(), []Task{
		{Coords: tile.Coords{Z: 13, X: 1, Y: 1}, Data: &types.TileData{}},
	})

	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Err != nil {
		t.Errorf("a plain generator should ignore the data and render: %v", results[0].Err)
	}
	if gen.callCount.Load() != 1 {
		t.Errorf("generator called %d times, want 1", gen.callCount.Load())
	}
}

// TestRunStreamEmitsOneResultPerTask is the invariant callers count failures
// against, carried over from Run to the streaming path.
func TestRunStreamEmitsOneResultPerTask(t *testing.T) {
	gen := &mockGenerator{}
	pool := New(Config{Workers: 3, Generator: gen})

	const total = 25
	taskCh := make(chan Task)
	go func() {
		defer close(taskCh)
		for i := 0; i < total; i++ {
			taskCh <- Task{Coords: tile.Coords{Z: 13, X: uint32(i), Y: 1}}
		}
	}()

	results := pool.RunStream(context.Background(), taskCh, total)
	if len(results) != total {
		t.Fatalf("got %d results, want %d", len(results), total)
	}

	seen := map[string]int{}
	for _, r := range results {
		seen[r.Task.Coords.String()]++
	}
	if len(seen) != total {
		t.Errorf("results cover %d distinct tiles, want %d", len(seen), total)
	}
}

// TestRunStreamReportsProgressAgainstTotal: a streamed run cannot count its
// tasks in advance, so the denominator has to come from the caller.
func TestRunStreamReportsProgressAgainstTotal(t *testing.T) {
	gen := &mockGenerator{}
	pool := New(Config{Workers: 2, Generator: gen})

	const total = 8
	var lastTotal atomic.Int32
	pool.onProgress = func(_, tot, _ int) { lastTotal.Store(int32(tot)) }

	taskCh := make(chan Task, total)
	for i := 0; i < total; i++ {
		taskCh <- Task{Coords: tile.Coords{Z: 13, X: uint32(i), Y: 1}}
	}
	close(taskCh)

	pool.RunStream(context.Background(), taskCh, total)

	if got := lastTotal.Load(); got != total {
		t.Errorf("progress reported a total of %d, want %d", got, total)
	}
}

// TestRunStreamHonoursCancellation: every task still produces a result, so a
// cancelled run reports failures rather than losing tiles silently.
func TestRunStreamHonoursCancellation(t *testing.T) {
	gen := &mockGenerator{delay: 50 * time.Millisecond}
	pool := New(Config{Workers: 1, Generator: gen})

	const total = 10
	taskCh := make(chan Task, total)
	for i := 0; i < total; i++ {
		taskCh <- Task{Coords: tile.Coords{Z: 13, X: uint32(i), Y: 1}}
	}
	close(taskCh)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	results := pool.RunStream(ctx, taskCh, total)
	if len(results) != total {
		t.Fatalf("got %d results, want %d — a cancelled run must not lose tasks", len(results), total)
	}
}

func TestRunStreamNilChannel(t *testing.T) {
	pool := New(Config{Workers: 1, Generator: &mockGenerator{}})
	if got := pool.RunStream(context.Background(), nil, 0); got != nil {
		t.Errorf("RunStream(nil) = %v, want nil", got)
	}
}
