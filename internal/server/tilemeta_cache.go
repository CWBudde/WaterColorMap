package server

import (
	"os"
	"time"

	"github.com/cwbudde/watercolormap/internal/lru"
	"github.com/cwbudde/watercolormap/internal/tile"
)

// Defaults for the tile-metadata cache.
//
// The TTL is seconds rather than minutes on purpose: `purge` runs out of
// process and deletes tiles this server knows nothing about, so the TTL is the
// only bound on how long a purged tile can still be served. Ten seconds keeps
// that window shorter than a user notices while still collapsing the burst of
// requests a single map pan produces.
const (
	DefaultTileMetaCacheEntries = 4096
	DefaultTileMetaCacheTTL     = 10 * time.Second
)

// tileMeta is everything the cache-hit path needs to know about a tile file
// before opening it: that it is there, that it satisfies the freshness policy,
// and how to identify it to a client.
//
// Only tiles that passed both checks are ever stored, so the presence of an
// entry *is* the answer to both questions -- there is no "known stale" state to
// represent, because a stale tile is re-rendered and the entry replaced.
type tileMeta struct {
	modTime time.Time
	etag    string
}

// newTileMetaCache builds the cache in front of the tile directory. Entries of
// zero or less disable it, and the returned cache then answers every call
// without storing anything, so the call sites need no branch.
func newTileMetaCache(entries int, ttl time.Duration) *lru.Cache[string, tileMeta] {
	return lru.New[string, tileMeta](entries, ttl)
}

// lookupTileMeta answers "may this tile be served from disk, and how do I
// identify it" -- from the cache when it can, from the filesystem and the stamp
// store when it cannot.
//
// This is what the cache is for: without it every hit costs an os.Stat to prove
// the file is there, a second one inside the file server, and -- whenever a
// --stale-* policy is configured -- a stamp-store lookup on top. A live entry
// costs a map lookup.
func (t *OnDemandTiles) lookupTileMeta(
	fullPath string,
	coords tile.Coords,
	suffix string,
	countStale bool,
) (tileMeta, bool) {
	if t.metaCache != nil {
		if meta, ok := t.metaCache.Get(fullPath); ok {
			return meta, true
		}
	}

	fi, err := os.Stat(fullPath)
	if err != nil || fi.IsDir() {
		return tileMeta{}, false
	}
	if !t.cachedTileIsFresh(coords, suffix, countStale) {
		return tileMeta{}, false
	}

	meta := tileMeta{modTime: fi.ModTime(), etag: tileETag(fi)}
	if t.metaCache != nil {
		t.metaCache.Put(fullPath, meta)
	}
	return meta, true
}

// invalidateTileMeta drops what the cache believes about one tile. It is called
// after every render -- successful or not -- because a render replaces the file
// the cached validator described, and after a failed open, because an entry
// whose file has gone is worse than no entry at all.
func (t *OnDemandTiles) invalidateTileMeta(fullPath string) {
	if t.metaCache != nil {
		t.metaCache.Remove(fullPath)
	}
}
