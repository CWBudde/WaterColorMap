package tilejson

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

// ContentType is the media type registered for TileJSON documents.
const ContentType = "application/json; charset=utf-8"

// Handler serves doc as JSON. Tile templates that are site-relative (they start
// with "/") are expanded to absolute URLs using the scheme and host of the
// incoming request, because the TileJSON spec asks for fully qualified tile
// URLs and the server cannot know its own public origin up front.
//
// The handler is read-only; doc is copied per request, never mutated.
func Handler(doc TileJSON, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD, OPTIONS")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		resolved := doc
		resolved.Tiles = absoluteTileURLs(doc.Tiles, requestOrigin(r))

		data, err := json.MarshalIndent(resolved, "", "  ")
		if err != nil {
			http.Error(w, "failed to encode tilejson", http.StatusInternalServerError)
			return
		}
		data = append(data, '\n')

		w.Header().Set("Content-Type", ContentType)
		if _, err := w.Write(data); err != nil && logger != nil {
			logger.Debug("tilejson response write failed", "error", err)
		}
	})
}

// requestOrigin reconstructs the scheme://host the client used.
func requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if r.Host == "" {
		return ""
	}
	return scheme + "://" + r.Host
}

// absoluteTileURLs prefixes site-relative templates with origin. Templates that
// are already absolute, and every template when the origin is unknown, are left
// untouched.
func absoluteTileURLs(tiles []string, origin string) []string {
	out := make([]string, len(tiles))
	for i, t := range tiles {
		if origin != "" && strings.HasPrefix(t, "/") {
			out[i] = origin + t
			continue
		}
		out[i] = t
	}
	return out
}
