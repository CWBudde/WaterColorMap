package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
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

// A `serve --stale-*` cutoff has to be consulted on the cache hit, not only
// after a cache miss. The freshness policy reached the generator, but the
// generator was only ever asked about a tile that did not exist yet, so a tile
// already on disk was served forever no matter how stale its stamp was — the
// flags were inert.
func TestStaleCachedTileIsReRendered(t *testing.T) {
	tilesDir := t.TempDir()
	store, err := tilestamp.OpenFolder(tilesDir)
	if err != nil {
		t.Fatalf("OpenFolder: %v", err)
	}
	defer store.Close() // nolint:errcheck

	const placeholder = "not a real tile"
	tilePath := filepath.Join(tilesDir, "z9_x269_y168.png")
	if err := os.WriteFile(tilePath, []byte(placeholder), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := store.Put(tilestamp.Stamp{
		Z: 9, X: 269, Y: 168, Format: "png",
		RenderedAt:  time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		RendererRev: "v0.0.1+ancient",
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	od := newStampFreshnessServer(t, tilesDir, store,
		pipeline.FreshnessPolicy{RenderedBefore: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	defer od.Stop()

	rec := httptest.NewRecorder()
	od.serveTile(rec, httptest.NewRequest(http.MethodGet, "/tiles/z9_x269_y168.png", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}

	onDisk, err := os.ReadFile(tilePath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(onDisk) == placeholder {
		t.Fatal("the stale cached tile was served untouched instead of being re-rendered")
	}

	stamp, ok, err := store.Get(9, 269, 168, "", "png")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("the re-rendered tile has no stamp")
	}
	if stamp.RendererRev != "v9.9.9+test" {
		t.Errorf("RendererRev = %q, want the running binary's", stamp.RendererRev)
	}
}

// The other half of the same policy: a tile whose stamp still satisfies the
// cutoff is served straight from disk. Re-rendering it would turn every request
// into a render on a server that stays up for weeks.
func TestFreshCachedTileIsServedFromDisk(t *testing.T) {
	tilesDir := t.TempDir()
	store, err := tilestamp.OpenFolder(tilesDir)
	if err != nil {
		t.Fatalf("OpenFolder: %v", err)
	}
	defer store.Close() // nolint:errcheck

	const placeholder = "cached bytes"
	tilePath := filepath.Join(tilesDir, "z9_x269_y168.png")
	if err := os.WriteFile(tilePath, []byte(placeholder), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := store.Put(tilestamp.Stamp{
		Z: 9, X: 269, Y: 168, Format: "png",
		RenderedAt:  time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		RendererRev: "v9.9.9+test",
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	od := newStampFreshnessServer(t, tilesDir, store,
		pipeline.FreshnessPolicy{RenderedBefore: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	defer od.Stop()

	rec := httptest.NewRecorder()
	od.serveTile(rec, httptest.NewRequest(http.MethodGet, "/tiles/z9_x269_y168.png", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != placeholder {
		t.Error("a fresh cached tile was re-rendered instead of served from disk")
	}
}

func newStampFreshnessServer(
	t *testing.T,
	tilesDir string,
	store pipeline.StampStore,
	policy pipeline.FreshnessPolicy,
) *OnDemandTiles {
	t.Helper()

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
		Freshness:                policy,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewOnDemandTiles: %v", err)
	}
	return od
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
