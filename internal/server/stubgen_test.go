package server

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cwbudde/watercolormap/internal/pipeline"
	"github.com/cwbudde/watercolormap/internal/tile"
	"github.com/cwbudde/watercolormap/internal/types"
)

// stubGenerator stands in for pipeline.Generator so the request path can be
// driven without Mapnik or a network. It writes fixed bytes where the real
// generator would write a tile, which is all the code above it observes.
type stubGenerator struct {
	tilesDir string
	// delay simulates render time, which is what makes the per-tile lock and
	// the render semaphore observable.
	delay time.Duration
	// renders counts calls, so a test can assert that N concurrent requests for
	// one tile produced one render.
	renders atomic.Int64
}

const stubTileBytes = "stub-tile-bytes"

func (g *stubGenerator) CalculateFetchBounds(coords tile.Coords) types.BoundingBox {
	return types.TileToBounds(types.TileCoordinate{Zoom: int(coords.Z), X: int(coords.X), Y: int(coords.Y)})
}

func (g *stubGenerator) GenerateWithData(
	ctx context.Context,
	coords tile.Coords,
	_ bool,
	filenameSuffix string,
	_ *pipeline.DebugContext,
	_ *types.TileData,
) (string, string, error) {
	g.renders.Add(1)
	if g.delay > 0 {
		select {
		case <-time.After(g.delay):
		case <-ctx.Done():
			return "", "", ctx.Err()
		}
	}

	full := filepath.Join(g.tilesDir, coords.FileName(filenameSuffix, "png"))
	if err := os.WriteFile(full, []byte(stubTileBytes), 0o600); err != nil {
		return "", "", err
	}
	return full, "", nil
}

func (g *stubGenerator) StampStore() pipeline.StampStore { return nil }

// newStubServer builds a server whose renders are stubbed out. cfg carries the
// caller's overrides; the fields this helper owns -- the tile directory and the
// generator seam -- are filled in afterwards, so a test cannot accidentally
// point them somewhere that renders for real.
func newStubServer(t *testing.T, cfg OnDemandTilesConfig, delay time.Duration) (*OnDemandTiles, *stubGenerator) {
	t.Helper()

	if cfg.TilesDir == "" {
		cfg.TilesDir = t.TempDir()
	}
	if cfg.MaxConcurrentGenerations == 0 {
		cfg.MaxConcurrentGenerations = 4
	}

	od, err := NewOnDemandTiles(nil, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewOnDemandTiles: %v", err)
	}
	t.Cleanup(od.Stop)

	gen := &stubGenerator{tilesDir: od.cfg.TilesDir, delay: delay}
	od.newGenerator = func(int) (tileGenerator, error) { return gen, nil }

	return od, gen
}

// writeFixtureTile puts a tile on disk without rendering one, so the cache-hit
// path can be tested for what it is: a file that is already there.
func writeFixtureTile(tb testing.TB, dir, name, body string) string {
	tb.Helper()

	full := filepath.Join(dir, name)
	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		tb.Fatalf("writing fixture tile %s: %v", name, err)
	}
	return full
}
