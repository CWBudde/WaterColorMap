package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/cwbudde/watercolormap/internal/mbtiles"
	"github.com/cwbudde/watercolormap/internal/tileformat"
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
	tiles, minZoom, maxZoom, imageFormat, err := scanTilesDirectory(inputDir)
	if err != nil {
		return fmt.Errorf("failed to scan tiles directory: %w", err)
	}

	if len(tiles) == 0 {
		return fmt.Errorf("no tiles found in %s", inputDir)
	}

	logger.Info("Found tiles", "count", len(tiles), "min_zoom", minZoom, "max_zoom", maxZoom,
		"image_format", imageFormat)

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
		Format:      imageFormat.String(),
		MinZoom:     mbtiles.Zoom(minZoom),
		MaxZoom:     mbtiles.Zoom(maxZoom),
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
	defer closeMBTilesWriter(writer)

	// Convert tiles
	logger.Info("Converting tiles...")
	for i, tileInfo := range tiles {
		// Read the tile bytes; convert copies them verbatim and never transcodes.
		tileData, err := os.ReadFile(tileInfo.path)
		if err != nil {
			logger.Error("Failed to read tile", "path", tileInfo.path, "error", err)
			continue
		}

		// Write to MBTiles
		if err := writer.WriteTile(tileInfo.z, tileInfo.x, tileInfo.y, tileData); err != nil {
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

// parseTilePathInLayout identifies one file below root as a tile, in either
// folder layout. It reports false for anything that is not one, which includes
// tilejson.json, stamps.db and any stray file a user has left in the folder.
func parseTilePathInLayout(root, path string) (tileInfo, tileformat.Format, bool) {
	filename := filepath.Base(path)

	if m := flatTilePattern.FindStringSubmatch(filename); m != nil {
		// The regexp only matches digits, so a failure here means the number
		// does not fit an int; skip such a file.
		z, x, y, ok := parseTileCoords(m)
		if !ok {
			logger.Warn("Skipping tile with out-of-range coordinates", "path", path)
			return tileInfo{}, "", false
		}
		// The regexp only admits extensions tileformat knows, so this cannot
		// fail; the check keeps the switch total.
		format, ok := tileformat.ParseExt(m[5])
		if !ok {
			return tileInfo{}, "", false
		}
		return tileInfo{z: z, x: x, y: y, suffix: m[4], path: path}, format, true
	}

	m := nestedTileFilePattern.FindStringSubmatch(filename)
	if m == nil {
		return tileInfo{}, "", false
	}

	// Nested tiles are {z}/{x}/{y}.{ext} relative to the root, so the two
	// directories above the file carry the rest of the coordinate. Anything at
	// another depth is not a tile of this tileset, whatever its name.
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return tileInfo{}, "", false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 3 {
		return tileInfo{}, "", false
	}

	z, x, y, ok := parseTileCoords([]string{"", parts[0], parts[1], m[1]})
	if !ok {
		logger.Warn("Skipping tile with out-of-range coordinates", "path", path)
		return tileInfo{}, "", false
	}
	format, ok := tileformat.ParseExt(m[3])
	if !ok {
		return tileInfo{}, "", false
	}

	return tileInfo{z: z, x: x, y: y, suffix: m[2], path: path}, format, true
}

// parseTileCoords converts the z/x/y capture groups of the tile filename
// pattern into ints. It reports false when any of them is not representable.
func parseTileCoords(matches []string) (z, x, y int, ok bool) {
	var err error
	if z, err = strconv.Atoi(matches[1]); err != nil {
		return 0, 0, 0, false
	}
	if x, err = strconv.Atoi(matches[2]); err != nil {
		return 0, 0, 0, false
	}
	if y, err = strconv.Atoi(matches[3]); err != nil {
		return 0, 0, 0, false
	}
	return z, x, y, true
}

type tileInfo struct {
	path string
	// suffix is "" for a base tile and "@2x" for its HiDPI sibling. convert
	// ignores it — an MBTiles file has one tile per z/x/y — but purge selects
	// on it, and the scan is the only place that can tell them apart.
	suffix string
	z      int
	x      int
	y      int
}

// flatTilePattern matches the flat layout: z{z}_x{x}_y{y}[@2x].{ext}.
var flatTilePattern = regexp.MustCompile(`^z(\d+)_x(\d+)_y(\d+)(@2x)?\.(png|webp)$`)

// nestedTileFilePattern matches the leaf of the nested layout: {y}[@2x].{ext}.
// The zoom and column come from the two directories above it, so this alone is
// not enough to identify a tile — see scanTilesDirectory.
var nestedTileFilePattern = regexp.MustCompile(`^(\d+)(@2x)?\.(png|webp)$`)

// walkTilesDirectory finds every tile file below dir, in either folder layout,
// and reports how many of each image format it saw along with the zoom range.
//
// It applies no policy: `convert` needs a single format and refuses a mixed
// folder, but `purge` deletes files and has no reason to care what they are
// encoded as. Keeping the policy in scanTilesDirectory means purge cannot be
// blocked by a rule that only exists for the MBTiles metadata table.
func walkTilesDirectory(dir string) (tiles []tileInfo, counts map[tileformat.Format]int, minZoom, maxZoom int, err error) {
	counts = map[tileformat.Format]int{}
	minZoom = 999

	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		tile, format, ok := parseTilePathInLayout(dir, path)
		if !ok {
			return nil
		}
		counts[format]++
		tiles = append(tiles, tile)

		if tile.z < minZoom {
			minZoom = tile.z
		}
		if tile.z > maxZoom {
			maxZoom = tile.z
		}

		return nil
	})
	if err != nil {
		return nil, nil, 0, 0, err
	}

	return tiles, counts, minZoom, maxZoom, nil
}

// scanTilesDirectory scans a directory for tile files and returns tile info
// along with the image format they are all in.
//
// Both folder layouts are recognised: flat (z{z}_x{x}_y{y}.{ext}) and nested
// ({z}/{x}/{y}.{ext}), the two --folder-structure produces. The scan used to
// see only the flat one, which made `convert` silently produce an empty MBTiles
// file from a nested folder — it found no filenames matching its pattern and
// reported "no tiles found" for a directory full of tiles. purge reads the same
// folders and would have had the same blind spot, so the layouts are handled
// here, once.
//
// The format is detected rather than configured: the folder is the authority on
// what its bytes are, and a wrong flag would produce an MBTiles file whose
// metadata lies about its own contents — served with the wrong Content-Type and
// with nothing to notice it. A folder holding both formats is refused for the
// same reason: one MBTiles file records exactly one format.
func scanTilesDirectory(dir string) ([]tileInfo, int, int, tileformat.Format, error) {
	tiles, counts, minZoom, maxZoom, err := walkTilesDirectory(dir)
	if err != nil {
		return nil, 0, 0, "", err
	}

	// Handle case where no tiles were found
	if len(tiles) == 0 {
		return tiles, 0, 0, tileformat.PNG, nil
	}

	if len(counts) > 1 {
		return nil, 0, 0, "", fmt.Errorf(
			"directory holds tiles in more than one image format (%d png, %d webp); "+
				"one MBTiles file records exactly one format, so convert them separately",
			counts[tileformat.PNG], counts[tileformat.WebP])
	}

	format := tileformat.PNG
	for f := range counts {
		format = f
	}

	return tiles, minZoom, maxZoom, format, nil
}
