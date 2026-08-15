package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/cwbudde/watercolormap/internal/pipeline"
	"github.com/cwbudde/watercolormap/internal/tilestamp"
	"github.com/cwbudde/watercolormap/internal/types"
)

// stampTestDataSource answers every fetch with no features and a known source
// and data version, so the render needs neither Overpass nor a network.
type stampTestDataSource struct{}

func (stampTestDataSource) FetchTileData(_ context.Context, coord types.TileCoordinate) (*types.TileData, error) {
	return &types.TileData{
		Coordinate:    coord,
		Bounds:        types.TileToBounds(coord),
		Source:        "test-source",
		DataTimestamp: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
	}, nil
}

// A tile the server rendered must carry provenance. Before this, `serve` built
// its generators with no stamp store at all, so every on-demand tile was
// unstamped — and an unstamped tile is one `purge --data-before` can never
// select and `generate --stale-*` must always re-render.
func TestServedTileIsStamped(t *testing.T) {
	tilesDir := t.TempDir()

	store, err := tilestamp.OpenFolder(tilesDir)
	if err != nil {
		t.Fatalf("OpenFolder: %v", err)
	}
	defer store.Close() // nolint:errcheck

	od, err := NewOnDemandTiles(stampTestDataSource{}, OnDemandTilesConfig{
		TilesDir:                 tilesDir,
		StylesDir:                filepath.Join("..", "..", "assets", "styles"),
		TexturesDir:              filepath.Join("..", "..", "assets", "textures"),
		BaseTileSize:             256,
		GenerateMissing:          true,
		MaxConcurrentGenerations: 1,
		GenerationTimeout:        4 * time.Minute,
		StampStore:               store,
		RendererRev:              "v9.9.9+test",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewOnDemandTiles: %v", err)
	}
	defer od.Stop()

	rec := httptest.NewRecorder()
	od.serveTile(rec, httptest.NewRequest(http.MethodGet, "/tiles/z9_x269_y168.png", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}

	stamp, ok, err := store.Get(9, 269, 168, "", "png")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("the served tile has no stamp")
	}
	if stamp.Source != "test-source" {
		t.Errorf("Source = %q, want the endpoint that answered", stamp.Source)
	}
	if stamp.RendererRev != "v9.9.9+test" {
		t.Errorf("RendererRev = %q, want the running binary's", stamp.RendererRev)
	}
	if stamp.Format != "png" {
		t.Errorf("Format = %q, want png", stamp.Format)
	}
	if stamp.RenderedAt.IsZero() {
		t.Error("RenderedAt is zero")
	}
}

// The stamp store is opened once with the server and shared by every generator
// it builds. The generators differ only in tile size and all write into one
// tile folder, so one store per generator would mean two SQLite handles on the
// same file — and, with the batch buffered in each of them, two disagreeing
// answers to "what is this tile's provenance".
func TestOnDemandTilesSharesOneStampStore(t *testing.T) {
	store, err := tilestamp.OpenFolder(t.TempDir())
	if err != nil {
		t.Fatalf("OpenFolder: %v", err)
	}
	defer store.Close() // nolint:errcheck

	od, err := NewOnDemandTiles(nil, OnDemandTilesConfig{
		TilesDir:     t.TempDir(),
		StylesDir:    filepath.Join("..", "..", "assets", "styles"),
		TexturesDir:  filepath.Join("..", "..", "assets", "textures"),
		BaseTileSize: 256,
		StampStore:   store,
		RendererRev:  "v9.9.9+test",
		Freshness:    pipeline.FreshnessPolicy{RenderedBefore: time.Unix(1, 0)},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewOnDemandTiles: %v", err)
	}
	defer od.Stop()

	base, err := od.getGenerator(256)
	if err != nil {
		t.Fatalf("getGenerator(256): %v", err)
	}
	hidpi, err := od.getGenerator(512)
	if err != nil {
		t.Fatalf("getGenerator(512): %v", err)
	}

	if base == hidpi {
		t.Fatal("getGenerator returned one generator for two tile sizes")
	}
	if base.StampStore() != pipeline.StampStore(store) {
		t.Error("the 256px generator does not write into the server's stamp store")
	}
	if hidpi.StampStore() != pipeline.StampStore(store) {
		t.Error("the 512px generator does not write into the server's stamp store")
	}

	// Asking twice must not build a second generator, let alone a second store.
	again, err := od.getGenerator(256)
	if err != nil {
		t.Fatalf("getGenerator(256) again: %v", err)
	}
	if again != base {
		t.Error("getGenerator built a second generator for the same tile size")
	}
}
