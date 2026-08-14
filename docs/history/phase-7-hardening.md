# Phase 7: Repository Quality & Hardening — completed sections

Archived from `PLAN.md` (sections 7.1, 7.2, 7.3 and 7.7, all closed). The open
sections — 7.4 CI/build/tooling, 7.5 docs/hygiene, 7.6 testing — are still in
`PLAN.md`.

Phase 7 came out of a multi-area full-repo review (code quality, testing,
CI/build, docs, security) in 2026-08, which found that several things advertised
as working were in fact broken or non-functional, giving a false sense of safety.
Priorities: `[P0]` broken/red today, `[P1]` high impact, `[P2]` should-fix,
`[P3]` polish.

Everything below is **done**. It is kept because most entries record _why_ a
thing is the way it is, and several are easy to "fix" back into a bug.

## 7.1 Make the build & test suite green again (was P0 — RED)

- **[P0]** `internal/geojson` test suite did not compile — `converter_test.go`
  referenced the removed field `Civic`; it had been renamed to `Urban` in
  `types/feature.go:39`. Fixed the field names (and the stale log label) so the
  package builds and tests pass.
- **[P0]** `internal/watercolor` **panicked (SIGSEGV)** —
  `TestPaintLayerAppliesMaskTintAndEdge` hit a nil-pointer deref at
  `mask/processor.go:290` because a per-layer style `MaskNoiseStrength: 0.18`
  overrode the test's `NoiseStrength = 0` and entered the noise branch with a nil
  `PerlinNoise`. Added a nil-guard in `processMask` (skip noise when
  `PerlinNoise == nil`; production always sets it) and made the test's no-noise
  intent explicit via `style.MaskNoiseStrength = 0`. Fixing the panic also
  unmasked two pre-existing failures in `quality_test.go` (blur/threshold tests
  varied global params that per-layer style overrides then shadowed) — fixed
  those too.
- **[P0]** `docker/Dockerfile` **did not build** — `RUN` blocks at lines 22, 55,
  71 ended in a dangling `&&` with no trailing `\` (verified via `cat -A`).
  Likely caused by shfmt reformatting the Dockerfile (see 7.4, still open).
  Restored the line continuations.
- **[P0]** CI `test-unit.yaml` installed no Mapnik, so renderer/pipeline/server/cmd
  never compiled in CI and geojson (above) failed regardless — the unit job cannot
  have been green. Added the `libmapnik-dev` install step (mirroring
  `test-can-build`). Follow-up in 7.6: split pure-Go tests behind a build tag so
  they can run without Mapnik.

## 7.2 Security & robustness of the tile server (was P0/P1 — not internet-safe)

- **[P0]** Validate tile coordinates at parse time (`tile/coords.go`
  `ParseCoords`): added `MaxZoom = 22` plus a `Coords.Validate()` enforcing
  `z ≤ 22` and `x,y < 2^z`, with distinct sentinels `ErrCoordsFormat` /
  `ErrCoordsOutOfRange` so handlers can answer 404 vs **400**. `parseTilePath` now
  returns the error instead of swallowing it into a bool, and both tile backends
  map it via the shared `writeTilePathError`.

  Also found that `fmt.Sscanf` silently ignores trailing input, so
  `z13_x1_y2JUNK` parsed cleanly — closed with a `c.String() != s` round-trip
  check, which also rejects zero-padded aliases like `z013_x1_y2` that would have
  split the disk cache. Deleted the dead duplicate `parseTilePathMBTiles`, and
  moved the MBTiles `Content-Type: image/png` below its error branch so 404 bodies
  are no longer served as PNG.

- **[P0]** Add `recover()` to background workers — added `internal/safe`
  (`Do`/`Go`), the repo's first panic recovery of any kind, and applied it to the
  fetch workers and the retry worker. Recovery is deliberately **per job**, not
  per goroutine: a goroutine-level recover would leave the worker dead and
  silently shrink the pool.

  Two things this surfaced: a panicking fetch job must still deliver a
  `FetchResult`, or the caller blocked in `SubmitAndWait` is stranded until its
  own context expires; and `retryWorker` released the semaphore by hand on each
  branch, so a panic leaked a generation slot for the life of the process — the
  job body is now extracted into `runRetryJob` with a `defer`ed release.

- **[P1]** Per-IP rate limiting + bounded request-admission queue on `/tiles/`.
  Admission lives **inside** `OnDemandTiles`, not in middleware: middleware cannot
  tell a cache hit from a miss and would shed requests for tiles already on disk.
  The gate sits before the per-tile lock, because that lock is taken before the
  semaphore and held across the whole fetch+render, so requests blocked there are
  the biggest pool of stuck goroutines — and are invisible to `queuedRenders`.

  New `MaxPendingGenerations` (default `max(32, MaxConcurrentGenerations*8)`) →
  503 + `Retry-After`. Rate limiting uses `golang.org/x/time/rate` keyed by client
  IP, with TTL+cap eviction so it does not become another unbounded map;
  `X-Forwarded-For` is honoured only from `--trusted-proxies`, and IPv6 keys
  collapse to /64 (a client controls its whole /64 and could otherwise rotate
  freely). Status endpoints are exempt — rate-limiting SSE causes a reconnect
  storm. Also added a bounded retry to the demo's tile loader: Leaflet never
  retries, so the first shed request would otherwise leave a permanently blank
  tile and make backpressure look like a bug.

- **[P1]** Use `QueryContext(ctx, query)` in `datasource/overpass.go` — the fork
  already exposes a context-aware `Client.QueryContext`, so this was a one-line
  swap off the deprecated `Query`. Also gave the default HTTP client an actual
  `Timeout` (3m): it was `http.DefaultClient`, which has none, so a hung upstream
  pinned a fetch worker even with the context now threaded.

- **[P1]** Set `ReadTimeout`, `IdleTimeout`, `WriteTimeout`, `MaxHeaderBytes` and
  `ErrorLog` on the `http.Server`, plus graceful shutdown via
  `signal.NotifyContext` + `srv.Shutdown` + `od.Stop()`. Rather than a separate
  SSE handler, the two long-lived routes extend their own socket deadline with
  `http.ResponseController` (`http.TimeoutHandler` is not usable — it buffers the
  whole response and breaks `http.Flusher`). That forced `sendStatusEvent` to
  start returning an error: with a deadline in play, a dead client would otherwise
  make the 250ms loop spin forever on a broken connection.

  Three further findings: `srv.Shutdown` never ends the SSE stream, so shutting
  down with a demo tab open burned the full timeout (fixed with `BeginShutdown`
  via `RegisterOnShutdown` — measured 1ms instead of 30s); `od.Stop()` returned
  without waiting, so the retry worker could be killed mid-`GenerateWithData`
  leaving a truncated PNG that the cache would serve forever (now a bounded
  `WaitGroup` wait, bounded because Mapnik is cgo and may ignore cancellation);
  and `FetchQueue.Stop` closed the jobs channel while `Submit` could still be
  sending — a send-on-closed-channel panic that was unreachable only because
  nothing ever called `Stop`.

- **[P2]** Bound Overpass response reads — the unbounded `io.ReadAll` is _inside_
  the go-overpass dependency (`query.go:84`), not this repo, so it cannot be fixed
  at the call site. Capped instead at the transport: a `limitedTransport`
  RoundTripper wraps the injectable `OverpassConfig.HTTPClient` and enforces
  `MaxResponseBytes` (default 64 MiB), rejecting an oversized `Content-Length`
  before reading a byte and failing mid-stream for chunked responses. It errors
  rather than truncating — a silently truncated body would either fail to parse
  confusingly, or parse into a partial result that renders as a plausible but
  wrong tile.

- **[P2]** Stop leaking raw internal error strings (including backend server
  names) to HTTP clients — all five sites now log the detail and return a generic
  message via the new `writeTileError`, which also sets `Cache-Control: no-store`.
  Error bodies previously inherited the tile `Cache-Control` header, so a
  cacheable failure could pin a tile to "broken" in browsers and proxies.

- **[P2]** Evict from the per-tile lock map — replaced the never-pruned `sync.Map`
  with a refcounted `map[string]*tileLock` behind a small mutex (`lockTile`
  returns its unlock func). Entries are dropped once the last holder _or waiter_ is
  gone; the refcount is taken before releasing the map lock, so a concurrent
  release cannot evict an entry someone is still waiting on. Steady-state memory
  is now proportional to concurrent requests, not to distinct tiles ever seen.

## 7.3 Code quality & correctness (was P1/P2)

- **[P0]** CI `test-lint` never installed Mapnik (out of scope for 7.3, fixed
  there to unblock it) — golangci-lint builds the packages it lints, so
  `internal/renderer`'s cgo import failed typecheck with
  `mapnik/version.hpp: No such file or directory` and the lint job was red on
  `main` and on every PR. 7.1 added the install step to `test-unit.yaml` and
  `test-can-build.yaml` already had it, but `test-lint.yaml` was missed. All four
  CI jobs now pass. (PR #17)

- **[P1]** Shared, non-unique GeoJSON temp path (`renderer/multipass.go`) — the
  directory was a fixed `os.TempDir()/watercolormap` and the filename
  `{coords}_{layer}.geojson`, but `Coords.String()` is `z{z}_x{x}_y{y}` and carries
  no tile size. Base (256px) and `@2x` (512px) renders are separate
  `*pipeline.Generator` instances cached in the same `sync.Map`, so concurrent
  requests for one z/x/y wrote and `defer os.Remove`d the identical file; that path
  is also substituted into the style XML as `DATASOURCE_PLACEHOLDER` **and** used
  as the Mapnik layer name, so a lost race fed Mapnik the wrong geometry or no file
  at all. Parallel `go test` binaries shared it too.

  Replaced with a per-renderer `os.MkdirTemp("", "watercolormap-geojson-*")`
  removed in `Close()` — chosen over salting the filename because it also sweeps
  orphans and keeps the derived layer name readable. Two constructor error paths
  were leaking the Mapnik renderer and the temp dir; both now unwind, and `Close()`
  is idempotent. (PR #13)

- **[P1]** Replaced `debugCtx interface{}` (`pipeline/generator.go`) — the
  unchecked type assertion had already been `ok`-checked in earlier work, so what
  actually remained was the `interface{}` typing, which existed only because
  `worker.Generator` declared it that way. No production caller ever passed a
  non-nil value; the single non-nil caller in the repo was one test. Rather than
  make the interface depend on the concrete `*DebugContext`, the debug parameter was
  dropped from `worker.Generator` entirely, so `worker` keeps a 4-arg `Generate`
  and needs no dependency on `pipeline` at all; `GenerateWithDebug` covers the
  debug case. (PR #15)

- **[P2]** Wired the bypassed buffer-pooling infrastructure up — a package-level
  `sync.Pool` (the repo had no `sync.Pool` anywhere before) now backs
  `paintFromFinalMask` and `EuclideanDistanceTransform`, reusing the existing
  `EnsureCapacity`; no signatures changed. BenchmarkPaintFromMask: allocs/op −73%,
  B/op −78%, and since painting runs on the padded metatile (padPx ≥ 64) the real
  saving is larger than the 256px benchmark suggests.

  Pooling surfaced two latent bugs that a throwaway context had hidden: the
  shade-branch buffer swap left `ctx.painted` and `ctx.tempNRGBA` aliasing the same
  buffer (harmless until reuse, then corrupting), and because `EnsureCapacity`
  grows but never shrinks while the result bounds are read back off the buffers, an
  oversized pooled context would have returned an oversized image — such contexts
  are now dropped instead of reused. (PR #19)

- **[P2]** `worker/pool.go` `break` inside `select` — the lint finding
  (staticcheck SA4011) was removed during the 7.1 lint cleanup (PR #10, commit
  `cc7acca`): the `break` never left the feeder loop, so it was deleted without
  changing behaviour. The cancellation semantics behind it were fixed separately;
  see 7.7 below.

- **[P2]** Consolidated duplicated Web-Mercator math — there were six
  implementations, not the three the review found: forward in `tile/coords.go`,
  `renderer/mapnik.go` and `raster/raster.go`, plus two different inverses
  (`tile.mercatorToLonLat`, and `types.mercatorToLat` via `Sinh`) and an unshared
  clamp constant in `types`. All now route through a new leaf package
  `internal/geo`, which imports nothing from `internal/` so it cannot introduce a
  cycle.

  The truncated `3.14159265359` turned out to be cosmetic — max deviation 1.3e-6 m,
  about 1 part in 1.5e13; the real hazard was `latLonToWebMercator` taking
  `(lat, lon)` while everything around it took `(lon, lat)`. Also fixed:
  `raster.lonLatToLocalPx` had no latitude clamp, so lat = ±90 produced a garbage
  pixel ordinate. (PR #16)

- **[P2]** Single source of truth for layer compositing order — three orders
  disagreed: the hard-coded slice in `pipeline/generator.go` (the one that actually
  shipped), the stale `composite.DefaultOrder` (9 entries, no rivers, water at the
  bottom, buildings below roads) and a third in `cmd/wasm/main.go`. `DefaultOrder`
  is now authoritative and holds the live generator slice verbatim, so production
  rendering is byte-for-byte unchanged; WASM now follows production, which _does_
  change WASM output — it previously drew water below land and civic above parks.
  (PR #15)

- **[P2]** Replaced the 12–18 positional-arg functions in `cmd/generate.go` —
  `runBatchGenerate` had already been converted to a `batchOptions` struct, so
  `runSingleGenerate`'s remaining 12 positional params now take a `singleOptions`
  struct following that same in-repo pattern (fields grouped by type to satisfy
  `fieldalignment`). Kept separate from `batchOptions` rather than sharing an
  embedded struct: only nine fields overlap, and embedding would have rewritten
  every `opts.` reference across six helpers. (PR #12)

- **[P3]** De-duplicated the threshold/noise funcs and the Overpass query
  builders. `ApplyNoiseToMask` was `ApplyNoiseToMaskAdaptive` with
  `noiseScale == 1`, and the two antialiased-threshold functions differed in three
  lines of polarity; both collapsed behind thin wrappers keeping the exported
  names, with the loop-invariant `lower`/`upper` hoisted, the existing `smoothstep`
  reused, and both doc comments corrected (they claimed "smootherstep (3t²−2t³)"
  while computing plain smoothstep). Bit-identity was verified exhaustively over
  all 256 thresholds × 256 gray levels × both polarities.

  The Overpass builders became `featureRule` tables with `[minZoom,maxZoom]`
  windows, guarded by golden tests landed _first_ (38 goldens: zooms 0–18 × both
  `clipGeomToBbox` values) and verified byte-identical across the rewrite.
  Tabulating them exposed three latent oddities; two were tracked in 7.7 (both now
  closed), and the third — identical z8-9 and z10-11 road regexes — collapsed into
  a single 8-11 rule as part of the rewrite, with byte-identical output.
  (PRs #19 and #14)

- **[P3]** MBTiles no longer gzips PNG payloads — tiles are stored as raw PNG per
  the MBTiles 1.3 spec (gzip applies to `pbf` vector tiles, not raster), so QGIS
  and tileserver-gl can read them; the metadata already said `format: png` with no
  compression key, so the files were actively mislabelled. The reader sniffs the
  gzip magic `1f 8b` and passes non-gzipped payloads straight through, so
  already-generated files stay readable and this is not a breaking change.

  Two adjacent defects fixed in the same area: `ToMap` omitted `minzoom`/`maxzoom`
  when `<= 0`, silently dropping z0 (now `*int`, since a `-1` sentinel would
  collide with Go's zero value), and `metadata(name)` gained its missing PRIMARY
  KEY. (PR #18)

- **[P3]** `Close()` errors on files being written — already fixed in the work
  that landed as PR #10. `texture.writePNG` uses a named return and reports the
  close error, and the pipeline write path was replaced by `encodePNGAtomic`, which
  closes explicitly and checks. The remaining unchecked closes are all on
  read/cleanup paths and are annotated as such.

## 7.7 Follow-ups surfaced while completing 7.3

Items the 7.3 work uncovered but deliberately did not change, so that each 7.3 PR
stayed scoped and behaviour-preserving. All three are closed.

- **[P2]** `worker/pool.go` cancellation semantics — resolved as "one `Result` per
  task, always". The fix turned out to be a deletion rather than an addition: the
  worker side was already correct (`Pool.worker` does a non-blocking `ctx.Done()`
  check per task and emits `Result{Err: ctx.Err()}`), so the only source of loss
  was the feeder's `select`. Since `taskCh` is buffered to `len(tasks)` a send can
  never block, which makes the `ctx.Done()` arm pure downside — it could only ever
  drop a task that was guaranteed to be deliverable.

  Feeding unconditionally makes `len(results) == len(tasks)` an unconditional
  invariant, which is what `runTilePool` needs since it counts failures off that
  slice. The feed **goroutine** went too: with no blocking send there is nothing to
  run concurrently, so `Run` now fills and closes `taskCh` inline. Chosen over a
  deterministic early exit because a short slice would have pushed "the rest were
  never attempted" onto every caller. Regression cover:
  `TestPool_CancelledBeforeRun` (table-driven over 1/8/4 workers) asserts one
  result per task and `context.Canceled` on all of them, and
  `TestPool_Cancellation` now asserts the count invariant instead of only logging
  it. Verified with `-race -count=5`.

- **[P3]** `buildParksQuery`'s duplicate `natural=heath` removed — the way-only
  rule at z ≥ 10 was dropped; the way+relation pair at z ≥ 8 already emits the
  identical `way[...]` line from z8 up. Behaviour-preserving (Overpass's union
  de-duplicates), so this only shrinks the query. The 18 affected goldens (z10–z18
  × plain/`-clipped`) were regenerated and audited line by line: each shows exactly
  one deletion, all 18 the same `way["natural"="heath"](...)` line, and z00–z09
  stayed byte-identical.

- **[P3]** `buildRoadsQuery` z8-9 — resolved in favour of the **behaviour**, not
  the old comment. Matching `primary` from z8 is what has always shipped and what
  every existing tile and golden was generated with, so "fixing" it to
  motorway+trunk would have silently invalidated already-generated z8-9 tiles for
  no reported defect. PR #14 had already corrected the doc block to name trunk and
  primary at z8-11, so all that remained was deleting the stale `NOTE` that pointed
  at this very follow-up, and folding a one-line rationale into the `roadsRules`
  doc comment so the next reader does not "correct" it back. Comment-only: zero
  golden churn.
