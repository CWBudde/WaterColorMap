package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/cwbudde/watercolormap/internal/mbtiles"
	"github.com/cwbudde/watercolormap/internal/tile"
	"github.com/cwbudde/watercolormap/internal/tilestamp"
)

// Config keys for purge. Per-command section, so underscores.
const (
	purgeTilesDirKey      = "purge.tiles_dir"
	purgeMBTilesKey       = "purge.mbtiles"
	purgeBBoxKey          = "purge.bbox"
	purgeZoomMinKey       = "purge.zoom_min"
	purgeZoomMaxKey       = "purge.zoom_max"
	purgeDataBeforeKey    = "purge.data_before"
	purgeRenderedBefore   = "purge.rendered_before"
	purgeRendererRevNot   = "purge.renderer_rev_not"
	purgeSuffixKey        = "purge.suffix"
	purgeYesKey           = "purge.yes"
	purgeCompactKey       = "purge.compact"
	purgeSampleSize       = 10
	purgeDeleteBatchLimit = 500
)

var purgeCmd = &cobra.Command{
	Use:   "purge",
	Short: "Delete rendered tiles by area, zoom or staleness",
	Long: `Delete tiles from a tile folder or an MBTiles file.

Tiles are selected by where they are (--bbox, --zoom-min/--zoom-max, --suffix)
and by what they were rendered from (--data-before, --rendered-before,
--renderer-rev-not), the last three reading the stamps written alongside the
tiles by ` + "`generate`" + `.

Purge is a dry run unless --yes is given: it always reports the count and a
sample of what it selected first. A tile with no stamp is never selected by a
staleness flag — an unknown source version is not evidence of an old one, and
deletion is not undoable.`,
	RunE: runPurge,
}

func init() {
	rootCmd.AddCommand(purgeCmd)

	purgeCmd.Flags().String("tiles-dir", "", "Tile directory to purge from")
	purgeCmd.Flags().String("mbtiles", "", "MBTiles file to purge from")
	purgeCmd.Flags().String("bbox", "", "Bounding box: minLon,minLat,maxLon,maxLat")
	purgeCmd.Flags().Int("zoom-min", -1, "Lowest zoom to consider (default: all)")
	purgeCmd.Flags().Int("zoom-max", -1, "Highest zoom to consider (default: all)")
	purgeCmd.Flags().String("data-before", "",
		"Select tiles whose source OSM data (osm_base_ts) is older than this RFC3339 timestamp")
	purgeCmd.Flags().String("rendered-before", "",
		"Select tiles rendered before this RFC3339 timestamp")
	purgeCmd.Flags().String("renderer-rev-not", "",
		"Select tiles stamped with a renderer revision other than this one")
	// "base" rather than the empty string the tiles themselves use: the empty
	// string is a real selection (the base tile), so it cannot double as "not
	// given" on a flag that has to have a default.
	purgeCmd.Flags().String("suffix", "any", "Restrict to a tile variant: any, base, or @2x")
	purgeCmd.Flags().Bool("yes", false, "Actually delete; without it purge only reports what it would delete")
	purgeCmd.Flags().Bool("compact", false, "VACUUM the MBTiles file afterwards to reclaim the freed space")

	bindFlags := []struct {
		key  string
		flag string
	}{
		{purgeTilesDirKey, "tiles-dir"},
		{purgeMBTilesKey, "mbtiles"},
		{purgeBBoxKey, "bbox"},
		{purgeZoomMinKey, "zoom-min"},
		{purgeZoomMaxKey, "zoom-max"},
		{purgeDataBeforeKey, "data-before"},
		{purgeRenderedBefore, "rendered-before"},
		{purgeRendererRevNot, "renderer-rev-not"},
		{purgeSuffixKey, "suffix"},
		{purgeYesKey, "yes"},
		{purgeCompactKey, "compact"},
	}

	for _, bf := range bindFlags {
		if err := viper.BindPFlag(bf.key, purgeCmd.Flags().Lookup(bf.flag)); err != nil {
			panic(fmt.Sprintf("failed to bind flag %s: %v", bf.flag, err))
		}
	}
}

// purgeOptions is the validated form of the flags.
type purgeOptions struct {
	dataBefore     time.Time
	renderedBefore time.Time
	suffix         *string
	minZoom        *int
	maxZoom        *int
	tilesDir       string
	mbtilesPath    string
	rendererRevNot string
	bbox           [4]float64
	hasBBox        bool
	yes            bool
	compact        bool
}

// stampFiltered reports whether any selector needs the stamp store.
func (o *purgeOptions) stampFiltered() bool {
	return !o.dataBefore.IsZero() || !o.renderedBefore.IsZero() || o.rendererRevNot != ""
}

// purgeTarget is one selected tile. path is set for folder tiles only.
type purgeTarget struct {
	path   string
	suffix string
	z      int
	x      int
	y      int
}

func (t purgeTarget) String() string {
	return fmt.Sprintf("%d/%d/%d%s", t.z, t.x, t.y, t.suffix)
}

func runPurge(cmd *cobra.Command, args []string) error {
	if logger == nil {
		initLogging()
	}

	opts, err := purgeOptionsFromConfig()
	if err != nil {
		return err
	}

	if opts.mbtilesPath != "" {
		return purgeMBTiles(opts)
	}
	return purgeFolder(opts)
}

// purgeOptionsFromConfig reads and validates the flags. Everything that can be
// wrong is caught here, before anything is opened, let alone deleted.
func purgeOptionsFromConfig() (*purgeOptions, error) {
	opts := &purgeOptions{
		tilesDir:       viper.GetString(purgeTilesDirKey),
		mbtilesPath:    viper.GetString(purgeMBTilesKey),
		rendererRevNot: viper.GetString(purgeRendererRevNot),
		yes:            viper.GetBool(purgeYesKey),
		compact:        viper.GetBool(purgeCompactKey),
	}

	switch {
	case opts.tilesDir == "" && opts.mbtilesPath == "":
		return nil, fmt.Errorf("one of --tiles-dir or --mbtiles is required")
	case opts.tilesDir != "" && opts.mbtilesPath != "":
		return nil, fmt.Errorf("--tiles-dir and --mbtiles are mutually exclusive: " +
			"a purge run deletes from exactly one tileset")
	}

	if err := readPurgeExtent(opts); err != nil {
		return nil, err
	}

	if err := readPurgeTimestamps(opts); err != nil {
		return nil, err
	}

	switch raw := viper.GetString(purgeSuffixKey); raw {
	case "", "any":
		// No variant filter; both the base tile and its @2x sibling qualify.
	case "base":
		base := ""
		opts.suffix = &base
	case "@2x":
		hidpi := "@2x"
		opts.suffix = &hidpi
	default:
		return nil, fmt.Errorf("invalid --suffix %q: expected any, base or @2x", raw)
	}

	return opts, nil
}

// readPurgeExtent reads the geographic and zoom selectors. A negative zoom is
// the "not given" marker: zoom 0 is a real level.
func readPurgeExtent(opts *purgeOptions) error {
	if raw := viper.GetString(purgeBBoxKey); raw != "" {
		bbox, err := parseBBox(raw)
		if err != nil {
			return fmt.Errorf("invalid --bbox: %w", err)
		}
		opts.bbox = bbox
		opts.hasBBox = true
	}

	if z := viper.GetInt(purgeZoomMinKey); z >= 0 {
		opts.minZoom = &z
	}
	if z := viper.GetInt(purgeZoomMaxKey); z >= 0 {
		opts.maxZoom = &z
	}
	if opts.minZoom != nil && opts.maxZoom != nil && *opts.minZoom > *opts.maxZoom {
		return fmt.Errorf("--zoom-min (%d) must be <= --zoom-max (%d)", *opts.minZoom, *opts.maxZoom)
	}

	return nil
}

// readPurgeTimestamps reads the staleness cutoffs. A malformed one fails here,
// before anything is opened: a run that ignored it would delete by the
// remaining selectors alone, which is a much larger set than intended.
func readPurgeTimestamps(opts *purgeOptions) error {
	for _, f := range []struct {
		dst *time.Time
		key string
	}{
		{&opts.dataBefore, purgeDataBeforeKey},
		{&opts.renderedBefore, purgeRenderedBefore},
	} {
		raw := viper.GetString(f.key)
		if raw == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return fmt.Errorf("invalid %s %q: expected an RFC3339 timestamp such as "+
				"2026-08-01T00:00:00Z: %w", f.key, raw, err)
		}
		*f.dst = parsed
	}
	return nil
}

// purgeFolder selects and deletes from a tile directory.
func purgeFolder(opts *purgeOptions) error {
	if _, err := os.Stat(opts.tilesDir); err != nil {
		return fmt.Errorf("tiles directory %q is not usable: %w", opts.tilesDir, err)
	}

	// walkTilesDirectory rather than scanTilesDirectory: a folder holding both
	// PNG and WebP tiles is a problem for `convert`, which has to declare one
	// format, and none at all for a command that deletes files.
	tiles, _, _, _, err := walkTilesDirectory(opts.tilesDir)
	if err != nil {
		return fmt.Errorf("failed to scan tiles directory: %w", err)
	}

	// purgeTarget is tileInfo plus a String method, field for field, so the
	// conversion is exact. Keep the two in step if either gains a field.
	targets := make([]purgeTarget, 0, len(tiles))
	for _, t := range tiles {
		targets = append(targets, purgeTarget(t))
	}

	// Opened even when nothing is being deleted: the stamp filters read it, and
	// the delete path has to remove the stamp of every tile it removes.
	stamps, err := tilestamp.OpenFolder(opts.tilesDir)
	if err != nil {
		return fmt.Errorf("failed to open the tile stamp store: %w", err)
	}
	defer closeStampStore(stamps)

	targets, err = selectTargets(targets, opts, stamps)
	if err != nil {
		return err
	}

	reportSelection(targets, opts)
	if !opts.yes || len(targets) == 0 {
		return nil
	}

	deleted := 0
	for _, t := range targets {
		if err := os.Remove(t.path); err != nil && !os.IsNotExist(err) {
			logger.Error("Failed to delete tile", "path", t.path, "error", err)
			continue
		}
		deleted++

		// The stamp describes a tile that no longer exists; leaving it behind
		// would let a later freshness run believe in a tile that is gone.
		if err := stamps.Delete(t.z, t.x, t.y, t.suffix); err != nil {
			logger.Error("Failed to delete tile stamp", "tile", t.String(), "error", err)
		}
	}

	logger.Info("Purge complete", "deleted", deleted, "tiles_dir", opts.tilesDir)
	return nil
}

// purgeMBTiles selects and deletes from an MBTiles file.
func purgeMBTiles(opts *purgeOptions) error {
	// OpenForUpdate, not New: New rewrites the metadata table, and a delete
	// command has no business redeclaring the tileset it is deleting from.
	writer, err := mbtiles.OpenForUpdate(opts.mbtilesPath)
	if err != nil {
		return fmt.Errorf("failed to open %q: %w", opts.mbtilesPath, err)
	}
	defer closeMBTilesWriter(writer)

	coords, err := writer.ListTiles(opts.minZoom, opts.maxZoom)
	if err != nil {
		return fmt.Errorf("failed to list tiles: %w", err)
	}

	targets := make([]purgeTarget, 0, len(coords))
	for _, c := range coords {
		// The tiles table has one row per z/x/y; @2x is a folder-only variant,
		// so an MBTiles tile is always the base one.
		targets = append(targets, purgeTarget{z: c.Z, x: c.X, y: c.Y})
	}

	targets, err = selectMBTilesTargets(targets, opts)
	if err != nil {
		return err
	}

	reportSelection(targets, opts)
	if !opts.yes || len(targets) == 0 {
		return nil
	}

	deleted, err := deleteMBTilesTargets(writer, targets)
	if err != nil {
		return err
	}

	logger.Info("Purge complete", "deleted", deleted, "mbtiles", opts.mbtilesPath)

	if opts.compact {
		logger.Info("Compacting MBTiles file (this rewrites it)", "mbtiles", opts.mbtilesPath)
		if err := writer.Vacuum(); err != nil {
			return fmt.Errorf("failed to compact %q: %w", opts.mbtilesPath, err)
		}
		logger.Info("Compaction complete", "mbtiles", opts.mbtilesPath)
	}

	return nil
}

// selectMBTilesTargets applies the selectors, opening the stamp store only when
// one of them needs it. The store is closed again before anything is deleted,
// so the delete path is the only writer on the file.
func selectMBTilesTargets(candidates []purgeTarget, opts *purgeOptions) ([]purgeTarget, error) {
	if !opts.stampFiltered() {
		return selectTargets(candidates, opts, nil)
	}

	stamps, err := tilestamp.OpenMBTiles(opts.mbtilesPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open the tile stamp store: %w", err)
	}
	selected, err := selectTargets(candidates, opts, stamps)
	closeStampStore(stamps)

	return selected, err
}

// deleteMBTilesTargets removes the selection in batches and reports how many
// rows went.
//
// Batched, because one transaction per tile makes fsync the cost of a purge,
// and one transaction for a whole tileset makes the rollback journal the size
// of the tileset.
func deleteMBTilesTargets(writer *mbtiles.Writer, targets []purgeTarget) (int, error) {
	deleted := 0
	batch := make([]mbtiles.TileCoord, 0, purgeDeleteBatchLimit)

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		n, err := writer.DeleteTiles(batch)
		if err != nil {
			return fmt.Errorf("failed to delete tiles: %w", err)
		}
		deleted += n
		batch = batch[:0]
		return nil
	}

	for _, t := range targets {
		batch = append(batch, mbtiles.TileCoord{Z: t.z, X: t.x, Y: t.y})
		if len(batch) >= purgeDeleteBatchLimit {
			if err := flush(); err != nil {
				return deleted, err
			}
		}
	}
	if err := flush(); err != nil {
		return deleted, err
	}

	return deleted, nil
}

// selectTargets narrows the candidates down to what the selectors ask for.
//
// stamps may be nil when no stamp selector is set. When one is, the store
// decides: a candidate is selected only if its stamp matches, so a tile with no
// stamp at all is never deleted by a staleness flag. That is the opposite of
// the render-side asymmetry, and deliberately so — there, uncertainty costs a
// re-render; here it would cost the tile.
func selectTargets(candidates []purgeTarget, opts *purgeOptions, stamps *tilestamp.Store) ([]purgeTarget, error) {
	selected := candidates

	if opts.suffix != nil {
		selected = filterTargets(selected, func(t purgeTarget) bool {
			return t.suffix == *opts.suffix
		})
	}

	if opts.minZoom != nil {
		selected = filterTargets(selected, func(t purgeTarget) bool { return t.z >= *opts.minZoom })
	}
	if opts.maxZoom != nil {
		selected = filterTargets(selected, func(t purgeTarget) bool { return t.z <= *opts.maxZoom })
	}

	if opts.hasBBox {
		inBox := bboxTileSet(opts, selected)
		selected = filterTargets(selected, func(t purgeTarget) bool {
			_, ok := inBox[tile.NewCoords(uint32(t.z), uint32(t.x), uint32(t.y))]
			return ok
		})
	}

	if !opts.stampFiltered() {
		return selected, nil
	}
	if stamps == nil {
		return nil, fmt.Errorf("a staleness selector was given but no stamp store is available")
	}

	matches, err := stamps.Query(tilestamp.Filter{
		DataBefore:     opts.dataBefore,
		RenderedBefore: opts.renderedBefore,
		RendererRevNot: opts.rendererRevNot,
		Suffix:         opts.suffix,
		MinZoom:        opts.minZoom,
		MaxZoom:        opts.maxZoom,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query tile stamps: %w", err)
	}

	stale := make(map[string]struct{}, len(matches))
	for _, s := range matches {
		stale[purgeTarget{z: s.Z, x: s.X, y: s.Y, suffix: s.Suffix}.String()] = struct{}{}
	}

	return filterTargets(selected, func(t purgeTarget) bool {
		_, ok := stale[t.String()]
		return ok
	}), nil
}

// bboxTileSet enumerates the tiles the bounding box covers, across the zoom
// levels the candidates actually occupy.
//
// Deriving the zoom range from the candidates rather than from 0..MaxZoom keeps
// the enumeration to the size of the tileset: a bbox with no zoom bounds would
// otherwise expand to 4^22 tiles at the deepest level alone.
func bboxTileSet(opts *purgeOptions, candidates []purgeTarget) map[tile.Coords]struct{} {
	if len(candidates) == 0 {
		return nil
	}

	minZ, maxZ := candidates[0].z, candidates[0].z
	for _, t := range candidates[1:] {
		if t.z < minZ {
			minZ = t.z
		}
		if t.z > maxZ {
			maxZ = t.z
		}
	}

	tiles := tile.TilesInBBox(opts.bbox, minZ, maxZ)
	set := make(map[tile.Coords]struct{}, len(tiles))
	for _, c := range tiles {
		set[c] = struct{}{}
	}
	return set
}

func filterTargets(targets []purgeTarget, keep func(purgeTarget) bool) []purgeTarget {
	out := targets[:0]
	for _, t := range targets {
		if keep(t) {
			out = append(out, t)
		}
	}
	return out
}

// reportSelection prints the count and a sample before anything happens, on
// both the dry run and the real one. The sample is what makes a wrong selector
// visible while it is still harmless.
func reportSelection(targets []purgeTarget, opts *purgeOptions) {
	target := opts.mbtilesPath
	if target == "" {
		target = opts.tilesDir
	}

	sample := make([]string, 0, purgeSampleSize)
	for _, t := range targets {
		if len(sample) == purgeSampleSize {
			break
		}
		sample = append(sample, t.String())
	}

	logger.Info("Purge selection", "tiles", len(targets), "target", target, "sample", sample)

	switch {
	case len(targets) == 0:
		logger.Info("Nothing selected; no tiles were deleted")
	case !opts.yes:
		logger.Info("Dry run: nothing was deleted. Re-run with --yes to delete the selection")
	}
}
