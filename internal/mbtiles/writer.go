package mbtiles

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"

	_ "modernc.org/sqlite" // SQLite driver
)

const (
	// DefaultBatchSize is the number of tiles to buffer before flushing to the database.
	DefaultBatchSize = 100

	// FormatPBF is the metadata format value for Mapbox vector tiles. The
	// MBTiles 1.3 spec requires pbf tile_data to be gzip-compressed, while
	// raster formats (png, jpg, webp) are stored verbatim.
	FormatPBF = "pbf"

	// FormatPNG is the metadata format value for PNG tiles, and the format
	// assumed for a non-empty tileset that declares none — see
	// checkFormatMatchesExistingTiles.
	FormatPNG = "png"
)

// TileEntry represents a single tile to be written.
type TileEntry struct {
	Data []byte // Encoded tile payload as handed to WriteTile, before any gzip envelope
	Z    int
	X    int
	Y    int
}

// tmsRow converts an XYZ tile row into the TMS row MBTiles stores, flipping the
// y axis. Both the insert path (flushLocked) and the lookup path (HasTile) must
// use it: a lookup that skipped the flip would answer about the vertically
// mirrored tile, which for a symmetric bounding box still produces a plausible —
// but wrong — tileset.
func tmsRow(z, y int) int {
	return (1 << z) - 1 - y
}

// Writer writes tiles to an MBTiles database.
type Writer struct {
	db        *sql.DB
	hasStmt   *sql.Stmt
	path      string
	batch     []TileEntry
	metadata  Metadata
	batchSize int
	// gzipTiles is true for pbf tilesets, whose payloads the spec requires to
	// be gzipped. Raster tilesets store their bytes unchanged.
	gzipTiles bool
	mu        sync.Mutex
}

// New creates a new MBTiles writer.
// The database is created if it doesn't exist, and the schema is initialized.
func New(path string, metadata Metadata) (*Writer, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Set performance pragmas
	pragmas := []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA cache_size = 50000",
		"PRAGMA temp_store = MEMORY",
	}
	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			return nil, errors.Join(fmt.Errorf("failed to set pragma %q: %w", pragma, err), db.Close())
		}
	}

	// Create schema
	if err := createSchema(db); err != nil {
		return nil, errors.Join(fmt.Errorf("failed to create schema: %w", err), db.Close())
	}

	// Before insertMetadata, which empties and rewrites the metadata table and
	// would otherwise relabel an existing tileset on the way past.
	if err := checkFormatMatchesExistingTiles(db, metadata.Format); err != nil {
		return nil, errors.Join(err, db.Close())
	}

	// Insert metadata
	if err := insertMetadata(db, metadata); err != nil {
		return nil, errors.Join(fmt.Errorf("failed to insert metadata: %w", err), db.Close())
	}

	// Retrofit the uniqueness constraint onto legacy databases. This has to run
	// after insertMetadata, which empties the table and therefore removes any
	// duplicate legacy rows that would make the index fail.
	if err := ensureMetadataNameUnique(db); err != nil {
		return nil, errors.Join(err, db.Close())
	}

	return &Writer{
		db:        db,
		path:      path,
		batch:     make([]TileEntry, 0, DefaultBatchSize),
		batchSize: DefaultBatchSize,
		metadata:  metadata,
		gzipTiles: strings.EqualFold(metadata.Format, FormatPBF),
	}, nil
}

// ErrFormatMismatch reports an attempt to write tiles into a tileset that
// already holds tiles in a different format.
var ErrFormatMismatch = errors.New("mbtiles: tileset already contains tiles in a different format")

// checkFormatMatchesExistingTiles refuses to reopen a non-empty tileset under a
// different tile format.
//
// This guards a genuinely nasty interaction. New runs insertMetadata, which
// DELETEs the metadata table and rewrites it, so reopening a PNG tileset as
// WebP would silently relabel the file `format=webp`. HasTile is keyed on z/x/y
// alone and knows nothing about formats, so the resume check would then skip
// every existing PNG tile as "already done". The result is a database full of
// PNGs whose metadata says WebP, served with the wrong Content-Type, with no
// error anywhere — the "a false skip leaves a permanent hole" failure mode in
// its worst form.
//
// An empty tileset may change format freely: there is nothing to be
// inconsistent with. A non-empty one that declares no format at all is treated
// as PNG rather than waved through — see the comment at that branch. --force
// deliberately does not bypass any of this, because force means "render these
// tiles again", not "reinterpret the bytes already in the file".
func checkFormatMatchesExistingTiles(db *sql.DB, format string) error {
	if format == "" {
		return nil
	}

	var existing string
	err := db.QueryRow("SELECT value FROM metadata WHERE name = 'format'").Scan(&existing)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		existing = "" // fresh file, or one that never recorded a format
	case err != nil:
		return fmt.Errorf("failed to read existing tileset format: %w", err)
	}

	if strings.EqualFold(existing, format) {
		return nil
	}

	// An empty tileset may change format freely, so the tile count decides
	// before the declaration does.
	var hasTiles bool
	if err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM tiles LIMIT 1)").Scan(&hasTiles); err != nil {
		return fmt.Errorf("failed to check for existing tiles: %w", err)
	}
	if !hasTiles {
		return nil
	}

	// A non-empty tileset that never declared a format is not a free pass: the
	// tiles are *some* format, and insertMetadata is about to label them with
	// this run's. Every file this project has written declared one, so an
	// undeclared non-empty set is a legacy or foreign database, and PNG is what
	// those were before WebP existed. Treating it as PNG lets a PNG run resume
	// normally and stops a WebP run from relabelling bytes it did not write.
	if existing == "" {
		if strings.EqualFold(format, FormatPNG) {
			return nil
		}
		return fmt.Errorf("%w: it holds tiles but declares no format, so they are "+
			"assumed to be %s, and this run writes %s. Write to a different file, "+
			"or delete this one and start over "+
			"(--force re-renders tiles, it does not convert them)",
			ErrFormatMismatch, FormatPNG, format)
	}

	return fmt.Errorf("%w: it holds %s tiles, and this run writes %s. "+
		"Write to a different file, or delete this one and start over "+
		"(--force re-renders tiles, it does not convert them)",
		ErrFormatMismatch, existing, format)
}

// createSchema creates the MBTiles database schema.
func createSchema(db *sql.DB) error {
	schema := `
		-- The spec requires metadata names to be unique; PRIMARY KEY enforces
		-- that for freshly created files. insertMetadata clears the table
		-- first, so re-writing is safe. Databases from older releases keep
		-- their existing table definition — ensureMetadataNameUnique covers
		-- those.
		CREATE TABLE IF NOT EXISTS metadata (
			name TEXT NOT NULL PRIMARY KEY,
			value TEXT
		);

		CREATE TABLE IF NOT EXISTS tiles (
			zoom_level INTEGER NOT NULL,
			tile_column INTEGER NOT NULL,
			tile_row INTEGER NOT NULL,
			tile_data BLOB NOT NULL
		);

		CREATE UNIQUE INDEX IF NOT EXISTS tile_index ON tiles (zoom_level, tile_column, tile_row);
	`

	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("failed to execute schema: %w", err)
	}

	return nil
}

// ensureMetadataNameUnique makes the uniqueness of metadata.name effective for
// databases that already existed. CREATE TABLE IF NOT EXISTS never alters an
// existing table, so the PRIMARY KEY in createSchema only applies to files this
// version created; a unique index, by contrast, is created on pre-existing
// tables as well. The practical exposure is small because insertMetadata
// deletes every row before writing, but the index makes the constraint hold for
// anything written afterwards too.
func ensureMetadataNameUnique(db *sql.DB) error {
	const stmt = `CREATE UNIQUE INDEX IF NOT EXISTS metadata_name_index ON metadata (name)`

	if _, err := db.Exec(stmt); err != nil {
		return fmt.Errorf("failed to create unique metadata name index: %w", err)
	}

	return nil
}

// insertMetadata inserts metadata into the database.
func insertMetadata(db *sql.DB, meta Metadata) (err error) {
	// Clear existing metadata
	if _, err := db.Exec("DELETE FROM metadata"); err != nil {
		return fmt.Errorf("failed to clear metadata: %w", err)
	}

	stmt, err := db.Prepare("INSERT INTO metadata (name, value) VALUES (?, ?)")
	if err != nil {
		return fmt.Errorf("failed to prepare metadata insert: %w", err)
	}
	defer func() {
		if closeErr := stmt.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("failed to close metadata insert statement: %w", closeErr)
		}
	}()

	metadata := meta.ToMap()

	for key, value := range metadata {
		if _, err := stmt.Exec(key, value); err != nil {
			return fmt.Errorf("failed to insert metadata %q: %w", key, err)
		}
	}

	return nil
}

// WriteTile adds a tile to the batch. When the batch is full, it is automatically flushed.
// Raster payloads (png, jpg, webp) are stored uncompressed, as the MBTiles 1.3 spec
// requires; for a pbf tileset the payload is gzipped on flush, as the same spec requires
// for vector tiles. Coordinates are converted to TMS format.
func (w *Writer) WriteTile(z, x, y int, tileData []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.batch = append(w.batch, TileEntry{
		Z:    z,
		X:    x,
		Y:    y,
		Data: tileData,
	})

	if len(w.batch) >= w.batchSize {
		return w.flushLocked()
	}

	return nil
}

// HasTile reports whether the tile is already stored, so a resumed run can skip
// re-rendering it. Coordinates are XYZ, as for WriteTile.
//
// Two things make this more than a plain SELECT. First, up to batchSize entries
// may still be sitting in w.batch, invisible to SQL, so the buffer is scanned
// before the database. Second, the stored row is flipped to TMS — see tmsRow.
//
// The query is a single seek against the UNIQUE tile_index. The key set is
// deliberately not preloaded: a resumed z14 planet tileset holds hundreds of
// millions of keys.
func (w *Writer) HasTile(z, x, y int) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Unflushed writes are not queryable yet, so check the buffer first.
	for i := range w.batch {
		if w.batch[i].Z == z && w.batch[i].X == x && w.batch[i].Y == y {
			return true, nil
		}
	}

	if w.hasStmt == nil {
		stmt, err := w.db.Prepare("SELECT 1 FROM tiles WHERE zoom_level=? AND tile_column=? AND tile_row=? LIMIT 1")
		if err != nil {
			return false, fmt.Errorf("failed to prepare tile lookup: %w", err)
		}
		w.hasStmt = stmt
	}

	var exists int
	err := w.hasStmt.QueryRow(z, x, tmsRow(z, y)).Scan(&exists)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("failed to look up tile %d/%d/%d: %w", z, x, y, err)
	}

	return true, nil
}

// Flush writes any buffered tiles to the database.
func (w *Writer) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.flushLocked()
}

// flushLocked writes buffered tiles to the database. Must be called with lock held.
func (w *Writer) flushLocked() (err error) {
	if len(w.batch) == 0 {
		return nil
	}

	tx, err := w.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback() // nolint:errcheck

	stmt, err := tx.Prepare("INSERT OR REPLACE INTO tiles (zoom_level, tile_column, tile_row, tile_data) VALUES (?, ?, ?, ?)")
	if err != nil {
		return fmt.Errorf("failed to prepare insert: %w", err)
	}
	defer func() {
		if closeErr := stmt.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("failed to close tile insert statement: %w", closeErr)
		}
	}()

	for _, tile := range w.batch {
		// Convert XYZ to TMS coordinates
		tmsY := tmsRow(tile.Z, tile.Y)

		// Raster bytes go in verbatim: MBTiles readers (QGIS, tileserver-gl,
		// mbutil) expect raster tile_data to be the image itself. Vector (pbf)
		// tiles are the opposite case — the spec requires a gzip envelope.
		data := tile.Data
		if w.gzipTiles {
			data, err = gzipBytes(tile.Data)
			if err != nil {
				return fmt.Errorf("failed to gzip tile %d/%d/%d: %w", tile.Z, tile.X, tile.Y, err)
			}
		}

		if _, err := stmt.Exec(tile.Z, tile.X, tmsY, data); err != nil {
			return fmt.Errorf("failed to insert tile %d/%d/%d: %w", tile.Z, tile.X, tile.Y, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	w.batch = w.batch[:0]
	return nil
}

// gzipBytes wraps data in a gzip stream, as the MBTiles spec mandates for pbf
// vector tiles. Reader.ReadTile unwraps it again via maybeGunzip.
func gzipBytes(data []byte) (compressed []byte, err error) {
	var buf bytes.Buffer

	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(data); err != nil {
		return nil, errors.Join(err, gw.Close())
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// closeHasStmt releases the lazily prepared HasTile statement, if any.
func (w *Writer) closeHasStmt() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.hasStmt == nil {
		return nil
	}

	err := w.hasStmt.Close()
	w.hasStmt = nil
	if err != nil {
		return fmt.Errorf("failed to close tile lookup statement: %w", err)
	}

	return nil
}

// Close flushes any remaining tiles and closes the database.
func (w *Writer) Close() error {
	if err := w.Flush(); err != nil {
		return errors.Join(err, w.closeHasStmt(), w.db.Close())
	}

	if err := w.closeHasStmt(); err != nil {
		return errors.Join(err, w.db.Close())
	}

	if err := w.db.Close(); err != nil {
		return fmt.Errorf("failed to close database: %w", err)
	}

	return nil
}
