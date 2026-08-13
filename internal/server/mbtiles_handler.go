package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"strings"

	"github.com/cwbudde/watercolormap/internal/mbtiles"
	"github.com/cwbudde/watercolormap/internal/tile"
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
	coords, suffix, ok := parseTilePath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}

	// Note: suffix (@2x) is ignored for MBTiles serving
	// Separate MBTiles files should be used for different tile sizes
	_ = suffix

	w.Header().Set("Cache-Control", h.cacheControl)
	w.Header().Set("Content-Type", "image/png")

	// Read tile from MBTiles
	data, err := h.reader.ReadTile(int(coords.Z), int(coords.X), int(coords.Y))
	if err != nil {
		h.log().Error("Failed to read tile", "coords", coords.String(), "error", err)
		http.Error(w, "Tile not found", http.StatusNotFound)
		return
	}

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

// parseTilePath parses a tile path like /tiles/z13_x4317_y2692.png
// Returns tile coordinates, suffix (e.g., "@2x"), and success flag.
func parseTilePathMBTiles(requestPath string) (tile.Coords, string, bool) {
	if !strings.HasPrefix(requestPath, "/tiles/") {
		return tile.Coords{}, "", false
	}

	base := path.Base(requestPath)
	if !strings.HasSuffix(base, ".png") {
		return tile.Coords{}, "", false
	}

	name := strings.TrimSuffix(base, ".png")
	suffix := ""

	// Handle @2x suffix
	if strings.HasSuffix(name, "@2x") {
		suffix = "@2x"
		name = strings.TrimSuffix(name, "@2x")
	}

	coords, err := tile.ParseCoords(name)
	if err != nil {
		return tile.Coords{}, "", false
	}

	return coords, suffix, true
}
