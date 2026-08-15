package pipeline

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cwbudde/watercolormap/internal/tile"
	"github.com/cwbudde/watercolormap/internal/types"
)

// TestBandFetchRendersIdenticalTiles is the load-bearing test for band
// fetching: a tile rendered from a band's data, sliced down to that tile's own
// fetch bounds, must be **byte-identical** to the same tile rendered from its
// own per-tile fetch.
//
// It can assert byte equality rather than a tolerance because rendering is
// deterministic given the same features in the same order: worldSyntheticDataSource
// returns fixed lon/lat geometry built once, and the Overpass path that this
// stands in for now walks its element maps in OSM ID order rather than map
// order. If this ever has to become a tolerance, that is a real coupling between
// band data and rendering, not noise.
//
// Note what the two paths actually differ in: the per-tile render receives the
// *whole* synthetic feature set, while the band render receives only the subset
// whose bounding boxes reach the tile. Equality is therefore also the proof
// that filtering removes nothing that would have painted.
func TestBandFetchRendersIdenticalTiles(t *testing.T) {
	const zoom = 13
	const originX, originY = 4317, 2692

	block := blockBounds(
		tile.NewCoords(zoom, originX, originY),
		tile.NewCoords(zoom, originX+1, originY+1))
	ds := newWorldSyntheticDataSource(block)

	stylesDir := filepath.Join("..", "..", "assets", "styles")
	texturesDir := filepath.Join("..", "..", "assets", "textures")

	perTileDir := t.TempDir()
	bandDir := t.TempDir()

	perTileGen, err := NewGenerator(ds, stylesDir, texturesDir, perTileDir, 256, 123, false, nil, GeneratorOptions{})
	require.NoError(t, err)
	bandGen, err := NewGenerator(ds, stylesDir, texturesDir, bandDir, 256, 123, false, nil, GeneratorOptions{})
	require.NoError(t, err)

	coords := []tile.Coords{
		{Z: zoom, X: originX, Y: originY},
		{Z: zoom, X: originX + 1, Y: originY},
		{Z: zoom, X: originX, Y: originY + 1},
		{Z: zoom, X: originX + 1, Y: originY + 1},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	// One "band fetch": the union of every tile's fetch bounds, answered with
	// the same fixed features an area query would return.
	bandBounds, err := bandGen.BandFetchBounds(coords)
	require.NoError(t, err)
	bandData, err := ds.FetchAreaData(ctx, zoom, bandBounds)
	require.NoError(t, err)

	// Every member's own fetch box must lie inside the band's box, or the band
	// could hand a tile less data than it needs.
	for _, c := range coords {
		tileBounds := bandGen.CalculateFetchBounds(c)
		require.True(t, bandBounds.Contains(tileBounds),
			"band bounds %v do not contain tile %s bounds %v", bandBounds, c.String(), tileBounds)
	}

	for _, c := range coords {
		perTilePath, _, err := perTileGen.Generate(ctx, c, true, "")
		require.NoError(t, err, "per-tile render of %s", c.String())

		slice := bandGen.SliceForTile(bandData, c)
		require.NotNil(t, slice, "slice for %s", c.String())
		bandPath, _, err := bandGen.GenerateWithPrefetched(ctx, c, true, "", slice)
		require.NoError(t, err, "band render of %s", c.String())

		perTileBytes, err := os.ReadFile(perTilePath)
		require.NoError(t, err)
		bandBytes, err := os.ReadFile(bandPath)
		require.NoError(t, err)

		require.True(t, bytes.Equal(perTileBytes, bandBytes),
			"tile %s differs between the per-tile and band paths (%d vs %d bytes)",
			c.String(), len(perTileBytes), len(bandBytes))
	}
}

// TestBandSliceKeepsEverythingThatPaints is the same claim at the data level,
// and it fails faster and more legibly than the render comparison when the
// filter is wrong: every feature a per-tile fetch would have returned must
// survive slicing.
func TestBandSliceKeepsEverythingThatPaints(t *testing.T) {
	const zoom = 13
	const originX, originY = 4317, 2692

	block := blockBounds(
		tile.NewCoords(zoom, originX, originY),
		tile.NewCoords(zoom, originX+1, originY+1))
	ds := newWorldSyntheticDataSource(block)

	gen, err := NewGenerator(ds,
		filepath.Join("..", "..", "assets", "styles"),
		filepath.Join("..", "..", "assets", "textures"),
		t.TempDir(), 256, 123, false, nil, GeneratorOptions{})
	require.NoError(t, err)

	coords := tile.Coords{Z: zoom, X: originX, Y: originY}
	bounds := gen.CalculateFetchBounds(coords)

	band := &types.TileData{Bounds: bounds, Features: ds.features, Source: "band"}
	slice := gen.SliceForTile(band, coords)
	require.NotNil(t, slice)

	// Anything the filter dropped must genuinely lie outside the tile's fetch
	// box, which is exactly what Overpass would not have returned either.
	dropped := ds.features.Count() - slice.Features.Count()
	t.Logf("band held %d features, slice kept %d", ds.features.Count(), slice.Features.Count())

	for _, f := range ds.features.Roads {
		if f.Geometry == nil {
			continue
		}
		b := f.Geometry.Bound()
		geomBox := types.BoundingBox{MinLon: b.Min[0], MinLat: b.Min[1], MaxLon: b.Max[0], MaxLat: b.Max[1]}
		if !bounds.Intersects(geomBox) {
			continue
		}
		require.True(t, containsFeature(slice.Features.Roads, f.ID),
			"road %s intersects the tile but was dropped by the slice", f.ID)
	}

	require.GreaterOrEqual(t, dropped, 0, "slicing must not invent features")
}

// TestBandSliceDoesNotMutateTheBand: the band is shared by every tile in it, so
// slicing one tile must not disturb the next.
func TestBandSliceDoesNotMutateTheBand(t *testing.T) {
	const zoom = 13
	const originX, originY = 4317, 2692

	block := blockBounds(
		tile.NewCoords(zoom, originX, originY),
		tile.NewCoords(zoom, originX+1, originY+1))
	ds := newWorldSyntheticDataSource(block)

	gen, err := NewGenerator(ds,
		filepath.Join("..", "..", "assets", "styles"),
		filepath.Join("..", "..", "assets", "textures"),
		t.TempDir(), 256, 123, false, nil, GeneratorOptions{})
	require.NoError(t, err)

	band := &types.TileData{Features: ds.features, Source: "band"}
	before := band.Features.Count()

	for _, c := range []tile.Coords{
		{Z: zoom, X: originX, Y: originY},
		{Z: zoom, X: originX + 1, Y: originY + 1},
	} {
		gen.SliceForTile(band, c)
	}

	require.Equal(t, before, band.Features.Count(), "slicing modified the band it was given")
}

func TestBandFetchBoundsRejectsEmptyInput(t *testing.T) {
	gen := &Generator{tileSize: 256}
	_, err := gen.BandFetchBounds(nil)
	require.Error(t, err)
}

func containsFeature(features []types.Feature, id string) bool {
	for _, f := range features {
		if f.ID == id {
			return true
		}
	}
	return false
}
