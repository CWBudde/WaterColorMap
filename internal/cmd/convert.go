package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/cwbudde/watercolormap/internal/mbtiles"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var convertCmd = &cobra.Command{
	Use:   "convert",
	Short: "Convert folder tiles to MBTiles format",
	Long:  `Convert existing tile folder to MBTiles database format.`,
	RunE:  runConvert,
}

func init() {
	rootCmd.AddCommand(convertCmd)

	convertCmd.Flags().String("input-dir", "./tiles", "Input directory containing tiles")
	convertCmd.Flags().StringP("output", "o", "", "Output MBTiles file path (required)")
	convertCmd.Flags().String("name", "WaterColorMap", "Tileset name")
	convertCmd.Flags().String("description", "Watercolor-styled map tiles", "Tileset description")
	convertCmd.Flags().String("attribution", "© OpenStreetMap contributors", "Attribution text")
	convertCmd.Flags().String("bounds", "", "Bounding box: minLon,minLat,maxLon,maxLat (optional)")

	bindFlags := []struct {
		key  string
		flag string
	}{
		{"convert.input_dir", "input-dir"},
		{"convert.output", "output"},
		{"convert.name", "name"},
		{"convert.description", "description"},
		{"convert.attribution", "attribution"},
		{"convert.bounds", "bounds"},
	}

	for _, bf := range bindFlags {
		if err := viper.BindPFlag(bf.key, convertCmd.Flags().Lookup(bf.flag)); err != nil {
			panic(fmt.Sprintf("failed to bind flag %s: %v", bf.flag, err))
		}
	}
}

func runConvert(cmd *cobra.Command, args []string) error {
	inputDir := viper.GetString("convert.input_dir")
	outputFile := viper.GetString("convert.output")
	name := viper.GetString("convert.name")
	description := viper.GetString("convert.description")
	attribution := viper.GetString("convert.attribution")
	boundsStr := viper.GetString("convert.bounds")

	if logger == nil {
		initLogging()
	}

	// Validate output file
	if outputFile == "" {
		return fmt.Errorf("--output is required")
	}

	// Verify input directory exists
	if _, err := os.Stat(inputDir); os.IsNotExist(err) {
		return fmt.Errorf("input directory does not exist: %s", inputDir)
	}

	logger.Info("Converting folder tiles to MBTiles",
		"input_dir", inputDir,
		"output", outputFile,
		"name", name,
	)

	// Scan tiles directory
	tiles, minZoom, maxZoom, err := scanTilesDirectory(inputDir)
	if err != nil {
		return fmt.Errorf("failed to scan tiles directory: %w", err)
	}

	if len(tiles) == 0 {
		return fmt.Errorf("no tiles found in %s", inputDir)
	}

	logger.Info("Found tiles", "count", len(tiles), "min_zoom", minZoom, "max_zoom", maxZoom)

	// Parse bounds if provided
	var bounds [4]float64
	if boundsStr != "" {
		parsedBounds, err := parseBBox(boundsStr)
		if err != nil {
			return fmt.Errorf("invalid bounds: %w", err)
		}
		bounds = parsedBounds
	}

	// Calculate center
	center := [3]float64{
		(bounds[0] + bounds[2]) / 2,
		(bounds[1] + bounds[3]) / 2,
		float64((minZoom + maxZoom) / 2),
	}

	// Create MBTiles metadata
	metadata := mbtiles.Metadata{
		Name:        name,
		Format:      "png",
		MinZoom:     minZoom,
		MaxZoom:     maxZoom,
		Bounds:      bounds,
		Center:      center,
		Attribution: attribution,
		Description: description,
		Type:        "baselayer",
		Version:     "1.0",
	}

	// Create MBTiles writer
	writer, err := mbtiles.New(outputFile, metadata)
	if err != nil {
		return fmt.Errorf("failed to create MBTiles writer: %w", err)
	}
	defer writer.Close()

	// Convert tiles
	logger.Info("Converting tiles...")
	for i, tileInfo := range tiles {
		// Read PNG file
		pngData, err := os.ReadFile(tileInfo.path)
		if err != nil {
			logger.Error("Failed to read tile", "path", tileInfo.path, "error", err)
			continue
		}

		// Write to MBTiles
		if err := writer.WriteTile(tileInfo.z, tileInfo.x, tileInfo.y, pngData); err != nil {
			logger.Error("Failed to write tile", "coords", fmt.Sprintf("%d/%d/%d", tileInfo.z, tileInfo.x, tileInfo.y), "error", err)
			continue
		}

		if (i+1)%100 == 0 {
			logger.Info("Progress", "converted", i+1, "total", len(tiles))
		}
	}

	// Flush remaining tiles
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("failed to flush tiles: %w", err)
	}

	logger.Info("Conversion complete", "output", outputFile, "tiles", len(tiles))
	return nil
}

type tileInfo struct {
	path string
	z    int
	x    int
	y    int
}

// scanTilesDirectory scans a directory for tile files and returns tile info.
func scanTilesDirectory(dir string) ([]tileInfo, int, int, error) {
	// Pattern: z{zoom}_x{x}_y{y}.png or z{zoom}_x{x}_y{y}@2x.png
	pattern := regexp.MustCompile(`^z(\d+)_x(\d+)_y(\d+)(?:@2x)?\.png$`)

	var tiles []tileInfo
	minZoom := 999
	maxZoom := 0

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		// Match filename
		filename := filepath.Base(path)
		matches := pattern.FindStringSubmatch(filename)
		if matches == nil {
			return nil
		}

		// Parse coordinates
		z, _ := strconv.Atoi(matches[1])
		x, _ := strconv.Atoi(matches[2])
		y, _ := strconv.Atoi(matches[3])

		tiles = append(tiles, tileInfo{
			z:    z,
			x:    x,
			y:    y,
			path: path,
		})

		// Track zoom range
		if z < minZoom {
			minZoom = z
		}
		if z > maxZoom {
			maxZoom = z
		}

		return nil
	})
	if err != nil {
		return nil, 0, 0, err
	}

	// Handle case where no tiles were found
	if len(tiles) == 0 {
		minZoom = 0
		maxZoom = 0
	}

	return tiles, minZoom, maxZoom, nil
}
