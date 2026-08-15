package renderer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOceanConfigEnabled(t *testing.T) {
	if (OceanConfig{}).Enabled() {
		t.Error("the zero config must be disabled, otherwise ocean rendering is not opt-in")
	}
	if !(OceanConfig{FullPath: "a.shp"}).Enabled() {
		t.Error("a configured full shapefile should enable ocean rendering")
	}
	if !(OceanConfig{SimplifiedPath: "a.shp"}).Enabled() {
		t.Error("a configured simplified shapefile should enable ocean rendering")
	}
}

func TestOceanConfigShapefileForZoom(t *testing.T) {
	both := OceanConfig{FullPath: "full.shp", SimplifiedPath: "simple.shp"}

	tests := []struct {
		name string
		want string
		cfg  OceanConfig
		zoom int
	}{
		{name: "default cutoff, low zoom", cfg: both, zoom: 5, want: "simple.shp"},
		{name: "default cutoff, at the boundary", cfg: both, zoom: DefaultSimplifiedMaxZoom, want: "simple.shp"},
		{name: "default cutoff, one past it", cfg: both, zoom: DefaultSimplifiedMaxZoom + 1, want: "full.shp"},
		{name: "default cutoff, high zoom", cfg: both, zoom: 16, want: "full.shp"},
		{
			name: "explicit cutoff overrides the default",
			cfg:  OceanConfig{FullPath: "full.shp", SimplifiedPath: "simple.shp", SimplifiedMaxZoom: 12},
			zoom: 11, want: "simple.shp",
		},
		{
			name: "explicit cutoff, one past it",
			cfg:  OceanConfig{FullPath: "full.shp", SimplifiedPath: "simple.shp", SimplifiedMaxZoom: 12},
			zoom: 13, want: "full.shp",
		},
		{
			name: "only simplified configured, high zoom falls back to it",
			cfg:  OceanConfig{SimplifiedPath: "simple.shp"},
			zoom: 15, want: "simple.shp",
		},
		{
			name: "only full configured, low zoom falls back to it",
			cfg:  OceanConfig{FullPath: "full.shp"},
			zoom: 3, want: "full.shp",
		},
		{name: "disabled config renders no ocean", cfg: OceanConfig{}, zoom: 9, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Paths come back absolute — Mapnik resolves a relative datasource
			// against the temp XML it was loaded from, not the working
			// directory, so ShapefileForZoom absolutizes.
			want := tt.want
			if want != "" {
				abs, err := filepath.Abs(want)
				if err != nil {
					t.Fatalf("filepath.Abs(%q): %v", want, err)
				}
				want = abs
			}
			if got := tt.cfg.ShapefileForZoom(tt.zoom); got != want {
				t.Errorf("ShapefileForZoom(%d) = %q, want %q", tt.zoom, got, want)
			}
		})
	}
}

func TestOceanConfigValidate(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "water_polygons.shp")
	if err := os.WriteFile(present, []byte("not really a shapefile"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	t.Run("disabled config is valid", func(t *testing.T) {
		if err := (OceanConfig{}).Validate(); err != nil {
			t.Errorf("the zero config must validate: %v", err)
		}
	})

	t.Run("existing paths are valid", func(t *testing.T) {
		if err := (OceanConfig{FullPath: present, SimplifiedPath: present}).Validate(); err != nil {
			t.Errorf("Validate: %v", err)
		}
	})

	t.Run("a missing shapefile is an error", func(t *testing.T) {
		err := (OceanConfig{FullPath: filepath.Join(dir, "absent.shp")}).Validate()
		if err == nil {
			t.Fatal("expected an error for a path that does not exist")
		}
	})

	t.Run("a missing simplified shapefile is an error", func(t *testing.T) {
		err := (OceanConfig{SimplifiedPath: filepath.Join(dir, "absent.shp")}).Validate()
		if err == nil {
			t.Fatal("expected an error for a path that does not exist")
		}
	})

	t.Run("a negative cutoff is an error", func(t *testing.T) {
		if err := (OceanConfig{FullPath: present, SimplifiedMaxZoom: -1}).Validate(); err == nil {
			t.Fatal("expected an error for a negative simplified-max-zoom")
		}
	})
}
