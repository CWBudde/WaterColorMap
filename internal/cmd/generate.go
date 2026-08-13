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

	"github.com/cwbudde/watercolormap/internal/datasource"
	"github.com/cwbudde/watercolormap/internal/mbtiles"
	"github.com/cwbudde/watercolormap/internal/pipeline"
	"github.com/cwbudde/watercolormap/internal/tile"
	"github.com/cwbudde/watercolormap/internal/worker"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
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
	generateCmd.Flags().Bool("force", false, "Force regeneration even if tile exists")
	generateCmd.Flags().Int("tile-size", 256, "Tile size in pixels (typically 256 or 512 for Hi-DPI)")
	generateCmd.Flags().Bool("hidpi", false, "Also generate a 2x (@2x) tile alongside the base tile")
	generateCmd.Flags().String("png-compression", "default", "PNG compression (default, speed, best, none)")
	generateCmd.Flags().Int64("seed", 1337, "Deterministic seed for noise/texture alignment")
	generateCmd.Flags().Bool("keep-layers", false, "Keep intermediate rendered layer PNGs for debugging")

	// Output format flags
	generateCmd.Flags().String("format", "folder", "Output format: folder or mbtiles")
	generateCmd.Flags().String("output-file", "", "Output file path for MBTiles format (e.g., tiles.mbtiles)")
	generateCmd.Flags().String("folder-structure", "flat", "Folder structure for folder format: flat (z{z}_x{x}_y{y}.png) or nested ({z}/{x}/{y}.png)")

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
		{"generate.png_compression", "png-compression"},
		{"generate.seed", "seed"},
		{"generate.keep_layers", "keep-layers"},
		{"generate.format", "format"},
		{"generate.output_file", "output-file"},
		{"generate.folder_structure", "folder-structure"},
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

	// Determine mode: batch (bbox provided) or single tile
	if bbox != "" {
		return runBatchGenerate(bbox, zoomMin, zoomMax, workers, showProgress, force, outputDir, dataSourceName, tileSize, hidpi, pngCompression, seed, keepLayers, format, outputFile, folderStructure, allowFailures)
	}

	return runSingleGenerate(zoom, x, y, force, outputDir, dataSourceName, tileSize, hidpi, pngCompression, seed, keepLayers, folderStructure)
}

func runSingleGenerate(zoom, x, y int, force bool, outputDir, dataSourceName string, tileSize int, hidpi bool, pngCompression string, seed int64, keepLayers bool, folderStructure string) error {
	coords := tile.NewCoords(uint32(zoom), uint32(x), uint32(y))

	logger.Info("Starting tile generation",
		"coords", coords.String(),
		"output_dir", outputDir,
		"force", force,
		"data_source", dataSourceName,
		"tile_size", tileSize,
		"hidpi", hidpi,
		"png_compression", pngCompression,
		"seed", seed,
		"keep_layers", keepLayers,
	)

	if zoom < 0 || x < 0 || y < 0 {
		return fmt.Errorf("invalid coordinates: zoom/x/y must be non-negative")
	}

	var ds pipeline.DataSource
	switch dataSourceName {
	case "overpass":
		ds = datasource.NewOverpassDataSource("")
	default:
		return fmt.Errorf("unsupported data source: %s", dataSourceName)
	}

	stylesDir := filepath.Join("assets", "styles")
	texturesDir := filepath.Join("assets", "textures")

	gen, err := pipeline.NewGenerator(ds, stylesDir, texturesDir, outputDir, tileSize, seed, keepLayers, logger, pipeline.GeneratorOptions{
		PNGCompression:  pngCompression,
		FolderStructure: folderStructure,
	})
	if err != nil {
		return fmt.Errorf("failed to init generator: %w", err)
	}

	path, layersDir, err := gen.Generate(context.Background(), coords, force, "", nil)
	if err != nil {
		return fmt.Errorf("failed to generate tile: %w", err)
	}

	logFields := []interface{}{"coords", coords.String(), "path", path}
	if keepLayers && layersDir != "" {
		logFields = append(logFields, "layers_dir", layersDir)
	}
	logger.Info("Tile generated", logFields...)

	if hidpi {
		gen2x, err := pipeline.NewGenerator(ds, stylesDir, texturesDir, outputDir, tileSize*2, seed, keepLayers, logger, pipeline.GeneratorOptions{
			PNGCompression:  pngCompression,
			FolderStructure: folderStructure,
		})
		if err != nil {
			return fmt.Errorf("failed to init hidpi generator: %w", err)
		}
		path2x, _, err := gen2x.Generate(context.Background(), coords, force, "@2x", nil)
		if err != nil {
			return fmt.Errorf("failed to generate hidpi tile: %w", err)
		}
		logger.Info("HiDPI tile generated", "coords", coords.String(), "path", path2x)
	}

	return nil
}

func runBatchGenerate(bboxStr string, zoomMin, zoomMax, workers int, showProgress, force bool, outputDir, dataSourceName string, tileSize int, hidpi bool, pngCompression string, seed int64, keepLayers bool, format, outputFile, folderStructure string, allowFailures bool) error {
	// Parse bounding box
	bbox, err := parseBBox(bboxStr)
	if err != nil {
		return fmt.Errorf("invalid bbox: %w", err)
	}

	// Validate zoom range
	if zoomMin <= 0 || zoomMax <= 0 {
		return fmt.Errorf("--zoom-min and --zoom-max are required for batch generation")
	}
	if zoomMin > zoomMax {
		return fmt.Errorf("--zoom-min (%d) must be <= --zoom-max (%d)", zoomMin, zoomMax)
	}

	// Default workers to CPU count
	if workers <= 0 {
		workers = runtime.NumCPU()
	}

	// Calculate tiles
	tiles := tile.TilesInBBox(bbox, zoomMin, zoomMax)
	totalTiles := len(tiles)

	// If hidpi, we'll generate 2x the tiles
	if hidpi {
		totalTiles *= 2
	}

	logger.Info("Starting batch tile generation",
		"bbox", bboxStr,
		"zoom_range", fmt.Sprintf("%d-%d", zoomMin, zoomMax),
		"tiles", len(tiles),
		"total_with_hidpi", totalTiles,
		"workers", workers,
		"output_dir", outputDir,
		"format", format,
	)

	// Setup data source
	var ds pipeline.DataSource
	switch dataSourceName {
	case "overpass":
		ds = datasource.NewOverpassDataSource("")
	default:
		return fmt.Errorf("unsupported data source: %s", dataSourceName)
	}

	stylesDir := filepath.Join("assets", "styles")
	texturesDir := filepath.Join("assets", "textures")

	// Create MBTiles writer if needed
	var mbtilesWriter *mbtiles.Writer
	var mbtilesWriterHiDPI *mbtiles.Writer
	if format == "mbtiles" {
		// Calculate bounds from bbox for metadata
		bounds := [4]float64{bbox[0], bbox[1], bbox[2], bbox[3]}
		center := [3]float64{
			(bbox[0] + bbox[2]) / 2,
			(bbox[1] + bbox[3]) / 2,
			float64((zoomMin + zoomMax) / 2),
		}

		metadata := mbtiles.Metadata{
			Name:        "WaterColorMap",
			Format:      "png",
			MinZoom:     zoomMin,
			MaxZoom:     zoomMax,
			Bounds:      bounds,
			Center:      center,
			Attribution: "© OpenStreetMap contributors",
			Description: "Watercolor-styled map tiles",
			Type:        "baselayer",
			Version:     "1.0",
		}

		mbtilesWriter, err = mbtiles.New(outputFile, metadata)
		if err != nil {
			return fmt.Errorf("failed to create MBTiles writer: %w", err)
		}
		defer mbtilesWriter.Close()

		// Create separate writer for HiDPI tiles
		if hidpi {
			hidpiFile := strings.TrimSuffix(outputFile, ".mbtiles") + "@2x.mbtiles"
			mbtilesWriterHiDPI, err = mbtiles.New(hidpiFile, metadata)
			if err != nil {
				mbtilesWriter.Close()
				return fmt.Errorf("failed to create HiDPI MBTiles writer: %w", err)
			}
			defer mbtilesWriterHiDPI.Close()
		}

		logger.Info("MBTiles writers created", "base", outputFile, "hidpi", hidpi)
	}

	// Create generator with optional TileWriter
	var tileWriter pipeline.TileWriter
	if format == "mbtiles" {
		tileWriter = mbtilesWriter
	}

	gen, err := pipeline.NewGenerator(ds, stylesDir, texturesDir, outputDir, tileSize, seed, keepLayers, logger, pipeline.GeneratorOptions{
		PNGCompression:  pngCompression,
		TileWriter:      tileWriter,
		FolderStructure: folderStructure,
	})
	if err != nil {
		return fmt.Errorf("failed to init generator: %w", err)
	}

	// Setup context with signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		logger.Info("Received interrupt signal, cancelling...")
		cancel()
	}()

	// Build task list for base tiles
	tasks := make([]worker.Task, 0, len(tiles))
	for _, coords := range tiles {
		tasks = append(tasks, worker.Task{
			Coords: coords,
			Force:  force,
			Suffix: "",
		})
	}

	// Setup progress tracking
	progress := worker.NewProgress(len(tasks), showProgress)

	// Create worker pool
	pool := worker.New(worker.Config{
		Workers:    workers,
		Generator:  gen,
		OnProgress: progress.Callback(),
	})

	// Run base tiles
	logger.Info("Generating base tiles", "count", len(tasks))
	results := pool.Run(ctx, tasks)
	progress.Done()

	// Check for failures
	var failedCount int
	for _, r := range results {
		if r.Err != nil {
			failedCount++
			logger.Error("Tile generation failed", "coords", r.Task.Coords.String(), "suffix", r.Task.Suffix, "error", r.Err)
		}
	}

	logger.Info(progress.Summary())

	if failedCount > 0 {
		if allowFailures {
			logger.Warn("Some tiles failed to generate, but continuing due to --allow-failures flag", "failed_count", failedCount)
		} else {
			return fmt.Errorf("%d base tiles failed to generate", failedCount)
		}
	}

	// Generate HiDPI tiles if requested
	if hidpi {
		logger.Info("Generating HiDPI tiles", "count", len(tiles))

		// Create HiDPI generator with appropriate writer
		var hidpiWriter pipeline.TileWriter
		if format == "mbtiles" {
			hidpiWriter = mbtilesWriterHiDPI
		}

		genHiDPI, err := pipeline.NewGenerator(ds, stylesDir, texturesDir, outputDir, tileSize*2, seed, keepLayers, logger, pipeline.GeneratorOptions{
			PNGCompression:  pngCompression,
			TileWriter:      hidpiWriter,
			FolderStructure: folderStructure,
		})
		if err != nil {
			return fmt.Errorf("failed to init HiDPI generator: %w", err)
		}

		// Build HiDPI task list
		hidpiTasks := make([]worker.Task, 0, len(tiles))
		for _, coords := range tiles {
			hidpiTasks = append(hidpiTasks, worker.Task{
				Coords: coords,
				Force:  force,
				Suffix: "@2x",
			})
		}

		// Setup progress tracking for HiDPI
		progressHiDPI := worker.NewProgress(len(hidpiTasks), showProgress)

		// Create worker pool for HiDPI
		poolHiDPI := worker.New(worker.Config{
			Workers:    workers,
			Generator:  genHiDPI,
			OnProgress: progressHiDPI.Callback(),
		})

		// Run HiDPI tiles
		resultsHiDPI := poolHiDPI.Run(ctx, hidpiTasks)
		progressHiDPI.Done()

		// Check for failures
		var hidpiFailedCount int
		for _, r := range resultsHiDPI {
			if r.Err != nil {
				hidpiFailedCount++
				logger.Error("HiDPI tile generation failed", "coords", r.Task.Coords.String(), "error", r.Err)
			}
		}

		logger.Info(progressHiDPI.Summary())

		if hidpiFailedCount > 0 {
			if allowFailures {
				logger.Warn("Some HiDPI tiles failed to generate, but continuing due to --allow-failures flag", "failed_count", hidpiFailedCount)
			} else {
				return fmt.Errorf("%d HiDPI tiles failed to generate", hidpiFailedCount)
			}
		}
	}

	// Flush MBTiles writers if used
	if format == "mbtiles" {
		logger.Info("Flushing MBTiles databases...")
		if err := mbtilesWriter.Flush(); err != nil {
			return fmt.Errorf("failed to flush base MBTiles: %w", err)
		}
		if hidpi && mbtilesWriterHiDPI != nil {
			if err := mbtilesWriterHiDPI.Flush(); err != nil {
				return fmt.Errorf("failed to flush HiDPI MBTiles: %w", err)
			}
		}
		logger.Info("MBTiles generation complete", "base", outputFile)
	}

	return nil
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
