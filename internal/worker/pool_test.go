package worker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cwbudde/watercolormap/internal/tile"
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

func TestPool_Cancellation(t *testing.T) {
	gen := &mockGenerator{delay: 100 * time.Millisecond}

	pool := New(Config{
		Workers:   2,
		Generator: gen,
	})

	tasks := make([]Task, 10)
	for i := range tasks {
		tasks[i] = Task{Coords: tile.NewCoords(13, 4297+uint32(i), 2754)}
	}

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

	// Some results may have errors due to cancellation
	var cancelledCount int
	for _, r := range results {
		if r.Err != nil && errors.Is(r.Err, context.Canceled) {
			cancelledCount++
		}
	}

	t.Logf("Completed with %d results (%d cancelled) in %v", len(results), cancelledCount, elapsed)
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
