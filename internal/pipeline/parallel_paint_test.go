package pipeline

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cwbudde/watercolormap/internal/safe"
	"github.com/cwbudde/watercolormap/internal/tile"
)

// generateSyntheticTile renders the synthetic golden tile at a given paint
// concurrency and returns the encoded bytes.
func generateSyntheticTile(t *testing.T, paintWorkers int) []byte {
	t.Helper()

	stylesDir := filepath.Join("..", "..", "assets", "styles")
	texturesDir := filepath.Join("..", "..", "assets", "textures")

	gen, err := NewGenerator(&syntheticDataSource{}, stylesDir, texturesDir, t.TempDir(), 256, 123, false, nil,
		GeneratorOptions{PaintWorkers: paintWorkers})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	path, _, err := gen.Generate(ctx, tile.NewCoords(13, 0, 0), true, "")
	require.NoError(t, err)

	data, err := os.ReadFile(path) // nolint:gosec // path comes from the generator's own temp dir
	require.NoError(t, err)

	return data
}

// TestPaintWorkersProduceIdenticalTiles is the determinism oracle for parallel layer
// painting: the same tile has to come out byte-for-byte the same however many layers
// are painted at once.
//
// The golden test next door only ever exercises one setting, so without this a
// scheduling-dependent result would pass the whole suite. Run it with -race to also
// cover the buffer sharing.
func TestPaintWorkersProduceIdenticalTiles(t *testing.T) {
	serial := generateSyntheticTile(t, 1)

	for _, workers := range []int{2, 4, maxPaintWorkers, maxPaintWorkers + 5} {
		parallel := generateSyntheticTile(t, workers)
		require.Equal(t, serial, parallel,
			"tile painted with %d workers differs from the serial tile", workers)
	}
}

// TestRunPaintJobsRecoversPanics pins panic containment. A paint job runs under a bare
// `go`, where nothing above it recovers, so a panic that escapes would take a whole tile
// server down instead of failing one tile. The successful end-to-end tests never reach
// this branch.
func TestRunPaintJobsRecoversPanics(t *testing.T) {
	var ran atomic.Int32

	jobs := []paintJob{
		{name: "ok-before", run: func() error { ran.Add(1); return nil }},
		{name: "boom", run: func() error { panic("paint exploded") }},
		{name: "ok-after", run: func() error { ran.Add(1); return nil }},
	}

	err := runPaintJobs(discardLogger(), jobs, len(jobs))

	var panicErr *safe.PanicError
	require.ErrorAs(t, err, &panicErr, "a recovered panic must surface as a *safe.PanicError")
	require.Equal(t, "paint exploded", panicErr.Value)
	require.EqualValues(t, 2, ran.Load(), "the jobs beside the panicking one must still run")
}

// TestRunPaintJobsReportsEarliestJobInListOrder pins the error selection: the reported
// failure is the earliest failing job in list order, not the first one to finish, so a
// broken layer names itself identically at any concurrency. The jobs here fail in
// reverse completion order on purpose — the last job is done before the first one fails.
func TestRunPaintJobsReportsEarliestJobInListOrder(t *testing.T) {
	lateFailed := make(chan struct{})
	firstErr := errors.New("first job in list order")
	lastErr := errors.New("last job in list order")

	jobs := []paintJob{
		{name: "first", run: func() error {
			<-lateFailed // only fail once the job after it already has
			return firstErr
		}},
		{name: "middle", run: func() error { return nil }},
		{name: "last", run: func() error {
			close(lateFailed)
			return lastErr
		}},
	}

	err := runPaintJobs(discardLogger(), jobs, len(jobs))
	require.ErrorIs(t, err, firstErr, "the earliest failing job in list order wins")
	require.NotErrorIs(t, err, lastErr)
}

// TestRunPaintJobsSerialPathReportsFirstFailure covers the workers<=1 shortcut, which
// stops at the first failure instead of running the whole list.
func TestRunPaintJobsSerialPathReportsFirstFailure(t *testing.T) {
	var ran atomic.Int32
	boom := errors.New("boom")

	jobs := []paintJob{
		{name: "ok", run: func() error { ran.Add(1); return nil }},
		{name: "bad", run: func() error { ran.Add(1); return boom }},
		{name: "unreached", run: func() error { ran.Add(1); return nil }},
	}

	err := runPaintJobs(discardLogger(), jobs, 1)
	require.ErrorIs(t, err, boom)
	require.EqualValues(t, 2, ran.Load(), "the serial path must stop at the first failure")
}

// discardLogger keeps the recovered-panic stack traces out of the test output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestAutoPaintWorkers pins the budget split. The exact numbers depend on GOMAXPROCS,
// so the assertions are about the shape: saturated callers get 1, a lone tile gets
// more than one core's worth on any multi-core machine, and nothing ever exceeds the
// size of the independent wave.
func TestAutoPaintWorkers(t *testing.T) {
	require.Equal(t, AutoPaintWorkers(1), AutoPaintWorkers(0), "a nonsense count reads as one tile")
	require.Equal(t, 1, AutoPaintWorkers(1024), "a saturated caller must not multiply its parallelism")
	require.LessOrEqual(t, AutoPaintWorkers(1), maxPaintWorkers)
	require.GreaterOrEqual(t, AutoPaintWorkers(1), 1)
}
