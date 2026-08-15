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

	"github.com/cwbudde/watercolormap/internal/mbtiles"
	"github.com/cwbudde/watercolormap/internal/pipeline"
	"github.com/cwbudde/watercolormap/internal/renderer"
	"github.com/cwbudde/watercolormap/internal/tile"
	"github.com/cwbudde/watercolormap/internal/tileformat"
	"github.com/cwbudde/watercolormap/internal/tilejson"
	"github.com/cwbudde/watercolormap/internal/watercolor"
	"github.com/cwbudde/watercolormap/internal/worker"
)

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
	generateCmd.Flags().Int("zoom-min", 0, "Minimum zoom level for batch generation")
	generateCmd.Flags().Int("zoom-max", 0, "Maximum zoom level for batch generation")
	generateCmd.Flags().IntP("workers", "w", 0, "Number of parallel workers (default: number of CPUs)")
	generateCmd.Flags().Bool("progress", true, "Show progress bar during batch generation")
	generateCmd.Flags().Bool("allow-failures", false, "Continue generation even if some tiles fail (useful for CI/CD with API rate limits)")

	// Common flags
	generateCmd.Flags().Bool("force", false, "Re-render tiles that already exist, for folder and MBTiles output alike (without it, existing tiles are skipped so a run can resume)")
	generateCmd.Flags().Int("tile-size", 256, "Tile size in pixels (typically 256 or 512 for Hi-DPI)")
	generateCmd.Flags().Bool("hidpi", false, "Also generate a 2x (@2x) tile alongside the base tile")
	generateCmd.Flags().String("image-format", "png", "Tile image encoding: png or webp (webp is lossless, ~1.2x smaller)")
	generateCmd.Flags().String("png-compression", "default", "PNG compression (default, speed, best, none); ignored for --image-format=webp")
	generateCmd.Flags().Int("webp-effort", tileformat.DefaultWebPEffort, "WebP compression effort 0-6 (0 = fastest); ignored for --image-format=png")
	generateCmd.Flags().Int64("seed", 1337, "Deterministic seed for noise/texture alignment")
	generateCmd.Flags().Bool("keep-layers", false, "Keep intermediate rendered layer PNGs for debugging")

	// Output format flags
	generateCmd.Flags().String("format", "folder", "Output container: folder or mbtiles (see --image-format for the tile image encoding)")
	generateCmd.Flags().String("output-file", "", "Output file path for MBTiles format (e.g., tiles.mbtiles)")
	generateCmd.Flags().String("folder-structure", "flat", "Folder structure for folder format: flat (z{z}_x{x}_y{y}.png) or nested ({z}/{x}/{y}.png)")

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
		{"generate.progress", "progress"},
		{"generate.allow_failures", "allow-failures"},
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
			hidpi:           hidpi,
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

	ds, err := newTileDataSource(opts.dataSourceName, ocean.Enabled())
	if err != nil {
		return err
	}

	stylesDir := filepath.Join("assets", "styles")
	texturesDir := filepath.Join("assets", "textures")

	wcOverrides, err := loadWatercolorOverrides()
	if err != nil {
		return err
	}

	gen, err := pipeline.NewGenerator(ds, stylesDir, texturesDir, opts.outputDir, opts.tileSize, opts.seed, opts.keepLayers, logger, pipeline.GeneratorOptions{
		PNGCompression:  opts.pngCompression,
		ImageFormat:     opts.imageFormat,
		WebPEffort:      opts.webpEffort,
		FolderStructure: opts.folderStructure,
		Watercolor:      wcOverrides,
		Ocean:           ocean,
	})
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
		gen2x, err := pipeline.NewGenerator(ds, stylesDir, texturesDir, opts.outputDir, opts.tileSize*2, opts.seed, opts.keepLayers, logger, pipeline.GeneratorOptions{
			PNGCompression:  opts.pngCompression,
			ImageFormat:     opts.imageFormat,
			WebPEffort:      opts.webpEffort,
			FolderStructure: opts.folderStructure,
			Watercolor:      wcOverrides,
			Ocean:           ocean,
		})
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
	dataSource      pipeline.DataSource
	bboxStr         string
	outputDir       string
	dataSourceName  string
	pngCompression  string
	format          string
	outputFile      string
	folderStructure string
	imageFormat     tileformat.Format
	// ocean is resolved in runBatchGenerate, not from a flag: it comes from the
	// `ocean:` config block and is shared by the base and HiDPI generators.
	ocean renderer.OceanConfig
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
	hidpi         bool
	keepLayers    bool
	allowFailures bool
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

	// Calculate tiles
	tiles := tile.TilesInBBox(bbox, opts.zoomMin, opts.zoomMax)
	logBatchStart(opts, len(tiles))

	ocean, err := oceanConfig()
	if err != nil {
		return err
	}
	opts.ocean = ocean

	ds, err := newTileDataSource(opts.dataSourceName, ocean.Enabled())
	if err != nil {
		return err
	}
	opts.dataSource = ds

	if err := checkBandFetchUsable(opts, ds); err != nil {
		return err
	}

	// Parse and validate the watercolor config before anything is opened for
	// writing. mbtiles.New empties and re-inserts the metadata table on open, so
	// loading this afterwards would let a typo in the watercolor block destroy
	// the metadata of an existing output database on its way to a startup error.
	wcOverrides, err := loadWatercolorOverrides()
	if err != nil {
		return err
	}

	// Create MBTiles writers if needed
	mbtilesWriter, mbtilesWriterHiDPI, err := openMBTilesWriters(opts, bbox)
	if err != nil {
		return err
	}
	defer closeMBTilesWriters(mbtilesWriter, mbtilesWriterHiDPI)

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
	logger.Info("Generating base tiles", "count", len(tiles))
	results, summary := runTilePool(ctx, gen, tiles, opts, "")
	failedCount := logTileFailures(results, "Tile generation failed")
	logger.Info(summary)
	if err := failureError(failedCount, "base", opts.allowFailures); err != nil {
		return err
	}

	// Generate HiDPI tiles if requested
	if opts.hidpi {
		if err := runHiDPIBatch(ctx, opts, ds, tiles, mbtilesWriterHiDPI, wcOverrides); err != nil {
			return err
		}
	}

	if err := writeFolderTileJSON(opts, bbox); err != nil {
		return err
	}

	return flushMBTilesWriters(opts, mbtilesWriter, mbtilesWriterHiDPI)
}

// validateBatchZoom checks that the batch zoom range is usable.
func validateBatchZoom(zoomMin, zoomMax int) error {
	if zoomMin <= 0 || zoomMax <= 0 {
		return fmt.Errorf("--zoom-min and --zoom-max are required for batch generation")
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
	totalTiles := tileCount

	// If hidpi, we'll generate 2x the tiles
	if opts.hidpi {
		totalTiles *= 2
	}

	logger.Info("Starting batch tile generation",
		"bbox", opts.bboxStr,
		"zoom_range", fmt.Sprintf("%d-%d", opts.zoomMin, opts.zoomMax),
		"tiles", tileCount,
		"total_with_hidpi", totalTiles,
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

// openMBTilesWriters creates the MBTiles writers for the run. It returns nil
// writers when the output format is not MBTiles.
func openMBTilesWriters(opts *batchOptions, bbox [4]float64) (base, hidpi *mbtiles.Writer, err error) {
	if opts.format != "mbtiles" {
		return nil, nil, nil
	}

	metadata := batchMetadata(opts, bbox)

	base, err = mbtiles.New(opts.outputFile, metadata)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create MBTiles writer: %w", err)
	}

	// Create separate writer for HiDPI tiles
	if opts.hidpi {
		hidpiFile := strings.TrimSuffix(opts.outputFile, ".mbtiles") + "@2x.mbtiles"
		hidpi, err = mbtiles.New(hidpiFile, metadata)
		if err != nil {
			closeMBTilesWriters(base)
			return nil, nil, fmt.Errorf("failed to create HiDPI MBTiles writer: %w", err)
		}
	}

	logger.Info("MBTiles writers created", "base", opts.outputFile, "hidpi", opts.hidpi)
	return base, hidpi, nil
}

// closeMBTilesWriters closes every non-nil writer, logging close failures.
func closeMBTilesWriters(writers ...*mbtiles.Writer) {
	for _, w := range writers {
		if w == nil {
			continue
		}
		if err := w.Close(); err != nil {
			logger.Error("Failed to close MBTiles writer", "error", err)
		}
	}
}

// flushMBTilesWriters flushes the MBTiles writers when they are in use.
func flushMBTilesWriters(opts *batchOptions, base, hidpi *mbtiles.Writer) error {
	if base == nil {
		return nil
	}

	logger.Info("Flushing MBTiles databases...")
	if err := base.Flush(); err != nil {
		return fmt.Errorf("failed to flush base MBTiles: %w", err)
	}
	if hidpi != nil {
		if err := hidpi.Flush(); err != nil {
			return fmt.Errorf("failed to flush HiDPI MBTiles: %w", err)
		}
	}
	logger.Info("MBTiles generation complete", "base", opts.outputFile)
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

// runTilePool renders the given tiles through a worker pool and returns the
// results together with the progress summary.
func runTilePool(ctx context.Context, gen worker.Generator, coords []tile.Coords, opts *batchOptions, suffix string) ([]worker.Result, string) {
	if opts.bandFetch {
		bandGen, _ := bandGeneratorFor(gen)
		if ds, ok := opts.dataSource.(areaDataSource); ok && bandGen != nil {
			return runBandedTilePool(ctx, gen, bandGen, ds, coords, opts, suffix)
		}
	}

	tasks := make([]worker.Task, 0, len(coords))
	for _, c := range coords {
		tasks = append(tasks, worker.Task{
			Coords: c,
			Force:  opts.force,
			Suffix: suffix,
		})
	}

	progress := worker.NewProgress(len(tasks), opts.showProgress)
	pool := worker.New(worker.Config{
		Workers:    opts.workers,
		Generator:  gen,
		OnProgress: progress.Callback(),
	})

	results := pool.Run(ctx, tasks)
	progress.Done()

	return results, progress.Summary()
}

// logTileFailures logs every failed result and returns the failure count.
func logTileFailures(results []worker.Result, msg string) int {
	var failedCount int
	for _, r := range results {
		if r.Err != nil {
			failedCount++
			logger.Error(msg, "coords", r.Task.Coords.String(), "suffix", r.Task.Suffix, "error", r.Err)
		}
	}
	return failedCount
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

// runHiDPIBatch generates the @2x variants of the given tiles.
func runHiDPIBatch(ctx context.Context, opts *batchOptions, ds pipeline.DataSource, tiles []tile.Coords, hidpiWriter *mbtiles.Writer, wcOverrides *watercolor.Overrides) error {
	logger.Info("Generating HiDPI tiles", "count", len(tiles))

	// Create HiDPI generator with appropriate writer
	var tileWriter pipeline.TileWriter
	if hidpiWriter != nil {
		tileWriter = hidpiWriter
	}

	genHiDPI, err := newBatchGenerator(opts, ds, opts.tileSize*2, tileWriter, wcOverrides)
	if err != nil {
		return fmt.Errorf("failed to init HiDPI generator: %w", err)
	}

	results, summary := runTilePool(ctx, genHiDPI, tiles, opts, "@2x")
	failedCount := logTileFailures(results, "HiDPI tile generation failed")
	logger.Info(summary)

	return failureError(failedCount, "HiDPI", opts.allowFailures)
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

	return bbox, nil
}
