package cmd

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"

	"github.com/cwbudde/watercolormap/internal/mbtiles"
	"github.com/cwbudde/watercolormap/internal/tilestamp"
)

// withCommandFlags sets command config keys for one test and restores viper and the
// package logger afterwards. viper is process-global, so leaking a key here
// would change the meaning of another test.
func withCommandFlags(t *testing.T, flags map[string]any) {
	t.Helper()

	previous := make(map[string]any, len(flags))
	for k := range flags {
		previous[k] = viper.Get(k)
	}
	for k, v := range flags {
		viper.Set(k, v)
	}
	t.Cleanup(func() {
		for k, v := range previous {
			viper.Set(k, v)
		}
	})

	prevLogger := logger
	logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	t.Cleanup(func() { logger = prevLogger })
}

// seedTilesDir writes a flat tile folder and returns its path.
func seedTilesDir(t *testing.T, names []string) string {
	t.Helper()

	dir := t.TempDir()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("tile"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func remainingTiles(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	var names []string
	for _, e := range entries {
		name := e.Name()
		// The stamp database lives in the tile folder and is not a tile.
		if name == tilestamp.FolderDBName || filepath.Ext(name) == ".db" ||
			filepath.Ext(name) == ".db-wal" || filepath.Ext(name) == ".db-shm" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// The default is a dry run: a selection is reported and nothing is removed.
func TestPurgeFolderDryRunDeletesNothing(t *testing.T) {
	files := []string{"z13_x1_y2.png", "z14_x3_y4.png"}
	dir := seedTilesDir(t, files)

	withCommandFlags(t, map[string]any{
		purgeTilesDirKey: dir,
		purgeMBTilesKey:  "",
		purgeBBoxKey:     "",
		purgeZoomMinKey:  -1,
		purgeZoomMaxKey:  -1,
		purgeSuffixKey:   "any",
		purgeYesKey:      false,
		purgeCompactKey:  false,
	})

	if err := runPurge(purgeCmd, nil); err != nil {
		t.Fatalf("runPurge: %v", err)
	}

	got := remainingTiles(t, dir)
	if len(got) != len(files) {
		t.Errorf("remaining tiles = %v, want all %v: a dry run must delete nothing", got, files)
	}
}

// --yes deletes exactly the selected set, and nothing outside it.
func TestPurgeFolderZoomSelection(t *testing.T) {
	dir := seedTilesDir(t, []string{
		"z12_x1_y1.png", "z13_x1_y2.png", "z13_x1_y2@2x.png", "z14_x3_y4.png",
	})

	withCommandFlags(t, map[string]any{
		purgeTilesDirKey: dir,
		purgeMBTilesKey:  "",
		purgeBBoxKey:     "",
		purgeZoomMinKey:  13,
		purgeZoomMaxKey:  13,
		purgeSuffixKey:   "any",
		purgeYesKey:      true,
		purgeCompactKey:  false,
	})

	if err := runPurge(purgeCmd, nil); err != nil {
		t.Fatalf("runPurge: %v", err)
	}

	want := []string{"z12_x1_y1.png", "z14_x3_y4.png"}
	got := remainingTiles(t, dir)
	if len(got) != len(want) {
		t.Fatalf("remaining tiles = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("remaining tiles = %v, want %v", got, want)
			break
		}
	}
}

// --suffix picks one variant of a tile and leaves the other.
func TestPurgeFolderSuffixSelection(t *testing.T) {
	dir := seedTilesDir(t, []string{"z13_x1_y2.png", "z13_x1_y2@2x.png"})

	withCommandFlags(t, map[string]any{
		purgeTilesDirKey: dir,
		purgeMBTilesKey:  "",
		purgeBBoxKey:     "",
		purgeZoomMinKey:  -1,
		purgeZoomMaxKey:  -1,
		purgeSuffixKey:   "@2x",
		purgeYesKey:      true,
		purgeCompactKey:  false,
	})

	if err := runPurge(purgeCmd, nil); err != nil {
		t.Fatalf("runPurge: %v", err)
	}

	got := remainingTiles(t, dir)
	if len(got) != 1 || got[0] != "z13_x1_y2.png" {
		t.Errorf("remaining tiles = %v, want just the base tile", got)
	}
}

// The staleness selectors read the stamps: a stale tile goes, a fresh one and
// an unstamped one both stay. The unstamped case is the important one —
// deletion is not undoable, so an unknown data version must never count as old.
func TestPurgeFolderDataBeforeSelection(t *testing.T) {
	dir := seedTilesDir(t, []string{
		"z13_x1_y1.png", // stale stamp
		"z13_x2_y2.png", // fresh stamp
		"z13_x3_y3.png", // no stamp at all
	})

	store, err := tilestamp.OpenFolder(dir)
	if err != nil {
		t.Fatalf("OpenFolder: %v", err)
	}
	stale := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fresh := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for _, s := range []tilestamp.Stamp{
		{Z: 13, X: 1, Y: 1, OSMBase: stale, RenderedAt: stale},
		{Z: 13, X: 2, Y: 2, OSMBase: fresh, RenderedAt: fresh},
	} {
		if err := store.Put(s); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	withCommandFlags(t, map[string]any{
		purgeTilesDirKey:    dir,
		purgeMBTilesKey:     "",
		purgeBBoxKey:        "",
		purgeZoomMinKey:     -1,
		purgeZoomMaxKey:     -1,
		purgeSuffixKey:      "any",
		purgeDataBeforeKey:  "2026-06-01T00:00:00Z",
		purgeRenderedBefore: "",
		purgeRendererRevNot: "",
		purgeYesKey:         true,
		purgeCompactKey:     false,
	})

	if err := runPurge(purgeCmd, nil); err != nil {
		t.Fatalf("runPurge: %v", err)
	}

	want := []string{"z13_x2_y2.png", "z13_x3_y3.png"}
	got := remainingTiles(t, dir)
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("remaining tiles = %v, want %v", got, want)
	}

	// The purged tile's stamp must go with it.
	store, err = tilestamp.OpenFolder(dir)
	if err != nil {
		t.Fatalf("OpenFolder: %v", err)
	}
	defer store.Close() // nolint:errcheck

	if _, ok, err := store.Get(13, 1, 1, ""); err != nil || ok {
		t.Errorf("stamp for the deleted tile still present (ok=%v, err=%v)", ok, err)
	}
	if _, ok, err := store.Get(13, 2, 2, ""); err != nil || !ok {
		t.Errorf("stamp for the surviving tile was removed (ok=%v, err=%v)", ok, err)
	}
}

// seedMBTiles writes a tileset holding the given XYZ tiles.
func seedMBTiles(t *testing.T, tiles []mbtiles.TileCoord) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "tiles.mbtiles")
	w, err := mbtiles.New(path, mbtiles.Metadata{Name: "test", Format: mbtiles.FormatPNG})
	if err != nil {
		t.Fatalf("mbtiles.New: %v", err)
	}
	for _, c := range tiles {
		if err := w.WriteTile(c.Z, c.X, c.Y, []byte("tile")); err != nil {
			t.Fatalf("WriteTile: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return path
}

func mbtilesTiles(t *testing.T, path string) []mbtiles.TileCoord {
	t.Helper()

	w, err := mbtiles.OpenForUpdate(path)
	if err != nil {
		t.Fatalf("OpenForUpdate: %v", err)
	}
	defer w.Close() // nolint:errcheck

	got, err := w.ListTiles(nil, nil)
	if err != nil {
		t.Fatalf("ListTiles: %v", err)
	}
	return got
}

func TestPurgeMBTilesDryRunDeletesNothing(t *testing.T) {
	tiles := []mbtiles.TileCoord{{Z: 5, X: 1, Y: 1}, {Z: 6, X: 2, Y: 2}}
	path := seedMBTiles(t, tiles)

	withCommandFlags(t, map[string]any{
		purgeTilesDirKey: "",
		purgeMBTilesKey:  path,
		purgeBBoxKey:     "",
		purgeZoomMinKey:  -1,
		purgeZoomMaxKey:  -1,
		purgeSuffixKey:   "any",
		purgeYesKey:      false,
		purgeCompactKey:  false,
	})

	if err := runPurge(purgeCmd, nil); err != nil {
		t.Fatalf("runPurge: %v", err)
	}

	if got := mbtilesTiles(t, path); len(got) != len(tiles) {
		t.Errorf("remaining tiles = %v, want all %v", got, tiles)
	}
}

func TestPurgeMBTilesDeletesExactlyTheSelection(t *testing.T) {
	tiles := []mbtiles.TileCoord{
		{Z: 5, X: 1, Y: 1}, {Z: 6, X: 2, Y: 2}, {Z: 6, X: 3, Y: 30}, {Z: 7, X: 4, Y: 4},
	}
	path := seedMBTiles(t, tiles)

	withCommandFlags(t, map[string]any{
		purgeTilesDirKey: "",
		purgeMBTilesKey:  path,
		purgeBBoxKey:     "",
		purgeZoomMinKey:  6,
		purgeZoomMaxKey:  6,
		purgeSuffixKey:   "any",
		purgeYesKey:      true,
		// Also exercise the VACUUM path: it must leave the surviving rows alone.
		purgeCompactKey: true,
	})

	if err := runPurge(purgeCmd, nil); err != nil {
		t.Fatalf("runPurge: %v", err)
	}

	want := []mbtiles.TileCoord{{Z: 5, X: 1, Y: 1}, {Z: 7, X: 4, Y: 4}}
	got := mbtilesTiles(t, path)
	if len(got) != len(want) {
		t.Fatalf("remaining tiles = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("remaining tiles = %v, want %v", got, want)
			break
		}
	}
}

// A bounding box selects by geography, at the zoom levels the tileset actually
// holds.
func TestPurgeMBTilesBBoxSelection(t *testing.T) {
	// z1 splits the world into four tiles; 0/0 is the north-west quadrant.
	tiles := []mbtiles.TileCoord{{Z: 1, X: 0, Y: 0}, {Z: 1, X: 1, Y: 1}}
	path := seedMBTiles(t, tiles)

	withCommandFlags(t, map[string]any{
		purgeTilesDirKey: "",
		purgeMBTilesKey:  path,
		// A small box well inside the north-west quadrant.
		purgeBBoxKey:    "-100,40,-90,50",
		purgeZoomMinKey: -1,
		purgeZoomMaxKey: -1,
		purgeSuffixKey:  "any",
		purgeYesKey:     true,
		purgeCompactKey: false,
	})

	if err := runPurge(purgeCmd, nil); err != nil {
		t.Fatalf("runPurge: %v", err)
	}

	got := mbtilesTiles(t, path)
	if len(got) != 1 || got[0] != (mbtiles.TileCoord{Z: 1, X: 1, Y: 1}) {
		t.Errorf("remaining tiles = %v, want just 1/1/1", got)
	}
}

func TestPurgeOptionsValidation(t *testing.T) {
	tests := []struct {
		name  string
		flags map[string]any
		want  string
	}{
		{
			name:  "no target",
			flags: map[string]any{purgeTilesDirKey: "", purgeMBTilesKey: ""},
			want:  "one of --tiles-dir or --mbtiles is required",
		},
		{
			name:  "two targets",
			flags: map[string]any{purgeTilesDirKey: "/tmp/a", purgeMBTilesKey: "/tmp/b.mbtiles"},
			want:  "mutually exclusive",
		},
		{
			name: "inverted zoom range",
			flags: map[string]any{
				purgeTilesDirKey: "/tmp/a", purgeMBTilesKey: "",
				purgeZoomMinKey: 10, purgeZoomMaxKey: 5,
			},
			want: "must be <=",
		},
		{
			name: "unparseable timestamp",
			flags: map[string]any{
				purgeTilesDirKey: "/tmp/a", purgeMBTilesKey: "",
				purgeZoomMinKey: -1, purgeZoomMaxKey: -1,
				purgeDataBeforeKey: "last tuesday",
			},
			want: "RFC3339",
		},
		{
			name: "unknown suffix",
			flags: map[string]any{
				purgeTilesDirKey: "/tmp/a", purgeMBTilesKey: "",
				purgeZoomMinKey: -1, purgeZoomMaxKey: -1,
				purgeSuffixKey: "@3x",
			},
			want: "invalid --suffix",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flags := map[string]any{
				purgeTilesDirKey: "", purgeMBTilesKey: "", purgeBBoxKey: "",
				purgeZoomMinKey: -1, purgeZoomMaxKey: -1, purgeSuffixKey: "any",
				purgeDataBeforeKey: "", purgeRenderedBefore: "", purgeRendererRevNot: "",
			}
			for k, v := range tt.flags {
				flags[k] = v
			}
			withCommandFlags(t, flags)

			_, err := purgeOptionsFromConfig()
			if err == nil {
				t.Fatalf("expected an error mentioning %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

// A bounding box is tested against the tiles that exist, never enumerated. A
// z22 tile under a wide box covers 4^22 theoretical tiles at that level alone,
// so an implementation that materialises the box does not answer this test
// slowly — it does not answer it.
func TestPurgeFolderBBoxWithDeepZoom(t *testing.T) {
	// 22/2210747/1378792 is in Hanover; 22/2097152/2097152 is null island.
	dir := seedTilesDir(t, []string{
		"z22_x2210747_y1378792.png",
		"z22_x2097152_y2097152.png",
	})

	withCommandFlags(t, map[string]any{
		purgeTilesDirKey: dir,
		purgeMBTilesKey:  "",
		purgeBBoxKey:     "9.7,52.3,9.8,52.4",
		purgeZoomMinKey:  -1,
		purgeZoomMaxKey:  -1,
		purgeSuffixKey:   "any",
		purgeYesKey:      true,
		purgeCompactKey:  false,
	})

	if err := runPurge(purgeCmd, nil); err != nil {
		t.Fatalf("runPurge: %v", err)
	}

	got := remainingTiles(t, dir)
	if len(got) != 1 || got[0] != "z22_x2097152_y2097152.png" {
		t.Errorf("remaining tiles = %v, want only the tile outside the bbox", got)
	}
}

// A dry run promises to change nothing, and a tile folder that has never been
// stamped must still be one after purge has reported on it.
func TestPurgeFolderDryRunCreatesNoStampStore(t *testing.T) {
	dir := seedTilesDir(t, []string{"z13_x1_y2.png"})

	withCommandFlags(t, map[string]any{
		purgeTilesDirKey:    dir,
		purgeMBTilesKey:     "",
		purgeBBoxKey:        "",
		purgeZoomMinKey:     -1,
		purgeZoomMaxKey:     -1,
		purgeSuffixKey:      "any",
		purgeDataBeforeKey:  "2026-06-01T00:00:00Z",
		purgeRenderedBefore: "",
		purgeRendererRevNot: "",
		purgeYesKey:         false,
		purgeCompactKey:     false,
	})

	if err := runPurge(purgeCmd, nil); err != nil {
		t.Fatalf("runPurge: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, tilestamp.FolderDBName)); !os.IsNotExist(err) {
		t.Errorf("a dry run created %s in the tile folder", tilestamp.FolderDBName)
	}
	if got := remainingTiles(t, dir); len(got) != 1 {
		t.Errorf("remaining tiles = %v, want the tile untouched", got)
	}
}
