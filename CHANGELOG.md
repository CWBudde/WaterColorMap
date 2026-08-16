# Changelog

## Unreleased

### Features

- **datasource:** add an optional on-disk cache for Overpass responses, off by default. It is a
  caching `http.RoundTripper` beneath the go-overpass client rather than a datasource wrapper,
  which keeps the fetch queue's concrete-type assertion working and covers multi-server routing
  per endpoint for free. The key hashes endpoint plus query text and contains **no tile
  identity**, so the cache cannot make output depend on which tile asked for the data; entries
  store the verbatim upstream bytes, so nothing downstream can observe a hit. Because `@2x`
  padding is computed in world pixels, the 512px query is byte-identical to the 256px one, so an
  on-demand `@2x` render reuses the base pass's entries instead of refetching every metatile.
  Configure under `cache:`; see `config.example.yaml`.

- **mbtiles:** batch generation to `--format mbtiles` now resumes. The skip-existing check used to
  stat the folder path even when output went through a `TileWriter`, so an MBTiles run re-rendered
  everything every time. `pipeline.TileProber` is an optional interface — a writer that cannot
  answer simply omits it — and every failure mode degrades to rendering rather than skipping,
  since a wrong skip leaves a permanent hole while a wrong render only costs time.

- **generate:** add `--band-fetch`, which issues one Overpass query per square block of same-zoom
  tiles instead of one per tile. **Off by default.** `out geom` returns unclipped geometry, so a
  motorway crossing a 4×4 block is transferred sixteen times by per-tile fetching; one query
  transfers it once. Since the fetch is ~71% of per-tile wall clock, this is where a bulk run's
  time actually goes — Germany's 237,424 z14 queries become roughly 15,000 at the default band
  size.

  **Output does not change.** A band's data is sliced back to each tile's own fetch bounds before
  rendering, which is not an optimisation but the thing that makes it behaviour-preserving: the
  renderer skips a zero-feature layer entirely, so handing a tile its neighbours' features would
  flip absent layers into present-but-blank ones. The emptiness check that catches silent Overpass
  failures stays per tile too, falling back to a real per-tile fetch when a slice comes out empty
  inside z8–13. `TestBandFetchRendersIdenticalTiles` asserts byte-identical output — not a
  tolerance — on data that genuinely differs.

  Two corrections to the plan item this closes. It called for stopping at z15, because the
  building rules are not monotone across z16; that constraint applies to reusing a _parent's_ data
  across zooms, not to grouping tiles of the same zoom, whose queries differ only in the bbox. And
  it assumed 8×8 blocks, which at the measured ~3 MB per padded z13 tile would exceed the 64 MiB
  response cap; the default is 4×4, and the real guard is adaptive — any band failure splits into
  quadrants and retries, bottoming out at ordinary per-tile fetches, so a failing tile still fails
  as itself rather than taking fifteen neighbours with it.

  One hazard found and closed on the way: multi-server routing matches coverage by intersection,
  which at band scale could answer sixteen tiles from a server holding data for one corner of the
  block. Band routing requires containment and splits otherwise.

  Banded runs keep the two guarantees the per-tile path has always given. **Resume still skips**:
  the existing-tile check lives inside the generator, i.e. after the fetch, so a band is now
  filtered to the tiles that still need rendering and skipped entirely when none do — a resumed
  run issues no Overpass queries at all, instead of re-fetching every block it had already
  finished. And **an interrupted run still fails**: every requested tile comes back with a result,
  so Ctrl-C reports failures and exits non-zero rather than counting none, writing TileJSON and
  flushing MBTiles metadata for a half-rendered tileset.

  Also fixes `types.FeatureCollection.Count()`, which omitted `Rivers` — so a tile whose only
  features were waterways counted as empty.

- **tileformat:** add WebP tile output, on `generate` and `serve` alike, via `--image-format webp`.
  New leaf package `internal/tileformat` owns format identity and encoding; PNG stays the default
  at every layer, including the zero value of every options struct, so nothing changes for a run
  that does not ask for WebP.

  **The measured saving is 1.21×, not the 9.24× `docs/data-scaling-strategy.md` quotes.** That
  figure is _lossy_ q80; the encoder here is `HugoSmits86/nativewebp`, which is VP8L — lossless —
  and pure Go. Re-measured over the same 689 rendered tiles: mean 122,326 B → 101,181 B, ratio
  0.827, consistent from z5 to z17, and larger than the PNG on none of them. The gap is the same
  fact that produced "no cheap empty tiles": the texture and noise fill every pixel, so a lossless
  codec has nothing to collapse. Encoding is ~4× slower than PNG (52 ms vs 14 ms per 256px tile),
  which is under 10% of a ~576 ms render. The doc has been corrected in place.

  Lossless was chosen for what it does not cost: no cgo, so `GOOS=js` and the cross-platform
  release matrix keep building with no build tags and no vendored C, and there is no round-trip
  damage to argue about. `Encoder` is an interface, so a lossy backend is one more implementation
  if that trade is ever worth making.

  Two correctness guards came with it. `mbtiles.New` now refuses to reopen a non-empty tileset
  under a different format: it rewrites the metadata table on open, so it would otherwise relabel
  a PNG tileset as WebP while `HasTile` — keyed on coordinates alone — skipped every existing tile
  as done. And `serve` answers only its configured extension, 404ing the other rather than serving
  one format's bytes under the other's name, which every cache downstream would then remember.

  `convert` detects the format from the folder instead of taking a flag, and refuses a folder
  holding both, since one MBTiles file records exactly one format.

- **generate:** `@2x` tiles are no longer pre-rendered in bulk. A batch run used to make a full
  second pass over the entire tile list, which is 2× the compute and 4× the storage for the whole
  run — on Germany z0–14, the difference between 151.5 GB / 24.6 days and 37.9 GB / 12.3 days.
  `serve` has always rendered `@2x` on demand, and because `@2x` padding is computed in world
  pixels its Overpass query is byte-identical to the base tile's — so with the response cache
  enabled, an `@2x` request that follows a base render while that entry is still live is served
  with no upstream traffic. Enabling the cache is not on its own enough: a cold, missing or
  expired entry is an ordinary miss and still fetches Overpass.

  **Note for MBTiles deployments:** on-demand `@2x` is a property of folder-backed serving.
  `serve --mbtiles` answers from the file and ignores the `@2x` suffix, so a retina request there
  gets base-resolution data. A deployment that needs true `@2x` from an MBTiles tileset has to
  serve the tile directory instead (`serve --tiles-dir`), which keeps the on-demand generator.

  **Breaking:** `generate --bbox --hidpi` is now an error rather than a silently smaller run.
  Ignoring the flag would have been worse — a script that asked for `@2x` would appear to succeed
  and produce half the expected tiles, surfacing only as a 404 later. `--hidpi` still works for a
  single tile, where it costs one extra render and is the easiest way to check that a `@2x` tile
  and its base show the same road classes at the same ground width. The `prebuild-hannover` and
  `prebuild` Justfile recipes dropped the flag accordingly.

### Performance

- **mask/watercolor:** run the per-layer mask pipeline over pooled scratch buffers instead of
  allocating a full-size image per stage. Every mask kernel gained an `*Into` variant writing a
  caller-owned destination, and `processMask` now keeps one allocation (the final mask, which the
  land path hands on to parks) where it used to make four to six. `BenchmarkFullPipeline`:
  5.85 MiB → 2.19 MiB per tile (−62%) in 38 allocations instead of 143 (−73%), with bit-identical
  output. Wall time is unchanged within measurement noise — the payoff is GC pace under the
  concurrent tile server. Closes PLAN.md § 5.11.3; see
  `docs/performance/allocation-optimization.md` for the invariants that keep recycled buffers from
  leaking stale pixels.

### Documentation

- **scaling:** add `docs/data-scaling-strategy.md`, closing PLAN.md § 5.1. Measured rather than
  assumed: the Overpass fetch is ~71% of per-tile wall clock; WebP q80 is a 9.2× reduction over
  PNG (the encoder that later shipped is lossless and measures 1.21×); and there are no cheap empty tiles — a Sahara tile with zero OSM features still costs
  108 KB. Records why vector tile input is rejected (MVT's pre-clipping reintroduces the exact
  artifact `out geom(bbox)` clipping was abandoned for) and what the three tiers of a global
  rollout would actually cost.

- **overpass:** correct the long-standing reading of the public API's `406 Not Acceptable`. It is
  not rate limiting — `overpass-api.de` rejects Go's default `User-Agent`, which `go-overpass`
  never overrides.

### Bug Fixes

- **datasource:** send a real `User-Agent` to Overpass. `overpass-api.de` answers
  `406 Not Acceptable` to Go's stdlib default `User-Agent: Go-http-client/1.1`, and go-overpass's
  `httpPost` sets only `Content-Type` — so every request this project made to the public API
  arrived with the rejected default. That 406 was documented for a long time as rate limiting; it
  was not, and the identical query from `curl` returns 200 in about half a second. The public API
  is now a usable fallback for coverage gaps, which is what the multi-server routing has always
  assumed.

  Implemented as a `userAgentTransport` alongside the existing limit and cache round-trippers
  rather than as a patch to the dependency: the request is built inside the go-overpass client
  where the call site cannot reach it, and doing it at this layer also covers the per-server
  clients `MultiOverpassDataSource` builds. It cannot perturb the response cache either way — the
  cache key is the endpoint plus the query text, and headers are no part of it. Override with
  `overpass.user_agent`, globally or per server.

- **datasource:** fail over to the next Overpass server instead of giving up on the first one.
  `MultiOverpassDataSource` took the first coverage match and returned its error verbatim, so one
  restarting local container failed **every tile inside its coverage box** without ever trying the
  nil-coverage public fallback — enough to kill a multi-day bulk run over a blip it could have
  ridden out. Matching servers are now tried in order, and the error returned when they all fail
  names every one of them.

  Two failures are deliberately not retried elsewhere, because another server cannot help: a
  cancelled or expired **caller** context, where the caller is already gone and a second attempt
  only delays shutdown, and a response over the size cap, which is a property of the data and the
  configured limit rather than of the server. Everything else — transport errors, 5xx, 429, an
  HTML error page, a server that hangs until the HTTP client's own timeout fires, and the empty
  mid-zoom response that is the shape of a silent upstream failure — is exactly what a second
  server exists to answer. The caller-gone test reads the context rather than the shape of the
  error on purpose: an `http.Client.Timeout` satisfies `errors.Is(err, context.DeadlineExceeded)`
  while the caller is still waiting, so classifying on the error would have abandoned failover in
  exactly the hung-server outage it exists for.

  Ocean rendering needed explicit care. It sets `AllowEmptyResponses` on every server, which turns
  the empty response into a _success_ — correct over open sea, but it would have let a regional
  server's silent 200-with-no-data failure over land bake a featureless tile. A featureless
  response now gets a second opinion without being treated as an error: the next candidate is
  tried, and the empty result is returned only if none does better.

  Failovers log at `warn`, so a run that quietly degrades to the public API reads as a broken
  server rather than an unexplained slowdown. Startup additionally warns about a coverage box
  fully contained in an earlier server's box: routing takes servers in order, so such a box can
  never be selected first, and the only symptom used to be paying the public API's rate limits for
  a region you built a local instance for.

- **watercolor:** anchor hi-DPI rendering to world position. Noise scale, all blur/shade/edge
  sigmas, the adaptive-noise distances and the paper/layer texture period are lengths, and they
  were fixed **device**-pixel constants while the sampling offsets scaled with the tile size. An
  `@2x` tile therefore drew grain, texture and blur at half the ground size of the 256px tile
  covering the same area. They are now scaled by `tileSize / 256`, so a 512px tile samples the
  same field as its 256px twin, just at twice the resolution.

  Behaviour change worth knowing about: **`--tile-size 512` without `@2x` now renders at scale 2.**
  That is correct and consistent, but it is a visible look change for anyone running a non-256
  base tile size. Output at the default 256px is bit-identical — the scale path is a no-op there.

- **renderer:** anchor Mapnik's vector rendering to world position too, by passing
  `RenderOpts.ScaleFactor = tileSize / 256`. This completes the fix above, and it corrects two
  things rather than one. `stroke-width`, font and marker sizes in `assets/styles/layers/*.xml`
  are fixed device-pixel values, so an `@2x` tile drew roads at the same pixel width as the 256px
  tile covering the same ground — half as wide in ground terms. Less visibly, Mapnik multiplies
  the scale denominator by the scale factor before evaluating `Min`/`MaxScaleDenominator`, and an
  `@2x` tile has half the denominator of its `@1x` twin, so the two could resolve **different
  detail tiers** and draw different road classes at the same zoom.

  Behaviour change: **existing `@2x` output looks different and should be regenerated.** Output at
  the default 256px is unchanged — the scale factor is exactly `1.0` there, which go-mapnik treats
  identically to the unset value the code passed before.

- **cmd:** `generate` now honours the configured Overpass servers. It hardcoded an empty endpoint and
  therefore always queried the public `overpass-api.de`, ignoring the `overpass.servers` /
  `overpass.endpoint` config that `serve` has always read — so a configured local instance went
  unused and every batch run took the public API's rate limits. Both commands now resolve their
  datasource the same way. Adds `--overpass-workers` to `generate` for parity with `serve`.

- **demo:** the Leaflet demo declares an inline `data:` favicon. Without it the browser requested
  `/favicon.ico`, which the tile server has no route for and answered 404 — the page's only console
  error.

### Features

- **datasource:** `WATERCOLORMAP_OVERPASS_ENDPOINT` overrides the endpoint used when a caller
  configures none. Integration tests build their datasource directly and never see `config.yaml`,
  so this is what points them at a local Overpass instance; an explicitly configured endpoint still
  wins, and unset the default remains the public API. See `docs/local-overpass.md`, plus
  `just test-integration-local` and `just smoke-local`.

## [1.0.0](https://github.com/CWBudde/WaterColorMap/compare/v0.3.0...v1.0.0) (2026-08-16)


### ⚠ BREAKING CHANGES

* **server:** --cache-control now defaults to "no-cache" rather than "no-store". Tiles become storable and revalidatable, which is what makes the ETag reach the client, while a tile removed by `purge` is still gone on the very next request. A positive max-age would be faster and would outlive a purge, so it stays the operator's call; --cache-control no-store restores the previous behaviour exactly.

### Features

* add local Overpass instance support for faster integration testing and data fetching ([1129141](https://github.com/CWBudde/WaterColorMap/commit/1129141fbb527f8484bf8cce2e57c6f65b53c151))
* add scaling support for texture processing and padding calculations ([be6e07f](https://github.com/CWBudde/WaterColorMap/commit/be6e07f9f17cbe0ed22355cbddc420cc9ff28bd0))
* **checkpoint:** record batch progress as a watermark over the enumeration ([0635622](https://github.com/CWBudde/WaterColorMap/commit/06356220442e3dce29b21dcb8496db32c0f2a6ee))
* **cmd,server:** make per-tile paint concurrency configurable ([c1f1cce](https://github.com/CWBudde/WaterColorMap/commit/c1f1ccec2c8228ddec493d45952217a87e0d0340))
* **cmd:** add the purge command and the generate --stale-* flags ([847a433](https://github.com/CWBudde/WaterColorMap/commit/847a433d41176d7db240abb748fdb2344ee32b6a))
* **datasource:** add an optional on-disk Overpass response cache ([2e7e8ba](https://github.com/CWBudde/WaterColorMap/commit/2e7e8ba57500f108db52d0e0968983bca2439fca))
* **datasource:** carry the OSM source-data version out of Overpass ([789c01a](https://github.com/CWBudde/WaterColorMap/commit/789c01a3093700640e25720eb94eb4743a8a3f75))
* document completion of manual step for local Overpass instance and verify tile generation results ([b2fbc0b](https://github.com/CWBudde/WaterColorMap/commit/b2fbc0bde272bfd01b6069d3122542d96d5a42d6))
* **generate:** fetch Overpass data per band instead of per tile ([c0b6454](https://github.com/CWBudde/WaterColorMap/commit/c0b645442558a376aa945d519b08f8d5bae6e658))
* **generate:** stream the batch run and checkpoint it ([3fc2a9c](https://github.com/CWBudde/WaterColorMap/commit/3fc2a9ca62c580a681c958452b2d213322a40556))
* implement hi-DPI scaling for Mapnik rendering and update tests ([9c654e9](https://github.com/CWBudde/WaterColorMap/commit/9c654e92e63cac0543e7068451862ac13c62e817))
* **lru:** add a bounded LRU with TTL and statistics ([34efbe8](https://github.com/CWBudde/WaterColorMap/commit/34efbe8c4a301f794cbd7a64fd7e54179d0f8449))
* **mbtiles:** add the tile delete path and OpenForUpdate ([6d4e836](https://github.com/CWBudde/WaterColorMap/commit/6d4e836aa184a133ff3568548852793ea5b83bb5))
* **mbtiles:** resume batch generation instead of re-rendering everything ([450f4c0](https://github.com/CWBudde/WaterColorMap/commit/450f4c0a004a56dd0ca9c0c767a4b0af4138b7f8))
* **ocean:** add ocean rendering support and configuration ([9f31af1](https://github.com/CWBudde/WaterColorMap/commit/9f31af1ac56aca9dad41722a54d5ade4905aa34c))
* **pipeline:** stamp written tiles and make skip-existing freshness-aware ([a79d3e1](https://github.com/CWBudde/WaterColorMap/commit/a79d3e1ba9f95d6207041b3069e403efcbe15abc))
* **renderer:** render z0-5 from Natural Earth ([6d2204e](https://github.com/CWBudde/WaterColorMap/commit/6d2204e551b890522da678de1197ae67fc7cc049))
* **server:** count cache hits and misses and report them per request ([2583143](https://github.com/CWBudde/WaterColorMap/commit/2583143ca06286c1e008bab0db8e581d1f5f6dca))
* **server:** stamp tiles served on demand and key stamps by image format ([9cb9b84](https://github.com/CWBudde/WaterColorMap/commit/9cb9b8490efce64759ca5e1108c679a4bd6b5d32))
* **server:** validate cached tiles with an ETag and revalidate by default ([9065751](https://github.com/CWBudde/WaterColorMap/commit/90657517cdacc492f2dd33898ec3fd2b05c8ceea))
* **server:** validate MBTiles rows with a content ETag ([a3d2dab](https://github.com/CWBudde/WaterColorMap/commit/a3d2dab963e70db6c8fac16db715729f9475ee3a))
* **tileformat:** add WebP tile output end to end ([93dff03](https://github.com/CWBudde/WaterColorMap/commit/93dff0310a73fcffffd189652ab261376a4706eb))
* **tilestamp:** add the per-tile source-data stamp store ([82fafe9](https://github.com/CWBudde/WaterColorMap/commit/82fafe90217749c4215e8003a36b91d5534727ff))
* **tile:** stream tile enumeration as an iter.Seq ([dbf2864](https://github.com/CWBudde/WaterColorMap/commit/dbf28644c1485daa82cf5c1c1e3302b3f734be96))
* update changelog and demo to support local Overpass instance and fix favicon error ([60f8595](https://github.com/CWBudde/WaterColorMap/commit/60f8595aba646f0c11d6304dcddf6ef88a734a1c))
* **worker:** let RunStream hand results to a callback instead of retaining them ([90e7080](https://github.com/CWBudde/WaterColorMap/commit/90e7080fb53f655fe2edd3bdae8a967a02714cba))


### Bug Fixes

* address further PR review comments (buffer size, OPTIONS, tilejson center, config order) ([5883982](https://github.com/CWBudde/WaterColorMap/commit/5883982e5264dbb4c50bd7967a8b4c1fb5376611))
* address PR review comments ([6b919e8](https://github.com/CWBudde/WaterColorMap/commit/6b919e82cf6f5cc914e08af1595bcc0849badd53))
* address PR review comments ([3adce86](https://github.com/CWBudde/WaterColorMap/commit/3adce86dd8361ebdb4bd7d7fc0a4c1c8852b7c42))
* address PR review comments ([55578e0](https://github.com/CWBudde/WaterColorMap/commit/55578e076a7b20a289c99b366ab1ca32ee45874a))
* address PR review comments ([f5aab07](https://github.com/CWBudde/WaterColorMap/commit/f5aab077d1d92dc105ba3a791fb1165e265a0b88))
* address PR review comments ([8339e52](https://github.com/CWBudde/WaterColorMap/commit/8339e52ec6570e812a540a9eb055f4287dadc8f1))
* address PR review comments ([e3a521d](https://github.com/CWBudde/WaterColorMap/commit/e3a521de97c9dac17119e3590755cfbded7b4dcc))
* address PR review comments ([71111b5](https://github.com/CWBudde/WaterColorMap/commit/71111b559f90b44f501d1159e764bbf77b3f0e7a))
* address PR review comments on tuning validation and TileJSON origin ([2e95cce](https://github.com/CWBudde/WaterColorMap/commit/2e95cceb4d2f9982c89887a3fa47422ea80611c6))
* **ci:** install mapnik for the lint job ([#17](https://github.com/CWBudde/WaterColorMap/issues/17)) ([3a8a31a](https://github.com/CWBudde/WaterColorMap/commit/3a8a31a3cc77d74906433ab4545c9f5740f63468))
* **ci:** unbreak all three failing jobs and clear the lint backlog ([#10](https://github.com/CWBudde/WaterColorMap/issues/10)) ([8c60a7b](https://github.com/CWBudde/WaterColorMap/commit/8c60a7b6083b4f8bdfcd36feba349944dccdaca8))
* **convert:** recognise the nested folder layout when scanning tiles ([2df46f6](https://github.com/CWBudde/WaterColorMap/commit/2df46f614ffcfff7fef3208c00d00dc4bae59213))
* **datasource:** address PR review findings on the response cache ([fb90d3b](https://github.com/CWBudde/WaterColorMap/commit/fb90d3ba453aa799f1c57ee02c47ac25ddc042e0))
* **datasource:** fail over to the next Overpass server ([b4c579a](https://github.com/CWBudde/WaterColorMap/commit/b4c579a26a98eb8b3aadf442a2a9c022bbc59a2c))
* **datasource:** send a real User-Agent to Overpass ([7793ac0](https://github.com/CWBudde/WaterColorMap/commit/7793ac0c9b78fe67138d2c3069893a5bf4952586))
* **datasource:** sort Overpass features by OSM ID ([bc4bb3c](https://github.com/CWBudde/WaterColorMap/commit/bc4bb3c2149f647b311a7239258ea68af6cd20c7))
* **demo:** request the format the server actually serves ([3c670db](https://github.com/CWBudde/WaterColorMap/commit/3c670db5d0b64cb34dbfb3d2b1f3aa00d6b9d5e5))
* **docker:** unbreak the image build and harden the build inputs ([82ead90](https://github.com/CWBudde/WaterColorMap/commit/82ead904319b114283f4620bfaa1e1294cf903f4))
* **docs:** update data scaling strategy to reflect new capabilities and close open items ([c751a3b](https://github.com/CWBudde/WaterColorMap/commit/c751a3bb1c45e73eddf237d7413e24366b94afa5))
* export the local endpoint from smoke-local instead of assuming config.yaml ([3ba2bbc](https://github.com/CWBudde/WaterColorMap/commit/3ba2bbc6be310c1cc2632c649692ae9d2596d771))
* formatting ([77761e7](https://github.com/CWBudde/WaterColorMap/commit/77761e7b1714c99688c25f448dba0bb586438d69))
* **generate:** keep resume and cancellation honest for banded runs ([8c24e3c](https://github.com/CWBudde/WaterColorMap/commit/8c24e3c863e1a066a00a94b50fa01e35efdef96c))
* **generate:** make zoom 0 reachable in batch generation ([f8a28c6](https://github.com/CWBudde/WaterColorMap/commit/f8a28c68356dfc4d0dc148342de0981cef20d6e7))
* **mask:** bound both edge-mask passes by the source mask ([43d71ff](https://github.com/CWBudde/WaterColorMap/commit/43d71ffa667defbb600a6b0fd4034f3a4348badd))
* **mbtiles:** do not let an undeclared format bypass the resume guard ([045f85a](https://github.com/CWBudde/WaterColorMap/commit/045f85a3895ddfdd6f5a3881a6b9e612eb026cc3))
* **mbtiles:** store png tiles uncompressed for interop ([#18](https://github.com/CWBudde/WaterColorMap/issues/18)) ([e01da0b](https://github.com/CWBudde/WaterColorMap/commit/e01da0bba856c2e590b03a330df69c04fbf0d43e))
* **renderer:** give each renderer its own geojson temp dir ([8900e39](https://github.com/CWBudde/WaterColorMap/commit/8900e394cb4a4816941d21528136be4b07acdda7))
* **renderer:** keep temp dir path when cleanup fails ([aabcedd](https://github.com/CWBudde/WaterColorMap/commit/aabcedd40c03790ccca45794fe3c7ceac000e9c3))
* **server:** 404 the wrong extension on the MBTiles backend too ([d2fb7fd](https://github.com/CWBudde/WaterColorMap/commit/d2fb7fdb77267b7fde29b507d9cdeb4151a31841))
* **server:** check tile freshness on the cache hit ([9fa1adb](https://github.com/CWBudde/WaterColorMap/commit/9fa1adbd0fe779ade2ccfd4e174a2f8ca93ce311))
* **server:** keep an uppercase tile extension parseable ([217fde6](https://github.com/CWBudde/WaterColorMap/commit/217fde6bdb41fdbc1c76f16925a931d0f6261e86))
* **test:** budget the padded metatile, not a bare 256px tile ([1c2448f](https://github.com/CWBudde/WaterColorMap/commit/1c2448ffcc5caca661a4c3798aad6475d17c1af0))
* **test:** move the per-layer budget behind !race and re-measure on new main ([bcdef1e](https://github.com/CWBudde/WaterColorMap/commit/bcdef1e7f391466be1fd08fc0109314f1d4a63bd))
* **tileformat:** let an explicit --webp-effort=0 reach the encoder ([180d023](https://github.com/CWBudde/WaterColorMap/commit/180d023793999efbde414a4403ccc44e2625bda5))
* **tile:** reject and clip out-of-world bbox bounds ([26852d1](https://github.com/CWBudde/WaterColorMap/commit/26852d16730b636cd76de84f08f0e6a138718d00))
* update .gitignore to include AI agent files ([075768f](https://github.com/CWBudde/WaterColorMap/commit/075768f59d83b1125ef5adeb1ebb40627af4f37f))
* **watercolor:** clear pooled buffers when mask is smaller than tile ([0f023ab](https://github.com/CWBudde/WaterColorMap/commit/0f023ab4f747d8e6f6b210c1085c38c311af2904))
* **watercolor:** reject a nil layer image explicitly ([483f82f](https://github.com/CWBudde/WaterColorMap/commit/483f82f60b764f9008edc6ef28470fcbc9c8a723))
* **worker:** return one Result per task when a run is cancelled ([6f82f5a](https://github.com/CWBudde/WaterColorMap/commit/6f82f5ab1a60ada9011d3b7c4e9f96ff559cd73c))


### Performance Improvements

* **blurkernel:** memoize the blur plan ([dfb5838](https://github.com/CWBudde/WaterColorMap/commit/dfb5838d0466dc57b32402854d5d64ab086d7052))
* **blur:** vectorise the box column pass ([8e65c79](https://github.com/CWBudde/WaterColorMap/commit/8e65c792223d1a149975e8b359d83a97426c7031))
* **composite:** read layers and crops straight from Pix ([b8fddba](https://github.com/CWBudde/WaterColorMap/commit/b8fddbafcf7d16a29e6e33111e8b8667942db8a0))
* **mask:** add destination-writing variants of the mask kernels ([d7685cd](https://github.com/CWBudde/WaterColorMap/commit/d7685cd0f8f7040b626b29847b3be6fd072609dc))
* **mask:** index Pix directly in the distance transform ([ea28cb2](https://github.com/CWBudde/WaterColorMap/commit/ea28cb2b6f2c5b5967c43cbfd41605425cf19d42))
* **mask:** index Pix directly in the mask kernels ([53de55d](https://github.com/CWBudde/WaterColorMap/commit/53de55d2a7d2bf13bcfe993760e14a6c677ae8c1))
* **mask:** index Pix directly in the soft edge pass ([cb17e9e](https://github.com/CWBudde/WaterColorMap/commit/cb17e9e94e14fc11e3f50557d0e4da5811a08f67))
* **mask:** rewrite the blur, 6-11x faster, and fix its width ([13c8f01](https://github.com/CWBudde/WaterColorMap/commit/13c8f01d9e9d2a3d2b9edb2703937ca269b6bfcf))
* **mask:** vectorise the soft-edge pass with an AVX2 kernel ([830949d](https://github.com/CWBudde/WaterColorMap/commit/830949d010cbef62642b891d001777e13981214d))
* **pipeline:** build the land debug composite only for debug runs ([5157a16](https://github.com/CWBudde/WaterColorMap/commit/5157a1607d29b82fded60e14bc9a8144ea42d125))
* **pipeline:** paint independent layers concurrently ([4ed4069](https://github.com/CWBudde/WaterColorMap/commit/4ed4069bf174c10e7e3fb1bf564e87c1375687f9))
* **pipeline:** stop allocating masks that buildMasks throws away ([f3e373a](https://github.com/CWBudde/WaterColorMap/commit/f3e373aae891ac08dd4012b27492d36761164a3c))
* **server:** cache tile metadata in front of the tile directory ([1dd1c04](https://github.com/CWBudde/WaterColorMap/commit/1dd1c0416d2f478ad34b0bd42fc2d6773494e638))
* **texture:** index Pix directly when tiling and masking ([f6abe97](https://github.com/CWBudde/WaterColorMap/commit/f6abe97116f8bacfde305c94a7fd66a2aeac284e))
* **texture:** normalise decoded textures to NRGBA at load time ([47224ab](https://github.com/CWBudde/WaterColorMap/commit/47224abee9b6bb03495be45343cd473d59f9848c))
* **texture:** tile and mask by row copy instead of per-texel sampling ([1a7d151](https://github.com/CWBudde/WaterColorMap/commit/1a7d15177b74acb3e266c1cf4687a3647cfdea24))
* **watercolor:** pool paint and distance buffers ([894890f](https://github.com/CWBudde/WaterColorMap/commit/894890f7d11bed05f02a26afbcf1b75ef9d47932))
* **watercolor:** run the mask pipeline over pooled scratch buffers ([07582d8](https://github.com/CWBudde/WaterColorMap/commit/07582d815a5b802fa1f9e28a6d6936cef94c2144))

## [0.3.0](https://github.com/CWBudde/WaterColorMap/compare/v0.2.0...v0.3.0) (2026-08-13)

### Features

- add railroad and civic layers, rename module to cwbudde ([#4](https://github.com/CWBudde/WaterColorMap/issues/4)) ([9f6b504](https://github.com/CWBudde/WaterColorMap/commit/9f6b5047dc3fe1d178445c62bb3dc3372098d8ec))
- improve layer ordering, add playgrounds, and enhance road sizing at high zoom levels ([1d64e69](https://github.com/CWBudde/WaterColorMap/commit/1d64e6903c949834fb20a42b3938e3fce5361952))
- improved inset shadow algorithm ([af6ce3a](https://github.com/CWBudde/WaterColorMap/commit/af6ce3aa7a88f6abf7ac3a382e346feba2473323))
- more advanced render pipeline ([0d14bda](https://github.com/CWBudde/WaterColorMap/commit/0d14bda4ccc134d73b92dee831118b505a00e42a))
- more stamen like style ([6947e01](https://github.com/CWBudde/WaterColorMap/commit/6947e0163527226f73017354f83eaf655c793083))
- new baseline ([0aabc7c](https://github.com/CWBudde/WaterColorMap/commit/0aabc7ca7627da0f534dea5253d01fae3525ae0e))
- new baseline rendering ([6a011e5](https://github.com/CWBudde/WaterColorMap/commit/6a011e56d141048a91b02bac7cf1326d25fc9b59))
- recent improvements ([e693390](https://github.com/CWBudde/WaterColorMap/commit/e693390ccd5dd16fddd4cd3659b488493a084812))
- rendering is fast and beautiful now. Features are so so... ([44d1989](https://github.com/CWBudde/WaterColorMap/commit/44d19898fa27cc0e87ccce3abb4d27f645b48420))

### Bug Fixes

- performance fix ([c4538f7](https://github.com/CWBudde/WaterColorMap/commit/c4538f702c600495342d44c8a84aac90cbcca4a8))
- polygon rendering fix ([a4044cc](https://github.com/CWBudde/WaterColorMap/commit/a4044cc3ae9bdc023933bbb990405c8f4d6832ef))

## [0.2.0](https://github.com/CWBudde/WaterColorMap/compare/v0.1.0...v0.2.0) (2025-12-20)

### Features

- **5.6a:** WASM Browser Playground with on-demand generation and GitHub Pages CI/CD ([e967eac](https://github.com/CWBudde/WaterColorMap/commit/e967eac8b7e59595c2a08943bc70440637a04e8f))
- add --allow-failures flag for graceful API rate limit handling ([1379adf](https://github.com/CWBudde/WaterColorMap/commit/1379adf4651f5acfc49503db5b1ab13d6be5d50b))
- added ci ([8d7bf4a](https://github.com/CWBudde/WaterColorMap/commit/8d7bf4a21e313ab69935fcdd6e0467df2420b39f))
- added release ([8d830a8](https://github.com/CWBudde/WaterColorMap/commit/8d830a804c34539e8fddbe7564902592d38a5446))
- added release please and reduced response ([844999a](https://github.com/CWBudde/WaterColorMap/commit/844999a6e002d59fc5938dc31f34b79e813e7bcf))
- completed phase 3 ([b2aff37](https://github.com/CWBudde/WaterColorMap/commit/b2aff37bf5eb8dd36562f88741aac83ddf483f18))
- MBTiles first draft ([839dadd](https://github.com/CWBudde/WaterColorMap/commit/839dadd3910dba0a920457405e0de36a5fed083e))
- more about license ([2f51e02](https://github.com/CWBudde/WaterColorMap/commit/2f51e027a19e7663a46ac858990401146343ae4c))
- more details ([aa75e14](https://github.com/CWBudde/WaterColorMap/commit/aa75e14a571c5091b0f022a89ff1e476b54af0ca))
- more progress ([c326074](https://github.com/CWBudde/WaterColorMap/commit/c3260747c007a254defe78208c8748b62338a176))
- new dockerfile ([3e261bc](https://github.com/CWBudde/WaterColorMap/commit/3e261bc4f8f124f9ec2254fa8c3a37735d47d266))
- recent work ([2227859](https://github.com/CWBudde/WaterColorMap/commit/2227859330c4cbec29d57e4e631a9d676d829b2f))
- static pre-rendering ([096c113](https://github.com/CWBudde/WaterColorMap/commit/096c1135355a550c7bdf48f9c6db8c4652a60a4d))
- task 5.2 ([94bca06](https://github.com/CWBudde/WaterColorMap/commit/94bca068404c2e9ef8f318101025868e9259efcd))

### Bug Fixes

- added exponential back-off strategy ([1562020](https://github.com/CWBudde/WaterColorMap/commit/15620205544690ed1dc0de0fb612d631e5be3eda))
- better strategy for border on blur ([0f33a89](https://github.com/CWBudde/WaterColorMap/commit/0f33a89e09ca67706625332dbbeffa731907e648))
- bug fixes ([76683c9](https://github.com/CWBudde/WaterColorMap/commit/76683c9e229cb334d755d2568e899192fa09abd1))
- CI for playground ([64dea5a](https://github.com/CWBudde/WaterColorMap/commit/64dea5a288fccf010e7f572d730779194604a521))
- deprecated API ([8b80020](https://github.com/CWBudde/WaterColorMap/commit/8b80020d7f8fa3e4f6d3429ae6793a77f1fbe100))
- format ([633e52e](https://github.com/CWBudde/WaterColorMap/commit/633e52e35e8000611d15562b8f2fe535f01dc915))
- formatting ([577dc00](https://github.com/CWBudde/WaterColorMap/commit/577dc00ae369eaf624ad52156623b840fa05bafc))
- formatting ([6b10498](https://github.com/CWBudde/WaterColorMap/commit/6b10498c251394739a406b263cbcd256ee467305))
- lint & formatting fixes ([84ce7e6](https://github.com/CWBudde/WaterColorMap/commit/84ce7e643e47433329617a38b9eaf17427d033b8))
- lint issues and test fixes ([009ac1c](https://github.com/CWBudde/WaterColorMap/commit/009ac1caa3dd2a77f8e48291ec4ab39ea78e2797))
- more dockerfile ([cf3e367](https://github.com/CWBudde/WaterColorMap/commit/cf3e367b51526ebd8b7e0b9f6b61a8f60e15b3a1))
- more features ([3a6387f](https://github.com/CWBudde/WaterColorMap/commit/3a6387fd374fc68567cfb8aac5040ffb66cf7a81))
- more wasm fixes ([edd9643](https://github.com/CWBudde/WaterColorMap/commit/edd9643a52b41aca9af5bc0cf90b8f30701e03b2))
- playground ([50bd0cc](https://github.com/CWBudde/WaterColorMap/commit/50bd0ccf76b9c15265f676c8594080a1a1be2310))
- playground ([931390b](https://github.com/CWBudde/WaterColorMap/commit/931390b090799e3ffbbef39541f45a4d2a5e62a7))
- playground ([e6de40c](https://github.com/CWBudde/WaterColorMap/commit/e6de40cc3eb0f8e16e615455c10ab2c080a884bc))
- playground issues fixed ([6ac711a](https://github.com/CWBudde/WaterColorMap/commit/6ac711a2f1cd252620bef286b9f204961a9f4594))
- playground issues fixed ([4c3c106](https://github.com/CWBudde/WaterColorMap/commit/4c3c106a456b2cec2ec63250b07f2a6ceac2b1e9))
- playground issues fixed ([788d186](https://github.com/CWBudde/WaterColorMap/commit/788d18629cdcd12b4d9feb5d8db30dac50359fa7))
- playground issues fixed ([556cea2](https://github.com/CWBudde/WaterColorMap/commit/556cea2f022d09ff250e37a4d1799f3dcfb1df22))
- playground issues fixed ([b2de348](https://github.com/CWBudde/WaterColorMap/commit/b2de348b453de5f9057b8e3a3cc1223cf2df12f7))
- playground issues fixed ([a637c67](https://github.com/CWBudde/WaterColorMap/commit/a637c67f73b412f02993b7e74b161e04b8c9aec2))
- playground issues fixed ([871ae21](https://github.com/CWBudde/WaterColorMap/commit/871ae21a206d0e15ba8e86d3c56f70c633543440))
- polygon issue fixed ([ad1ff70](https://github.com/CWBudde/WaterColorMap/commit/ad1ff70c5bd81af66da0045c72a763af277e9184))
- update ([3724ab3](https://github.com/CWBudde/WaterColorMap/commit/3724ab32e75950d68432fb489d4da2819e9e9fc6))
- update .gitignore to not ignore cmd/watercolormap directory ([9517977](https://github.com/CWBudde/WaterColorMap/commit/9517977a4d805112e778d77bd123851f240615f2))
- wasm ([80f3586](https://github.com/CWBudde/WaterColorMap/commit/80f358634669de4346896328f7ec82bc4e5b08f8))
- wasm deploy ([4882969](https://github.com/CWBudde/WaterColorMap/commit/48829697c5112da91d120dcbdf56fa5f2c2e2767))
- wasm deploy ([4d29a53](https://github.com/CWBudde/WaterColorMap/commit/4d29a53bc7c676687b3828162b7bca409f138db0))
