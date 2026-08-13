package mbtiles

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Reader reads tiles from an MBTiles database.
type Reader struct {
	db   *sql.DB
	path string
}

// OpenReader opens an MBTiles database for reading.
func OpenReader(path string) (*Reader, error) {
	// Open in read-only mode with immutable flag
	db, err := sql.Open("sqlite", path+"?mode=ro&immutable=1")
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Verify schema exists
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='tiles'").Scan(&count)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("failed to verify schema: %w", err), db.Close())
	}
	if count == 0 {
		return nil, errors.Join(errors.New("database does not contain tiles table"), db.Close())
	}

	return &Reader{
		db:   db,
		path: path,
	}, nil
}

// ReadTile reads a tile from the database and returns the PNG data.
// Spec-conformant raster tiles are stored uncompressed, but tiles written by
// older versions of this package were gzipped, so the blob is gunzipped when
// it carries the gzip magic bytes.
// Coordinates are in XYZ format and will be converted to TMS internally.
func (r *Reader) ReadTile(z, x, y int) ([]byte, error) {
	// Convert XYZ to TMS coordinates
	tmsY := (1 << z) - 1 - y

	var tileData []byte
	err := r.db.QueryRow(
		"SELECT tile_data FROM tiles WHERE zoom_level=? AND tile_column=? AND tile_row=?",
		z, x, tmsY,
	).Scan(&tileData)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("tile not found: %d/%d/%d", z, x, y)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query tile: %w", err)
	}

	data, err := maybeGunzip(tileData)
	if err != nil {
		return nil, fmt.Errorf("failed to decompress tile: %w", err)
	}

	return data, nil
}

// Metadata reads metadata from the database.
func (r *Reader) Metadata() (Metadata, error) {
	metaMap, err := r.readMetadataMap()
	if err != nil {
		return Metadata{}, err
	}

	return metadataFromMap(metaMap), nil
}

// readMetadataMap reads all name/value pairs from the metadata table.
func (r *Reader) readMetadataMap() (metaMap map[string]string, err error) {
	rows, err := r.db.Query("SELECT name, value FROM metadata")
	if err != nil {
		return nil, fmt.Errorf("failed to query metadata: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			metaMap, err = nil, fmt.Errorf("failed to close metadata rows: %w", closeErr)
		}
	}()

	metaMap = make(map[string]string)

	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return nil, fmt.Errorf("failed to scan metadata row: %w", err)
		}
		metaMap[name] = value
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating metadata: %w", err)
	}

	return metaMap, nil
}

// metadataFromMap converts raw metadata name/value pairs into a Metadata struct.
// Missing or malformed numeric values are ignored, leaving the corresponding
// field at its zero value (nil for MinZoom/MaxZoom).
func metadataFromMap(metaMap map[string]string) Metadata {
	meta := Metadata{}

	meta.Name = metaMap["name"]
	meta.Format = metaMap["format"]
	meta.Attribution = metaMap["attribution"]
	meta.Description = metaMap["description"]
	meta.Type = metaMap["type"]
	meta.Version = metaMap["version"]

	if v, ok := metaMap["minzoom"]; ok {
		if i, err := strconv.Atoi(v); err == nil {
			meta.MinZoom = Zoom(i)
		}
	}
	if v, ok := metaMap["maxzoom"]; ok {
		if i, err := strconv.Atoi(v); err == nil {
			meta.MaxZoom = Zoom(i)
		}
	}

	// Parse bounds: "minLon,minLat,maxLon,maxLat"
	parseFloatList(metaMap["bounds"], meta.Bounds[:])

	// Parse center: "lon,lat,zoom"
	parseFloatList(metaMap["center"], meta.Center[:])

	return meta
}

// parseFloatList parses a comma-separated list of floats into dst.
// It is a no-op unless the list has exactly len(dst) elements; individual
// values that fail to parse leave their destination element untouched.
func parseFloatList(value string, dst []float64) {
	if value == "" {
		return
	}

	parts := strings.Split(value, ",")
	if len(parts) != len(dst) {
		return
	}

	for i, part := range parts {
		if f, err := strconv.ParseFloat(strings.TrimSpace(part), 64); err == nil {
			dst[i] = f
		}
	}
}

// Close closes the database connection.
func (r *Reader) Close() error {
	if err := r.db.Close(); err != nil {
		return fmt.Errorf("failed to close database: %w", err)
	}
	return nil
}

// gzipMagic is the two-byte header every gzip stream starts with (RFC 1952).
var gzipMagic = [2]byte{0x1f, 0x8b}

// maybeGunzip decompresses data when it starts with the gzip magic bytes and
// returns it unchanged otherwise. Raster MBTiles blobs are stored as-is per the
// spec, but databases written by earlier versions of this package gzipped their
// PNGs, so both layouts have to stay readable.
func maybeGunzip(data []byte) (uncompressed []byte, err error) {
	if len(data) < len(gzipMagic) || data[0] != gzipMagic[0] || data[1] != gzipMagic[1] {
		return data, nil
	}

	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := gr.Close(); closeErr != nil && err == nil {
			uncompressed, err = nil, closeErr
		}
	}()

	uncompressed, err = io.ReadAll(gr)
	if err != nil {
		return nil, err
	}

	return uncompressed, nil
}
