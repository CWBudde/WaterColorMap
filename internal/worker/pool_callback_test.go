package worker

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cwbudde/watercolormap/internal/tile"
)

// TestRunStreamOnResultDeliversEveryResultAndRetainsNothing: with a callback,
// the pool must still answer for every task, and must not hold on to the
// results — that slice is what a country-sized run cannot afford.
func TestRunStreamOnResultDeliversEveryResultAndRetainsNothing(t *testing.T) {
	gen := &mockGenerator{failTiles: map[string]bool{
		tile.Coords{Z: 13, X: 3, Y: 1}.String(): true,
	}}

	var (
		mu     sync.Mutex
		seen   = map[string]int{}
		failed int
	)
	pool := New(Config{
		Workers:   3,
		Generator: gen,
		OnResult: func(r Result) {
			mu.Lock()
			defer mu.Unlock()
			seen[r.Task.Coords.String()]++
			if r.Err != nil {
				failed++
			}
		},
	})

	const total = 25
	taskCh := make(chan Task)
	go func() {
		defer close(taskCh)
		for i := 0; i < total; i++ {
			taskCh <- Task{Coords: tile.Coords{Z: 13, X: uint32(i), Y: 1}, Index: i} //nolint:gosec // small test loop
		}
	}()

	results := pool.RunStream(context.Background(), taskCh, total)
	if results != nil {
		t.Errorf("RunStream retained %d results despite OnResult", len(results))
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != total {
		t.Fatalf("callback saw %d distinct tiles, want %d", len(seen), total)
	}
	for coords, n := range seen {
		if n != 1 {
			t.Errorf("callback saw %s %d times, want exactly once", coords, n)
		}
	}
	if failed != 1 {
		t.Errorf("callback reported %d failures, want 1", failed)
	}
}

// TestTaskIndexSurvivesTheRoundTrip: the checkpoint identifies a tile by its
// position in the enumeration, and that position is only useful if it comes
// back with the result.
func TestTaskIndexSurvivesTheRoundTrip(t *testing.T) {
	pool := New(Config{Workers: 4, Generator: &mockGenerator{}})

	const total = 12
	tasks := make([]Task, 0, total)
	for i := 0; i < total; i++ {
		tasks = append(tasks, Task{Coords: tile.Coords{Z: 13, X: uint32(i), Y: 1}, Index: i}) //nolint:gosec // small test loop
	}

	results := pool.Run(context.Background(), tasks)
	if len(results) != total {
		t.Fatalf("got %d results, want %d", len(results), total)
	}
	for _, r := range results {
		if int(r.Task.Coords.X) != r.Task.Index {
			t.Errorf("tile %s came back with index %d", r.Task.Coords.String(), r.Task.Index)
		}
	}
}

// TestRunIgnoresOnResult pins the contract Run's callers count failures
// against: len(results) == len(tasks), whatever the config says.
func TestRunIgnoresOnResult(t *testing.T) {
	var called atomic.Int32
	pool := New(Config{
		Workers:   2,
		Generator: &mockGenerator{},
		OnResult:  func(Result) { called.Add(1) },
	})

	tasks := []Task{
		{Coords: tile.Coords{Z: 13, X: 1, Y: 1}},
		{Coords: tile.Coords{Z: 13, X: 2, Y: 1}},
		{Coords: tile.Coords{Z: 13, X: 3, Y: 1}},
	}

	results := pool.Run(context.Background(), tasks)
	if len(results) != len(tasks) {
		t.Fatalf("Run returned %d results for %d tasks", len(results), len(tasks))
	}
	if got := called.Load(); got != 0 {
		t.Errorf("OnResult was called %d times by Run, want 0", got)
	}
}
