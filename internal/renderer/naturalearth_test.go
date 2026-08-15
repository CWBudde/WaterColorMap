package renderer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cwbudde/watercolormap/internal/geojson"
)

// neDir writes empty stand-ins for every dataset `just fetch-natural-earth`
// produces. ShapefileForLayer only stats, never parses, so the contents do not
// matter — what is under test is which path gets chosen.
func neDir(t *testing.T, names ...string) string {
	t.Helper()

	dir := t.TempDir()
	if len(names) == 0 {
		names = []string{
			"ne_110m_ocean.shp", "ne_110m_lakes.shp", "ne_110m_rivers_lake_centerlines.shp",
			"ne_50m_ocean.shp", "ne_50m_lakes.shp", "ne_50m_rivers_lake_centerlines.shp",
		}
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("not really a shapefile"), 0o644); err != nil {
			t.Fatalf("failed to write %s: %v", name, err)
		}
	}
	return dir
}

func TestNaturalEarthConfigEnabled(t *testing.T) {
	if (NaturalEarthConfig{}).Enabled() {
		t.Error("the zero value must be disabled: an unconfigured build has to render exactly as it did before")
	}
	if !(NaturalEarthConfig{Dir: "data/natural-earth"}).Enabled() {
		t.Error("a configured directory must enable it")
	}
}

func TestNaturalEarthConfigCoversZoom(t *testing.T) {
	tests := []struct {
		name    string
		cfg     NaturalEarthConfig
		zoom    int
		covered bool
	}{
		{name: "disabled covers nothing", cfg: NaturalEarthConfig{}, zoom: 0, covered: false},
		{name: "world tile", cfg: NaturalEarthConfig{Dir: "d"}, zoom: 0, covered: true},
		{name: "default ceiling", cfg: NaturalEarthConfig{Dir: "d"}, zoom: DefaultNaturalEarthMaxZoom, covered: true},
		{name: "one past the ceiling", cfg: NaturalEarthConfig{Dir: "d"}, zoom: DefaultNaturalEarthMaxZoom + 1, covered: false},
		{name: "regional zoom", cfg: NaturalEarthConfig{Dir: "d"}, zoom: 13, covered: false},
		{name: "explicit ceiling", cfg: NaturalEarthConfig{Dir: "d", MaxZoom: 7}, zoom: 7, covered: true},
		{name: "past an explicit ceiling", cfg: NaturalEarthConfig{Dir: "d", MaxZoom: 7}, zoom: 8, covered: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.CoversZoom(tt.zoom); got != tt.covered {
				t.Errorf("CoversZoom(%d) = %v, want %v", tt.zoom, got, tt.covered)
			}
		})
	}
}

// TestNaturalEarthShapefileForLayer pins the layer-and-scale selection, which is
// the whole of the "zoom range strategy": which dataset answers, and — just as
// important — which layers do not exist at all below z6.
func TestNaturalEarthShapefileForLayer(t *testing.T) {
	dir := neDir(t)
	cfg := NaturalEarthConfig{Dir: dir}

	tests := []struct {
		name  string
		layer geojson.LayerType
		want  string
		zoom  int
	}{
		{name: "ocean at z0 uses 110m", layer: geojson.LayerOcean, zoom: 0, want: "ne_110m_ocean.shp"},
		{name: "lakes at z2 uses 110m", layer: geojson.LayerWater, zoom: 2, want: "ne_110m_lakes.shp"},
		{
			name:  "rivers at z2 uses 110m",
			layer: geojson.LayerRivers,
			zoom:  2,
			want:  "ne_110m_rivers_lake_centerlines.shp",
		},
		{name: "ocean at z3 switches to 50m", layer: geojson.LayerOcean, zoom: 3, want: "ne_50m_ocean.shp"},
		{name: "lakes at the ceiling", layer: geojson.LayerWater, zoom: DefaultNaturalEarthMaxZoom, want: "ne_50m_lakes.shp"},

		// Above the ceiling Overpass takes over, so nothing resolves here even
		// though the files exist.
		{name: "ocean above the ceiling", layer: geojson.LayerOcean, zoom: DefaultNaturalEarthMaxZoom + 1, want: ""},

		// The layers Natural Earth does not carry. Their absence is the
		// low-zoom style: at world scale there are no roads or buildings.
		{name: "roads never", layer: geojson.LayerRoads, zoom: 2, want: ""},
		{name: "highways never", layer: geojson.LayerHighways, zoom: 2, want: ""},
		{name: "railroads never", layer: geojson.LayerRailroads, zoom: 2, want: ""},
		{name: "buildings never", layer: geojson.LayerBuildings, zoom: 2, want: ""},
		{name: "civic never", layer: geojson.LayerCivic, zoom: 2, want: ""},
		{name: "urban never", layer: geojson.LayerUrban, zoom: 2, want: ""},
		{name: "parks never", layer: geojson.LayerParks, zoom: 2, want: ""},
		{name: "land never", layer: geojson.LayerLand, zoom: 2, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cfg.ShapefileForLayer(tt.layer, tt.zoom)

			if tt.want == "" {
				if got != "" {
					t.Fatalf("ShapefileForLayer(%s, %d) = %q, want \"\"", tt.layer, tt.zoom, got)
				}
				return
			}

			want, err := filepath.Abs(filepath.Join(dir, tt.want))
			if err != nil {
				t.Fatalf("failed to absolutize expected path: %v", err)
			}
			if got != want {
				t.Fatalf("ShapefileForLayer(%s, %d) = %q, want %q", tt.layer, tt.zoom, got, want)
			}
		})
	}
}

// TestNaturalEarthShapefileIsAbsolute pins the one thing that is silent when it
// breaks. Mapnik resolves a relative datasource path against the directory of
// the XML it was loaded from, and LoadXML writes that XML to a temp file, so a
// relative path is looked up next to /tmp and the layer vanishes without an
// error. The ocean pass had exactly this bug.
func TestNaturalEarthShapefileIsAbsolute(t *testing.T) {
	dir := neDir(t)

	rel, err := filepath.Rel(mustGetwd(t), dir)
	if err != nil {
		t.Skipf("cannot express %s relative to the working directory: %v", dir, err)
	}

	got := NaturalEarthConfig{Dir: rel}.ShapefileForLayer(geojson.LayerOcean, 0)
	if !filepath.IsAbs(got) {
		t.Fatalf("ShapefileForLayer returned a relative path %q; Mapnik would look it up next to /tmp", got)
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	return wd
}

// TestNaturalEarthMissingDatasetIsAbsentNotFatal pins the per-dataset stance: a
// partial download costs the layers it is missing, not the whole tile. This is
// the same trade OceanConfig makes — a wrong-detail coastline beats an inverted
// one — and it is why Validate exists to catch the typo case separately.
func TestNaturalEarthMissingDatasetIsAbsentNotFatal(t *testing.T) {
	dir := neDir(t, "ne_110m_ocean.shp")
	cfg := NaturalEarthConfig{Dir: dir}

	if got := cfg.ShapefileForLayer(geojson.LayerOcean, 0); got == "" {
		t.Error("the dataset that is present must still resolve")
	}
	if got := cfg.ShapefileForLayer(geojson.LayerWater, 0); got != "" {
		t.Errorf("a missing dataset must resolve to \"\", got %q", got)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("a partial download must still validate: %v", err)
	}
}

func TestNaturalEarthConfigValidate(t *testing.T) {
	t.Run("zero value is valid", func(t *testing.T) {
		if err := (NaturalEarthConfig{}).Validate(); err != nil {
			t.Errorf("the zero value must validate (it means disabled): %v", err)
		}
	})

	t.Run("a real download validates", func(t *testing.T) {
		if err := (NaturalEarthConfig{Dir: neDir(t)}).Validate(); err != nil {
			t.Errorf("a complete download must validate: %v", err)
		}
	})

	t.Run("a missing directory is a startup error", func(t *testing.T) {
		err := NaturalEarthConfig{Dir: filepath.Join(t.TempDir(), "definitely-not-here")}.Validate()
		if err == nil {
			t.Fatal("a mistyped directory must fail before the first tile, not render an empty world")
		}
	})

	t.Run("an empty directory is a startup error", func(t *testing.T) {
		if err := (NaturalEarthConfig{Dir: t.TempDir()}).Validate(); err == nil {
			t.Fatal("a directory holding no datasets must not pass as a working configuration")
		}
	})

	t.Run("a file instead of a directory is an error", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "natural-earth")
		if err := os.WriteFile(file, []byte("not a directory"), 0o644); err != nil {
			t.Fatalf("failed to write the stand-in file: %v", err)
		}
		if err := (NaturalEarthConfig{Dir: file}).Validate(); err == nil {
			t.Fatal("a file must not pass as the data directory")
		}
	})

	t.Run("a negative ceiling is an error", func(t *testing.T) {
		if err := (NaturalEarthConfig{Dir: neDir(t), MaxZoom: -1}).Validate(); err == nil {
			t.Fatal("a negative max-zoom must be rejected")
		}
	})
}
