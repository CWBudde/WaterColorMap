package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

func writeShapefileFixture(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("fixture"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestOceanConfigDefaultsToDisabled(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	cfg, err := oceanConfig()
	if err != nil {
		t.Fatalf("oceanConfig: %v", err)
	}
	if cfg.Enabled() {
		t.Error("ocean rendering must stay off with no config, so existing tiles are unchanged")
	}
}

func TestOceanConfigReadsPaths(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	full := writeShapefileFixture(t, "water_polygons.shp")
	simplified := writeShapefileFixture(t, "simplified_water_polygons.shp")

	viper.Set(oceanShapefileKey, full)
	viper.Set(oceanSimplifiedKey, simplified)
	viper.Set(oceanSimplifiedMaxZoomKey, 8)

	cfg, err := oceanConfig()
	if err != nil {
		t.Fatalf("oceanConfig: %v", err)
	}
	if !cfg.Enabled() {
		t.Fatal("expected ocean rendering to be enabled")
	}
	if got := cfg.ShapefileForZoom(8); got != simplified {
		t.Errorf("z8 = %q, want the simplified set %q", got, simplified)
	}
	if got := cfg.ShapefileForZoom(9); got != full {
		t.Errorf("z9 = %q, want the full set %q", got, full)
	}
}

func TestOceanConfigDisabledFlagKeepsPaths(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	viper.Set(oceanEnabledKey, false)
	viper.Set(oceanShapefileKey, "/definitely/not/here.shp")

	// enabled: false must win before validation, so switching ocean off for a
	// comparison run does not also have to mean deleting the paths.
	cfg, err := oceanConfig()
	if err != nil {
		t.Fatalf("oceanConfig: %v", err)
	}
	if cfg.Enabled() {
		t.Error("expected ocean rendering to be disabled")
	}
}

func TestOceanConfigRejectsMissingShapefile(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	viper.Set(oceanShapefileKey, filepath.Join(t.TempDir(), "absent.shp"))

	if _, err := oceanConfig(); err == nil {
		t.Error("a mistyped path must fail at startup, not silently render tan oceans")
	}
}
