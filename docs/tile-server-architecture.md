# The tile server

What `watercolormap serve` is, how a tile request travels through it, and which
of its several caches answers what. Read this before changing admission control,
the per-tile lock, the cache-hit path or the `Cache-Control` policy — several of
them look like accidents and are not.

The server has two backends. `--tiles-dir` serves a tile **folder** and renders
what is missing (`internal/server/ondemand_tiles.go`); `--mbtiles` serves a
finished **tileset** read-only and renders nothing
(`internal/server/mbtiles_handler.go`). Routing, middleware, timeouts and
shutdown live in the command (`internal/cmd/serve.go`), not in the package, so
the handlers cannot override a policy the operator set.

## The request path

```
GET /tiles/z13_x4317_y2692.png
  │
  ├─ withCORS            serve.go — single owner of the CORS headers; answers preflights
  ├─ withReadMethods     GET/HEAD only, so an OPTIONS can never trigger a render
  ├─ IPRateLimiter       per-IP token bucket, keyed by the rightmost untrusted XFF hop
  │
  ├─ parseTilePath       coords + @2x suffix + extension; 400 vs 404 sentinels
  ├─ format check        this server renders one format; the other extension is 404
  │
  ├─ CACHE CHECK ────────► HIT: metadata (cached) → open → ServeContent → 200 or 304
  │
  ├─ admit()             bounded backlog; over the limit → 503 + Retry-After
  ├─ lockTile(filename)  refcounted per-tile lock: one render per tile, everyone else waits
  ├─ CACHE RE-CHECK ────► HIT-COALESCED: someone else rendered it while we waited
  │
  ├─ render semaphore    MaxConcurrentGenerations
  ├─ FetchQueue          Overpass, decoupled from rendering; skipped on Natural-Earth zooms
  ├─ GenerateWithData    Mapnik + the watercolor pipeline; writes by atomic rename
  ├─ stamp               provenance for `purge` and `generate --stale-*`
  └─ MISS                serve the file just written
```

Failure at the fetch or render step queues a retry (`retryQueue`, zoom-aware
backoff, `maxRetries = 3`) and answers 502. The write deadline is extended —
and re-armed after every long wait — because generation legitimately outlives
the server-wide 30 s `WriteTimeout`, while the cache-hit path keeps the short
default.

### Why the admission gate sits _below_ the cache check

Because a middleware could not tell a hit from a miss, and would therefore shed
requests for tiles that already exist on disk — breaking the demo hardest
exactly when the map is fully cached and there is nothing to protect. Admission
counts requests **in the generation path**, which is the only thing worth
bounding. `TestServeTileCacheHitBypassesAdmission` pins this.

Per-IP rate limiting is not backpressure and is not a substitute: it bounds one
client, not total concurrent renders, and it also cannot tell a hit from a miss.
Both exist because they answer different questions.

### Why a refcounted lock map instead of `singleflight`

Waiters here do not want another goroutine's return value; they want the _file_,
which the winner writes and they then serve like any other cache hit — with
their own validator and their own conditional-request handling. And the map has
to shrink: a crawler walking the z18 grid would otherwise leak an entry per tile
ever requested. Entries are refcounted over holders **and** waiters and dropped
at zero (`TestLockTileEvictsWhenUnused`).

## Four caches, and what each is for

| Layer              | Where              | Holds                           | Bounded by                                           |
| ------------------ | ------------------ | ------------------------------- | ---------------------------------------------------- |
| HTTP validators    | client, proxy, CDN | the tile itself                 | `Cache-Control` + `ETag` revalidation                |
| Tile metadata      | this process       | exists + fresh + ETag, per tile | `--tile-meta-cache-entries`, `--tile-meta-cache-ttl` |
| Tile files         | the node's disk    | the rendered tiles              | the tile directory; `purge`                          |
| Overpass responses | the node's disk    | upstream OSM answers            | `cache.max_size`, `cache.ttl` (opt-in)               |

**The OS page cache is the tile-byte cache.** Nothing in this process holds tile
bytes in memory, deliberately: the kernel already caches recently served files,
a copy on the Go heap would add GC pressure proportional to the cache size, and
it would introduce a coherence problem with `purge`, which deletes files behind
the server's back. The metadata cache stores tens of bytes per tile, not
kilobytes, which is why it can be bounded and short-lived without giving much
back.

### The tile-metadata cache

`internal/server/tilemeta_cache.go`, over `internal/lru`. One entry per tile
file: modification time and ETag. Only tiles that exist _and_ satisfy the
freshness policy are ever stored, so the presence of an entry answers both
questions — there is no "known stale" state, because a stale tile is re-rendered
and its entry replaced.

Without it, every hit paid two `os.Stat` calls and, with a `--stale-*` policy
configured, a SQLite lookup against the stamp store as well. Measured with
`just load-test` on an i7-1255U (12th gen), `-cpu 8`, stubbed renderer:

| Benchmark                             | before                | after                 |
| ------------------------------------- | --------------------- | --------------------- |
| `BenchmarkTileHitHandler`             | 3151 ns/op, 44 allocs | 2203 ns/op, 38 allocs |
| `BenchmarkTileHitWithFreshnessPolicy` | 3769 ns/op            | 2278 ns/op            |
| `BenchmarkTileHitServer/plain`        | 23210 ns/op           | 18060 ns/op           |

Invalidated after every render — successful or not, in the request path and in
the retry worker — and after any failed open. What it cannot see is
out-of-process change: `purge` deletes tiles this server knows nothing about, so
**a purged tile can still be served for up to the TTL**. That is the whole
reason the default is 10 s rather than minutes. A deleted file never produces a
200: the open fails, the entry is dropped, and the request falls through to
generation or 404.

### HTTP validators, and the fight with `purge`

Both backends send a strong `ETag` and answer conditional requests.

- **Folder backend**: `"<mtime-nanos-hex>-<size-hex>"`, nginx's shape, taken
  from the `FileInfo` the response already needed. Not a content hash — hashing
  every response is precisely the work the hit path exists to avoid — and not
  the data stamp, which is optional and would cost the lookup the metadata cache
  just removed. Note that on a filesystem with coarse `mtime` resolution the
  validator only moves once the size changes or the clock ticks.
- **MBTiles backend**: `"<len-hex>-<fnv64-hex>"` over the tile bytes. A row has
  no modification time, and the database file's would be wrong the moment
  `purge` writes underneath the open handle. Hashing is affordable here because
  the request has already paid for the SQLite read. `Last-Modified` is
  deliberately absent rather than invented.

`--cache-control` defaults to **`no-cache`**: store the tile, revalidate every
time. This is the smallest default that makes the validator real — the previous
`no-store` forbade the client from keeping anything, so it could never send a
conditional request and net/http's precondition handling was dead code — while
keeping `purge` authoritative, since a purged tile is gone on the very next
request.

A positive `max-age` is faster and is **not** the default, because it fights
`purge` directly: a tile removed from disk stays in browser and CDN caches for
the full duration with no way to reach it. Behind a CDN, where that trade is
usually worth making:

```bash
watercolormap serve --cache-control 'public, max-age=300, stale-while-revalidate=86400'
```

`--cache-control no-store` restores the pre-ETag behaviour exactly.

Error responses always carry `no-store` and no validator: a cached 404 pins a
tile broken long after the render that would have fixed it.

## Observability

Every tile response carries `X-Cache`: `HIT`, `HIT-COALESCED` (another request
rendered it while this one waited), `MISS`, or `BYPASS` (`--disable-cache`). It
is exposed cross-origin via `Access-Control-Expose-Headers`, without which a
browser hides it from the page.

`/tiles/status` (JSON) and `/tiles/status/stream` (SSE, 250 ms) carry the
running totals under `cache`:

```json
{
  "hits": 3,
  "hits_coalesced": 0,
  "misses": 0,
  "stale": 0,
  "bypasses": 0,
  "meta_cache_hits": 2,
  "meta_cache_misses": 1,
  "meta_cache_evictions": 0,
  "not_modified": 1
}
```

`stale` is a reason rather than an outcome: it counts misses caused by a tile
that existed but failed the freshness policy, which is the number that says a
`--stale-*` cutoff is re-rendering more than intended. It is recorded in the
first cache check only, so one stale tile is never reported twice. `not_modified`
overlaps the hit counters — a revalidated hit is still a hit, just a cheap one.

There are no Prometheus, OTEL or expvar metrics. The status endpoints are the
whole story, and are deliberately not rate limited: the SSE stream reconnects
after any error, so throttling it produces a reconnect storm.

## Load testing

`just load-test` runs the benchmarks in `internal/server/load_test.go` at
`-cpu 1,4,8`. They drive the real handler — and, for the server variants, a real
socket and the real middleware chain — with the render replaced through the
`tileGenerator` seam, so what they measure is server work rather than Mapnik.
Two consequences worth stating plainly: the miss-path numbers are **not**
end-to-end tile latency, and the package still links Mapnik, so a build still
needs it.

`BenchmarkTileMissDedup` reports `renders/req`; 16 concurrent requests for one
tile measure 0.0625, which is exactly one render. A number drifting up is the
per-tile lock failing to coalesce.

Benchmarks do not run in CI, so `TestServeTileUnderConcurrentLoad` covers the
same paths under `-race` with hits and misses interleaved.

## Redis, and why there is none

`watercolormap` is a single static binary whose cache of record is the tile
directory (or the MBTiles file). It already has correct deduplication, correct
backpressure and correct provenance. A shared Redis would add a mandatory
service to a single-node deployment and hold a second copy of bytes the
filesystem already has — and it would not answer the one question it would be
bought for, cross-node coordination, better than a shared filesystem with a CDN
in front, which is what the `ETag` and `Cache-Control` work above enables.

Worth revisiting only if cross-node render coalescing becomes a measured need
rather than an anticipated one. Multi-node hosting is [PLAN.md](../PLAN.md)
§5.5's problem, not this file's.

## Known limits

- **Single node.** Two servers over one tile directory each keep their own
  metadata cache and their own locks, so they can render the same tile twice.
- **Purge visibility is TTL-bounded** (above).
- **The MBTiles backend has no generator**: a missing tile is a 404, and an
  `@2x` request is answered with base-resolution bytes rather than 404ing a tile
  the client can still use.
- **No `@2x` in MBTiles at all** — a tileset holds one tile size. Retina needs
  the folder backend.
