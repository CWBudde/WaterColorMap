package mbtiles

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// TileCoord is an XYZ tile address, as every caller outside this package speaks
// it. The y flip to the TMS row the file stores happens here, in tmsRow, and
// nowhere else.
type TileCoord struct {
	Z int
	X int
	Y int
}

// OpenForUpdate opens an existing MBTiles file for modification.
//
// Unlike New this does not write metadata, and that is the whole reason it
// exists: New empties and rewrites the metadata table on open, which is right
// for a run that is about to (re)declare a tileset and catastrophic for a
// command that only means to remove some rows from it. `purge --mbtiles` would
// otherwise relabel a tileset as a side effect of deleting from it.
//
// The tileset's declared format is read rather than supplied, so a pbf file
// keeps its gzip behaviour if anything writes through this handle afterwards.
func OpenForUpdate(path string) (*Writer, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	var count int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='tiles'",
	).Scan(&count); err != nil {
		return nil, errors.Join(fmt.Errorf("failed to verify schema: %w", err), db.Close())
	}
	if count == 0 {
		return nil, errors.Join(
			fmt.Errorf("%s does not contain a tiles table", path), db.Close())
	}

	var format string
	err = db.QueryRow("SELECT value FROM metadata WHERE name = 'format'").Scan(&format)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		// A missing metadata table is as harmless here as a missing row: this
		// handle is for deleting tiles, and the format only decides how new
		// payloads would be enveloped.
		format = ""
	}

	return &Writer{
		db:        db,
		path:      path,
		batch:     make([]TileEntry, 0, DefaultBatchSize),
		batchSize: DefaultBatchSize,
		metadata:  Metadata{Format: format},
		gzipTiles: strings.EqualFold(format, FormatPBF),
	}, nil
}

// DeleteTile removes one tile, and its stamp row when the file carries a stamp
// table. Coordinates are XYZ, as for WriteTile. Deleting a tile that is not
// there is not an error: the caller's intent already holds.
func (w *Writer) DeleteTile(z, x, y int) (int, error) {
	return w.DeleteTiles([]TileCoord{{Z: z, X: x, Y: y}})
}

// DeleteTiles removes many tiles in one transaction and reports how many rows
// the tiles table actually lost. Buffered writes are flushed first, or a
// pending insert would reappear after the delete.
//
// The tile and its stamp go in the same transaction on purpose. A stamp is a
// claim that a tile exists with a given provenance; a crash between the two
// statements would leave that claim standing for a tile that is gone, and the
// next freshness run would then skip re-rendering it — a permanent hole, which
// is exactly the failure the skip-existing asymmetry exists to prevent.
func (w *Writer) DeleteTiles(tiles []TileCoord) (deleted int, err error) {
	if len(tiles) == 0 {
		return 0, nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.flushLocked(); err != nil {
		return 0, err
	}

	hasStamps, err := w.hasStampTable()
	if err != nil {
		return 0, err
	}

	tx, err := w.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("failed to begin delete transaction: %w", err)
	}
	defer tx.Rollback() // nolint:errcheck

	deleted, err = deleteTilesTx(tx, tiles, hasStamps)
	if err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to commit deletes: %w", err)
	}

	return deleted, nil
}

// deleteTilesTx issues the deletes inside an open transaction and reports how
// many tile rows went.
func deleteTilesTx(tx *sql.Tx, tiles []TileCoord, hasStamps bool) (deleted int, err error) {
	tileStmt, err := tx.Prepare(
		"DELETE FROM tiles WHERE zoom_level=? AND tile_column=? AND tile_row=?")
	if err != nil {
		return 0, fmt.Errorf("failed to prepare tile delete: %w", err)
	}
	defer func() {
		if closeErr := tileStmt.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("failed to close tile delete statement: %w", closeErr)
		}
	}()

	var stampStmt *sql.Stmt
	if hasStamps {
		stampStmt, err = tx.Prepare(
			"DELETE FROM tile_stamp WHERE zoom_level=? AND tile_column=? AND tile_row=?")
		if err != nil {
			return 0, fmt.Errorf("failed to prepare stamp delete: %w", err)
		}
		defer func() {
			if closeErr := stampStmt.Close(); closeErr != nil && err == nil {
				err = fmt.Errorf("failed to close stamp delete statement: %w", closeErr)
			}
		}()
	}

	for _, t := range tiles {
		res, err := tileStmt.Exec(t.Z, t.X, tmsRow(t.Z, t.Y))
		if err != nil {
			return 0, fmt.Errorf("failed to delete tile %d/%d/%d: %w", t.Z, t.X, t.Y, err)
		}
		if n, err := res.RowsAffected(); err == nil {
			deleted += int(n)
		}

		if stampStmt == nil {
			continue
		}
		// Stamp rows are XYZ — see the schema comment in internal/tilestamp.
		// This is the one place both conventions meet, which is why the flip is
		// visible on the line above and absent here.
		if _, err := stampStmt.Exec(t.Z, t.X, t.Y); err != nil {
			return 0, fmt.Errorf("failed to delete stamp %d/%d/%d: %w", t.Z, t.X, t.Y, err)
		}
	}

	return deleted, nil
}

// hasStampTable reports whether this file carries a tilestamp table. Must be
// called with the lock held.
func (w *Writer) hasStampTable() (bool, error) {
	var count int
	if err := w.db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='tile_stamp'",
	).Scan(&count); err != nil {
		return false, fmt.Errorf("failed to look for the stamp table: %w", err)
	}
	return count > 0, nil
}

// ListTiles returns every tile in the file as XYZ coordinates, optionally
// bounded by zoom. Pass nil for either bound to leave it open.
//
// SQLite frees deleted pages for reuse but does not shrink the file, so a purge
// wants to know what it is about to remove before it removes it; this is how
// the command enumerates candidates it was not handed a bounding box for.
func (w *Writer) ListTiles(minZoom, maxZoom *int) ([]TileCoord, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.flushLocked(); err != nil {
		return nil, err
	}

	q := "SELECT zoom_level, tile_column, tile_row FROM tiles"
	var args []any
	var conds []string
	if minZoom != nil {
		conds = append(conds, "zoom_level >= ?")
		args = append(args, *minZoom)
	}
	if maxZoom != nil {
		conds = append(conds, "zoom_level <= ?")
		args = append(args, *maxZoom)
	}
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += " ORDER BY zoom_level, tile_column, tile_row"

	rows, err := w.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list tiles: %w", err)
	}
	defer rows.Close() // nolint:errcheck

	var out []TileCoord
	for rows.Next() {
		var z, x, tmsY int
		if err := rows.Scan(&z, &x, &tmsY); err != nil {
			return nil, fmt.Errorf("failed to scan tile row: %w", err)
		}
		// tmsRow is its own inverse.
		out = append(out, TileCoord{Z: z, X: x, Y: tmsRow(z, tmsY)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read tile rows: %w", err)
	}

	return out, nil
}

// Vacuum rebuilds the database file, returning the space deleted tiles freed.
//
// Deleting rows only marks their pages reusable, so a purge that is meant to
// reclaim disk has to be followed by this. It rewrites the whole file, which
// costs roughly the size of the tileset in time and temporarily in disk, so it
// is a separate step the caller asks for rather than something delete does.
func (w *Writer) Vacuum() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.flushLocked(); err != nil {
		return err
	}

	// VACUUM cannot run inside a transaction, and it also cannot run while a
	// statement is open on the connection — the lazily prepared HasTile
	// statement is the one candidate, so it is closed first and simply
	// re-prepared on the next probe.
	if w.hasStmt != nil {
		if err := w.hasStmt.Close(); err != nil {
			return fmt.Errorf("failed to close tile lookup statement: %w", err)
		}
		w.hasStmt = nil
	}

	if _, err := w.db.Exec("VACUUM"); err != nil {
		return fmt.Errorf("failed to vacuum database: %w", err)
	}
	return nil
}
