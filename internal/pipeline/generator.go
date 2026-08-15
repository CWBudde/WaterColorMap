package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cwbudde/watercolormap/internal/composite"
	"github.com/cwbudde/watercolormap/internal/geojson"
	"github.com/cwbudde/watercolormap/internal/mask"
	"github.com/cwbudde/watercolormap/internal/renderer"
	"github.com/cwbudde/watercolormap/internal/texture"
	"github.com/cwbudde/watercolormap/internal/tile"
	"github.com/cwbudde/watercolormap/internal/tileformat"
	"github.com/cwbudde/watercolormap/internal/tilestamp"
	"github.com/cwbudde/watercolormap/internal/types"
	"github.com/cwbudde/watercolormap/internal/watercolor"
)

// StageCapture represents a single captured intermediate stage.
type StageCapture struct {
	Image       image.Image // The actual image data
	Name        string      // e.g., "01_water_alpha"
	Description string      // e.g., "Alpha mask extracted from water layer"
	ZOrder      int         // For sorting (01, 02, etc.)
}

// DebugContext optionally collects intermediate pipeline stages.
type DebugContext struct {
	Stages []StageCapture
	mu     sync.Mutex // Thread-safe
}

// Capture adds a stage to the debug context if it exists.
func (dc *DebugContext) Capture(name, description string, img image.Image, zorder int) {
	if dc == nil {
		return // Fast path: no debug context, no overhead
	}
	dc.mu.Lock()
	defer dc.mu.Unlock()
	dc.Stages = append(dc.Stages, StageCapture{
		Name:        name,
		Description: description,
		Image:       img,
		ZOrder:      zorder,
	})
}

// SortedStages returns stages sorted by ZOrder.
func (dc *DebugContext) SortedStages() []StageCapture {
	if dc == nil {
		return nil
	}
	dc.mu.Lock()
	defer dc.mu.Unlock()

	sorted := make([]StageCapture, len(dc.Stages))
	copy(sorted, dc.Stages)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].ZOrder < sorted[j].ZOrder
	})
	return sorted
}

// GeneratorOptions controls output and encoding behavior.
//
// Field order follows govet's fieldalignment: the pointer-bearing fields come
// first so the GC scans as little of the struct as possible.
type GeneratorOptions struct {
	// TileWriter optionally writes tiles to an alternative storage backend (e.g., MBTiles).
	// If nil, tiles are written to disk in outputDir.
	TileWriter TileWriter

	// StampStore, when non-nil, records what source data each written tile was
	// rendered from, and is read back by the freshness check below. Nil means
	// exactly today's behaviour: nothing is written and nothing is consulted,
	// so a caller that has no stamp store — including every test fake — is
	// unaffected.
	StampStore StampStore

	// Freshness turns "does this tile exist" into "is this tile still good".
	// The zero value asks nothing beyond existence, which is what every
	// pre-existing caller gets.
	Freshness FreshnessPolicy

	// Watercolor optionally overrides the watercolor parameters from config.
	// Nil means "use DefaultParams verbatim", with no arithmetic in between.
	// It is validated in NewGenerator so a bad config fails at startup rather
	// than at tile 5000.
	Watercolor *watercolor.Overrides

	// PNGCompression controls PNG encoding. Supported values:
	// "default", "speed", "best", "none". Ignored unless ImageFormat is PNG.
	PNGCompression string

	// FolderStructure controls file naming for folder format. Supported values:
	// "flat" (z{z}_x{x}_y{y}.{ext}), "nested" ({z}/{x}/{y}.{ext}).
	FolderStructure string

	// RendererRev identifies this binary in the stamps it writes, so a later
	// run can re-render what an older renderer produced. The generate command
	// builds it from the ldflags-injected version and commit.
	RendererRev string

	// ImageFormat selects the tile image encoding. The zero value is PNG, so
	// every existing construction site keeps its behaviour. It is resolved into
	// a tileformat.Encoder in NewGenerator, which is where an unusable format
	// fails — at startup, not at tile 5000.
	ImageFormat tileformat.Format

	// Ocean points the ocean pass at the processed OSM water polygons.
	// The zero value disables it, and the pipeline then behaves exactly as it
	// did before ocean rendering existed.
	Ocean renderer.OceanConfig

	// NaturalEarth points the low-zoom passes at the Natural Earth shapefiles.
	// The zero value disables them, and every zoom then goes through Overpass
	// exactly as it did before.
	NaturalEarth renderer.NaturalEarthConfig

	// WebPEffort is nativewebp's compression level (0-6), every value
	// explicit — 0 is the fastest level, not "unset". Ignored unless
	// ImageFormat is WebP. The generate command defaults it to
	// tileformat.DefaultWebPEffort.
	WebPEffort int
}

// TileWriter writes tile data to a storage backend.
type TileWriter interface {
	WriteTile(z, x, y int, pngData []byte) error
}

// TileProber is the optional half of TileWriter: a backend that can say whether
// a tile is already stored. A writer that cannot answer simply omits it, and the
// generator then never skips — the same behaviour as today.
//
// It is deliberately not folded into TileWriter, which would force the method
// onto every test fake and onto any future writer that has no way to answer.
type TileProber interface {
	HasTile(z, x, y int) (bool, error)
}

// StampStore records and reads the per-tile source-data stamps that make
// freshness decidable. *tilestamp.Store implements it; it is an interface here
// so the pipeline neither owns the file nor forces a test to open one.
type StampStore interface {
	Put(tilestamp.Stamp) error
	Get(z, x, y int, suffix string) (tilestamp.Stamp, bool, error)
}

// FreshnessPolicy says which already-rendered tiles are nevertheless out of
// date. Every field is opt-in; the zero value asks nothing, and then the
// existing-tile check is the pure existence test it has always been.
type FreshnessPolicy struct {
	// DataBefore re-renders a tile whose source data (osm_base_ts) predates
	// this instant — "everything older than the last import".
	DataBefore time.Time
	// RenderedBefore re-renders a tile written before this instant, regardless
	// of its data. Useful after a styling change that is not a code change.
	RenderedBefore time.Time
	// RendererRev re-renders a tile stamped by a different binary than the one
	// running, using GeneratorOptions.RendererRev as the comparison.
	RendererRev bool
}

// Enabled reports whether the policy asks anything at all.
func (p FreshnessPolicy) Enabled() bool {
	return !p.DataBefore.IsZero() || !p.RenderedBefore.IsZero() || p.RendererRev
}

// DataSource fetches OSM features for a tile coordinate.
type DataSource interface {
	FetchTileData(context.Context, types.TileCoordinate) (*types.TileData, error)
}

type dataSourceWithBounds interface {
	FetchTileDataWithBounds(context.Context, types.TileCoordinate, types.BoundingBox) (*types.TileData, error)
}

// Generator wires datasource, rendering, watercolor, and compositing into a single step.
type Generator struct {
	ds       DataSource
	textures map[geojson.LayerType]image.Image
	logger   *slog.Logger
	tuner    *watercolor.Tuner
	// enc is resolved once in NewGenerator and is never nil afterwards, so
	// the per-tile path carries no format branching at all.
	enc        tileformat.Encoder
	stylesDir  string
	outputDir  string
	options    GeneratorOptions
	tileSize   int
	seed       int64
	keepLayers bool
}

// NewGenerator loads textures and prepares a generator.
func NewGenerator(ds DataSource, stylesDir, texturesDir, outputDir string, tileSize int, seed int64, keepLayers bool, logger *slog.Logger, opts GeneratorOptions) (*Generator, error) {
	if tileSize <= 0 {
		return nil, fmt.Errorf("tile size must be positive")
	}

	textures, err := texture.LoadDefaultTextures(texturesDir)
	if err != nil {
		return nil, err
	}

	enc, err := tileformat.NewEncoder(tileformat.EncoderOptions{
		Format:         opts.ImageFormat,
		PNGCompression: opts.PNGCompression,
		WebPEffort:     opts.WebPEffort,
	})
	if err != nil {
		return nil, fmt.Errorf("invalid tile image format: %w", err)
	}

	// Validate and precompute here, not per tile: an invalid config should stop
	// the run before the first Overpass request, and tinting a texture is an
	// allocation we do exactly once.
	tuner, err := watercolor.NewTuner(opts.Watercolor, textures)
	if err != nil {
		return nil, fmt.Errorf("invalid watercolor configuration: %w", err)
	}

	return &Generator{
		ds:         ds,
		stylesDir:  stylesDir,
		outputDir:  outputDir,
		textures:   textures,
		enc:        enc,
		tileSize:   tileSize,
		seed:       seed,
		keepLayers: keepLayers,
		logger:     logger,
		options:    opts,
		tuner:      tuner,
	}, nil
}

// Generate renders, paints, composites, and writes the final tile PNG.
// Returns the final tile path and (optionally) the layer directory when kept.
// It satisfies worker.Generator; use GenerateWithDebug to capture intermediate stages.
func (g *Generator) Generate(ctx context.Context, coords tile.Coords, force bool, filenameSuffix string) (string, string, error) {
	return g.GenerateWithData(ctx, coords, force, filenameSuffix, nil, nil)
}

// GenerateWithDebug is Generate with stage capturing.
// dc may be nil; pass nil in production for zero overhead.
func (g *Generator) GenerateWithDebug(ctx context.Context, coords tile.Coords, force bool, filenameSuffix string, dc *DebugContext) (string, string, error) {
	return g.GenerateWithData(ctx, coords, force, filenameSuffix, dc, nil)
}

// GenerateWithData renders a tile with optionally pre-fetched data.
// If prefetchedData is nil, data will be fetched from the datasource.
// This allows decoupling data fetching from rendering for better error handling and retry logic.
// dc may be nil; pass nil in production for zero overhead.
func (g *Generator) GenerateWithData(ctx context.Context, coords tile.Coords, force bool, filenameSuffix string, dc *DebugContext, prefetchedData *types.TileData) (string, string, error) {
	suffix := strings.TrimSpace(filenameSuffix)

	// Both derived from one place, so TileExists cannot come to disagree with
	// the check below about which file this tile is.
	finalPath, tileDir := g.TilePath(coords, suffix)

	if !force && g.tileExists(coords, finalPath, suffix) {
		g.log().Info("Tile already exists; skipping", "coords", coords.String(), "path", finalPath)
		return finalPath, "", nil
	}

	// Only the folder backend needs a directory. Creating one in MBTiles mode
	// would leave a stray, permanently empty ./tiles behind.
	if g.options.TileWriter == nil {
		if err := os.MkdirAll(tileDir, 0o755); err != nil {
			return "", "", fmt.Errorf("failed to create output dir: %w", err)
		}
	}

	// Phase 1: Setup and render all layers (optionally with pre-fetched data)
	renderResult, err := g.renderLayersWithData(ctx, coords, prefetchedData)
	if err != nil {
		return "", "", err
	}
	// Clean up temp layer directory unless keepLayers is set
	if !g.keepLayers {
		defer os.RemoveAll(renderResult.layerDir) // nolint:errcheck
	}

	// Phase 2: Build masks from rendered layers
	masks := buildMasks(renderResult.rawLayers, renderResult.params, dc)

	// Phase 3: Paint all layers with watercolor effects
	painted, err := paintAllLayers(renderResult.rawLayers, masks, renderResult.params, g.textures, dc)
	if err != nil {
		return "", "", err
	}

	// Phase 4: Composite and write final tile
	return g.compositeAndWrite(painted, coords, finalPath, suffix, renderResult, dc)
}

// tileExists reports whether the tile may be skipped: present in whichever
// backend this generator writes to *and*, when a FreshnessPolicy is configured,
// still current according to its stamp.
//
// Every uncertain case answers false, i.e. "render it". That asymmetry is
// deliberate: a wrong "skip" leaves a permanent hole in the tileset that nothing
// later in the run will fill, while a wrong "render" costs a few seconds of CPU
// and overwrites the tile with an identical one. So a writer that is not a
// TileProber, and a probe that fails, both fall through to rendering — and so
// does every unanswerable freshness question: no stamp store, no stamp for this
// tile, an unreadable store, a stamp with no usable timestamp. A missing stamp
// in particular is the common case on the first run after this feature landed,
// and re-rendering those tiles is the only answer that cannot leave a hole.
func (g *Generator) tileExists(coords tile.Coords, finalPath, suffix string) bool {
	if !g.tilePresent(coords, finalPath) {
		return false
	}
	return g.tileIsFresh(coords, suffix)
}

// tilePresent is the existence half of tileExists: the configured TileWriter
// when there is one (if it can answer, i.e. implements TileProber), otherwise
// the file at finalPath.
func (g *Generator) tilePresent(coords tile.Coords, finalPath string) bool {
	if g.options.TileWriter != nil {
		prober, ok := g.options.TileWriter.(TileProber)
		if !ok {
			return false
		}

		exists, err := prober.HasTile(int(coords.Z), int(coords.X), int(coords.Y))
		if err != nil {
			g.log().Warn("Tile existence check failed; rendering anyway",
				"coords", coords.String(), "error", err)
			return false
		}

		return exists
	}

	_, err := os.Stat(finalPath)
	return err == nil
}

// tileIsFresh reports whether an existing tile still satisfies the configured
// FreshnessPolicy.
//
// With no policy it returns true without touching the stamp store at all, which
// is what makes an unconfigured run byte-for-byte what it was before stamps
// existed. With a policy, anything it cannot establish counts as stale, for the
// reason spelled out on tileExists.
func (g *Generator) tileIsFresh(coords tile.Coords, suffix string) bool {
	policy := g.options.Freshness
	if !policy.Enabled() {
		return true
	}

	store := g.options.StampStore
	if store == nil {
		g.log().Warn("Freshness check requested but no stamp store is open; rendering",
			"coords", coords.String())
		return false
	}

	stamp, ok, err := store.Get(int(coords.Z), int(coords.X), int(coords.Y), suffix)
	if err != nil {
		g.log().Warn("Stamp lookup failed; rendering anyway",
			"coords", coords.String(), "error", err)
		return false
	}
	if !ok {
		g.log().Debug("Tile has no stamp; rendering", "coords", coords.String())
		return false
	}

	switch {
	case !policy.DataBefore.IsZero() &&
		(stamp.OSMBase.IsZero() || stamp.OSMBase.Before(policy.DataBefore)):
		return false
	case !policy.RenderedBefore.IsZero() &&
		(stamp.RenderedAt.IsZero() || stamp.RenderedAt.Before(policy.RenderedBefore)):
		return false
	case policy.RendererRev && stamp.RendererRev != g.options.RendererRev:
		return false
	}

	return true
}

// putStamp records what this tile was rendered from.
//
// A failure here is logged and swallowed on purpose: the tile is on disk and
// correct, and the stamp is bookkeeping about it. Failing the tile would turn a
// sidecar problem into a hole in the tileset — the very outcome the whole
// skip-existing asymmetry is built to avoid. The cost of a lost stamp is that a
// later freshness run re-renders that tile, which is the safe direction.
func (g *Generator) putStamp(coords tile.Coords, suffix string, res *renderLayersResult) {
	store := g.options.StampStore
	if store == nil {
		return
	}

	err := store.Put(tilestamp.Stamp{
		Z:           int(coords.Z),
		X:           int(coords.X),
		Y:           int(coords.Y),
		Suffix:      suffix,
		OSMBase:     res.dataTimestamp,
		RenderedAt:  time.Now(),
		Source:      res.dataSource,
		RendererRev: g.options.RendererRev,
	})
	if err != nil {
		g.log().Warn("Failed to record tile stamp; the tile itself is fine",
			"coords", coords.String(), "suffix", suffix, "error", err)
	}
}

func cropNRGBA(src image.Image, rect image.Rectangle) *image.NRGBA {
	if src == nil {
		return nil
	}
	if rect.Empty() {
		return image.NewNRGBA(image.Rect(0, 0, 0, 0))
	}
	if !rect.In(src.Bounds()) {
		// Best effort: intersect and return what we can.
		rect = rect.Intersect(src.Bounds())
	}

	dst := image.NewNRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	for y := 0; y < rect.Dy(); y++ {
		for x := 0; x < rect.Dx(); x++ {
			dst.Set(x, y, src.At(rect.Min.X+x, rect.Min.Y+y))
		}
	}
	return dst
}

func readPNG(path string) (image.Image, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	img, err := png.Decode(file)
	if cerr := file.Close(); cerr != nil && err == nil {
		return nil, cerr
	}
	return img, err
}

func (g *Generator) log() *slog.Logger {
	if g.logger != nil {
		return g.logger
	}
	return slog.Default()
}

// watercolorParams builds the watercolor parameters and the metatile padding
// for a tile, and is the single source of truth for both.
//
// Both the Overpass fetch bbox and the metatile size are derived from the same
// padding, which in turn is derived from every sigma in the params. If the two
// were computed separately and ever disagreed, the fetched data would not cover
// the metatile and polygons would clip at its edge — an error the final crop
// hides at 256px but not at 512. Keeping one accessor makes that divergence
// impossible rather than merely unlikely.
// The three steps below are ordered, not interchangeable:
//
//  1. the tuner, on values still at the 256px reference size, because config
//     keys are lengths on the ground and the user should not have to restate
//     them per tile size;
//  2. ApplyScale, which converts every length to device pixels;
//  3. ZoomAdjustedBlurSigma, which is a per-zoom look adjustment and must see
//     the sigma that will actually be applied.
//
// With no config present the tuner is nil and step 1 performs no arithmetic at
// all, which is what keeps the 256px goldens byte-identical.
func (g *Generator) watercolorParams(zoom int) (watercolor.Params, int) {
	params := watercolor.DefaultParams(g.tileSize, g.seed, g.textures)
	g.tuner.Apply(&params)
	params.ApplyScale(watercolor.ScaleForTileSize(g.tileSize))

	params.BlurSigma = watercolor.ZoomAdjustedBlurSigma(params.BlurSigma, zoom)
	params.AntialiasSigma = watercolor.ZoomAdjustedBlurSigma(params.AntialiasSigma, zoom)

	padPx := watercolor.RequiredPaddingPx(params)
	if padPx > g.tileSize {
		padPx = g.tileSize
	}

	return params, padPx
}

// CalculateFetchBounds returns the bounding box needed to fetch data for a tile.
// This includes padding for metatile rendering to avoid edge artifacts.
func (g *Generator) CalculateFetchBounds(coords tile.Coords) types.BoundingBox {
	_, padPx := g.watercolorParams(int(coords.Z))

	tileCoord := types.TileCoordinate{
		Zoom: int(coords.Z),
		X:    int(coords.X),
		Y:    int(coords.Y),
	}

	dataBounds := types.TileToBounds(tileCoord)
	if padPx > 0 {
		padFrac := float64(padPx) / float64(g.tileSize)
		dataBounds = dataBounds.ExpandByFraction(padFrac)
	}

	return dataBounds
}

// TileSize returns the configured tile size for this generator.
func (g *Generator) TileSize() int {
	return g.tileSize
}

// TileExists reports whether this generator's output backend already holds the
// tile, i.e. whether Generate would skip it.
//
// Exported so a caller can avoid work it would otherwise do *before* reaching
// Generate — band fetching in particular, which would otherwise query Overpass
// for a block whose tiles are all already rendered. It answers on exactly the
// same terms as the internal check, including erring towards "does not exist"
// whenever it cannot tell, so acting on it can never skip a tile that Generate
// would have rendered.
func (g *Generator) TileExists(coords tile.Coords, filenameSuffix string) bool {
	suffix := strings.TrimSpace(filenameSuffix)
	finalPath, _ := g.TilePath(coords, suffix)
	return g.tileExists(coords, finalPath, suffix)
}

// BandFetchBounds returns the bounding box that covers every given tile's own
// fetch bounds.
//
// Deliberately the union of CalculateFetchBounds and not the ancestor tile
// expanded by the same fraction: ExpandByFraction pads by a fraction of the
// box's own extent, so an ancestor's padding is several times too wide, and in
// latitude Mercator makes it wrong rather than merely generous. The union keeps
// CalculateFetchBounds the single expression for how much padding a tile needs,
// which is the same reason renderLayersWithData calls it instead of recomputing.
//
// Because every member's fetch box is inside the result, and Overpass returns
// unclipped geometry for anything intersecting the query box, the band response
// is a superset of every member's per-tile response.
func (g *Generator) BandFetchBounds(coords []tile.Coords) (types.BoundingBox, error) {
	if len(coords) == 0 {
		return types.BoundingBox{}, fmt.Errorf("cannot compute band bounds for an empty tile set")
	}

	bounds := g.CalculateFetchBounds(coords[0])
	for _, c := range coords[1:] {
		bounds = bounds.Union(g.CalculateFetchBounds(c))
	}
	return bounds, nil
}

// SliceForTile narrows a band's data down to what a single tile would have
// fetched on its own. band is never modified.
//
// This is not an optimisation, it is what makes band fetching
// behaviour-preserving; see types.FeatureCollection.FilterByBounds for why
// handing a tile the whole band would change more than performance.
//
// The slices share the band's underlying geometry — orb geometries are values
// behind an interface — so this costs only the Feature headers, not a copy of
// every coordinate.
func (g *Generator) SliceForTile(band *types.TileData, coords tile.Coords) *types.TileData {
	if band == nil {
		return nil
	}

	bounds := g.CalculateFetchBounds(coords)
	return &types.TileData{
		Coordinate: types.TileCoordinate{
			Zoom: int(coords.Z),
			X:    int(coords.X),
			Y:    int(coords.Y),
		},
		Bounds:   bounds,
		Features: band.Features.FilterByBounds(bounds),
		// Provenance is a property of the response, and every tile in the band
		// came out of the same one, so all three carry over verbatim. Dropping
		// them here would leave banded runs writing unstamped tiles — invisible
		// until a later refresh had to re-render them for want of a stamp.
		FetchedAt:     band.FetchedAt,
		DataTimestamp: band.DataTimestamp,
		Source:        band.Source,
	}
}

// GenerateWithPrefetched renders a tile from data already in hand.
//
// A thin delegate, so internal/worker can hand a task its data without
// importing pipeline.DebugContext.
func (g *Generator) GenerateWithPrefetched(
	ctx context.Context,
	coords tile.Coords,
	force bool,
	filenameSuffix string,
	data *types.TileData,
) (string, string, error) {
	return g.GenerateWithData(ctx, coords, force, filenameSuffix, nil, data)
}

// Format returns the image format this generator writes.
func (g *Generator) Format() tileformat.Format {
	return g.enc.Format()
}

// TilePath returns where this generator writes the given tile, and the
// directory that has to exist for it. suffix is "" or "@2x".
//
// Exported because the tile server needs to name the very file the generator is
// about to write. Having both derive the name from one function is what keeps a
// format or layout change from producing a file the reader never looks for.
func (g *Generator) TilePath(coords tile.Coords, suffix string) (finalPath, tileDir string) {
	ext := g.enc.Format().Ext()

	if g.options.FolderStructure == "nested" {
		// Nested structure: {z}/{x}/{y}.{ext}
		tileDir = filepath.Join(g.outputDir,
			fmt.Sprintf("%d", coords.Z),
			fmt.Sprintf("%d", coords.X))
		return filepath.Join(tileDir, fmt.Sprintf("%d%s.%s", coords.Y, suffix, ext)), tileDir
	}

	// Flat structure (default): z{z}_x{x}_y{y}{suffix}.{ext}
	return filepath.Join(g.outputDir, coords.FileName(suffix, ext)), g.outputDir
}

// renderLayersWithData handles setup, data fetching (if needed), and rendering of all map layers.
// If prefetchedData is provided, it will be used instead of fetching from the datasource.
func (g *Generator) renderLayersWithData(
	ctx context.Context,
	coords tile.Coords,
	prefetchedData *types.TileData,
) (*renderLayersResult, error) {
	params, padPx := g.watercolorParams(int(coords.Z))

	// Switch the pipeline to operate on a padded metatile
	metatileSize := g.tileSize + 2*padPx
	params.TileSize = metatileSize
	params.OffsetX = int(coords.X)*g.tileSize - padPx
	params.OffsetY = int(coords.Y)*g.tileSize - padPx

	// Generate Perlin noise once for all layers to avoid redundant allocations
	params.PerlinNoise = mask.GeneratePerlinNoiseWithOffset(
		params.TileSize, params.TileSize,
		params.NoiseScale, params.Seed,
		params.OffsetX, params.OffsetY,
	)

	tileCoord := types.TileCoordinate{
		Zoom: int(coords.Z),
		X:    int(coords.X),
		Y:    int(coords.Y),
	}

	// Deliberately reuses CalculateFetchBounds rather than recomputing the
	// expansion: the fetched data must cover exactly this metatile, so the two
	// need to be the same expression, not two copies of it.
	dataBounds := g.CalculateFetchBounds(coords)

	// Use prefetched data if available, otherwise fetch from datasource
	var data *types.TileData
	var err error
	switch {
	case prefetchedData != nil:
		g.log().Info("Using pre-fetched tile data", "coords", coords.String())
		data = prefetchedData
	case g.options.NaturalEarth.CoversZoom(int(coords.Z)):
		// Below z6 every feature comes from Natural Earth, so there is nothing
		// to ask Overpass for. Skipping the fetch here rather than inside the
		// renderer is what makes the low tier cheap *and* offline: one z2 query
		// would ask a regional instance for a quarter of the planet. Doing it in
		// the generator covers `generate`, `generate --bbox`, banded runs and
		// `serve`'s on-demand path at once, because they all pass through here.
		g.log().Info("Skipping tile data fetch: zoom is served from Natural Earth",
			"coords", coords.String())
		// No Features and no FetchedAt: nothing was fetched, and stamping a
		// time here would claim otherwise. The renderer takes every low-zoom
		// layer from the shapefiles and never looks at Features.
		data = &types.TileData{
			Coordinate: tileCoord,
			Bounds:     dataBounds,
			Source:     "natural-earth",
		}
	default:
		g.log().Info("Fetching tile data", "coords", coords.String(), "padPx", padPx)
		if dsb, ok := g.ds.(dataSourceWithBounds); ok {
			data, err = dsb.FetchTileDataWithBounds(ctx, tileCoord, dataBounds)
		} else {
			data, err = g.ds.FetchTileData(ctx, tileCoord)
		}
		if err != nil {
			return nil, fmt.Errorf("failed to fetch tile data: %w", err)
		}
	}

	// Create temp directory for rendered layer PNGs
	layerDir, err := os.MkdirTemp("", "watercolormap-layers-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp layer dir: %w", err)
	}

	layerDirReturn := ""
	if g.keepLayers {
		layerDirReturn = layerDir
		g.log().Info("Keeping rendered layer PNGs", "coords", coords.String(), "dir", layerDir)
	}

	// Render all layers via Mapnik
	g.log().Info("Rendering layers", "coords", coords.String())
	mpRenderer, err := renderer.NewMultiPassRenderer(g.stylesDir, layerDir, g.tileSize, padPx)
	if err != nil {
		return nil, fmt.Errorf("failed to create multipass renderer: %w", err)
	}
	mpRenderer.SetOceanConfig(g.options.Ocean)
	mpRenderer.SetNaturalEarthConfig(g.options.NaturalEarth)
	defer mpRenderer.Close() // nolint:errcheck

	renderResult, err := mpRenderer.RenderTile(coords, data)
	if err != nil {
		return nil, fmt.Errorf("failed to render layers: %w", err)
	}

	// Read rendered PNG files into memory
	rawLayers := make(map[geojson.LayerType]image.Image)
	for layer, res := range renderResult.Layers {
		if res == nil {
			continue
		}
		// Check the error before the empty path, not after. A layer that fails
		// to render reports both — an error and no output — so testing the path
		// first swallowed every render failure as "layer absent". For ocean that
		// silently reinstates the exact bug 4.10 is about: a missing shapefile
		// would produce a tan sea and no complaint.
		if res.Error != nil {
			return nil, fmt.Errorf("failed to render layer %s: %w", layer, res.Error)
		}
		if res.OutputPath == "" {
			g.log().Debug("Skipping empty layer", "layer", layer, "coords", coords.String())
			continue
		}

		g.log().Debug("Painting layer", "layer", layer, "coords", coords.String())
		img, err := readPNG(res.OutputPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read layer %s: %w", layer, err)
		}

		rawLayers[layer] = img
	}

	foldOceanIntoWater(rawLayers)

	return &renderLayersResult{
		rawLayers:      rawLayers,
		dataTimestamp:  data.DataTimestamp,
		dataSource:     data.Source,
		params:         params,
		padPx:          padPx,
		layerDir:       layerDir,
		layerDirReturn: layerDirReturn,
	}, nil
}

// foldOceanIntoWater merges the ocean pass into the water layer and drops the
// ocean key, so that everything downstream sees a single water layer.
//
// This is what actually fixes 4.10, and it fixes both symptoms at once. Land is
// not rendered from land polygons — it is painted by inverting the union of
// everything that cuts into it (see buildMasks). Ocean therefore has to arrive
// as water: once it does, open sea is blue and the land inversion excludes it,
// so coastal tiles stop coming out backwards. Nothing else in the pipeline needs
// to know ocean exists.
//
// Both passes render the same #0000FF over transparency at the same metatile
// bounds and size, so drawing one over the other is exactly their union.
func foldOceanIntoWater(rawLayers map[geojson.LayerType]image.Image) {
	ocean := rawLayers[geojson.LayerOcean]
	if ocean == nil {
		return
	}
	delete(rawLayers, geojson.LayerOcean)

	water := rawLayers[geojson.LayerWater]
	if water == nil {
		rawLayers[geojson.LayerWater] = ocean
		return
	}

	bounds := ocean.Bounds()
	if water.Bounds() != bounds {
		// Cannot happen for a single tile — both come from the same metatile
		// render — but silently unioning mismatched images would shift the
		// coastline, so keep the ocean and say nothing false.
		rawLayers[geojson.LayerWater] = ocean
		return
	}

	merged := image.NewNRGBA(bounds)
	draw.Draw(merged, bounds, ocean, bounds.Min, draw.Src)
	draw.Draw(merged, bounds, water, bounds.Min, draw.Over)
	rawLayers[geojson.LayerWater] = merged
}

// renderLayersResult holds the output from the rendering phase.
type renderLayersResult struct {
	rawLayers map[geojson.LayerType]image.Image
	// dataTimestamp and dataSource carry the fetched data's provenance through
	// to the stamp written after the tile lands. They are copied out of the
	// TileData here because that value does not survive this function.
	dataTimestamp  time.Time
	dataSource     string
	layerDir       string
	layerDirReturn string
	params         watercolor.Params
	padPx          int
}

// maskSet holds all extracted alpha masks for a tile.
type maskSet struct {
	waterMask     *image.Gray
	riversMask    *image.Gray
	roadsMask     *image.Gray
	railroadsMask *image.Gray
	highwaysAlpha *image.Gray
	nonLandUnion  *image.Gray // Union of water + rivers + roads + railroads (used as base for land inversion)
}

// buildMasks extracts alpha masks from rendered layers and creates the non-land union.
// The actual blur/noise/threshold processing is now handled by the watercolor processor.
func buildMasks(
	rawLayers map[geojson.LayerType]image.Image,
	params watercolor.Params,
	dc *DebugContext,
) *maskSet {
	// Extract image references
	waterImg := rawLayers[geojson.LayerWater]
	riversImg := rawLayers[geojson.LayerRivers]
	roadsImg := rawLayers[geojson.LayerRoads]
	railroadsImg := rawLayers[geojson.LayerRailroads]
	highwaysImg := rawLayers[geojson.LayerHighways]
	urbanImg := rawLayers[geojson.LayerUrban]
	civicImg := rawLayers[geojson.LayerCivic]
	buildingsImg := rawLayers[geojson.LayerBuildings]

	baseBounds := image.Rect(0, 0, params.TileSize, params.TileSize)

	// Extract alpha masks from each layer
	waterMask := mask.NewEmptyMask(baseBounds)
	riversMask := mask.NewEmptyMask(baseBounds)
	roadsMask := mask.NewEmptyMask(baseBounds)
	railroadsMask := mask.NewEmptyMask(baseBounds)
	highwaysAlpha := mask.NewEmptyMask(baseBounds)
	urbanMask := mask.NewEmptyMask(baseBounds)
	civicMask := mask.NewEmptyMask(baseBounds)
	buildingsMask := mask.NewEmptyMask(baseBounds)

	if waterImg != nil {
		waterMask = mask.ExtractAlphaMask(waterImg)
	}
	if riversImg != nil {
		riversMask = mask.ExtractAlphaMask(riversImg)
	}
	if roadsImg != nil {
		roadsMask = mask.ExtractAlphaMask(roadsImg)
	}
	if railroadsImg != nil {
		railroadsMask = mask.ExtractAlphaMask(railroadsImg)
	}
	if highwaysImg != nil {
		highwaysAlpha = mask.ExtractAlphaMask(highwaysImg)
	}
	if urbanImg != nil {
		urbanMask = mask.ExtractAlphaMask(urbanImg)
	}
	if civicImg != nil {
		civicMask = mask.ExtractAlphaMask(civicImg)
	}
	if buildingsImg != nil {
		buildingsMask = mask.ExtractAlphaMask(buildingsImg)
	}

	// Capture alpha masks (all grayscale)
	dc.Capture("01_water_alpha", "Alpha mask from water layer", waterMask, 1)
	dc.Capture("02_rivers_alpha", "Alpha mask from rivers layer", riversMask, 2)
	dc.Capture("03_roads_alpha", "Alpha mask from roads layer", roadsMask, 3)
	dc.Capture("03_railroads_alpha", "Alpha mask from railroads layer", railroadsMask, 3)
	dc.Capture("03_highways_alpha", "Alpha mask from highways layer", highwaysAlpha, 3)
	dc.Capture("04_urban_alpha", "Alpha mask from urban layer", urbanMask, 4)
	dc.Capture("04_civic_alpha", "Alpha mask from civic layer", civicMask, 4)
	dc.Capture("04_buildings_alpha", "Alpha mask from buildings layer", buildingsMask, 4)

	// Combine water, rivers, roads, railroads, highways, urban, civic, and buildings into non-land union mask
	// This is the base mask for land - will be inverted during processing (InvertMask=true)
	// All these layers are subtracted from land
	nonLandUnion := mask.MaxMasks(waterMask, riversMask, roadsMask, railroadsMask, highwaysAlpha, urbanMask, civicMask, buildingsMask)
	dc.Capture("05_nonland_union", "Union of water + rivers + roads + railroads + highways + urban + civic + buildings masks", nonLandUnion, 5)

	return &maskSet{
		waterMask:     waterMask,
		riversMask:    riversMask,
		roadsMask:     roadsMask,
		railroadsMask: railroadsMask,
		highwaysAlpha: highwaysAlpha,
		nonLandUnion:  nonLandUnion,
	}
}

// paintAllLayers applies watercolor effects to all layers.
func paintAllLayers(
	rawLayers map[geojson.LayerType]image.Image,
	masks *maskSet,
	params watercolor.Params,
	textures map[geojson.LayerType]image.Image,
	dc *DebugContext,
) (map[geojson.LayerType]image.Image, error) {
	painted := make(map[geojson.LayerType]image.Image)

	if err := paintWaterLayers(painted, rawLayers, params, dc); err != nil {
		return nil, err
	}

	landMask, err := paintLandLayer(painted, masks, params, textures, dc)
	if err != nil {
		return nil, err
	}

	if err := paintLineLayers(painted, rawLayers, params, dc); err != nil {
		return nil, err
	}

	if err := paintAreaLayers(painted, rawLayers, masks, landMask, params, dc); err != nil {
		return nil, err
	}

	return painted, nil
}

// paintDirectLayer paints a layer straight from its own alpha, if it was rendered.
func paintDirectLayer(
	painted map[geojson.LayerType]image.Image,
	rawLayers map[geojson.LayerType]image.Image,
	layer geojson.LayerType,
	params watercolor.Params,
	dc *DebugContext,
	stage string,
	description string,
	zorder int,
) error {
	img := rawLayers[layer]
	if img == nil {
		return nil
	}
	result, err := watercolor.PaintLayer(img, layer, params)
	if err != nil {
		return fmt.Errorf("failed to paint %s: %w", layer, err)
	}
	painted[layer] = result
	dc.Capture(stage, description, result, zorder)
	return nil
}

// paintWaterLayers paints water and rivers from their own alpha masks.
func paintWaterLayers(
	painted map[geojson.LayerType]image.Image,
	rawLayers map[geojson.LayerType]image.Image,
	params watercolor.Params,
	dc *DebugContext,
) error {
	// Paint water from its own alpha mask (not the combined non-land mask)
	if err := paintDirectLayer(painted, rawLayers, geojson.LayerWater, params, dc,
		"12_painted_water", "Watercolor-painted water layer", 12); err != nil {
		return err
	}

	// Paint rivers from their own alpha mask
	return paintDirectLayer(painted, rawLayers, geojson.LayerRivers, params, dc,
		"13_painted_rivers", "Watercolor-painted rivers layer", 18)
}

// paintLandLayer paints land from the non-land union mask and returns the land mask.
func paintLandLayer(
	painted map[geojson.LayerType]image.Image,
	masks *maskSet,
	params watercolor.Params,
	textures map[geojson.LayerType]image.Image,
	dc *DebugContext,
) (*image.Gray, error) {
	// Paint land from non-land union mask (will be inverted during processing due to InvertMask=true)
	// The watercolor processor handles blur/noise/threshold/invert/edges uniformly
	paintedLand, landMask, err := watercolor.PaintLayerFromMaskWithMask(masks.nonLandUnion, geojson.LayerLand, params)
	if err != nil {
		return nil, fmt.Errorf("failed to paint land: %w", err)
	}
	painted[geojson.LayerLand] = paintedLand
	dc.Capture("10_painted_land", "Watercolor-painted land layer", paintedLand, 10)

	// Create composite of land on white canvas for debugging
	whiteCanvas := texture.TileTextureScaled(textures[geojson.LayerPaper], params.TileSize, params.OffsetX, params.OffsetY, params.Scale)
	landOnCanvas, err := composite.CompositeLayersOverBase(
		whiteCanvas,
		map[geojson.LayerType]image.Image{geojson.LayerLand: paintedLand},
		[]geojson.LayerType{geojson.LayerLand},
		params.TileSize,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to composite land on canvas: %w", err)
	}
	dc.Capture("11_painted_land_on_canvas", "Land layer composited on white canvas", landOnCanvas, 11)

	return landMask, nil
}

// paintLineLayers paints roads, railroads and highways from their own alpha masks.
func paintLineLayers(
	painted map[geojson.LayerType]image.Image,
	rawLayers map[geojson.LayerType]image.Image,
	params watercolor.Params,
	dc *DebugContext,
) error {
	// Paint roads from their own alpha mask
	// NOTE: Roads are also part of the derived non-land union mask, so they carve holes
	// into land. Painting roads fills those holes with the intended style (instead of
	// leaving paper showing through).
	if err := paintDirectLayer(painted, rawLayers, geojson.LayerRoads, params, dc,
		"15_painted_roads", "Watercolor-painted roads layer", 15); err != nil {
		return err
	}

	// Paint railroads from their own alpha mask
	if err := paintDirectLayer(painted, rawLayers, geojson.LayerRailroads, params, dc,
		"16_painted_railroads", "Watercolor-painted railroads layer", 16); err != nil {
		return err
	}

	// Paint highways/major roads on top
	return paintDirectLayer(painted, rawLayers, geojson.LayerHighways, params, dc,
		"19_painted_highways", "Watercolor-painted highways layer", 19)
}

// paintAreaLayers paints urban, civic, parks and buildings, subtracting line layers
// from the developed areas.
func paintAreaLayers(
	painted map[geojson.LayerType]image.Image,
	rawLayers map[geojson.LayerType]image.Image,
	masks *maskSet,
	landMask *image.Gray,
	params watercolor.Params,
	dc *DebugContext,
) error {
	// Create roads+railroads+highways union mask for subtracting from urban/civic areas
	roadsRailroadsHighwaysUnion := mask.MaxMasks(masks.roadsMask, masks.railroadsMask, masks.highwaysAlpha)
	dc.Capture("14a_roads_railroads_highways_union", "Union of roads + railroads + highways for area subtraction", roadsRailroadsHighwaysUnion, 14)

	// Paint urban with roads/railroads/highways subtracted (similar to land subtraction)
	if err := paintAreaMinusRoads(painted, rawLayers, geojson.LayerUrban, roadsRailroadsHighwaysUnion, params, dc,
		"14b_urban_minus_roads", "Urban areas with roads/railroads/highways subtracted", 14,
		"14_painted_urban", "Watercolor-painted urban layer", 14); err != nil {
		return err
	}

	// Paint civic with roads/railroads/highways subtracted (similar to land subtraction)
	if err := paintAreaMinusRoads(painted, rawLayers, geojson.LayerCivic, roadsRailroadsHighwaysUnion, params, dc,
		"14c_civic_minus_roads", "Civic areas with roads/railroads/highways subtracted", 14,
		"15_painted_civic", "Watercolor-painted civic layer", 15); err != nil {
		return err
	}

	if err := paintParksLayer(painted, rawLayers, landMask, params, dc); err != nil {
		return err
	}

	// Buildings painted directly (they are subtracted from land, rendered on top)
	return paintDirectLayer(painted, rawLayers, geojson.LayerBuildings, params, dc,
		"18_painted_buildings", "Watercolor-painted buildings layer", 18)
}

// paintAreaMinusRoads paints an area layer with the road union subtracted from it.
func paintAreaMinusRoads(
	painted map[geojson.LayerType]image.Image,
	rawLayers map[geojson.LayerType]image.Image,
	layer geojson.LayerType,
	roadsUnion *image.Gray,
	params watercolor.Params,
	dc *DebugContext,
	minusStage string,
	minusDescription string,
	minusZOrder int,
	paintedStage string,
	paintedDescription string,
	paintedZOrder int,
) error {
	img := rawLayers[layer]
	if img == nil {
		return nil
	}
	areaMask := mask.ExtractAlphaMask(img)
	// Subtract roads, railroads, and highways from the area
	areaMinusRoads := mask.SubtractMask(areaMask, roadsUnion)
	dc.Capture(minusStage, minusDescription, areaMinusRoads, minusZOrder)
	result, err := watercolor.PaintLayerFromMask(areaMinusRoads, layer, params)
	if err != nil {
		return fmt.Errorf("failed to paint %s: %w", layer, err)
	}
	painted[layer] = result
	dc.Capture(paintedStage, paintedDescription, result, paintedZOrder)
	return nil
}

// paintParksLayer constrains parks to land+urban+civic and paints them.
func paintParksLayer(
	painted map[geojson.LayerType]image.Image,
	rawLayers map[geojson.LayerType]image.Image,
	landMask *image.Gray,
	params watercolor.Params,
	dc *DebugContext,
) error {
	// Constrain parks to land+urban+civic combined (parks render on top of developed areas)
	parksImg := rawLayers[geojson.LayerParks]
	if parksImg == nil {
		return nil
	}

	// Parks can appear on land OR on urban/civic areas
	urbanImg := rawLayers[geojson.LayerUrban]
	civicImg := rawLayers[geojson.LayerCivic]
	urbanMask := mask.NewEmptyMask(landMask.Bounds())
	civicMask := mask.NewEmptyMask(landMask.Bounds())
	if urbanImg != nil {
		urbanMask = mask.ExtractAlphaMask(urbanImg)
	}
	if civicImg != nil {
		civicMask = mask.ExtractAlphaMask(civicImg)
	}
	landPlusUrbanCivic := mask.MaxMasks(landMask, urbanMask, civicMask)
	parksMask := mask.MinMask(mask.ExtractAlphaMask(parksImg), landPlusUrbanCivic)
	dc.Capture("16_parks_constrained", "Parks constrained to land+urban+civic", parksMask, 16)
	parksPainted, err := watercolor.PaintLayerFromMask(parksMask, geojson.LayerParks, params)
	if err != nil {
		return fmt.Errorf("failed to paint parks: %w", err)
	}
	painted[geojson.LayerParks] = parksPainted
	dc.Capture("17_painted_parks", "Watercolor-painted parks layer", parksPainted, 17)
	return nil
}

// compositeAndWrite composites all painted layers, crops to tile size, and writes the final PNG.
func (g *Generator) compositeAndWrite(
	painted map[geojson.LayerType]image.Image,
	coords tile.Coords,
	finalPath string,
	suffix string,
	res *renderLayersResult,
	dc *DebugContext,
) (string, string, error) {
	params := res.params
	padPx := res.padPx
	layerDirReturn := res.layerDirReturn

	// Paper base: fill the entire tile with a white texture so road cutouts show through
	base := texture.TileTextureScaled(g.textures[geojson.LayerPaper], params.TileSize, params.OffsetX, params.OffsetY, params.Scale)

	composited, err := composite.CompositeLayersOverBase(
		base,
		painted,
		composite.DefaultOrder,
		params.TileSize,
	)
	if err != nil {
		return "", "", fmt.Errorf("failed to composite layers: %w", err)
	}
	dc.Capture("20_combined_metatile", "Composited layers (before crop)", composited, 20)

	// Crop back to the requested tile size
	final := composited
	if padPx > 0 {
		cropRect := image.Rect(padPx, padPx, padPx+g.tileSize, padPx+g.tileSize)
		final = cropNRGBA(composited, cropRect)
	}
	dc.Capture("21_combined_final", "Final tile (after crop)", final, 21)

	// Use TileWriter if provided, otherwise write to disk
	if g.options.TileWriter != nil {
		// Encode to bytes buffer
		var buf bytes.Buffer
		if err := g.enc.Encode(&buf, final); err != nil {
			return "", "", fmt.Errorf("failed to encode tile: %w", err)
		}

		// Write through TileWriter interface
		g.log().Info("Writing tile via TileWriter", "coords", coords.String())
		if err := g.options.TileWriter.WriteTile(int(coords.Z), int(coords.X), int(coords.Y), buf.Bytes()); err != nil {
			return "", "", fmt.Errorf("failed to write tile: %w", err)
		}

		// After the write, on both branches: a stamp is a claim that the tile
		// exists with this provenance, so it must never precede the tile.
		g.putStamp(coords, suffix, res)

		return finalPath, layerDirReturn, nil
	}

	// Traditional file output
	g.log().Info("Writing final tile", "coords", coords.String(), "path", finalPath)
	if err := encodeTileAtomic(g.enc, finalPath, final); err != nil {
		return "", "", err
	}

	g.putStamp(coords, suffix, res)

	return finalPath, layerDirReturn, nil
}

// encodeTileAtomic encodes img to a temporary file next to path and renames it
// into place.
//
// Writing straight to the final path meant an interrupted or timed-out encode
// left a truncated image behind, which the tile cache then served as a
// permanently broken tile. The rename is atomic within the directory, so a tile
// file either does not exist or is complete.
func encodeTileAtomic(enc tileformat.Encoder, path string, img image.Image) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("failed to create tile file: %w", err)
	}
	tmpName := tmp.Name()

	// Best effort cleanup of the failure paths; on success the file is already
	// closed and renamed away, so both calls are no-ops.
	defer func() {
		tmp.Close()        // nolint:errcheck
		os.Remove(tmpName) // nolint:errcheck
	}()

	if err := enc.Encode(tmp, img); err != nil {
		return fmt.Errorf("failed to encode final tile: %w", err)
	}
	// CreateTemp uses 0600; tiles are world-readable static files.
	if err := tmp.Chmod(0o644); err != nil {
		return fmt.Errorf("failed to set tile file mode: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close tile file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("failed to publish tile file: %w", err)
	}
	return nil
}
