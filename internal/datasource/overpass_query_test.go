package datasource

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cwbudde/watercolormap/internal/types"
)

// goldenQueryBounds is a fixed bbox so the golden text depends only on the
// zoom level and the clipping mode, never on the tile coordinates.
var goldenQueryBounds = types.BoundingBox{
	MinLat: 52.320000,
	MinLon: 9.650000,
	MaxLat: 52.430000,
	MaxLon: 9.850000,
}

// TestBuildTileQueryGolden pins the exact Overpass QL emitted by buildTileQuery
// for every zoom level 0..18 and both clipping modes. These goldens capture the
// current behaviour verbatim; refactors of the query builders must not change
// a single byte. Regenerate with `UPDATE_GOLDEN=1 go test ./internal/datasource/...`.
func TestBuildTileQueryGolden(t *testing.T) {
	goldenDir := filepath.Join("..", "..", "testdata", "golden", "overpass-query")
	update := os.Getenv("UPDATE_GOLDEN") == "1"
	if update {
		require.NoError(t, os.MkdirAll(goldenDir, 0o755))
	}

	for _, clip := range []bool{false, true} {
		for zoom := 0; zoom <= 18; zoom++ {
			name := fmt.Sprintf("z%02d", zoom)
			if clip {
				name += "-clipped"
			}

			t.Run(name, func(t *testing.T) {
				ds := &OverpassDataSource{clipGeomToBbox: clip}
				got := ds.buildTileQuery(goldenQueryBounds, zoom)

				goldenPath := filepath.Join(goldenDir, name+".txt")
				if update {
					require.NoError(t, os.WriteFile(goldenPath, []byte(got), 0o600))
					return
				}

				want, err := os.ReadFile(goldenPath) //nolint:gosec // test-controlled path
				require.NoError(t, err, "golden file missing: %s (run with UPDATE_GOLDEN=1)", goldenPath)
				require.Equal(t, string(want), got, "query for %s differs from golden", name)
			})
		}
	}
}
