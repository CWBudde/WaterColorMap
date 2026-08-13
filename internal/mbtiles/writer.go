package mbtiles

import (
	"database/sql"
	"errors"
	"fmt"
	"sync"

	_ "modernc.org/sqlite" // SQLite driver
)

const (
	// DefaultBatchSize is the number of tiles to buffer before flushing to the database.
	DefaultBatchSize = 100
)

// TileEntry represents a single tile to be written.
type TileEntry struct {
	Data []byte // Raw PNG data, stored verbatim (raster tiles are never gzipped)
	Z    int
	X    int
	Y    int
}

// Writer writes tiles to an MBTiles database.
type Writer struct {
	db        *sql.DB
	path      string
	batch     []TileEntry
	metadata  Metadata
	batchSize int
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

	// Insert metadata
	if err := insertMetadata(db, metadata); err != nil {
		return nil, errors.Join(fmt.Errorf("failed to insert metadata: %w", err), db.Close())
	}

	return &Writer{
		db:        db,
		path:      path,
		batch:     make([]TileEntry, 0, DefaultBatchSize),
		batchSize: DefaultBatchSize,
		metadata:  metadata,
	}, nil
}

// createSchema creates the MBTiles database schema.
func createSchema(db *sql.DB) error {
	schema := `
		-- The spec requires metadata names to be unique; PRIMARY KEY enforces
		-- that. insertMetadata clears the table first, so re-writing is safe.
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
// The PNG data is stored uncompressed, as the MBTiles 1.3 spec requires for raster
// formats; gzip applies to pbf vector tiles only. Coordinates are converted to TMS format.
func (w *Writer) WriteTile(z, x, y int, pngData []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.batch = append(w.batch, TileEntry{
		Z:    z,
		X:    x,
		Y:    y,
		Data: pngData,
	})

	if len(w.batch) >= w.batchSize {
		return w.flushLocked()
	}

	return nil
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
		tmsY := (1 << tile.Z) - 1 - tile.Y

		// Store the PNG bytes verbatim: MBTiles readers (QGIS, tileserver-gl,
		// mbutil) expect raster tile_data to be the image itself.
		if _, err := stmt.Exec(tile.Z, tile.X, tmsY, tile.Data); err != nil {
			return fmt.Errorf("failed to insert tile %d/%d/%d: %w", tile.Z, tile.X, tile.Y, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	w.batch = w.batch[:0]
	return nil
}

// Close flushes any remaining tiles and closes the database.
func (w *Writer) Close() error {
	if err := w.Flush(); err != nil {
		return errors.Join(err, w.db.Close())
	}

	if err := w.db.Close(); err != nil {
		return fmt.Errorf("failed to close database: %w", err)
	}

	return nil
}
