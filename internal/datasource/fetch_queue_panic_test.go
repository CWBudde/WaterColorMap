package datasource

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/cwbudde/watercolormap/internal/types"
)

// A panic inside a fetch used to take down the whole process, because the
// fetch workers run in bare goroutines and net/http only recovers handler
// goroutines. A nil datasource panics on the first field access inside
// doFetch, which exercises the real path rather than a synthetic panic.
func TestFetchQueueSurvivesPanickingFetch(t *testing.T) {
	fq := NewFetchQueue(nil, FetchQueueConfig{
		Workers: 1,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	fq.Start()
	t.Cleanup(fq.Stop)

	coord := types.TileCoordinate{Zoom: 13, X: 4317, Y: 2692}
	bounds := types.TileToBounds(coord)

	// The caller must be handed an error rather than left blocked. Without
	// the result being delivered, SubmitAndWait only unblocks when its own
	// context expires, stranding the request for the full timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := fq.SubmitAndWait(ctx, coord, bounds)
	if err != nil {
		t.Fatalf("SubmitAndWait returned a transport error: %v", err)
	}
	if result.Error == nil {
		t.Fatal("expected a failed FetchResult from the panicking fetch, got success")
	}

	// The worker must still be alive: recovery is per job, not per goroutine.
	if got := fq.Status().TotalFailed; got != 1 {
		t.Fatalf("TotalFailed = %d, want 1", got)
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()

	if _, err := fq.SubmitAndWait(ctx2, coord, bounds); err != nil {
		t.Fatalf("worker did not survive the first panic: %v", err)
	}
	if got := fq.Status().TotalFailed; got != 2 {
		t.Fatalf("TotalFailed = %d, want 2 (worker stopped consuming jobs)", got)
	}
}

// Delivering a result on a closed channel panics even inside a select with a
// default case, so the send needs its own recovery: without it the panic
// escapes the per-job boundary and kills the worker goroutine.
func TestFetchQueueSurvivesClosedResultChan(t *testing.T) {
	fq := NewFetchQueue(nil, FetchQueueConfig{
		Workers: 1,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	fq.Start()
	t.Cleanup(fq.Stop)

	coord := types.TileCoordinate{Zoom: 13, X: 4317, Y: 2692}
	bounds := types.TileToBounds(coord)

	closed := make(chan FetchResult, 1)
	close(closed)
	fq.jobs <- FetchJob{Coordinate: coord, Bounds: bounds, ResultChan: closed}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := fq.SubmitAndWait(ctx, coord, bounds); err != nil {
		t.Fatalf("worker did not survive delivery on a closed channel: %v", err)
	}
}
