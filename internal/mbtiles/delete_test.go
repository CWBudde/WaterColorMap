package mbtiles

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// newSeededWriter creates a tileset holding the given XYZ tiles.
func newSeededWriter(t *testing.T, tiles []TileCoord) (*Writer, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "tiles.mbtiles")
	w, err := New(path, Metadata{Name: "test", Format: FormatPNG})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for _, c := range tiles {
		if err := w.WriteTile(c.Z, c.X, c.Y, []byte("tile")); err != nil {
			t.Fatalf("WriteTile: %v", err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	return w, path
}

func TestDeleteTile(t *testing.T) {
	// Asymmetric y coordinates, so a missed TMS flip cannot pass by accident.
	tiles := []TileCoord{{Z: 4, X: 3, Y: 1}, {Z: 4, X: 3, Y: 9}}
	w, _ := newSeededWriter(t, tiles)
	defer w.Close() // nolint:errcheck

	deleted, err := w.DeleteTile(4, 3, 1)
	if err != nil {
		t.Fatalf("DeleteTile: %v", err)
	}
	if deleted != 1 {
		t.Errorf("DeleteTile removed %d rows, want 1", deleted)
	}

	// HasTile applies the same flip the insert did, so it only agrees with the
	// delete if the delete flipped too. A delete that forgot tmsRow would have
	// removed the mirrored row and left this one standing.
	if has, err := w.HasTile(4, 3, 1); err != nil || has {
		t.Errorf("HasTile(4/3/1) = %v, %v; want false after delete", has, err)
	}
	if has, err := w.HasTile(4, 3, 9); err != nil || !has {
		t.Errorf("HasTile(4/3/9) = %v, %v; the other tile must survive", has, err)
	}
}

func TestDeleteMissingTileIsNoOp(t *testing.T) {
	w, _ := newSeededWriter(t, []TileCoord{{Z: 4, X: 3, Y: 1}})
	defer w.Close() // nolint:errcheck

	deleted, err := w.DeleteTile(4, 3, 7)
	if err != nil {
		t.Fatalf("DeleteTile(missing) = %v, want nil", err)
	}
	if deleted != 0 {
		t.Errorf("DeleteTile(missing) reported %d deletions, want 0", deleted)
	}
	if has, err := w.HasTile(4, 3, 1); err != nil || !has {
		t.Errorf("HasTile(4/3/1) = %v, %v; the existing tile must be untouched", has, err)
	}
}

// A buffered write must not survive a delete of the same tile.
func TestDeleteFlushesPendingWrites(t *testing.T) {
	w, _ := newSeededWriter(t, nil)
	defer w.Close() // nolint:errcheck

	if err := w.WriteTile(6, 2, 2, []byte("tile")); err != nil {
		t.Fatalf("WriteTile: %v", err)
	}

	if _, err := w.DeleteTile(6, 2, 2); err != nil {
		t.Fatalf("DeleteTile: %v", err)
	}
	if has, err := w.HasTile(6, 2, 2); err != nil || has {
		t.Errorf("HasTile = %v, %v; a buffered write must not outlive the delete", has, err)
	}
}

func TestDeleteTilesBatch(t *testing.T) {
	tiles := []TileCoord{
		{Z: 5, X: 1, Y: 1}, {Z: 5, X: 1, Y: 2}, {Z: 5, X: 1, Y: 3},
	}
	w, _ := newSeededWriter(t, tiles)
	defer w.Close() // nolint:errcheck

	deleted, err := w.DeleteTiles(tiles[:2])
	if err != nil {
		t.Fatalf("DeleteTiles: %v", err)
	}
	if deleted != 2 {
		t.Errorf("DeleteTiles removed %d rows, want 2", deleted)
	}

	for _, c := range tiles[:2] {
		if has, _ := w.HasTile(c.Z, c.X, c.Y); has {
			t.Errorf("tile %d/%d/%d survived the batch delete", c.Z, c.X, c.Y)
		}
	}
	if has, _ := w.HasTile(5, 1, 3); !has {
		t.Error("tile 5/1/3 was not selected and must survive")
	}
}

// Deleting a tile has to take its stamp with it, in the same transaction.
func TestDeleteRemovesStampRow(t *testing.T) {
	w, path := newSeededWriter(t, []TileCoord{{Z: 4, X: 3, Y: 1}, {Z: 4, X: 3, Y: 9}})

	// The stamp table is created by internal/tilestamp; importing it here would
	// be circular, so the test writes the same schema by hand. Rows are XYZ.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE tile_stamp (
			zoom_level INTEGER NOT NULL, tile_column INTEGER NOT NULL, tile_row INTEGER NOT NULL,
			suffix TEXT NOT NULL DEFAULT '', osm_base_ts TEXT, rendered_at TEXT NOT NULL,
			source TEXT, renderer_rev TEXT,
			PRIMARY KEY (zoom_level, tile_column, tile_row, suffix));
		INSERT INTO tile_stamp VALUES (4, 3, 1, '', NULL, 'now', NULL, NULL);
		INSERT INTO tile_stamp VALUES (4, 3, 1, '@2x', NULL, 'now', NULL, NULL);
		INSERT INTO tile_stamp VALUES (4, 3, 9, '', NULL, 'now', NULL, NULL);
	`); err != nil {
		t.Fatalf("create stamp table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if _, err := w.DeleteTile(4, 3, 1); err != nil {
		t.Fatalf("DeleteTile: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close() // nolint:errcheck

	var gone, kept int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM tile_stamp WHERE zoom_level=4 AND tile_column=3 AND tile_row=1").
		Scan(&gone); err != nil {
		t.Fatalf("count deleted stamps: %v", err)
	}
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM tile_stamp WHERE zoom_level=4 AND tile_column=3 AND tile_row=9").
		Scan(&kept); err != nil {
		t.Fatalf("count kept stamps: %v", err)
	}

	// Both suffixes of the deleted tile go; the untouched tile's stamp stays.
	if gone != 0 {
		t.Errorf("%d stamp rows survived the tile they describe", gone)
	}
	if kept != 1 {
		t.Errorf("kept stamp rows = %d, want 1", kept)
	}
}

// A file with no stamp table at all must delete normally.
func TestDeleteWithoutStampTable(t *testing.T) {
	w, _ := newSeededWriter(t, []TileCoord{{Z: 3, X: 1, Y: 1}})
	defer w.Close() // nolint:errcheck

	if _, err := w.DeleteTile(3, 1, 1); err != nil {
		t.Fatalf("DeleteTile: %v", err)
	}
}

func TestListTiles(t *testing.T) {
	tiles := []TileCoord{{Z: 3, X: 1, Y: 1}, {Z: 5, X: 2, Y: 30}, {Z: 7, X: 4, Y: 5}}
	w, _ := newSeededWriter(t, tiles)
	defer w.Close() // nolint:errcheck

	all, err := w.ListTiles(nil, nil)
	if err != nil {
		t.Fatalf("ListTiles: %v", err)
	}
	if len(all) != len(tiles) {
		t.Fatalf("ListTiles returned %d tiles, want %d", len(all), len(tiles))
	}
	// The listing must come back XYZ, i.e. flipped back from the stored TMS row.
	for i, want := range tiles {
		if all[i] != want {
			t.Errorf("ListTiles[%d] = %+v, want %+v", i, all[i], want)
		}
	}

	minZoom, maxZoom := 4, 6
	ranged, err := w.ListTiles(&minZoom, &maxZoom)
	if err != nil {
		t.Fatalf("ListTiles(zoom range): %v", err)
	}
	if len(ranged) != 1 || ranged[0] != tiles[1] {
		t.Errorf("ListTiles(4..6) = %+v, want just %+v", ranged, tiles[1])
	}
}

// OpenForUpdate must leave the metadata table exactly as it found it — New's
// rewrite is what makes it unusable for purge.
func TestOpenForUpdatePreservesMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tiles.mbtiles")
	meta := Metadata{
		Name: "Kept", Format: FormatPNG, Description: "do not touch",
		MinZoom: Zoom(3), MaxZoom: Zoom(9),
	}
	w, err := New(path, meta)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := w.WriteTile(3, 1, 1, []byte("tile")); err != nil {
		t.Fatalf("WriteTile: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	u, err := OpenForUpdate(path)
	if err != nil {
		t.Fatalf("OpenForUpdate: %v", err)
	}
	if _, err := u.DeleteTile(3, 1, 1); err != nil {
		t.Fatalf("DeleteTile: %v", err)
	}
	if err := u.Vacuum(); err != nil {
		t.Fatalf("Vacuum: %v", err)
	}
	if err := u.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r, err := OpenReader(path)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer r.Close() // nolint:errcheck

	got, err := r.Metadata()
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if got.Name != meta.Name || got.Description != meta.Description || got.Format != meta.Format {
		t.Errorf("metadata = %+v, want it unchanged (%+v)", got, meta)
	}
}

func TestOpenForUpdateRejectsNonTileset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if w, err := OpenForUpdate(path); err == nil {
		w.Close() // nolint:errcheck
		t.Error("OpenForUpdate accepted a file with no tiles table")
	}
}
