package datasource

import (
	"context"
	"errors"
	"log/slog"

	"github.com/cwbudde/watercolormap/internal/types"
)

// shouldTryNextServer reports whether err is worth retrying against another
// Overpass server.
//
// Not every failure is. Burning a second server on an error the second server
// would reproduce wastes the fetch budget, and doing it during shutdown holds
// the process open past the point the caller gave up.
//
//   - Context cancellation and deadline expiry mean the *caller* is gone. Another
//     server cannot help, and the request would fail on the same context anyway.
//   - ErrResponseTooLarge is a property of the data and the configured cap, not
//     of the server: the same bbox at the same zoom returns a similarly oversized
//     body everywhere. (The cap is also usually shared across servers.)
//
// Everything else — transport failures, 5xx, 429, an HTML error page, and
// ErrEmptyOverpassResponse, which validateFeatureResponse documents as the shape
// of a silent upstream failure — is exactly what a healthy second server exists
// to answer.
func shouldTryNextServer(err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return false
	case errors.Is(err, ErrResponseTooLarge):
		return false
	default:
		return true
	}
}

// candidates returns the servers whose coverage admits bounds, in configuration
// order. A nil coverage matches everything.
func (mds *MultiOverpassDataSource) candidates(bounds types.BoundingBox) []serverInstance {
	matching := make([]serverInstance, 0, len(mds.servers))
	for _, srv := range mds.servers {
		if srv.coverage == nil || intersects(bounds, *srv.coverage) {
			matching = append(matching, srv)
		}
	}
	return matching
}

// warnUnreachableCoverage logs coverage boxes that can never be selected first.
//
// Routing takes servers in configuration order, so a server whose coverage is
// entirely inside an earlier server's coverage is shadowed: every tile it could
// answer is claimed by the earlier entry. That is a configuration mistake with
// no other symptom — the tiles still render, just from the wrong server, which
// for a "local instance plus public fallback" setup means quietly paying the
// public API's rate limits for a region you built a local instance for.
//
// A warning rather than an error: overlapping-but-not-contained boxes are
// legitimate and common, and failing startup over a layout choice would be
// worse than saying so once.
//
// Failover makes shadowing less harmful than it was — a shadowed server is now
// tried when the one in front of it fails — but it does not make the ordering
// right, so this still deserves saying.
func warnUnreachableCoverage(servers []serverInstance) {
	for i, srv := range servers {
		if srv.coverage == nil {
			// A nil-coverage server matches everything, so it shadows every
			// later entry by construction. That is the documented way to
			// declare a fallback, so it is only worth mentioning when it is
			// not last.
			if i < len(servers)-1 {
				slog.Warn("Overpass server has no coverage but is not last; later servers are only reachable when it fails",
					"server", srv.name, "position", i+1, "servers", len(servers))
			}
			continue
		}

		for j := 0; j < i; j++ {
			earlier := servers[j]
			if earlier.coverage == nil || contains(*earlier.coverage, *srv.coverage) {
				slog.Warn("Overpass server coverage is shadowed by an earlier server; it is only reachable when that one fails",
					"server", srv.name, "shadowed_by", earlier.name)
				break
			}
		}
	}
}

// contains reports whether outer fully encloses inner.
func contains(outer, inner types.BoundingBox) bool {
	return outer.MinLon <= inner.MinLon && outer.MaxLon >= inner.MaxLon &&
		outer.MinLat <= inner.MinLat && outer.MaxLat >= inner.MaxLat
}
