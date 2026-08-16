package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/cwbudde/watercolormap/internal/checkpoint"
	"github.com/cwbudde/watercolormap/internal/mbtiles"
	"github.com/cwbudde/watercolormap/internal/pipeline"
	"github.com/cwbudde/watercolormap/internal/renderer"
	"github.com/cwbudde/watercolormap/internal/tile"
	"github.com/cwbudde/watercolormap/internal/tileformat"
	"github.com/cwbudde/watercolormap/internal/tilejson"
	"github.com/cwbudde/watercolormap/internal/tilestamp"
	"github.com/cwbudde/watercolormap/internal/watercolor"
	"github.com/cwbudde/watercolormap/internal/worker"
)

// checkpointAuto is the sentinel `--checkpoint` takes when given without a
// value: put the checkpoint next to the run's output under its default name —
// the output directory for a folder run, the MBTiles file's directory for an
// MBTiles one, which is the only one of the two an MBTiles run creates.
const checkpointAuto = "auto"

var generateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Generate map tiles",
	Long:  `Generate watercolor-styled map tiles for specified coordinates and zoom levels.`,
	RunE:  runGenerate,
}

func init() {
	rootCmd.AddCommand(generateCmd)

	// Single tile flags
	generateCmd.Flags().IntP("zoom", "z", 13, "Zoom level (for single tile mode)")
	generateCmd.Flags().IntP("x", "x", 0, "X tile coordinate (for single tile mode)")
	generateCmd.Flags().IntP("y", "y", 0, "Y tile coordinate (for single tile mode)")

	// Batch generation flags
	generateCmd.Flags().String("bbox", "", "Bounding box: minLon,minLat,maxLon,maxLat (e.g., \"9.7,52.3,9.9,52.4\")")
	// The unset sentinel is -1, not 0. Zoom 0 is a real, renderable zoom — it is
	// the single world tile, and the whole low-zoom tier starts there — so it
	// cannot double as "flag not supplied".
	generateCmd.Flags().Int("zoom-min", unsetZoom, "Minimum zoom level for batch generation")
	generateCmd.Flags().Int("zoom-max", unsetZoom, "Maximum zoom level for batch generation")
	generateCmd.Flags().IntP("workers", "w", 0, "Number of parallel workers (default: number of CPUs)")
	generateCmd.Flags().Int("paint-workers", 0,
		"Layers painted at the same time within one tile "+
			"(0 = auto: the CPU budget divided by --workers, so a saturated batch run paints serially)")
	generateCmd.Flags().Bool("progress", true, "Show progress bar during batch generation")
	generateCmd.Flags().Bool("allow-failures", false, "Continue generation even if some tiles fail (useful for CI/CD with API rate limits)")

	// Checkpointing. Off unless asked for: it writes a file into the output and
	// changes what a rerun does, which an operator opts into.
	generateCmd.Flags().String("checkpoint", "",
		"Record batch progress in this file so an interrupted run resumes where it stopped; "+
			"give the flag without a value to use "+checkpoint.FileName+" next to the output "+
			"(<output-dir> for folder runs, the --output-file's directory for MBTiles)")
	generateCmd.Flags().Lookup("checkpoint").NoOptDefVal = checkpointAuto
	generateCmd.Flags().Int("checkpoint-interval", checkpoint.DefaultInterval, "Write the checkpoint every N completed tiles")

	// Common flags
	generateCmd.Flags().Bool("force", false, "Re-render tiles that already exist, for folder and MBTiles output alike (without it, existing tiles are skipped so a run can resume)")
	generateCmd.Flags().Int("tile-size", 256, "Tile size in pixels (typically 256 or 512 for Hi-DPI)")
	generateCmd.Flags().Bool("hidpi", false, "Also generate a 2x (@2x) tile alongside the base tile (single-tile mode only; `serve` produces @2x on demand)")
	generateCmd.Flags().String("image-format", "png", "Tile image encoding: png or webp (webp is lossless, ~1.2x smaller)")
	generateCmd.Flags().String("png-compression", "default", "PNG compression (default, speed, best, none); ignored for --image-format=webp")
	generateCmd.Flags().Int("webp-effort", tileformat.DefaultWebPEffort, "WebP compression effort 0-6 (0 = fastest); ignored for --image-format=png")
	generateCmd.Flags().Int64("seed", 1337, "Deterministic seed for noise/texture alignment")
	generateCmd.Flags().Bool("keep-layers", false, "Keep intermediate rendered layer PNGs for debugging")

	// Output format flags
	generateCmd.Flags().String("format", "folder", "Output container: folder or mbtiles (see --image-format for the tile image encoding)")
	generateCmd.Flags().String("output-file", "", "Output file path for MBTiles format (e.g., tiles.mbtiles)")
	generateCmd.Flags().String("folder-structure", "flat", "Folder structure for folder format: flat (z{z}_x{x}_y{y}.png) or nested ({z}/{x}/{y}.png)")

	// Freshness. All three are opt-in: with none of them set, an existing tile
	// is skipped exactly as it always was. They read the stamps written
	// alongside the tiles, so they can only re-render what a stamped run
	// produced — an unstamped tile has no recorded data version and is
	// therefore always re-rendered, never wrongly skipped.
	generateCmd.Flags().String("stale-data-before", "",
		"Re-render tiles whose source OSM data (osm_base_ts) is older than this RFC3339 timestamp")
	generateCmd.Flags().String("stale-rendered-before", "",
		"Re-render tiles written before this RFC3339 timestamp, whatever their data")
	generateCmd.Flags().Bool("stale-renderer-rev", false,
		"Re-render tiles stamped by a different build of this binary")

	// Overpass flags. Only used by the single-server path; when overpass.servers
	// is configured, each server carries its own worker count.
	generateCmd.Flags().Int("overpass-workers", 4, "Number of parallel Overpass API requests (2-4 recommended for public API)")

	// Band fetching. Off by default: it changes the shape and size of the
	// queries sent upstream, which an operator running against a shared or
	// rate-limited Overpass has to opt into. See docs/data-scaling-strategy.md.
	generateCmd.Flags().Bool("band-fetch", false, "Fetch Overpass data once per block of tiles instead of once per tile")
	generateCmd.Flags().Int("band-level", 2, "Band size as zoom levels above the tile: 1 = 2x2, 2 = 4x4, 3 = 8x8")
	generateCmd.Flags().Int("band-min-zoom", 10, "Do not band tiles below this zoom (a single low-zoom tile is already huge)")
	generateCmd.Flags().Int("band-fetch-ahead", 1, "How many bands may be fetched ahead of rendering")
	generateCmd.Flags().Int("band-timeout", 180, "Server-side Overpass timeout in seconds for a band query")

	bindFlags := []struct {
		key  string
		flag string
	}{
		{"generate.zoom", "zoom"},
		{"generate.x", "x"},
		{"generate.y", "y"},
		{"generate.bbox", "bbox"},
		{"generate.zoom_min", "zoom-min"},
		{"generate.zoom_max", "zoom-max"},
		{"generate.workers", "workers"},
		{"generate.paint_workers", "paint-workers"},
		{"generate.progress", "progress"},
		{"generate.allow_failures", "allow-failures"},
		{"generate.checkpoint", "checkpoint"},
		{"generate.checkpoint_interval", "checkpoint-interval"},
		{"generate.force", "force"},
		{"generate.tile_size", "tile-size"},
		{"generate.hidpi", "hidpi"},
		{"generate.image_format", "image-format"},
		{"generate.png_compression", "png-compression"},
		{"generate.webp_effort", "webp-effort"},
		{"generate.seed", "seed"},
		{"generate.keep_layers", "keep-layers"},
		{"generate.format", "format"},
		{"generate.output_file", "output-file"},
		{"generate.folder_structure", "folder-structure"},
		{staleDataBeforeKey, "stale-data-before"},
		{staleRenderedBeforeKey, "stale-rendered-before"},
		{staleRendererRevKey, "stale-renderer-rev"},
		{"generate.overpass_workers", "overpass-workers"},
		{"generate.band_fetch", "band-fetch"},
		{"generate.band_level", "band-level"},
		{"generate.band_min_zoom", "band-min-zoom"},
		{"generate.band_fetch_ahead", "band-fetch-ahead"},
		{"generate.band_timeout", "band-timeout"},
	}

	for _, bf := range bindFlags {
		if err := viper.BindPFlag(bf.key, generateCmd.Flags().Lookup(bf.flag)); err != nil {
			panic(fmt.Sprintf("failed to bind flag %s: %v", bf.flag, err))
		}
	}
}

func runGenerate(cmd *cobra.Command, args []string) error {
	// Read all config values
	zoom := viper.GetInt("generate.zoom")
	x := viper.GetInt("generate.x")
	y := viper.GetInt("generate.y")
	bbox := viper.GetString("generate.bbox")
	zoomMin := viper.GetInt("generate.zoom_min")
	zoomMax := viper.GetInt("generate.zoom_max")
	workers := viper.GetInt("generate.workers")
	showProgress := viper.GetBool("generate.progress")
	force := viper.GetBool("generate.force")
	outputDir := viper.GetString("output-dir")
	dataSourceName := viper.GetString("data-source")
	tileSize := viper.GetInt("generate.tile_size")
	hidpi := viper.GetBool("generate.hidpi")
	pngCompression := viper.GetString("generate.png_compression")
	webpEffort := viper.GetInt("generate.webp_effort")
	seed := viper.GetInt64("generate.seed")
	keepLayers := viper.GetBool("generate.keep_layers")
	format := viper.GetString("generate.format")
	outputFile := viper.GetString("generate.output_file")
	folderStructure := viper.GetString("generate.folder_structure")

	if logger == nil {
		initLogging()
	}

	// Validate format
	if format != "folder" && format != "mbtiles" {
		return fmt.Errorf("invalid format %q: must be 'folder' or 'mbtiles'", format)
	}

	// Resolve the image format here rather than deep in the pipeline, so a typo
	// fails before anything is opened for writing.
	imageFormat, err := tileformat.Parse(viper.GetString("generate.image_format"))
	if err != nil {
		return err
	}

	// Validate folder structure
	if folderStructure != "flat" && folderStructure != "nested" {
		return fmt.Errorf("invalid folder-structure %q: must be 'flat' or 'nested'", folderStructure)
	}

	// Validate MBTiles requirements
	if format == "mbtiles" {
		if outputFile == "" {
			return fmt.Errorf("--output-file is required when using --format=mbtiles")
		}
		if bbox == "" {
			return fmt.Errorf("mbtiles format requires batch generation (use --bbox)")
		}
	}

	allowFailures := viper.GetBool("generate.allow_failures")

	bandFetch := viper.GetBool("generate.band_fetch")
	band, err := bandOptionsFromConfig()
	if err != nil {
		return err
	}

	// Determine mode: batch (bbox provided) or single tile
	if bbox != "" {
		// Erroring rather than ignoring is deliberate. Anyone who scripted
		// `--bbox --hidpi` would otherwise get a run that looks entirely
		// successful while producing half the tiles they expect, and would find
		// out only when a @2x request 404s in production.
		if hidpi {
			return fmt.Errorf("--hidpi is not supported for batch generation: " +
				"pre-rendering @2x doubles compute and quadruples storage for the whole run. " +
				"`watercolormap serve --tiles-dir` generates @2x tiles on demand instead " +
				"(note that `serve --mbtiles` does not: it answers from the file and ignores " +
				"the @2x suffix, so retina requests get base-resolution tiles); " +
				"--hidpi still works for a single tile")
		}

		return runBatchGenerate(&batchOptions{
			bboxStr:         bbox,
			outputDir:       outputDir,
			dataSourceName:  dataSourceName,
			pngCompression:  pngCompression,
			imageFormat:     imageFormat,
			webpEffort:      webpEffort,
			format:          format,
			outputFile:      outputFile,
			folderStructure: folderStructure,
			seed:            seed,
			zoomMin:         zoomMin,
			zoomMax:         zoomMax,
			workers:         workers,
			tileSize:        tileSize,
			showProgress:    showProgress,
			force:           force,
			keepLayers:      keepLayers,
			allowFailures:   allowFailures,
			bandFetch:       bandFetch,
			band:            band,
		})
	}

	return runSingleGenerate(&singleOptions{
		outputDir:       outputDir,
		dataSourceName:  dataSourceName,
		pngCompression:  pngCompression,
		folderStructure: folderStructure,
		imageFormat:     imageFormat,
		webpEffort:      webpEffort,
		seed:            seed,
		zoom:            zoom,
		x:               x,
		y:               y,
		tileSize:        tileSize,
		force:           force,
		hidpi:           hidpi,
		keepLayers:      keepLayers,
	})
}

// singleOptions collects every setting for a single-tile generation run. It
// exists to keep runSingleGenerate from taking a dozen loose parameters, and
// mirrors the shared subset of batchOptions. Fields are grouped by type to
// satisfy the fieldalignment check.
type singleOptions struct {
	outputDir       string
	dataSourceName  string
	pngCompression  string
	folderStructure string
	imageFormat     tileformat.Format
	seed            int64
	webpEffort      int
	zoom            int
	x               int
	y               int
	tileSize        int
	force           bool
	hidpi           bool
	keepLayers      bool
}

// singleGeneratorOptions assembles the pipeline options for a single-tile run,
// together with the stamp store the caller has to close. The base tile and its
// @2x sibling share one value, so the two generators cannot come to disagree
// about format, watercolor tuning or where the stamps go.
func singleGeneratorOptions(
	opts *singleOptions,
	ocean renderer.OceanConfig,
	naturalEarth renderer.NaturalEarthConfig,
) (pipeline.GeneratorOptions, *tilestamp.Store, error) {
	wcOverrides, err := loadWatercolorOverrides()
	if err != nil {
		return pipeline.GeneratorOptions{}, nil, err
	}

	freshness, err := freshnessPolicyFromConfig()
	if err != nil {
		return pipeline.GeneratorOptions{}, nil, err
	}

	// Single-tile mode always writes a folder: --format=mbtiles is refused
	// without a bbox.
	stamps, err := openStampStore("folder", opts.outputDir, "")
	if err != nil {
		return pipeline.GeneratorOptions{}, nil, fmt.Errorf("failed to open the tile stamp store: %w", err)
	}

	return pipeline.GeneratorOptions{
		PNGCompression:  opts.pngCompression,
		ImageFormat:     opts.imageFormat,
		WebPEffort:      opts.webpEffort,
		FolderStructure: opts.folderStructure,
		// One tile at a time, even with --hidpi: the base tile and its @2x
		// sibling are rendered one after the other.
		PaintWorkers: resolvePaintWorkers("generate.paint_workers", 1),
		Watercolor:   wcOverrides,
		Ocean:        ocean,
		NaturalEarth: naturalEarth,
		StampStore:   stampStoreOption(stamps),
		Freshness:    freshness,
		RendererRev:  rendererRev(),
	}, stamps, nil
}

func runSingleGenerate(opts *singleOptions) error {
	coords := tile.NewCoords(uint32(opts.zoom), uint32(opts.x), uint32(opts.y))

	logger.Info("Starting tile generation",
		"coords", coords.String(),
		"output_dir", opts.outputDir,
		"force", opts.force,
		"data_source", opts.dataSourceName,
		"tile_size", opts.tileSize,
		"hidpi", opts.hidpi,
		"png_compression", opts.pngCompression,
		"seed", opts.seed,
		"keep_layers", opts.keepLayers,
	)

	if opts.zoom < 0 || opts.x < 0 || opts.y < 0 {
		return fmt.Errorf("invalid coordinates: zoom/x/y must be non-negative")
	}

	ocean, err := oceanConfig()
	if err != nil {
		return err
	}

	naturalEarth, err := naturalEarthConfig()
	if err != nil {
		return err
	}

	ds, err := newTileDataSource(opts.dataSourceName, ocean.Enabled())
	if err != nil {
		return err
	}

	stylesDir := filepath.Join("assets", "styles")
	texturesDir := filepath.Join("assets", "textures")

	genOpts, stamps, err := singleGeneratorOptions(opts, ocean, naturalEarth)
	if err != nil {
		return err
	}
	defer closeStampStore(stamps)

	gen, err := pipeline.NewGenerator(ds, stylesDir, texturesDir, opts.outputDir, opts.tileSize, opts.seed, opts.keepLayers, logger, genOpts)
	if err != nil {
		return fmt.Errorf("failed to init generator: %w", err)
	}

	path, layersDir, err := gen.Generate(context.Background(), coords, opts.force, "")
	if err != nil {
		return fmt.Errorf("failed to generate tile: %w", err)
	}

	logFields := []interface{}{"coords", coords.String(), "path", path}
	if opts.keepLayers && layersDir != "" {
		logFields = append(logFields, "layers_dir", layersDir)
	}
	logger.Info("Tile generated", logFields...)

	if opts.hidpi {
		gen2x, err := pipeline.NewGenerator(ds, stylesDir, texturesDir, opts.outputDir, opts.tileSize*2, opts.seed, opts.keepLayers, logger, genOpts)
		if err != nil {
			return fmt.Errorf("failed to init hidpi generator: %w", err)
		}
		path2x, _, err := gen2x.Generate(context.Background(), coords, opts.force, "@2x")
		if err != nil {
			return fmt.Errorf("failed to generate hidpi tile: %w", err)
		}
		logger.Info("HiDPI tile generated", "coords", coords.String(), "path", path2x)
	}

	return nil
}

// batchOptions collects every setting for a batch generation run. It exists to
// keep runBatchGenerate and its helpers from passing a dozen loose parameters.
type batchOptions struct {
	// dataSource is set by runBatchGenerate once the source is built, so
	// runTilePool can ask whether it supports area fetching without threading
	// it through every call site.
	dataSource pipeline.DataSource
	// stampStore records what source data each tile was rendered from, and is
	// read back by the freshness policy below. Nil when no store could be
	// opened, which the pipeline treats as "behave exactly as before".
	stampStore pipeline.StampStore
	// freshness is the validated --stale-* selection; the zero value means
	// "an existing tile is a finished tile", the pre-existing behaviour.
	freshness pipeline.FreshnessPolicy
	// checkpoint is nil unless --checkpoint was given.
	checkpoint      *checkpoint.Tracker
	bboxStr         string
	outputDir       string
	dataSourceName  string
	pngCompression  string
	format          string
	outputFile      string
	folderStructure string
	imageFormat     tileformat.Format
	// ocean is resolved in runBatchGenerate, not from a flag: it comes from the
	// `ocean:` config block.
	ocean renderer.OceanConfig
	// naturalEarth likewise comes from the `natural-earth:` config block.
	naturalEarth renderer.NaturalEarthConfig
	// band holds the validated band-fetching knobs; only read when bandFetch.
	band          bandOptions
	seed          int64
	zoomMin       int
	zoomMax       int
	workers       int
	webpEffort    int
	tileSize      int
	showProgress  bool
	force         bool
	bandFetch     bool
	keepLayers    bool
	allowFailures bool
}

// configureBatchSources resolves the render-source configuration for a batch run
// and stores it on opts: the ocean and Natural Earth tiers, the tile data source
// they imply, and the band-fetch precondition.
//
// It runs before anything is opened for writing, so a misconfigured source stops
// the run at startup rather than after an output file has been created.
func configureBatchSources(opts *batchOptions) error {
	ocean, err := oceanConfig()
	if err != nil {
		return err
	}
	opts.ocean = ocean

	naturalEarth, err := naturalEarthConfig()
	if err != nil {
		return err
	}
	opts.naturalEarth = naturalEarth

	ds, err := newTileDataSource(opts.dataSourceName, ocean.Enabled())
	if err != nil {
		return err
	}
	opts.dataSource = ds

	return checkBandFetchUsable(opts, ds)
}

func runBatchGenerate(opts *batchOptions) error {
	// Parse bounding box
	bbox, err := parseBBox(opts.bboxStr)
	if err != nil {
		return fmt.Errorf("invalid bbox: %w", err)
	}

	if err := validateBatchZoom(opts.zoomMin, opts.zoomMax); err != nil {
		return err
	}

	// Default workers to CPU count
	if opts.workers <= 0 {
		opts.workers = runtime.NumCPU()
	}

	// Count tiles rather than enumerate them: the non-banded path streams the
	// enumeration and never needs the list.
	tileCount := tile.TileCount(bbox, opts.zoomMin, opts.zoomMax)
	logBatchStart(opts, tileCount)

	cp, err := setupCheckpoint(opts, bbox)
	if err != nil {
		return err
	}
	opts.checkpoint = cp

	if err := configureBatchSources(opts); err != nil {
		return err
	}
	ds := opts.dataSource

	// Parse and validate the watercolor config before anything is opened for
	// writing. mbtiles.New empties and re-inserts the metadata table on open, so
	// loading this afterwards would let a typo in the watercolor block destroy
	// the metadata of an existing output database on its way to a startup error.
	wcOverrides, err := loadWatercolorOverrides()
	if err != nil {
		return err
	}

	// Freshness is parsed before anything is opened for writing, for the same
	// reason the watercolor block is: a malformed timestamp should stop the run
	// at startup.
	freshness, err := freshnessPolicyFromConfig()
	if err != nil {
		return err
	}
	opts.freshness = freshness

	// Create the MBTiles writer if needed
	mbtilesWriter, err := openMBTilesWriter(opts, bbox)
	if err != nil {
		return err
	}

	// Opened after the MBTiles writer, because for that format the stamps live
	// in the file it just created.
	stamps, err := openStampStore(opts.format, opts.outputDir, opts.outputFile)
	if err != nil {
		// Nothing has registered a cleanup for the writer yet, so it is closed
		// here rather than leaked on the way out.
		closeMBTilesWriter(mbtilesWriter)
		return fmt.Errorf("failed to open the tile stamp store: %w", err)
	}

	// One cleanup for both, in the order they have to happen: the tiles are
	// committed before the provenance describing them. Two defers would run
	// LIFO and give the reverse — a run that fails between the last stamp and
	// the tile flush would leave stamps for tiles that were never written, and
	// a later freshness check would believe them.
	defer func() {
		closeMBTilesWriter(mbtilesWriter)
		closeStampStore(stamps)
	}()
	opts.stampStore = stampStoreOption(stamps)

	bindCheckpointToWriter(cp, mbtilesWriter)

	// Create generator with optional TileWriter
	var tileWriter pipeline.TileWriter
	if mbtilesWriter != nil {
		tileWriter = mbtilesWriter
	}

	gen, err := newBatchGenerator(opts, ds, opts.tileSize, tileWriter, wcOverrides)
	if err != nil {
		return fmt.Errorf("failed to init generator: %w", err)
	}

	// Setup context with signal handling
	ctx, cancel := newSignalContext()
	defer cancel()

	// Run base tiles
	logger.Info("Generating base tiles", "count", tileCount)
	run := runTilePool(ctx, gen, bbox, opts, "")
	failedCount := run.logFailures("Tile generation failed")
	logger.Info(run.summary)
	if err := failureError(failedCount, "base", opts.allowFailures); err != nil {
		return err
	}

	if err := writeFolderTileJSON(opts, bbox); err != nil {
		return err
	}

	return flushMBTilesWriter(opts, mbtilesWriter)
}

// unsetZoom marks --zoom-min / --zoom-max as not supplied.
//
// It cannot be 0: zoom 0 is the single world tile, and a global low-zoom run
// starts there, so treating 0 as "unset" made `--zoom-min 0` indistinguishable
// from omitting the flag and put the whole tier out of reach.
const unsetZoom = -1

// validateBatchZoom checks that the batch zoom range is usable.
//
// Absence and range are separate checks on purpose. "You forgot the flag" and
// "that zoom does not exist" are different mistakes and read badly when merged.
func validateBatchZoom(zoomMin, zoomMax int) error {
	if zoomMin == unsetZoom || zoomMax == unsetZoom {
		return fmt.Errorf("--zoom-min and --zoom-max are required for batch generation")
	}
	for _, z := range []struct {
		flag  string
		value int
	}{
		{"--zoom-min", zoomMin},
		{"--zoom-max", zoomMax},
	} {
		if z.value < 0 || z.value > tile.MaxZoom {
			return fmt.Errorf("%s (%d) must be between 0 and %d", z.flag, z.value, tile.MaxZoom)
		}
	}
	if zoomMin > zoomMax {
		return fmt.Errorf("--zoom-min (%d) must be <= --zoom-max (%d)", zoomMin, zoomMax)
	}
	return nil
}

// newTileDataSource resolves the configured data source name.
//
// This goes through the same createOverpassDataSource that `serve` uses, so both
// commands honour the `overpass.servers` / `overpass.endpoint` config. Before,
// `generate` hardcoded the empty endpoint and therefore always hit the public
// overpass-api.de, ignoring a configured local instance and taking its rate
// limits — see docs/local-overpass.md.
func newTileDataSource(name string, allowEmpty bool) (pipeline.DataSource, error) {
	switch name {
	case "overpass":
		if logger == nil {
			initLogging()
		}
		return createOverpassDataSource(viper.GetInt("generate.overpass_workers"), allowEmpty, logger)
	default:
		return nil, fmt.Errorf("unsupported data source: %s", name)
	}
}

// logBatchStart reports what the batch run is about to do.
func logBatchStart(opts *batchOptions, tileCount int) {
	logger.Info("Starting batch tile generation",
		"bbox", opts.bboxStr,
		"zoom_range", fmt.Sprintf("%d-%d", opts.zoomMin, opts.zoomMax),
		"tiles", tileCount,
		"workers", opts.workers,
		"output_dir", opts.outputDir,
		"format", opts.format,
	)
}

// batchMetadata describes the tile set produced by a batch run. Both the
// MBTiles metadata table and the folder tilejson.json are built from it, so the
// two deliveries describe the same set.
func batchMetadata(opts *batchOptions, bbox [4]float64) mbtiles.Metadata {
	return mbtiles.Metadata{
		Name:    tilejson.DefaultName,
		Format:  opts.imageFormat.String(),
		MinZoom: mbtiles.Zoom(opts.zoomMin),
		MaxZoom: mbtiles.Zoom(opts.zoomMax),
		// bbox is already [minLon, minLat, maxLon, maxLat], the TileJSON
		// bounds order.
		Bounds: bbox,
		Center: [3]float64{
			(bbox[0] + bbox[2]) / 2,
			(bbox[1] + bbox[3]) / 2,
			float64((opts.zoomMin + opts.zoomMax) / 2),
		},
		Attribution: tilejson.DefaultAttribution,
		Description: tilejson.DefaultDescription,
		Type:        "baselayer",
		Version:     "1.0",
	}
}

// writeFolderTileJSON emits tilejson.json next to the tiles of a folder run, so
// a client can discover bounds, zoom range and attribution without the CLI
// flags that produced them. MBTiles output carries the same information in its
// metadata table and needs no sidecar.
func writeFolderTileJSON(opts *batchOptions, bbox [4]float64) error {
	if opts.format != "folder" {
		return nil
	}

	doc := tilejson.FromMBTilesMetadata(
		batchMetadata(opts, bbox),
		tilejson.FolderTileTemplate(opts.folderStructure, opts.imageFormat.String()),
	)

	path, err := tilejson.WriteFile(opts.outputDir, doc)
	if err != nil {
		return fmt.Errorf("failed to write tilejson: %w", err)
	}

	logger.Info("TileJSON written", "path", path, "tiles", doc.Tiles[0])
	return nil
}

// openMBTilesWriter creates the MBTiles writer for the run. It returns a nil
// writer when the output format is not MBTiles.
func openMBTilesWriter(opts *batchOptions, bbox [4]float64) (*mbtiles.Writer, error) {
	if opts.format != "mbtiles" {
		return nil, nil
	}

	writer, err := mbtiles.New(opts.outputFile, batchMetadata(opts, bbox))
	if err != nil {
		return nil, fmt.Errorf("failed to create MBTiles writer: %w", err)
	}

	logger.Info("MBTiles writer created", "output", opts.outputFile)
	return writer, nil
}

// bindCheckpointToWriter makes the checkpoint wait for durable MBTiles rows.
//
// A watermark is a promise that the tiles below it are in the output.
// mbtiles.Writer buffers rows and commits them a batch at a time, so a
// successful render is not yet a written tile: without this, a crash could leave
// a durable watermark over rows that never reached SQLite, and the resumed run
// would skip them for good. Folder output needs nothing here — encodeTileAtomic
// has already renamed the file into place by the time the result arrives.
func bindCheckpointToWriter(cp *checkpoint.Tracker, w *mbtiles.Writer) {
	if cp == nil || w == nil {
		return
	}
	cp.SetFlush(w.Flush)
}

// closeMBTilesWriter closes the writer when it is in use, logging failures.
func closeMBTilesWriter(w *mbtiles.Writer) {
	if w == nil {
		return
	}
	if err := w.Close(); err != nil {
		logger.Error("Failed to close MBTiles writer", "error", err)
	}
}

// flushMBTilesWriter flushes the MBTiles writer when it is in use.
func flushMBTilesWriter(opts *batchOptions, w *mbtiles.Writer) error {
	if w == nil {
		return nil
	}

	logger.Info("Flushing MBTiles database...")
	if err := w.Flush(); err != nil {
		return fmt.Errorf("failed to flush MBTiles: %w", err)
	}
	logger.Info("MBTiles generation complete", "output", opts.outputFile)
	return nil
}

// newBatchGenerator builds a pipeline generator for the given tile size.
func newBatchGenerator(opts *batchOptions, ds pipeline.DataSource, tileSize int, tileWriter pipeline.TileWriter, wcOverrides *watercolor.Overrides) (*pipeline.Generator, error) {
	return pipeline.NewGenerator(
		ds,
		filepath.Join("assets", "styles"),
		filepath.Join("assets", "textures"),
		opts.outputDir,
		tileSize,
		opts.seed,
		opts.keepLayers,
		logger,
		pipeline.GeneratorOptions{
			PNGCompression:  opts.pngCompression,
			ImageFormat:     opts.imageFormat,
			WebPEffort:      opts.webpEffort,
			TileWriter:      tileWriter,
			FolderStructure: opts.folderStructure,
			Watercolor:      wcOverrides,
			Ocean:           opts.ocean,
			NaturalEarth:    opts.naturalEarth,
			StampStore:      opts.stampStore,
			Freshness:       opts.freshness,
			RendererRev:     rendererRev(),
			// opts.workers is already resolved to NumCPU by the time a generator
			// is built, so the auto split sees the real tile-level parallelism.
			PaintWorkers: resolvePaintWorkers("generate.paint_workers", opts.workers),
		},
	)
}

// newSignalContext returns a context cancelled on SIGINT/SIGTERM.
func newSignalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		logger.Info("Received interrupt signal, cancelling...")
		cancel()
	}()

	return ctx, cancel
}

// runTilePool renders every tile of bbox through a worker pool and reports what
// happened, without holding a task or a result per tile.
//
// The banded path is the exception: it schedules by band rather than by
// enumeration order, so it still takes the materialised tile list. That list is
// the price of band grouping, not of the pool.
func runTilePool(ctx context.Context, gen worker.Generator, bbox [4]float64, opts *batchOptions, suffix string) tileRunResult {
	if opts.bandFetch {
		bandGen, _ := bandGeneratorFor(gen)
		if ds, ok := opts.dataSource.(areaDataSource); ok && bandGen != nil {
			coords := tile.TilesInBBox(bbox, opts.zoomMin, opts.zoomMax)
			results, summary := runBandedTilePool(ctx, gen, bandGen, ds, coords, opts, suffix)
			return aggregateResults(results, summary)
		}
	}

	return runStreamingTilePool(ctx, gen, bbox, opts, suffix)
}

// failureError turns a failure count into an error unless failures are allowed.
func failureError(failedCount int, kind string, allowFailures bool) error {
	if failedCount == 0 {
		return nil
	}
	if allowFailures {
		logger.Warn("Some tiles failed to generate, but continuing due to --allow-failures flag",
			"kind", kind, "failed_count", failedCount)
		return nil
	}
	return fmt.Errorf("%d %s tiles failed to generate", failedCount, kind)
}

// parseBBox parses a bounding box string "minLon,minLat,maxLon,maxLat" into [4]float64.
func parseBBox(s string) ([4]float64, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return [4]float64{}, fmt.Errorf("expected 4 comma-separated values, got %d", len(parts))
	}

	var bbox [4]float64
	for i, part := range parts {
		val, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil {
			return [4]float64{}, fmt.Errorf("invalid number at position %d: %w", i, err)
		}
		bbox[i] = val
	}

	// Validate
	if bbox[0] >= bbox[2] {
		return [4]float64{}, fmt.Errorf("minLon (%.4f) must be < maxLon (%.4f)", bbox[0], bbox[2])
	}
	if bbox[1] >= bbox[3] {
		return [4]float64{}, fmt.Errorf("minLat (%.4f) must be < maxLat (%.4f)", bbox[1], bbox[3])
	}
	// Reject bounds outside the world instead of letting the tile grid clamp
	// them: a bbox like "181,-1,182,1" does not intersect the Web Mercator
	// domain at all, and silently rendering the easternmost column instead of
	// erroring hides a typo behind a plausible-looking batch run.
	if bbox[0] < tile.MinLon || bbox[2] > tile.MaxLon {
		return [4]float64{}, fmt.Errorf("longitude range (%.4f, %.4f) must lie within [%.0f, %.0f]",
			bbox[0], bbox[2], tile.MinLon, tile.MaxLon)
	}
	if bbox[1] < -90 || bbox[3] > 90 {
		return [4]float64{}, fmt.Errorf("latitude range (%.4f, %.4f) must lie within [-90, 90]", bbox[1], bbox[3])
	}

	return bbox, nil
}
