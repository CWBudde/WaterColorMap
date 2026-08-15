package tilejson

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

// ContentType is the media type registered for TileJSON documents.
const ContentType = "application/json; charset=utf-8"

// TrustForwarded reports whether the forwarding headers on r may be believed,
// i.e. whether r demonstrably arrived through a proxy the operator runs. A nil
// TrustForwarded trusts nothing, which is the safe default: X-Forwarded-Proto
// is attacker-controlled on a directly reachable server.
type TrustForwarded func(r *http.Request) bool

// Handler serves doc as JSON. Tile templates that are site-relative (they start
// with "/") are expanded to absolute URLs using the scheme and host of the
// incoming request, because the TileJSON spec asks for fully qualified tile
// URLs and the server cannot know its own public origin up front.
//
// trust decides whether X-Forwarded-Proto is honoured; pass the server's
// trusted-proxy policy, or nil to ignore the header entirely.
//
// The handler is read-only; doc is copied per request, never mutated.
func Handler(doc TileJSON, logger *slog.Logger, trust TrustForwarded) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD, OPTIONS")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		resolved := doc
		resolved.Tiles = absoluteTileURLs(doc.Tiles, requestOrigin(r, trust))

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
//
// Behind a TLS-terminating proxy the backend connection is plain HTTP, so
// r.TLS is nil even though the browser used https. Advertising http:// tile
// URLs to a page loaded over https gets them blocked as mixed content, which
// breaks the map without any error the operator would connect to TileJSON. So
// X-Forwarded-Proto is honoured — but only when trust says the request really
// came through a proxy we run, since the header is otherwise trivially forged.
func requestOrigin(r *http.Request, trust TrustForwarded) string {
	if r.Host == "" {
		return ""
	}

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if trust != nil && trust(r) {
		if proto := forwardedProto(r); proto != "" {
			scheme = proto
		}
	}
	return scheme + "://" + r.Host
}

// forwardedProto reads the client's scheme from the forwarding headers,
// preferring the standard Forwarded header over the de-facto
// X-Forwarded-Proto. Only "http" and "https" are accepted; anything else is a
// malformed or hostile value and is ignored rather than spliced into a URL.
func forwardedProto(r *http.Request) string {
	if fwd := r.Header.Get("Forwarded"); fwd != "" {
		// "for=1.2.3.4;proto=https, for=5.6.7.8" — the first element is the
		// one closest to the client.
		first, _, _ := strings.Cut(fwd, ",")
		for param := range strings.SplitSeq(first, ";") {
			key, value, found := strings.Cut(strings.TrimSpace(param), "=")
			if found && strings.EqualFold(strings.TrimSpace(key), "proto") {
				return normalizeScheme(strings.Trim(strings.TrimSpace(value), `"`))
			}
		}
	}

	// A proxy chain appends, so the client-side value is the leftmost hop.
	proto, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")
	return normalizeScheme(strings.TrimSpace(proto))
}

func normalizeScheme(s string) string {
	switch strings.ToLower(s) {
	case "http":
		return "http"
	case "https":
		return "https"
	default:
		return ""
	}
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
