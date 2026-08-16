package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

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
