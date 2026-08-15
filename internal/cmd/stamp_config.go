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
const (
	staleDataBeforeKey     = "generate.stale_data_before"
	staleRenderedBeforeKey = "generate.stale_rendered_before"
	staleRendererRevKey    = "generate.stale_renderer_rev"
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
	var policy pipeline.FreshnessPolicy

	for _, f := range []struct {
		dst *time.Time
		key string
	}{
		{&policy.DataBefore, staleDataBeforeKey},
		{&policy.RenderedBefore, staleRenderedBeforeKey},
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

	policy.RendererRev = viper.GetBool(staleRendererRevKey)

	return policy, nil
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
