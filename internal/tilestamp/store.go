// Package tilestamp records, per rendered tile, which source data went into it.
//
// A tile file says nothing about the OpenStreetMap extract it was rendered
// from: its mtime is when the bytes were written, which after a resumed or
// re-run job has no relation to how old the data is. Without that, "re-render
// everything whose data predates the last import" is not a question anything
// can answer, so the only available refresh is "render it all again". The stamp
// store is the sidecar that makes the question answerable, and it is what both
// `generate`'s freshness checks and `purge`'s selectors read.
//
// The store lives next to the tiles it describes: an extra table inside the
// same `.mbtiles` file, or a `stamps.db` beside `tilejson.json` in a tile
// folder. Same schema, same code, one type — see Open.
package tilestamp

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // SQLite driver
)

// DefaultBatchSize is the number of stamps buffered before a flush. It matches
// mbtiles.DefaultBatchSize for the same reason: a tile run puts one stamp per
// tile, and committing each one on its own makes fsync, not rendering, the
// thing the run waits for.
const DefaultBatchSize = 100

// FolderDBName is the stamp database written into a tile directory. It sits
// beside tilejson.json so that everything describing a tile folder is in the
// folder.
const FolderDBName = "stamps.db"

// tsLayout is how timestamps are stored: UTC, fixed width, nanosecond
// precision.
//
// Fixed width is the point: every row written by this package sorts
// chronologically as text, so `ORDER BY` and an eyeball on the table agree with
// each other. Plain RFC3339 does not guarantee that — a value with a fractional
// part sorts before the same second without one ('.' < 'Z'), and a non-UTC
// offset compares against a different instant than it names. That is also why
// the filters do not compare timestamps in SQL: rows written by hand or by
// another tool need not follow this layout, and a string comparison over those
// answers about spelling rather than about time. See Filter.matches.
//
// The result is still valid RFC3339, so anything reading these rows outside
// this package can parse them normally.
const tsLayout = "2006-01-02T15:04:05.000000000Z"

// Stamp records the provenance of one rendered tile.
//
// Coordinates are XYZ — see the schema comment in createSchema.
type Stamp struct {
	// OSMBase is the source-data version: Overpass's
	// osm3s.timestamp_osm_base, i.e. how current the OSM data behind this tile
	// is. Zero when the fetch did not report one (a synthetic or offline
	// source), which every consumer must treat as "unknown", never as "old".
	OSMBase time.Time
	// RenderedAt is when this tile was written.
	RenderedAt time.Time
	// Source identifies where the data came from — the Overpass endpoint that
	// answered. Under multi-server routing that varies per tile, which is
	// exactly why it is stored per tile.
	Source string
	// RendererRev identifies the binary that rendered the tile, so a renderer
	// change can be turned into a re-render selector.
	RendererRev string
	// Suffix is "" for a base tile and "@2x" for its HiDPI sibling. They are
	// separate tiles and get separate stamps.
	Suffix string
	Z      int
	X      int
	Y      int
}

// Filter selects stamps. The zero value matches every stamp; each field that is
// set narrows the result further, and all set fields must hold.
type Filter struct {
	// DataBefore matches stamps whose OSMBase is strictly older. A stamp with
	// no recorded OSMBase never matches: "unknown" is not "old", and this
	// filter drives deletion.
	DataBefore time.Time
	// RenderedBefore matches stamps rendered strictly before this instant. A
	// stamp whose rendered_at cannot be parsed never matches, for the same
	// reason DataBefore ignores a missing one.
	RenderedBefore time.Time
	// Suffix, when non-nil, matches that suffix exactly — including the empty
	// string, which is why this is a pointer rather than a plain string.
	Suffix *string
	// MinZoom and MaxZoom bound the zoom range inclusively. Pointers because
	// zoom 0 is a real zoom level.
	MinZoom *int
	MaxZoom *int
	// RendererRevNot matches stamps whose RendererRev differs from this value.
	// Empty means no renderer filter.
	RendererRevNot string
}

// ErrNotFound reports that there is no stamp store to read: no stamps.db beside
// the tiles, or an MBTiles file without a tile_stamp table. It is what the
// read-only openers return instead of creating one, and callers turn it into
// "this tileset has no stamps" rather than into a failure.
var ErrNotFound = errors.New("no tile stamp store")

// errReadOnly is returned by the write methods of a store opened with
// OpenReadOnly. Writing through one is a programming mistake, not a runtime
// condition, so it is stated plainly rather than left to SQLite.
var errReadOnly = errors.New("store opened read-only")

// Store is the stamp table, whichever file it lives in.
type Store struct {
	db        *sql.DB
	path      string
	batch     []Stamp
	batchSize int
	mu        sync.Mutex
	readOnly  bool
}

// OpenMBTiles opens the stamp table inside an existing (or new) MBTiles file.
//
// Adding a table to an MBTiles database is safe. The spec requires certain
// tables to be present, not that no others are, and every reader addresses
// `tiles` and `metadata` by name. Nothing in internal/mbtiles touches this
// table either: insertMetadata clears `metadata` only.
func OpenMBTiles(path string) (*Store, error) {
	return Open(path)
}

// OpenFolder opens the stamp database for a tile directory, creating it if
// necessary. The directory itself must already exist.
func OpenFolder(dir string) (*Store, error) {
	return Open(filepath.Join(dir, FolderDBName))
}

// Open opens (or creates) a stamp store at an explicit path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("failed to open stamp database %q: %w", path, err)
	}

	// The same pragmas internal/mbtiles sets, for the same reason: the write
	// pattern is a long run of small batched inserts.
	pragmas := []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA temp_store = MEMORY",
	}
	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			return nil, errors.Join(fmt.Errorf("failed to set pragma %q: %w", pragma, err), db.Close())
		}
	}

	if err := createSchema(db); err != nil {
		return nil, errors.Join(err, db.Close())
	}

	return &Store{
		db:        db,
		path:      path,
		batch:     make([]Stamp, 0, DefaultBatchSize),
		batchSize: DefaultBatchSize,
	}, nil
}

// OpenFolderReadOnly opens an existing stamps.db in a tile directory without
// creating or changing anything. See OpenReadOnly.
func OpenFolderReadOnly(dir string) (*Store, error) {
	return OpenReadOnly(filepath.Join(dir, FolderDBName))
}

// OpenMBTilesReadOnly opens the stamp table of an existing MBTiles file without
// creating or changing anything. See OpenReadOnly.
func OpenMBTilesReadOnly(path string) (*Store, error) {
	return OpenReadOnly(path)
}

// OpenReadOnly opens an existing stamp store for reading only.
//
// It exists for the callers that only ask questions — a `purge` dry run is the
// one that matters. Open would answer the same questions, but on the way it
// creates the file, sets journal_mode and creates the schema, so a run whose
// entire promise is "this changes nothing" would write to a legacy tileset and
// fail outright on read-only storage. This opens the file with SQLite's mode=ro,
// sets no pragmas and creates no schema; when there is nothing to open it
// reports ErrNotFound, which is the honest answer to "which of these tiles are
// stale" for a tileset that has no stamps.
func OpenReadOnly(path string) (*Store, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%q: %w", path, ErrNotFound)
		}
		return nil, fmt.Errorf("failed to open stamp database %q: %w", path, err)
	}

	// mode=ro is a SQLite URI parameter, honoured because the driver opens
	// every DSN with SQLITE_OPEN_URI. It is what makes this safe on a file the
	// process may not write.
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("failed to open stamp database %q: %w", path, err)
	}

	// An MBTiles file always exists before its stamp table does, so the file
	// being there says nothing about there being stamps in it.
	var name string
	err = db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name='tile_stamp'").Scan(&name)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, errors.Join(fmt.Errorf("%q: %w", path, ErrNotFound), db.Close())
	case err != nil:
		return nil, errors.Join(
			fmt.Errorf("failed to inspect stamp database %q: %w", path, err), db.Close())
	}

	return &Store{
		db:        db,
		path:      path,
		batchSize: DefaultBatchSize,
		readOnly:  true,
	}, nil
}

// createSchema creates the stamp table.
//
// The rows are XYZ, deliberately unlike the MBTiles `tiles` table next to them,
// which stores TMS rows. Two conventions in one file is a trap, so this states
// which one applies once, here, rather than leaving every call site to remember
// it: mbtiles.tmsRow exists precisely because a missed y-flip does not fail —
// it answers about the mirrored tile, which for a symmetric bounding box looks
// entirely plausible. A stamp store is queried by humans running `purge` with a
// bounding box in hand, and XYZ is what that bounding box, tile.Coords, the
// pipeline and the tile server all speak. Converting at the one place that
// needs TMS (the MBTiles delete path) is a conversion someone can see; a second
// silent convention is not.
func createSchema(db *sql.DB) error {
	const schema = `
		CREATE TABLE IF NOT EXISTS tile_stamp (
			zoom_level INTEGER NOT NULL,
			tile_column INTEGER NOT NULL,
			tile_row INTEGER NOT NULL,
			suffix TEXT NOT NULL DEFAULT '',
			osm_base_ts TEXT,
			rendered_at TEXT NOT NULL,
			source TEXT,
			renderer_rev TEXT,
			PRIMARY KEY (zoom_level, tile_column, tile_row, suffix)
		);
	`

	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("failed to create stamp schema: %w", err)
	}
	return nil
}

// Put records a stamp, replacing any previous one for the same tile. Writes are
// buffered; see Flush.
func (s *Store) Put(stamp Stamp) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.readOnly {
		return fmt.Errorf("stamp store %q is open read-only: %w", s.path, errReadOnly)
	}

	// Replace an unflushed stamp for the same tile rather than queueing a
	// second one, so the buffer cannot grow with re-renders of one tile and
	// Get cannot see a stale entry ahead of a fresh one.
	for i := range s.batch {
		if sameTile(s.batch[i], stamp) {
			s.batch[i] = stamp
			return nil
		}
	}

	s.batch = append(s.batch, stamp)
	if len(s.batch) >= s.batchSize {
		return s.flushLocked()
	}
	return nil
}

// Get returns the stamp for a tile, and whether there is one.
func (s *Store) Get(z, x, y int, suffix string) (Stamp, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Buffered stamps are not queryable yet, so the buffer answers first.
	want := Stamp{Z: z, X: x, Y: y, Suffix: suffix}
	for i := range s.batch {
		if sameTile(s.batch[i], want) {
			return s.batch[i], true, nil
		}
	}

	const q = `SELECT zoom_level, tile_column, tile_row, suffix, osm_base_ts, rendered_at, source, renderer_rev
		FROM tile_stamp WHERE zoom_level=? AND tile_column=? AND tile_row=? AND suffix=?`

	row := s.db.QueryRow(q, z, x, y, suffix)
	stamp, err := scanStamp(row)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Stamp{}, false, nil
	case err != nil:
		return Stamp{}, false, fmt.Errorf("failed to read stamp %d/%d/%d%s: %w", z, x, y, suffix, err)
	}
	return stamp, true, nil
}

// Delete removes the stamp for a tile. Removing a stamp that is not there is
// not an error: the caller's intent ("this tile has no stamp") already holds.
func (s *Store) Delete(z, x, y int, suffix string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.readOnly {
		return fmt.Errorf("stamp store %q is open read-only: %w", s.path, errReadOnly)
	}

	// Drop any buffered stamp first, or the flush would resurrect it.
	want := Stamp{Z: z, X: x, Y: y, Suffix: suffix}
	for i := range s.batch {
		if sameTile(s.batch[i], want) {
			s.batch = append(s.batch[:i], s.batch[i+1:]...)
			break
		}
	}

	const q = `DELETE FROM tile_stamp WHERE zoom_level=? AND tile_column=? AND tile_row=? AND suffix=?`
	if _, err := s.db.Exec(q, z, x, y, suffix); err != nil {
		return fmt.Errorf("failed to delete stamp %d/%d/%d%s: %w", z, x, y, suffix, err)
	}
	return nil
}

// Query returns every stamp matching the filter, ordered by zoom, column, row
// and suffix so callers (and their tests) see a stable order.
//
// A slice rather than an iterator: the callers are the purge command, which has
// to count and sample the selection before doing anything with it, and tests.
// Neither streams, and a filtered selection is small next to the tileset.
func (s *Store) Query(f Filter) ([]Stamp, error) {
	if err := s.Flush(); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	where, args := f.sql()
	q := `SELECT zoom_level, tile_column, tile_row, suffix, osm_base_ts, rendered_at, source, renderer_rev
		FROM tile_stamp` + where + ` ORDER BY zoom_level, tile_column, tile_row, suffix`

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query stamps: %w", err)
	}
	defer rows.Close() // nolint:errcheck

	var out []Stamp
	for rows.Next() {
		stamp, err := scanStamp(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan stamp: %w", err)
		}
		// The timestamp selectors are applied here rather than in SQL; see
		// Filter.matches.
		if !f.matches(stamp) {
			continue
		}
		out = append(out, stamp)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read stamps: %w", err)
	}
	return out, nil
}

// matches applies the timestamp selectors to an already-scanned stamp.
//
// They are not part of the WHERE clause because SQL would compare the stored
// text, and text order is only chronological order for values written in
// tsLayout. A row carrying a valid RFC3339 value with an offset would then be
// ordered against the wrong instant, and a malformed one such as "2025-bad"
// would sort below any cutoff and be selected — for deletion — even though
// scanStamp reads it as "unknown". Comparing the parsed values keeps the
// documented rule intact: an unreadable or absent timestamp is unknown, and
// unknown is never evidence of being old.
func (f Filter) matches(s Stamp) bool {
	if !f.DataBefore.IsZero() {
		if s.OSMBase.IsZero() || !s.OSMBase.Before(f.DataBefore) {
			return false
		}
	}
	if !f.RenderedBefore.IsZero() {
		if s.RenderedAt.IsZero() || !s.RenderedAt.Before(f.RenderedBefore) {
			return false
		}
	}
	return true
}

// sql renders the filter as a WHERE clause and its arguments. The timestamp
// selectors are missing from it on purpose — see Filter.matches.
func (f Filter) sql() (string, []any) {
	var conds []string
	var args []any

	if f.RendererRevNot != "" {
		// COALESCE so that a row with no recorded revision counts as different
		// from the running binary — which it is; nothing recorded it.
		conds = append(conds, "COALESCE(renderer_rev, '') <> ?")
		args = append(args, f.RendererRevNot)
	}
	if f.Suffix != nil {
		conds = append(conds, "suffix = ?")
		args = append(args, *f.Suffix)
	}
	if f.MinZoom != nil {
		conds = append(conds, "zoom_level >= ?")
		args = append(args, *f.MinZoom)
	}
	if f.MaxZoom != nil {
		conds = append(conds, "zoom_level <= ?")
		args = append(args, *f.MaxZoom)
	}

	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// Flush writes buffered stamps to the database.
func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flushLocked()
}

// flushLocked writes buffered stamps. Must be called with the lock held.
func (s *Store) flushLocked() (err error) {
	if len(s.batch) == 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin stamp transaction: %w", err)
	}
	defer tx.Rollback() // nolint:errcheck

	const q = `INSERT OR REPLACE INTO tile_stamp
		(zoom_level, tile_column, tile_row, suffix, osm_base_ts, rendered_at, source, renderer_rev)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

	stmt, err := tx.Prepare(q)
	if err != nil {
		return fmt.Errorf("failed to prepare stamp insert: %w", err)
	}
	defer func() {
		if closeErr := stmt.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("failed to close stamp insert statement: %w", closeErr)
		}
	}()

	for _, stamp := range s.batch {
		var osmBase any
		if !stamp.OSMBase.IsZero() {
			osmBase = formatTime(stamp.OSMBase)
		}
		renderedAt := stamp.RenderedAt
		if renderedAt.IsZero() {
			// rendered_at is NOT NULL, and a stamp written by the pipeline
			// always carries one. Defaulting rather than failing keeps a
			// bookkeeping gap from becoming a tile-write failure.
			renderedAt = time.Now()
		}

		if _, err := stmt.Exec(stamp.Z, stamp.X, stamp.Y, stamp.Suffix,
			osmBase, formatTime(renderedAt), stamp.Source, stamp.RendererRev); err != nil {
			return fmt.Errorf("failed to insert stamp %d/%d/%d%s: %w",
				stamp.Z, stamp.X, stamp.Y, stamp.Suffix, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit stamps: %w", err)
	}

	s.batch = s.batch[:0]
	return nil
}

// Close flushes and closes the store.
func (s *Store) Close() error {
	if err := s.Flush(); err != nil {
		return errors.Join(err, s.db.Close())
	}
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("failed to close stamp database %q: %w", s.path, err)
	}
	return nil
}

// Path returns the file the store was opened from.
func (s *Store) Path() string { return s.path }

func sameTile(a, b Stamp) bool {
	return a.Z == b.Z && a.X == b.X && a.Y == b.Y && a.Suffix == b.Suffix
}

// formatTime renders a timestamp in the storage layout; see tsLayout.
func formatTime(t time.Time) string {
	return t.UTC().Format(tsLayout)
}

// ParseTime parses a stored timestamp. It accepts any RFC3339 value, not only
// this package's layout, so a row written by hand or by another tool is still
// readable.
func ParseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339, s)
}

// rowScanner is the part of *sql.Row and *sql.Rows scanStamp needs.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanStamp reads one row into a Stamp.
//
// An unparseable timestamp is returned as the zero time rather than as an
// error. Callers treat a zero OSMBase as "unknown", and every uncertain case in
// the freshness check renders and every uncertain case in purge is left alone —
// so a corrupt cell degrades to "no information", which both of those handle,
// instead of failing an entire run over one bad row.
func scanStamp(row rowScanner) (Stamp, error) {
	var (
		stamp      Stamp
		osmBase    sql.NullString
		renderedAt string
		source     sql.NullString
		rev        sql.NullString
	)

	if err := row.Scan(&stamp.Z, &stamp.X, &stamp.Y, &stamp.Suffix,
		&osmBase, &renderedAt, &source, &rev); err != nil {
		return Stamp{}, err
	}

	if osmBase.Valid && osmBase.String != "" {
		if t, err := ParseTime(osmBase.String); err == nil {
			stamp.OSMBase = t
		}
	}
	if t, err := ParseTime(renderedAt); err == nil {
		stamp.RenderedAt = t
	}
	stamp.Source = source.String
	stamp.RendererRev = rev.String

	return stamp, nil
}
