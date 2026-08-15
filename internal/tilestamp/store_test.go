package tilestamp

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func mustOpen(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "stamps.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return store
}

func TestPutGetRoundTrip(t *testing.T) {
	store := mustOpen(t)

	want := Stamp{
		Z: 13, X: 4297, Y: 2754, Suffix: "",
		OSMBase:     time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC),
		RenderedAt:  time.Date(2026, 8, 15, 9, 30, 0, 0, time.UTC),
		Source:      "https://overpass-api.de/api/interpreter",
		RendererRev: "v1.2.3+abc1234",
	}

	if err := store.Put(want); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Before the flush the stamp is only in the buffer, which Get has to see.
	got, ok, err := store.Get(want.Z, want.X, want.Y, "")
	if err != nil || !ok {
		t.Fatalf("Get before flush = %v, %v, %v", got, ok, err)
	}
	if got != want {
		t.Errorf("Get before flush = %+v, want %+v", got, want)
	}

	if err := store.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	got, ok, err = store.Get(want.Z, want.X, want.Y, "")
	if err != nil || !ok {
		t.Fatalf("Get after flush = %v, %v, %v", got, ok, err)
	}
	if !got.OSMBase.Equal(want.OSMBase) || !got.RenderedAt.Equal(want.RenderedAt) {
		t.Errorf("timestamps did not round-trip: got %+v, want %+v", got, want)
	}
	if got.Source != want.Source || got.RendererRev != want.RendererRev {
		t.Errorf("Get after flush = %+v, want %+v", got, want)
	}
}

func TestGetMissing(t *testing.T) {
	store := mustOpen(t)

	if _, ok, err := store.Get(5, 1, 1, ""); err != nil || ok {
		t.Fatalf("Get(missing) = %v, %v, want false, nil", ok, err)
	}
}

// The base tile and its @2x sibling are separate tiles, so they must be
// separate rows rather than one overwriting the other.
func TestSuffixIsPartOfTheKey(t *testing.T) {
	store := mustOpen(t)

	base := Stamp{Z: 10, X: 1, Y: 2, RenderedAt: time.Now(), Source: "base"}
	hidpi := base
	hidpi.Suffix = "@2x"
	hidpi.Source = "hidpi"

	for _, s := range []Stamp{base, hidpi} {
		if err := store.Put(s); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	for _, tc := range []struct{ suffix, want string }{{"", "base"}, {"@2x", "hidpi"}} {
		got, ok, err := store.Get(10, 1, 2, tc.suffix)
		if err != nil || !ok {
			t.Fatalf("Get(%q) = %v, %v", tc.suffix, ok, err)
		}
		if got.Source != tc.want {
			t.Errorf("Get(%q).Source = %q, want %q", tc.suffix, got.Source, tc.want)
		}
	}
}

func TestPutReplaces(t *testing.T) {
	store := mustOpen(t)

	first := Stamp{Z: 3, X: 1, Y: 1, RenderedAt: time.Now(), RendererRev: "old"}
	if err := store.Put(first); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	second := first
	second.RendererRev = "new"
	if err := store.Put(second); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	all, err := store.Query(Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("Query returned %d stamps, want 1", len(all))
	}
	if all[0].RendererRev != "new" {
		t.Errorf("RendererRev = %q, want %q", all[0].RendererRev, "new")
	}
}

// A full batch must reach the database without anyone calling Flush, and the
// entries that follow must still be visible through Get.
func TestBatchFlushes(t *testing.T) {
	store := mustOpen(t)

	total := DefaultBatchSize + 7
	for i := 0; i < total; i++ {
		if err := store.Put(Stamp{Z: 12, X: i, Y: 5, RenderedAt: time.Now()}); err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
	}

	// Count straight from SQL: Query would flush first and hide the point.
	db, err := sql.Open("sqlite", store.Path())
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close() // nolint:errcheck

	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM tile_stamp").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != DefaultBatchSize {
		t.Errorf("committed rows = %d, want %d (one automatic flush)", n, DefaultBatchSize)
	}

	for i := 0; i < total; i++ {
		if _, ok, err := store.Get(12, i, 5, ""); err != nil || !ok {
			t.Fatalf("Get(12/%d/5) = %v, %v; every stamp must be visible", i, ok, err)
		}
	}
}

func TestDelete(t *testing.T) {
	store := mustOpen(t)

	// One flushed, one still buffered: both have to go.
	if err := store.Put(Stamp{Z: 1, X: 0, Y: 0, RenderedAt: time.Now()}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if err := store.Put(Stamp{Z: 1, X: 1, Y: 0, RenderedAt: time.Now()}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	for _, x := range []int{0, 1} {
		if err := store.Delete(1, x, 0, ""); err != nil {
			t.Fatalf("Delete(1/%d/0): %v", x, err)
		}
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	all, err := store.Query(Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("Query returned %+v, want nothing", all)
	}

	// Deleting what is not there is a no-op, not an error.
	if err := store.Delete(9, 9, 9, ""); err != nil {
		t.Errorf("Delete(missing) = %v, want nil", err)
	}
}

func TestQueryFilters(t *testing.T) {
	store := mustOpen(t)

	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	cutoff := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	stamps := []Stamp{
		{Z: 10, X: 1, Y: 1, OSMBase: old, RenderedAt: old, RendererRev: "a"},
		{Z: 11, X: 2, Y: 2, OSMBase: recent, RenderedAt: recent, RendererRev: "b"},
		{Z: 12, X: 3, Y: 3, RenderedAt: recent, RendererRev: "a"}, // no source timestamp
		{Z: 12, X: 3, Y: 3, Suffix: "@2x", OSMBase: old, RenderedAt: recent, RendererRev: "a"},
	}
	for _, s := range stamps {
		if err := store.Put(s); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}

	base := ""
	hidpi := "@2x"
	minZoom := 11
	maxZoom := 11

	tests := []struct {
		name   string
		filter Filter
		want   int
	}{
		{"no filter matches everything", Filter{}, 4},
		// The z12 base tile has no osm_base_ts and must not match: unknown is
		// not old.
		{"data before", Filter{DataBefore: cutoff}, 2},
		{"rendered before", Filter{RenderedBefore: cutoff}, 1},
		{"renderer rev differs", Filter{RendererRevNot: "a"}, 1},
		{"suffix base", Filter{Suffix: &base}, 3},
		{"suffix hidpi", Filter{Suffix: &hidpi}, 1},
		{"zoom range", Filter{MinZoom: &minZoom, MaxZoom: &maxZoom}, 1},
		{"combined", Filter{DataBefore: cutoff, Suffix: &hidpi}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := store.Query(tt.filter)
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			if len(got) != tt.want {
				t.Errorf("Query(%+v) returned %d stamps, want %d: %+v", tt.filter, len(got), tt.want, got)
			}
		})
	}
}

// A row with no recorded renderer revision differs from every running binary,
// so --stale-renderer-rev has to select it.
func TestRendererRevNotMatchesEmptyRow(t *testing.T) {
	store := mustOpen(t)

	if err := store.Put(Stamp{Z: 4, X: 1, Y: 1, RenderedAt: time.Now()}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := store.Query(Filter{RendererRevNot: "v9"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("Query returned %d stamps, want 1", len(got))
	}
}

// Both backings are the same schema in a different file. The folder one has to
// land on stamps.db in the directory.
func TestFolderBacking(t *testing.T) {
	{
		dir := t.TempDir()
		store, err := OpenFolder(dir)
		if err != nil {
			t.Fatalf("OpenFolder: %v", err)
		}
		if err := store.Put(Stamp{Z: 2, X: 1, Y: 1, RenderedAt: time.Now()}); err != nil {
			t.Fatalf("Put: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		if _, err := os.Stat(filepath.Join(dir, FolderDBName)); err != nil {
			t.Fatalf("expected %s in the tiles dir: %v", FolderDBName, err)
		}
	}
}

// The MBTiles backing has to coexist with the tiles table rather than
// disturbing it: the stamp table is simply an extra table in the same file.
func TestMBTilesBacking(t *testing.T) {
	{
		path := filepath.Join(t.TempDir(), "tiles.mbtiles")

		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatalf("sql.Open: %v", err)
		}
		if _, err := db.Exec(`CREATE TABLE tiles (zoom_level INTEGER, tile_column INTEGER, tile_row INTEGER, tile_data BLOB);
			INSERT INTO tiles VALUES (1, 0, 0, x'00');`); err != nil {
			t.Fatalf("seed tiles: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		store, err := OpenMBTiles(path)
		if err != nil {
			t.Fatalf("OpenMBTiles: %v", err)
		}
		if err := store.Put(Stamp{Z: 1, X: 0, Y: 0, RenderedAt: time.Now(), Source: "s"}); err != nil {
			t.Fatalf("Put: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		db, err = sql.Open("sqlite", path)
		if err != nil {
			t.Fatalf("sql.Open: %v", err)
		}
		defer db.Close() // nolint:errcheck

		var tiles, stamps int
		if err := db.QueryRow("SELECT COUNT(*) FROM tiles").Scan(&tiles); err != nil {
			t.Fatalf("count tiles: %v", err)
		}
		if err := db.QueryRow("SELECT COUNT(*) FROM tile_stamp").Scan(&stamps); err != nil {
			t.Fatalf("count stamps: %v", err)
		}
		if tiles != 1 || stamps != 1 {
			t.Errorf("tiles=%d stamps=%d, want 1 and 1", tiles, stamps)
		}
	}
}

// The stored layout has to sort chronologically as text, so that ORDER BY and a
// human reading the table agree with each other. Values that trip plain RFC3339
// — a fractional second, a non-UTC offset — are the ones worth pinning.
func TestStoredTimestampsSortChronologically(t *testing.T) {
	berlin := time.FixedZone("CEST", 2*60*60)

	ordered := []time.Time{
		time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 15, 9, 0, 0, 500_000_000, time.UTC),
		time.Date(2026, 8, 15, 11, 0, 1, 0, berlin), // 09:00:01 UTC
		time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC),
	}

	for i := 1; i < len(ordered); i++ {
		prev, cur := formatTime(ordered[i-1]), formatTime(ordered[i])
		if prev >= cur {
			t.Errorf("formatTime(%s)=%q is not < formatTime(%s)=%q",
				ordered[i-1], prev, ordered[i], cur)
		}
		if _, err := ParseTime(cur); err != nil {
			t.Errorf("ParseTime(%q) = %v, want a valid RFC3339 value", cur, err)
		}
	}
}

// insertRawStamp writes a row exactly as given, bypassing formatTime. That is
// what a row written by another tool — or a corrupted one — looks like.
func insertRawStamp(t *testing.T, path string, z, x, y int, osmBase, renderedAt string) {
	t.Helper()

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close() // nolint:errcheck

	if _, err := db.Exec(`INSERT OR REPLACE INTO tile_stamp
		(zoom_level, tile_column, tile_row, suffix, osm_base_ts, rendered_at)
		VALUES (?, ?, ?, '', ?, ?)`, z, x, y, osmBase, renderedAt); err != nil {
		t.Fatalf("insert raw stamp: %v", err)
	}
}

// A timestamp the store cannot parse reads as "unknown", and unknown is never
// evidence of being old — so it must not be selected by a staleness filter,
// however it happens to compare as text. A valid RFC3339 value in another zone
// has to be compared as the instant it names, not as its spelling.
func TestQueryComparesTimestampsChronologically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stamps.db")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	cutoff := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)

	// 1/0/0: malformed both ways. Sorts below the cutoff as text.
	insertRawStamp(t, path, 1, 0, 0, "2025-bad", "2025-bad")
	// 1/0/1: valid, older than the cutoff, but written with an offset and no
	// fractional part, so it sorts *above* the canonical cutoff as text.
	insertRawStamp(t, path, 1, 0, 1, "2026-08-15T13:00:00+02:00", "2026-08-15T13:00:00+02:00")
	// 1/0/2: valid and newer than the cutoff.
	insertRawStamp(t, path, 1, 0, 2, "2026-08-15T18:00:00Z", "2026-08-15T18:00:00Z")

	store, err = OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer store.Close() // nolint:errcheck

	for _, tc := range []struct {
		name   string
		filter Filter
	}{
		{"data", Filter{DataBefore: cutoff}},
		{"rendered", Filter{RenderedBefore: cutoff}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := store.Query(tc.filter)
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("Query returned %d stamps, want only the genuinely older one: %+v", len(got), got)
			}
			if got[0].Y != 1 {
				t.Errorf("Query selected 1/0/%d, want 1/0/1", got[0].Y)
			}
		})
	}
}

// A read-only open answers questions without creating anything. That is what
// makes a purge dry run harmless on a tileset that has no stamps and on storage
// the process cannot write.
func TestOpenReadOnly(t *testing.T) {
	dir := t.TempDir()

	if _, err := OpenFolderReadOnly(dir); !errors.Is(err, ErrNotFound) {
		t.Fatalf("OpenFolderReadOnly on an empty dir = %v, want ErrNotFound", err)
	}
	if _, err := os.Stat(filepath.Join(dir, FolderDBName)); !os.IsNotExist(err) {
		t.Fatalf("OpenFolderReadOnly created %s", FolderDBName)
	}

	store, err := OpenFolder(dir)
	if err != nil {
		t.Fatalf("OpenFolder: %v", err)
	}
	if err := store.Put(Stamp{Z: 3, X: 2, Y: 1, RenderedAt: time.Now()}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	ro, err := OpenFolderReadOnly(dir)
	if err != nil {
		t.Fatalf("OpenFolderReadOnly: %v", err)
	}
	defer ro.Close() // nolint:errcheck

	if _, ok, err := ro.Get(3, 2, 1, ""); err != nil || !ok {
		t.Fatalf("Get on a read-only store = ok:%v err:%v, want the stamp", ok, err)
	}
	if err := ro.Put(Stamp{Z: 9, X: 9, Y: 9, RenderedAt: time.Now()}); err == nil {
		t.Error("Put on a read-only store succeeded, want an error")
	}
	if err := ro.Delete(3, 2, 1, ""); err == nil {
		t.Error("Delete on a read-only store succeeded, want an error")
	}
}

// An MBTiles file that predates stamps has no tile_stamp table, and that is a
// tileset without stamps rather than a failure.
func TestOpenMBTilesReadOnlyWithoutStampTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tiles.mbtiles")

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE tiles (zoom_level INTEGER)`); err != nil {
		t.Fatalf("seed tiles: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := OpenMBTilesReadOnly(path); !errors.Is(err, ErrNotFound) {
		t.Fatalf("OpenMBTilesReadOnly = %v, want ErrNotFound", err)
	}
}
