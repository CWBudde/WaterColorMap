package cmd

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/paulmach/orb"
	"github.com/spf13/viper"

	"github.com/cwbudde/watercolormap/internal/datasource"
	"github.com/cwbudde/watercolormap/internal/renderer"
	"github.com/cwbudde/watercolormap/internal/tile"
	"github.com/cwbudde/watercolormap/internal/types"
	"github.com/cwbudde/watercolormap/internal/worker"
)

// fakeAreaSource records the areas it was asked for and can be told to fail
// the first N of them, which is how the split path gets exercised.
type fakeAreaSource struct {
	// failWhile reports whether this request should fail.
	failWhile func(n int, bounds types.BoundingBox) error
	requests  []types.BoundingBox
	// features is what a successful fetch returns.
	features types.FeatureCollection
	mu       sync.Mutex
}

func (f *fakeAreaSource) FetchAreaData(_ context.Context, _ int, bounds types.BoundingBox, _ datasource.AreaFetchOptions) (*types.TileData, error) {
	f.mu.Lock()
	f.requests = append(f.requests, bounds)
	n := len(f.requests)
	f.mu.Unlock()

	if f.failWhile != nil {
		if err := f.failWhile(n, bounds); err != nil {
			return nil, err
		}
	}
	return &types.TileData{Bounds: bounds, Features: f.features, Source: "fake"}, nil
}

func (f *fakeAreaSource) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// fakeBandGenerator implements the narrow slice of pipeline.Generator the
// scheduler needs, without requiring Mapnik.
type fakeBandGenerator struct {
	// emptyFor makes SliceForTile return an empty collection for these tiles,
	// standing in for a tile with no features of its own inside a non-empty
	// band.
	emptyFor map[string]bool
	// existing stands in for tiles already on disk or in the MBTiles file.
	existing map[string]bool
}

func (g *fakeBandGenerator) TileExists(coords tile.Coords, _ string) bool {
	return g.existing != nil && g.existing[coords.String()]
}

// tileBox converts a tile's bounds into the BoundingBox shape the datasource
// works in. tile.Coords.Bounds returns the TileJSON [w,s,e,n] array.
func tileBox(c tile.Coords) types.BoundingBox {
	b := c.Bounds()
	return types.BoundingBox{MinLon: b[0], MinLat: b[1], MaxLon: b[2], MaxLat: b[3]}
}

func (g *fakeBandGenerator) BandFetchBounds(coords []tile.Coords) (types.BoundingBox, error) {
	if len(coords) == 0 {
		return types.BoundingBox{}, errors.New("no tiles")
	}
	bounds := tileBox(coords[0])
	for _, c := range coords[1:] {
		bounds = bounds.Union(tileBox(c))
	}
	return bounds, nil
}

func (g *fakeBandGenerator) SliceForTile(band *types.TileData, coords tile.Coords) *types.TileData {
	if band == nil {
		return nil
	}
	slice := &types.TileData{
		Coordinate: types.TileCoordinate{Zoom: int(coords.Z), X: int(coords.X), Y: int(coords.Y)},
		Bounds:     tileBox(coords),
		Source:     band.Source,
	}
	if g.emptyFor == nil || !g.emptyFor[coords.String()] {
		slice.Features = band.Features
	}
	return slice
}

// recordingGenerator records how each tile was rendered.
type recordingGenerator struct {
	withData    map[string]bool
	withoutData map[string]bool
	// failFor makes Generate fail for these tiles, which is how the streaming
	// path's failure accounting and the checkpoint watermark get exercised.
	failFor map[string]bool
	// rendered is every tile Generate was called for, in completion order.
	rendered []tile.Coords
	mu       sync.Mutex
}

func newRecordingGenerator() *recordingGenerator {
	return &recordingGenerator{withData: map[string]bool{}, withoutData: map[string]bool{}}
}

func (g *recordingGenerator) Generate(_ context.Context, coords tile.Coords, _ bool, _ string) (string, string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.withoutData[coords.String()] = true
	g.rendered = append(g.rendered, coords)
	if g.failFor != nil && g.failFor[coords.String()] {
		return "", "", errors.New("simulated render failure")
	}
	return coords.String(), "", nil
}

// renderedSet returns the tiles Generate saw, as a set.
func (g *recordingGenerator) renderedSet() map[tile.Coords]bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make(map[tile.Coords]bool, len(g.rendered))
	for _, c := range g.rendered {
		out[c] = true
	}
	return out
}

func (g *recordingGenerator) GenerateWithPrefetched(_ context.Context, coords tile.Coords, _ bool, _ string, _ *types.TileData) (string, string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.withData[coords.String()] = true
	return coords.String(), "", nil
}

func (g *recordingGenerator) counts() (with, without int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.withData), len(g.withoutData)
}

// oneFeature is a non-empty collection, so slices do not look empty.
func oneFeature() types.FeatureCollection {
	return types.FeatureCollection{
		Roads: []types.Feature{{
			ID:       "way/1",
			Type:     types.FeatureTypeRoad,
			Geometry: orb.LineString{{9.7, 52.3}, {9.8, 52.4}},
		}},
	}
}

func block(z uint32, x0, y0, size uint32) []tile.Coords {
	out := make([]tile.Coords, 0, size*size)
	for dx := uint32(0); dx < size; dx++ {
		for dy := uint32(0); dy < size; dy++ {
			out = append(out, tile.Coords{Z: z, X: x0 + dx, Y: y0 + dy})
		}
	}
	return out
}

func bandTestOptions(level uint32, minZoom int) *batchOptions {
	return &batchOptions{
		workers:   2,
		bandFetch: true,
		band: bandOptions{
			level:      level,
			minZoom:    minZoom,
			fetchAhead: 1,
			timeoutSec: 180,
		},
	}
}

func init() {
	// The scheduler logs; give it somewhere to log to in tests.
	if logger == nil {
		initLogging()
	}
}

// TestBandFetchIssuesOneQueryPerBlock is the whole point: sixteen tiles, one
// upstream query.
func TestBandFetchIssuesOneQueryPerBlock(t *testing.T) {
	coords := block(14, 8632, 5380, 4)
	ds := &fakeAreaSource{features: oneFeature()}
	gen := newRecordingGenerator()

	results, _ := runBandedTilePool(context.Background(), gen, &fakeBandGenerator{}, ds,
		coords, bandTestOptions(2, 10), "")

	if len(results) != len(coords) {
		t.Fatalf("got %d results, want %d", len(results), len(coords))
	}
	for _, r := range results {
		if r.Err != nil {
			t.Errorf("tile %s failed: %v", r.Task.Coords.String(), r.Err)
		}
	}
	if got := ds.count(); got != 1 {
		t.Errorf("upstream saw %d queries for a 4x4 band, want 1", got)
	}

	with, without := gen.counts()
	if with != 16 || without != 0 {
		t.Errorf("%d tiles rendered from band data and %d fetched individually, want 16 and 0", with, without)
	}
}

// TestBandFetchSplitsOnFailure: a band that cannot be fetched must not take its
// members down with it. It splits, and the sub-bands are tried independently.
func TestBandFetchSplitsOnFailure(t *testing.T) {
	coords := block(14, 8632, 5380, 4)
	ds := &fakeAreaSource{
		features: oneFeature(),
		failWhile: func(n int, _ types.BoundingBox) error {
			if n == 1 {
				return errors.New("response too large")
			}
			return nil
		},
	}
	gen := newRecordingGenerator()

	results, _ := runBandedTilePool(context.Background(), gen, &fakeBandGenerator{}, ds,
		coords, bandTestOptions(2, 10), "")

	if len(results) != len(coords) {
		t.Fatalf("got %d results, want %d", len(results), len(coords))
	}
	for _, r := range results {
		if r.Err != nil {
			t.Errorf("tile %s failed after the split: %v", r.Task.Coords.String(), r.Err)
		}
	}
	// One failed 4x4 attempt, then four 2x2 sub-bands.
	if got := ds.count(); got != 5 {
		t.Errorf("upstream saw %d queries, want 5 (1 failed 4x4 + 4 sub-bands)", got)
	}

	with, _ := gen.counts()
	if with != 16 {
		t.Errorf("%d tiles rendered from band data, want 16", with)
	}
}

// TestBandFetchFallsBackToPerTile: when every band size fails, the recursion
// bottoms out at ordinary per-tile fetches, so a tile fails as itself with the
// error it always had rather than being blamed on its neighbours.
func TestBandFetchFallsBackToPerTile(t *testing.T) {
	coords := block(14, 8632, 5380, 2)
	ds := &fakeAreaSource{
		features:  oneFeature(),
		failWhile: func(int, types.BoundingBox) error { return errors.New("always fails") },
	}
	gen := newRecordingGenerator()

	results, _ := runBandedTilePool(context.Background(), gen, &fakeBandGenerator{}, ds,
		coords, bandTestOptions(1, 10), "")

	if len(results) != len(coords) {
		t.Fatalf("got %d results, want %d", len(results), len(coords))
	}
	for _, r := range results {
		if r.Err != nil {
			t.Errorf("tile %s should have rendered via the per-tile path: %v", r.Task.Coords.String(), r.Err)
		}
	}

	with, without := gen.counts()
	if with != 0 || without != 4 {
		t.Errorf("%d tiles used band data and %d fetched individually, want 0 and 4", with, without)
	}
}

// TestBandFetchEmptySliceFallsBackInsideTheCheckedZooms: an empty slice at z8-13
// is either a genuinely empty tile or a silent upstream failure, and only a real
// per-tile fetch can tell those apart on the terms the rest of the pipeline
// already agreed.
func TestBandFetchEmptySliceFallsBackInsideTheCheckedZooms(t *testing.T) {
	tests := []struct {
		name        string
		zoom        uint32
		wantWith    int
		wantWithout int
	}{
		{"z12 falls back to a per-tile fetch", 12, 3, 1},
		{"z14 renders the empty slice as-is", 14, 4, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			coords := block(tt.zoom, 100, 100, 2)
			empty := coords[0].String()

			ds := &fakeAreaSource{features: oneFeature()}
			gen := newRecordingGenerator()
			bandGen := &fakeBandGenerator{emptyFor: map[string]bool{empty: true}}

			results, _ := runBandedTilePool(context.Background(), gen, bandGen, ds,
				coords, bandTestOptions(1, 0), "")

			if len(results) != len(coords) {
				t.Fatalf("got %d results, want %d", len(results), len(coords))
			}

			with, without := gen.counts()
			if with != tt.wantWith || without != tt.wantWithout {
				t.Errorf("%d tiles used band data and %d fetched individually, want %d and %d",
					with, without, tt.wantWith, tt.wantWithout)
			}
		})
	}
}

// TestBandFetchSkipsLowZooms: below the threshold a single tile already covers
// a huge area, so banding buys little and risks a lot.
func TestBandFetchSkipsLowZooms(t *testing.T) {
	coords := append(block(14, 8632, 5380, 2), tile.Coords{Z: 5, X: 16, Y: 10})

	ds := &fakeAreaSource{features: oneFeature()}
	gen := newRecordingGenerator()

	results, _ := runBandedTilePool(context.Background(), gen, &fakeBandGenerator{}, ds,
		coords, bandTestOptions(1, 10), "")

	if len(results) != len(coords) {
		t.Fatalf("got %d results, want %d", len(results), len(coords))
	}

	with, without := gen.counts()
	if with != 4 {
		t.Errorf("%d tiles used band data, want 4", with)
	}
	if without != 1 {
		t.Errorf("%d tiles fetched individually, want 1 (the z5 tile)", without)
	}
}

// TestBandFetchEveryTileGetsExactlyOneResult across a ragged set that does not
// fill its bands.
func TestBandFetchEveryTileGetsExactlyOneResult(t *testing.T) {
	coords := []tile.Coords{
		{Z: 14, X: 8632, Y: 5380},
		{Z: 14, X: 8633, Y: 5381},
		{Z: 14, X: 8640, Y: 5390}, // a different band
		{Z: 13, X: 4316, Y: 2690}, // a different zoom
	}

	ds := &fakeAreaSource{features: oneFeature()}
	gen := newRecordingGenerator()

	results, _ := runBandedTilePool(context.Background(), gen, &fakeBandGenerator{}, ds,
		coords, bandTestOptions(2, 10), "")

	seen := map[string]int{}
	for _, r := range results {
		seen[r.Task.Coords.String()]++
	}
	if len(seen) != len(coords) {
		t.Fatalf("results cover %d tiles, want %d", len(seen), len(coords))
	}
	for _, c := range coords {
		if seen[c.String()] != 1 {
			t.Errorf("tile %s got %d results, want exactly 1", c.String(), seen[c.String()])
		}
	}
}

// TestBandFetchHonoursCancellation is the guard against an interrupted run
// reporting success.
//
// runBatchGenerate counts failures from the returned results and from nothing
// else, so a cancelled banded run that simply emitted fewer results would show
// zero failures -- and would go on to write TileJSON and flush the MBTiles
// metadata for a tileset that is only partly rendered. Every requested tile
// must therefore come back, carrying an error.
func TestBandFetchHonoursCancellation(t *testing.T) {
	coords := block(14, 8632, 5380, 4)
	ds := &fakeAreaSource{features: oneFeature()}
	gen := newRecordingGenerator()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	results, _ := runBandedTilePool(ctx, gen, &fakeBandGenerator{}, ds,
		coords, bandTestOptions(2, 10), "")

	if len(results) != len(coords) {
		t.Fatalf("got %d results for %d tiles; a cancelled run must not lose tiles, "+
			"or the caller counts zero failures and reports success", len(results), len(coords))
	}

	seen := map[string]int{}
	failed := 0
	for _, r := range results {
		if r.Task.Coords == (tile.Coords{}) {
			t.Error("result with no task attached")
		}
		seen[r.Task.Coords.String()]++
		if r.Err != nil {
			failed++
		}
	}
	for _, c := range coords {
		if seen[c.String()] != 1 {
			t.Errorf("tile %s got %d results, want exactly 1", c.String(), seen[c.String()])
		}
	}
	if failed == 0 {
		t.Error("a cancelled run reported no failures at all")
	}
}

// TestReconcileBandResultsFillsGaps covers the reconciliation directly,
// including the case where the producer stops for a reason other than
// cancellation -- which must still be a failure and not a silent success.
func TestReconcileBandResultsFillsGaps(t *testing.T) {
	coords := block(14, 8632, 5380, 2)

	partial := []worker.Result{{Task: worker.Task{Coords: coords[0]}}}

	for _, cause := range []error{context.Canceled, nil} {
		got := reconcileBandResults(partial, coords, "", cause)
		if len(got) != len(coords) {
			t.Fatalf("cause=%v: got %d results, want %d", cause, len(got), len(coords))
		}

		failures := 0
		for _, r := range got {
			if r.Err != nil {
				failures++
			}
		}
		if want := len(coords) - 1; failures != want {
			t.Errorf("cause=%v: %d failures, want %d", cause, failures, want)
		}
	}

	// A complete result set is returned untouched.
	complete := make([]worker.Result, 0, len(coords))
	for _, c := range coords {
		complete = append(complete, worker.Result{Task: worker.Task{Coords: c}})
	}
	if got := reconcileBandResults(complete, coords, "", context.Canceled); len(got) != len(coords) {
		t.Errorf("a complete result set was modified: %d results, want %d", len(got), len(coords))
	}
}

// TestBandFetchSkipsCompletedBands: the skip-existing check lives inside the
// generator, i.e. after the fetch, so without filtering here a resumed run
// would re-query Overpass for every band it had already finished -- and the
// fetch is ~71% of a run's wall clock.
func TestBandFetchSkipsCompletedBands(t *testing.T) {
	coords := block(14, 8632, 5380, 2)

	existing := map[string]bool{}
	for _, c := range coords {
		existing[c.String()] = true
	}

	ds := &fakeAreaSource{features: oneFeature()}
	gen := newRecordingGenerator()
	bandGen := &fakeBandGenerator{existing: existing}

	results, _ := runBandedTilePool(context.Background(), gen, bandGen, ds,
		coords, bandTestOptions(1, 10), "")

	if got := ds.count(); got != 0 {
		t.Errorf("a fully rendered band issued %d Overpass queries, want 0", got)
	}
	// Every tile still answers, so the caller's accounting is unaffected and
	// the generator gets to log its own skips.
	if len(results) != len(coords) {
		t.Fatalf("got %d results, want %d", len(results), len(coords))
	}
	with, without := gen.counts()
	if with != 0 || without != len(coords) {
		t.Errorf("%d tiles used band data and %d took the plain path, want 0 and %d",
			with, without, len(coords))
	}
}

// TestBandFetchStillFetchesPartiallyCompletedBands: one missing tile is enough
// to justify the query, and the band must not be skipped wholesale.
func TestBandFetchStillFetchesPartiallyCompletedBands(t *testing.T) {
	coords := block(14, 8632, 5380, 2)

	// All but the last already rendered.
	existing := map[string]bool{}
	for _, c := range coords[:len(coords)-1] {
		existing[c.String()] = true
	}

	ds := &fakeAreaSource{features: oneFeature()}
	gen := newRecordingGenerator()
	bandGen := &fakeBandGenerator{existing: existing}

	results, _ := runBandedTilePool(context.Background(), gen, bandGen, ds,
		coords, bandTestOptions(1, 10), "")

	if got := ds.count(); got != 1 {
		t.Errorf("a partly rendered band issued %d queries, want 1", got)
	}
	if len(results) != len(coords) {
		t.Fatalf("got %d results, want %d", len(results), len(coords))
	}
	with, without := gen.counts()
	if with != 1 {
		t.Errorf("%d tiles rendered from band data, want 1 (the missing one)", with)
	}
	if without != len(coords)-1 {
		t.Errorf("%d tiles took the plain path, want %d", without, len(coords)-1)
	}
}

// TestBandFetchIgnoresExistingTilesUnderForce: --force means render it again,
// so nothing may be skipped.
func TestBandFetchIgnoresExistingTilesUnderForce(t *testing.T) {
	coords := block(14, 8632, 5380, 2)

	existing := map[string]bool{}
	for _, c := range coords {
		existing[c.String()] = true
	}

	ds := &fakeAreaSource{features: oneFeature()}
	gen := newRecordingGenerator()
	bandGen := &fakeBandGenerator{existing: existing}

	opts := bandTestOptions(1, 10)
	opts.force = true

	runBandedTilePool(context.Background(), gen, bandGen, ds, coords, opts, "")

	if got := ds.count(); got != 1 {
		t.Errorf("--force issued %d queries, want 1 — existing tiles must not be skipped", got)
	}
	if with, _ := gen.counts(); with != len(coords) {
		t.Errorf("%d tiles rendered from band data, want %d", with, len(coords))
	}
}

func TestBandOptionsValidation(t *testing.T) {
	tests := []struct {
		name    string
		level   int
		minZoom int
		wantErr bool
	}{
		{"defaults", 2, 10, false},
		{"level 0 is per-tile", 0, 10, false},
		{"level 4 is the ceiling", 4, 10, false},
		{"level 5 is refused", 5, 10, true},
		{"negative level is refused", -1, 10, true},
		{"zoom above max is refused", 2, 99, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetBandViper(t, tt.level, tt.minZoom)

			_, err := bandOptionsFromConfig()
			if tt.wantErr && err == nil {
				t.Error("expected an error")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// TestBandFetchUnavailableRejectsNonAreaSource: erroring rather than silently
// falling back, because a run that quietly ignored --band-fetch would show up
// only as an unexplained lack of speedup.
func TestBandFetchUnavailableRejectsNonAreaSource(t *testing.T) {
	if err := bandFetchUnavailable(&plainDataSource{}, nil); err == nil {
		t.Error("expected an error for a datasource that cannot fetch by area")
	}
	if err := bandFetchUnavailable(&areaCapableSource{}, nil); err != nil {
		t.Errorf("an area-capable source should be accepted: %v", err)
	}
}

// TestBandFetchSkipsNaturalEarthZooms: --band-min-zoom and
// natural-earth.max-zoom may overlap, and when they do the band scheduler must
// not fetch. The band query happens before the generator ever sees the tile, so
// without the exclusion the run would still issue the continent-scale query the
// low tier exists to avoid, only to have the renderer throw the answer away.
func TestBandFetchSkipsNaturalEarthZooms(t *testing.T) {
	coords := block(4, 4, 5, 2)

	ds := &fakeAreaSource{features: oneFeature()}
	gen := newRecordingGenerator()

	opts := bandTestOptions(1, 0)
	opts.naturalEarth = renderer.NaturalEarthConfig{Dir: t.TempDir(), MaxZoom: 5}

	results, _ := runBandedTilePool(context.Background(), gen, &fakeBandGenerator{}, ds,
		coords, opts, "")

	if len(results) != len(coords) {
		t.Fatalf("got %d results, want %d", len(results), len(coords))
	}
	if len(ds.requests) != 0 {
		t.Errorf("%d band fetches issued for Natural-Earth-covered zooms, want 0", len(ds.requests))
	}

	with, without := gen.counts()
	if with != 0 {
		t.Errorf("%d tiles got prefetched band data, want 0", with)
	}
	if without != len(coords) {
		t.Errorf("%d tiles went the plain path, want %d", without, len(coords))
	}
}

// TestBandFetchStillBandsAboveTheNaturalEarthCeiling guards the other side: the
// exclusion must not cost banding at the zooms it was built for.
func TestBandFetchStillBandsAboveTheNaturalEarthCeiling(t *testing.T) {
	coords := block(14, 8632, 5380, 2)

	ds := &fakeAreaSource{features: oneFeature()}
	gen := newRecordingGenerator()

	opts := bandTestOptions(1, 10)
	opts.naturalEarth = renderer.NaturalEarthConfig{Dir: t.TempDir(), MaxZoom: 5}

	if _, _ = runBandedTilePool(context.Background(), gen, &fakeBandGenerator{}, ds,
		coords, opts, ""); len(ds.requests) != 1 {
		t.Fatalf("%d band fetches at z14, want 1", len(ds.requests))
	}
}

type plainDataSource struct{}

func (plainDataSource) FetchTileData(context.Context, types.TileCoordinate) (*types.TileData, error) {
	return nil, errors.New("not implemented")
}

type areaCapableSource struct{ plainDataSource }

func (areaCapableSource) FetchAreaData(context.Context, int, types.BoundingBox, datasource.AreaFetchOptions) (*types.TileData, error) {
	return nil, errors.New("not implemented")
}

var _ worker.Generator = (*recordingGenerator)(nil)
var _ worker.DataGenerator = (*recordingGenerator)(nil)

// resetBandViper sets just the band keys, restoring viper afterwards.
func resetBandViper(t *testing.T, level, minZoom int) {
	t.Helper()
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set("generate.band_level", level)
	viper.Set("generate.band_min_zoom", minZoom)
	viper.Set("generate.band_fetch_ahead", 1)
	viper.Set("generate.band_timeout", 180)
}
