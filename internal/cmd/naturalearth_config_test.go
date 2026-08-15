package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"

	"github.com/cwbudde/watercolormap/internal/geojson"
	"github.com/cwbudde/watercolormap/internal/renderer"
)

// writeNaturalEarthFixture creates a directory shaped like a real
// `just fetch-natural-earth` download. Only the paths matter — nothing parses
// these files.
func writeNaturalEarthFixture(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	for _, name := range []string{
		"ne_110m_ocean.shp", "ne_110m_lakes.shp", "ne_110m_rivers_lake_centerlines.shp",
		"ne_50m_ocean.shp", "ne_50m_lakes.shp", "ne_50m_rivers_lake_centerlines.shp",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0o644); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
	}
	return dir
}

func TestNaturalEarthConfigDefaultsToDisabled(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	cfg, err := naturalEarthConfig()
	if err != nil {
		t.Fatalf("naturalEarthConfig: %v", err)
	}
	if cfg.Enabled() {
		t.Error("low-zoom rendering must stay off with no config, so existing tiles are unchanged")
	}
	if cfg.CoversZoom(0) {
		t.Error("a disabled config must not claim any zoom, or z0 would render from nothing")
	}
}

func TestNaturalEarthConfigReadsDir(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	dir := writeNaturalEarthFixture(t)
	viper.Set(naturalEarthDirKey, dir)

	cfg, err := naturalEarthConfig()
	if err != nil {
		t.Fatalf("naturalEarthConfig: %v", err)
	}
	if !cfg.Enabled() {
		t.Fatal("expected low-zoom rendering to be enabled")
	}
	if !cfg.CoversZoom(renderer.DefaultNaturalEarthMaxZoom) {
		t.Errorf("z%d must be covered by default", renderer.DefaultNaturalEarthMaxZoom)
	}
	if cfg.CoversZoom(renderer.DefaultNaturalEarthMaxZoom + 1) {
		t.Errorf("z%d must fall through to Overpass", renderer.DefaultNaturalEarthMaxZoom+1)
	}
	if cfg.ShapefileForLayer(geojson.LayerOcean, 0) == "" {
		t.Error("the ocean dataset must resolve at z0")
	}
}

func TestNaturalEarthConfigHonoursMaxZoom(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	viper.Set(naturalEarthDirKey, writeNaturalEarthFixture(t))
	viper.Set(naturalEarthMaxZoomKey, 3)

	cfg, err := naturalEarthConfig()
	if err != nil {
		t.Fatalf("naturalEarthConfig: %v", err)
	}
	if !cfg.CoversZoom(3) {
		t.Error("z3 must be covered by an explicit max-zoom of 3")
	}
	if cfg.CoversZoom(4) {
		t.Error("z4 must fall through to Overpass with max-zoom 3")
	}
}

func TestNaturalEarthConfigDisabledFlagKeepsDir(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	viper.Set(naturalEarthEnabledKey, false)
	viper.Set(naturalEarthDirKey, "/definitely/not/here")

	// enabled: false must win before validation, so switching the low-zoom
	// source off for a comparison run does not also mean deleting the path.
	cfg, err := naturalEarthConfig()
	if err != nil {
		t.Fatalf("naturalEarthConfig: %v", err)
	}
	if cfg.Enabled() {
		t.Error("expected low-zoom rendering to be disabled")
	}
}

func TestNaturalEarthConfigRejectsMissingDir(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	viper.Set(naturalEarthDirKey, filepath.Join(t.TempDir(), "absent"))

	if _, err := naturalEarthConfig(); err == nil {
		t.Error("a mistyped directory must fail at startup, not silently render an empty world")
	}
}
