package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/viper"

	"github.com/cwbudde/watercolormap/internal/pipeline"
	"github.com/cwbudde/watercolormap/internal/tilestamp"
)

// Config keys for the freshness flags. Per-command section, so underscores.
//
// `serve` has its own set rather than reading `generate`'s: the two commands
// are configured independently everywhere else, and a staleness cutoff meant
// for an overnight batch run silently re-rendering tiles under a live server
// would be a surprise. The flags, their parsing and their meaning are shared —
// see freshnessPolicyFromKeys.
const (
	staleDataBeforeKey     = "generate.stale_data_before"
	staleRenderedBeforeKey = "generate.stale_rendered_before"
	staleRendererRevKey    = "generate.stale_renderer_rev"

	serveStaleDataBeforeKey     = "serve.stale_data_before"
	serveStaleRenderedBeforeKey = "serve.stale_rendered_before"
	serveStaleRendererRevKey    = "serve.stale_renderer_rev"
)

// rendererRev identifies this binary in the stamps it writes.
//
// Built from the same ldflags-injected values `version` prints, so what a stamp
// records is what `watercolormap version` reports. A development build stamps
// "dev+none", which is honest: it says the tile came from an unidentifiable
// binary, and --stale-renderer-rev will re-render it as soon as a released one
// runs.
func rendererRev() string {
	return fmt.Sprintf("%s+%s", version, commit)
}

// freshnessPolicyFromConfig reads the --stale-* flags.
//
// All three are opt-in and the zero policy is what a run without them gets, so
// the skip-existing behaviour of every existing invocation is untouched. A
// malformed timestamp fails here, before the first tile: a run that silently
// ignored it would report success having re-rendered nothing.
func freshnessPolicyFromConfig() (pipeline.FreshnessPolicy, error) {
	return freshnessPolicyFromKeys(
		staleDataBeforeKey, staleRenderedBeforeKey, staleRendererRevKey)
}

// serveFreshnessPolicyFromConfig reads `serve`'s copy of the same flags. An
// on-demand server skips a tile it already has; with a policy set it re-renders
// the ones whose stamps say they are out of date, which is the same question
// `generate --stale-*` answers, asked one tile at a time.
func serveFreshnessPolicyFromConfig() (pipeline.FreshnessPolicy, error) {
	return freshnessPolicyFromKeys(
		serveStaleDataBeforeKey, serveStaleRenderedBeforeKey, serveStaleRendererRevKey)
}

// freshnessPolicyFromKeys is the shared parser behind both of the above, so the
// two commands cannot come to disagree about what a cutoff means.
func freshnessPolicyFromKeys(dataKey, renderedKey, revKey string) (pipeline.FreshnessPolicy, error) {
	var policy pipeline.FreshnessPolicy

	for _, f := range []struct {
		dst *time.Time
		key string
	}{
		{&policy.DataBefore, dataKey},
		{&policy.RenderedBefore, renderedKey},
	} {
		raw := viper.GetString(f.key)
		if raw == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return pipeline.FreshnessPolicy{},
				fmt.Errorf("invalid %s %q: expected an RFC3339 timestamp such as "+
					"2026-08-01T00:00:00Z: %w", f.key, raw, err)
		}
		*f.dst = parsed
	}

	policy.RendererRev = viper.GetBool(revKey)

	return policy, nil
}

// openServeStampStore opens the stamp store for a folder-backed serve run.
//
// Failure is a warning, not a startup error: stamps are bookkeeping about the
// tiles, and a server that refuses to start because it cannot write a sidecar
// would trade a whole tile service for a provenance record. That is the same
// stance the rest of the codebase takes on optional stores — a stamp that
// cannot be written is logged and the tile is still produced. The caller then
// serves unstamped, which is exactly what it did before stamps existed.
//
// Batching is turned off (size 1). The batch exists so a run writing hundreds
// of tiles a minute does not make fsync the thing it waits for; a server
// renders a tile now and then and stays up for weeks, so a buffer would hold
// stamps in memory indefinitely and lose all of them to a crash. Writing each
// stamp as it is made costs one small WAL transaction next to a Mapnik render
// and an image write, and leaves no window at all.
func openServeStampStore(tilesDir string) *tilestamp.Store {
	if tilesDir == "" {
		return nil
	}

	if err := os.MkdirAll(tilesDir, 0o755); err != nil {
		logger.Warn("Serving without tile stamps: the tile directory is not usable",
			"tiles_dir", tilesDir, "error", err)
		return nil
	}

	store, err := tilestamp.OpenFolder(tilesDir)
	if err != nil {
		logger.Warn("Serving without tile stamps: the stamp store could not be opened",
			"tiles_dir", tilesDir, "error", err)
		return nil
	}

	if err := store.SetBatchSize(1); err != nil {
		logger.Warn("Failed to make the tile stamp store write through; "+
			"stamps may be lost if the server is killed", "error", err)
	}
	return store
}

// openStampStore opens the stamp store belonging to this run's output, or
// returns nil when there is nowhere sensible to put one.
//
// The store lives with the tiles: an extra table in the .mbtiles file, or
// stamps.db in the tile folder. Both are created on demand, so a run needs no
// migration step and an existing tileset simply starts accumulating stamps for
// the tiles it re-renders.
func openStampStore(format, outputDir, outputFile string) (*tilestamp.Store, error) {
	if format == "mbtiles" {
		if outputFile == "" {
			return nil, nil
		}
		return tilestamp.OpenMBTiles(outputFile)
	}

	if outputDir == "" {
		return nil, nil
	}
	// The pipeline creates tile directories as it goes, but the stamp database
	// has to be openable before the first tile is written.
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create output dir %q: %w", outputDir, err)
	}
	return tilestamp.OpenFolder(outputDir)
}

// closeStampStore closes the store when there is one, logging failures. A
// buffered stamp lost at shutdown only costs a re-render later, so this must
// not turn into a run failure.
func closeStampStore(s *tilestamp.Store) {
	if s == nil {
		return
	}
	if err := s.Close(); err != nil {
		logger.Error("Failed to close the tile stamp store", "error", err)
	}
}

// stampStoreOption adapts the store for GeneratorOptions.
//
// A typed nil pointer assigned to the interface field would be non-nil as an
// interface, and the pipeline's "nil store means today's behaviour exactly"
// contract would quietly break. Returning the interface from one place keeps
// that mistake out of every call site.
func stampStoreOption(s *tilestamp.Store) pipeline.StampStore {
	if s == nil {
		return nil
	}
	return s
}
