package server

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/cwbudde/watercolormap/internal/mbtiles"
	"github.com/cwbudde/watercolormap/internal/tileformat"
)

// MBTilesHandler serves tiles from an MBTiles database.
type MBTilesHandler struct {
	reader       *mbtiles.Reader
	logger       *slog.Logger
	cacheControl string
	// format and contentType are resolved once at construction from the
	// tileset's own metadata, rather than per request or from a flag: the file
	// is the only authority on what its bytes are.
	format      tileformat.Format
	contentType string
}

// MBTilesConfig configures the MBTiles handler.
type MBTilesConfig struct {
	MBTilesPath  string
	CacheControl string
}

// NewMBTilesHandler creates a new MBTiles handler.
func NewMBTilesHandler(cfg MBTilesConfig, logger *slog.Logger) (*MBTilesHandler, error) {
	reader, err := mbtiles.OpenReader(cfg.MBTilesPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open MBTiles: %w", err)
	}

	// Callers may pass a nil logger; the handler's own log() handles that
	// everywhere else, and construction has to do the same.
	log := logger
	if log == nil {
		log = slog.Default()
	}

	// An unreadable or unrecognised format is not fatal: pre-existing files
	// predate the metadata being trustworthy, and PNG is what they all were.
	format := tileformat.PNG
	if meta, metaErr := reader.Metadata(); metaErr != nil {
		log.Warn("could not read MBTiles metadata; serving tiles as PNG",
			"path", cfg.MBTilesPath, "err", metaErr)
	} else if parsed, parseErr := tileformat.Parse(meta.Format); parseErr != nil {
		log.Warn("MBTiles tileset declares an unrecognised format; serving tiles as PNG",
			"path", cfg.MBTilesPath, "format", meta.Format)
	} else {
		format = parsed
	}

	return &MBTilesHandler{
		reader:       reader,
		logger:       logger,
		cacheControl: cfg.CacheControl,
		format:       format,
		contentType:  format.ContentType(),
	}, nil
}

// Handler returns the HTTP handler function.
//
// CORS is not handled here: it is owned entirely by the serve command's
// withCORS middleware, which also answers preflights, so the toggle there
// cannot be overridden from inside the handler.
func (h *MBTilesHandler) Handler() http.HandlerFunc {
	return h.serveTile
}

// serveTile serves a single tile from the MBTiles database.
func (h *MBTilesHandler) serveTile(w http.ResponseWriter, r *http.Request) {
	coords, suffix, format, err := parseTilePath(r.URL.Path)
	if err != nil {
		writeTilePathError(w, r, h.log(), err)
		return
	}

	// The @2x suffix is ignored here: a tileset holds one tile size, and this
	// handler only reads what the file already contains. Unlike the folder
	// backend, it has no generator to render a 512px tile on demand, so a
	// retina request is answered with base-resolution data rather than 404ing
	// a tile the client can still use. A deployment that needs true @2x has to
	// serve the tile directory (`serve --tiles-dir`) instead of the MBTiles
	// file — see the HiDPI note in CHANGELOG.md.
	_ = suffix

	// A tileset holds exactly one format, recorded in its metadata. Answering
	// the other extension with those same bytes would put WebP behind a .png
	// URL, which is the cache/URL lie the on-demand handler refuses for the
	// same reason: it outlives the request in every cache downstream. So the
	// extension has to match the file, and a mismatch is simply not found.
	if format != h.format {
		h.log().Debug("rejected tile request for a format this tileset does not hold",
			"path", r.URL.Path, "requested", format, "tileset", h.format)
		writeTileError(w, "tile not found", http.StatusNotFound)
		return
	}

	// Read tile from MBTiles before committing to an image response, so the
	// error path is not served with an image content type.
	data, err := h.reader.ReadTile(int(coords.Z), int(coords.X), int(coords.Y))
	if err != nil {
		h.log().Error("Failed to read tile", "coords", coords.String(), "error", err)
		http.Error(w, "Tile not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Cache-Control", h.cacheControl)
	w.Header().Set("Content-Type", h.contentType)
	// Always a hit: this backend reads a finished tileset and has no generator,
	// so there is no other outcome for it to report.
	w.Header().Set(cacheStatusHeader, cacheStatusHit)

	// Write the tile bytes verbatim
	if _, err := w.Write(data); err != nil {
		h.log().Error("Failed to write response", "error", err)
	}
}

// Close closes the MBTiles reader.
func (h *MBTilesHandler) Close() error {
	return h.reader.Close()
}

func (h *MBTilesHandler) log() *slog.Logger {
	if h.logger != nil {
		return h.logger
	}
	return slog.Default()
}
