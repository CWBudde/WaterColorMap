package server

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/cwbudde/watercolormap/internal/mbtiles"
)

// MBTilesHandler serves tiles from an MBTiles database.
type MBTilesHandler struct {
	reader       *mbtiles.Reader
	logger       *slog.Logger
	cacheControl string
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

	return &MBTilesHandler{
		reader:       reader,
		logger:       logger,
		cacheControl: cfg.CacheControl,
	}, nil
}

// Handler returns the HTTP handler function.
func (h *MBTilesHandler) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		h.serveTile(w, r)
	}
}

// serveTile serves a single tile from the MBTiles database.
func (h *MBTilesHandler) serveTile(w http.ResponseWriter, r *http.Request) {
	coords, suffix, err := parseTilePath(r.URL.Path)
	if err != nil {
		writeTilePathError(w, r, h.log(), err)
		return
	}

	// Note: suffix (@2x) is ignored for MBTiles serving
	// Separate MBTiles files should be used for different tile sizes
	_ = suffix

	// Read tile from MBTiles before committing to a PNG response, so the
	// error path is not served with an image/png content type.
	data, err := h.reader.ReadTile(int(coords.Z), int(coords.X), int(coords.Y))
	if err != nil {
		h.log().Error("Failed to read tile", "coords", coords.String(), "error", err)
		http.Error(w, "Tile not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Cache-Control", h.cacheControl)
	w.Header().Set("Content-Type", "image/png")

	// Write PNG data
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
