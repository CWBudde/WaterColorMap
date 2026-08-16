# Watercolor Map Tiles - Implementation Plan

This document outlines the complete implementation plan for creating Stamen Watercolor-style map tiles in Go, starting with Hanover and eventually scaling globally.

> **Completed phases have been archived** out of this file so that what remains
> here is work that is still open. See:
>
> - Phases 1–2 (data prep, tooling, base map rendering) →
>   [docs/history/phases-1-2-foundation.md](docs/history/phases-1-2-foundation.md)
> - Phase 3 (watercolor mask design, Stamen-aligned) →
>   [docs/watercolor-mask-design.md](docs/watercolor-mask-design.md)
> - Phase 4.1–4.9 (compositing, hi-DPI, demo server, Hanover coverage, TileJSON) →
>   [docs/history/phase-4-compositing-delivery.md](docs/history/phase-4-compositing-delivery.md)
>   — 4.10 (ocean/coastline) is complete; its record stays below
> - Phase 5.11.2 (blur rewrite: measurements, rationale, rescaled sigmas) →
>   [docs/performance/blur-optimization.md](docs/performance/blur-optimization.md)
> - Phase 5.11.3 (buffer reuse: measurements, invariants, `*Into` conventions) →
>   [docs/performance/allocation-optimization.md](docs/performance/allocation-optimization.md)
> - Phase 5.11.4 (pixel access: row-slice loops, clipping rules, measurements) →
>   [docs/performance/pixel-access-optimization.md](docs/performance/pixel-access-optimization.md)
> - Phase 5.11.6 (texture tiling: row copies, load-time NRGBA, why no atlas) →
>   [docs/performance/texture-optimization.md](docs/performance/texture-optimization.md)
> - Phase 5.11.7 (SIMD: the soft-edge and box-column AVX2 kernels, rejections,
>   accuracy argument) →
>   [docs/performance/simd-optimization.md](docs/performance/simd-optimization.md)
> - Phase 7.1/7.2/7.3/7.7 (build, tile-server hardening, code quality) →
>   [docs/history/phase-7-hardening.md](docs/history/phase-7-hardening.md)

## Phase 1: Data Preparation and Tool Setup ✅ COMPLETE

Tile coordinate system and flat tile storage, Overpass OSM fetching, the
Mapnik 3.1 / Web-Mercator rendering stack, six seamless watercolor textures, and
the Go project and YAML config skeleton.

## Phase 2: Rendering Base Map Layers ✅ COMPLETE

Multi-pass Mapnik rendering producing one colour-coded PNG mask per layer
(land, water, parks, civic, roads), with layer isolation via map-object reset and
a 128px buffer for seamless tile edges. 68 unit tests plus 3×3 grid integration
tests.

## Phase 3: Image Processing - Watercolor Effect ✅ COMPLETE

Cross-layer mask construction: `nonLandMask` = the union of every layer that
punches a hole in land (water, rivers, roads, railroads, highways, urban, civic,
buildings) → blur → noise → threshold → antialias → invert for land. Urban and
civic are painted with the road/rail/highway union subtracted rather than
intersected with land — they are already subtracted _from_ it — and parks are
constrained to land plus urban and civic, so a park in a built-up area survives.
Further-blurred masks are reused as darkening overlays. All work items done,
parameters retuned.
→ [docs/watercolor-mask-design.md](docs/watercolor-mask-design.md)

## Phase 4: Compositing and Tile Delivery

### 4.1–4.9 ✅ COMPLETE

Compositing engine and draw order; zoom-aware road fidelity; label-free tiles by
policy; seam verification via metatile padding plus a control-step border test;
`@2x` output world-anchored end to end; `watercolormap serve` with the Leaflet demo
and on-demand generation; per-layer tuning config with golden-image regression;
the Hanover z10–15 tile set; and TileJSON with attribution.
→ [docs/history/phase-4-compositing-delivery.md](docs/history/phase-4-compositing-delivery.md)

Worth keeping in view here, because both outlive the phase:

- **Unresolved — `TestRenderAdjacentTilesWithRealData/EdgeAlignment`**
  (`internal/renderer/multipass_test.go`) fails ~12 times. It is the naive
  predecessor of `TestCompositedTileSeams`: a fixed ±60 per-pixel threshold between
  the last pixel column of one tile and the first column of its neighbour. Those are
  adjacent but _different_ pixels, so an antialiased edge crossing the seam
  legitimately differs; observed gaps reach ~120. It fails identically on `837537a`,
  so it predates the hi-DPI work. Either the tolerance is wrong for raw layer masks
  or there is a real half-pixel offset — fold it into the control-step approach.
- **The Hanover set does not fill the viewport below z13.** `prebuild-hannover`
  generates the same `9.65,52.32,9.85,52.43` bbox at every zoom, so at z10–z12 a
  screenful is far larger than the box (z10 is 2 tiles for a viewport wanting ~36)
  and the surrounding tiles are generated on demand. A full-screen low-zoom view
  needs a wider bbox at those zooms, not a deeper zoom range. The 4.10 ocean
  prerequisite is now met, so this is unblocked.

### 4.10 Ocean/Coastline Rendering ✅ COMPLETE

**Problem**: OpenStreetMap has no ocean polygons — the sea is modelled as the
absence of land. `natural=coastline` is a line, `natural=water` covers lakes and
bays but not the open sea, and `place=sea` is a point label. The pipeline paints
land by _inverting_ the union of everything that cuts into it (`buildMasks`), so
with nothing covering the open sea every ocean tile inverted to full tan land and
coastal tiles came out backwards: lakes blue, sea tan.

**Solution (Option 1 of the three considered)**: the processed water polygons
from <https://osmdata.openstreetmap.de/data/water-polygons.html>, rendered
through Mapnik's native `shape` input plugin. No Go geometry dependency: the
Go-side work is download management and per-tile layer wiring. Option 2
(hardcoded ocean bboxes plus a "zero features means ocean" heuristic) was
rejected as wrong by construction and no help on coastal tiles; Option 3
(reimplementing `osmcoastline`) stayed out of scope.

- [x] `just fetch-water-polygons` downloads, unzips and `shapeindex`es both the
      full and the simplified 3857 datasets into the gitignored `./data`. The 3857
      variants need no reprojection; the `.index` sidecars turn every tile lookup
      into a bbox query instead of a full shapefile scan.
- [x] `ocean:` config block (`internal/cmd/ocean_config.go`) → `renderer.OceanConfig`,
      threaded through `pipeline.GeneratorOptions`. Simplified set for z ≤ 9, full
      set above; either stands in when only one is configured. Paths are validated
      at startup. Unconfigured means disabled, and inland tiles then render
      byte-identically to a build without ocean support.
- [x] Dedicated ocean pass: `assets/styles/layers/ocean.xml` (`shape` plugin,
      same `#0000FF` mask fill as `water.xml`) rendered by
      `MultiPassRenderer.renderOceanLayer`, which deliberately bypasses the
      zero-feature skip that would otherwise drop it.
- [x] `foldOceanIntoWater` (`internal/pipeline/generator.go`) unions the ocean
      pass into the water raw layer and drops the ocean key. Because land is
      derived by inversion, this fixes ocean tiles and coastal inversion at once,
      and nothing downstream — texture, tuning, composite order — needs to know
      ocean exists.
- [x] `WithEmptyResponsesAllowed` on the Overpass datasource: an empty z8–13
      response used to fail the tile, which is exactly the shape of an open-ocean
      tile. With ocean data configured it logs instead. The trade-off is
      deliberate and documented at the method: silent-Overpass-failure detection
      is given up in exchange for correct ocean tiles, since telling the two apart
      would need the water-polygon geometry in Go.

Two bugs found while verifying, both fixed:

- Mapnik resolves a relative datasource path against the directory of the XML it
  was loaded from, and `LoadXML` writes that XML to a temp file — so a relative
  shapefile path was looked up next to `/tmp` and the ocean silently vanished.
  `ShapefileForZoom` now returns an absolute path.
- `renderLayersWithData` tested `OutputPath == ""` before `res.Error != nil`, so
  every layer render _failure_ was swallowed as "layer absent". For ocean that
  quietly reinstated the original bug. The checks are now the other way round.

**Testing**:

- [x] Pure ocean tile (`z9_x266_y164`, North Sea) renders as water
- [x] Coastal tile (`z9_x267_y165`, East Frisian coast) renders sea _and_ land
- [x] Inland tile is pixel-identical with the ocean pass on and off
- [x] Zoom-dependent shapefile selection, config parsing/validation, the
      ocean/water union, and the empty-response opt-out are unit-tested
- [x] Verified visually at z9 and z13 against the local Overpass instance

The ocean render tests need the downloaded shapefiles and skip without them;
point `WATERCOLORMAP_WATER_POLYGONS_DIR` elsewhere if they do not live in `./data`.

**Still open, inherited from this work**: seam behaviour along a coastline
crossing a tile border is only checked by eye. `TestCompositedTileSeams` uses a
synthetic data source and never sees a coastline; extending it to a coastal pair
would need the shapefile, which is why it is not gated in yet.

## Phase 5: Scaling and Modern Improvements

### 5.1 Data Scaling Strategy ✅ COMPLETE

- [x] Plan regional database approach
- [x] Evaluate vector tile input option
- [x] Document data management for large regions
- [x] Plan storage requirements
- [x] Design data update pipeline

The five items were inherited verbatim from `docs/goal.md`, a brief written before the
implementation existed and assuming PostGIS. They could not be closed as written — the code went
Overpass-only, and `internal/cmd/datasource_config_test.go:83` asserts that `"postgis"` is rejected
— so they are answered against what the code is, staged concretely for Niedersachsen → Germany with
global coverage costed rather than planned. The measurements that change decisions elsewhere in this
plan (the Overpass fetch is ~71% of per-tile wall clock; there are no cheap empty tiles in PNG; WebP
is 9.2× lossy but 1.21× with the lossless encoder that shipped), the two capabilities that landed
with the document because the recommended workflow needs them (MBTiles resume via
`pipeline.TileProber`, and the off-by-default on-disk Overpass response cache), and the finding that
the public API's `406` was a rejected `User-Agent` rather than rate limiting, are all archived in
→ [docs/data-scaling-strategy.md](docs/data-scaling-strategy.md).

### 5.1a Follow-ups surfaced by the scaling analysis

Filed rather than smuggled into the 5.1 work, in rough value order.

- [x] **[P1]** Set a `User-Agent` on the Overpass client — the public API's `406` was the server
      rejecting Go's default UA, not rate limiting
      ([docs/data-scaling-strategy.md § 5](docs/data-scaling-strategy.md)). Done in
      `internal/datasource/useragent.go`, as a `RoundTripper` beside the existing limit and cache
      transports rather than as a patch to `go-overpass`: the request is built inside the client
      where the call site cannot reach it, and this layer also covers the per-server clients
      `MultiOverpassDataSource` builds, with no dependency release needed. Overridable via
      `overpass.user_agent`, globally or per server. The public API is now a usable fallback,
      which is what the routing recommendations assumed.
- [x] **[P1]** WebP output end to end — `--image-format webp` on `generate` and `serve`,
      `internal/tileformat` owning format identity and encoding, PNG still the default. The
      shipped encoder is pure-Go `nativewebp` (VP8L, lossless), which measures **1.21×**, not the
      9.2× of lossy q80 — lossless bought the absence of cgo, and the texture fills every pixel so
      there is nothing to collapse. Measurements, the two "false skip" guards that came with it,
      and the corrected § 3:
      [docs/data-scaling-strategy.md § 3](docs/data-scaling-strategy.md).

- [x] **[P2]** Overpass failover. Every matching server is now tried in order rather than the
      first coverage match being terminal, and the joined error names each one that failed.
      `shouldTryNextServer` (`internal/datasource/failover.go`) decides what earns a second
      server, and shadowed coverage boxes warn at startup instead of quietly costing public API
      rate limits. Rationale, the full retry classification and the ocean-rendering interaction
      are archived in [docs/MULTI-SERVER-OVERPASS.md § Failover](docs/MULTI-SERVER-OVERPASS.md#failover).
- [x] **[P2]** Make `@2x` on-demand-only rather than a second full render pass. `runHiDPIBatch`
      is gone; `generate --bbox --hidpi` now errors instead of silently producing half the tiles
      a script asked for, and `--hidpi` survives for single tiles. `serve` already rendered `@2x`
      on demand, and since the `@2x` query is byte-identical to the base one, a warm response
      cache serves it with no upstream traffic. 4× storage and 2× compute recovered.
- [x] **[P2]** Fetch per metatile band instead of per tile — `--band-fetch`, **off by default**.
      `out geom` returns unclipped geometry, so one query per block transfers a crossing motorway
      once instead of once per tile; at the default 4×4, Germany's 237,424 z14 queries become
      ~15k. A band's data is sliced to each tile's own fetch bounds before rendering, so absent
      layers stay absent rather than becoming present-but-blank. Why 8×8 is unsafe, why the guard
      is adaptive quadrant-splitting rather than a zoom ceiling, why band routing requires
      coverage **containment** where per-tile routing needs only intersection, and the two
      corrections this item originally got wrong:
      [docs/data-scaling-strategy.md § 4](docs/data-scaling-strategy.md).

- [x] **[P3]** Sort features by OSM ID in `ExtractFeaturesFromOverpassResult`. Both element loops
      now walk `slices.Sorted(maps.Keys(…))` rather than the raw `map[int64]*…`, so the same tile
      renders byte-identically twice instead of drifting by up to 36/255 on the antialiased edges
      where draw order flipped — which had put a tolerance floor under every PNG-level regression
      test. Rationale, measurements and the golden-update note:
      [docs/data-scaling-strategy.md](docs/data-scaling-strategy.md).
- [x] **[P3]** A tile purge command, and a source-data version stamp on rendered tiles. Neither is
      useful alone — a stamp nothing reads answers no question, and a purge with nothing to select
      on can only delete by geometry. `generate` and `serve` record per tile which OSM data it
      rendered from (Overpass's `osm3s.timestamp_osm_base`, not the clock), `--stale-*` turns
      skip-existing into a freshness question, and `watercolormap purge` deletes by area, zoom,
      suffix or staleness, dry run by default. Why the timestamp comes from the response body, why
      the stamp key carries the image format, why an older schema is refused rather than migrated,
      and why the server treats an absent store differently from `generate`:
      [docs/tile-stamps-and-purge.md](docs/tile-stamps-and-purge.md).

- [x] **[P3]** Streaming tile enumeration and a checkpoint file. `generate`'s non-banded path no
      longer materialises anything per tile — `tile.TilesInBBoxSeq` is the enumeration as an
      `iter.Seq`, a producer goroutine feeds a `workers*2` channel, and `worker.Config.OnResult`
      takes results one at a time. The checkpoint (`internal/checkpoint`, `--checkpoint`, off by
      default) stores a **watermark over the enumeration**, not a tile set, so resuming is
      arithmetic rather than a re-stat of hundreds of thousands of tiles; a failed tile blocks the
      watermark so a resume re-attempts it. Why `Pool.Run` keeps its `len(results) == len(tasks)`
      contract, why the banded path still materialises its list, and why `--checkpoint` is refused
      together with `--band-fetch`:
      [docs/data-scaling-strategy.md § 1](docs/data-scaling-strategy.md).

- [ ] **[P2]** Lossy WebP encoding — still open, and still the largest storage lever here. The
      shipped lossless encoder gives 1.21×; lossy q80 measured 9.2× on the same tiles, and it is
      the only thing that makes an empty tile cheap (108 KB → 6 KB). `tileformat.Encoder` is an
      interface, so this is one more implementation plus a decision about acceptable round-trip
      damage (~4/255 mean per channel; the tiles are label-free by policy, so there is no text to
      smear). The cost is that every lossy Go encoder needs cgo, which is what the current choice
      bought its way out of — `GOOS=js` and the release matrix build with no build tags.

### 5.2 Parallel Tile Rendering

- [x] Implement worker pool for tile generation (`internal/worker/pool.go`)
- [x] Add goroutine-based parallel processing (configurable worker count, defaults to NumCPU)
- [x] Implement database connection pooling (N/A - Overpass API, generators are per-worker)
- [x] Add progress tracking and logging (`internal/worker/progress.go`)
- [x] Test parallel rendering performance (unit tests in `internal/worker/pool_test.go`)
- [x] Optimize worker count (defaults to `runtime.NumCPU()`)
- [x] Add batch CLI command (`--bbox`, `--zoom-min`, `--zoom-max`, `--workers`, `--progress`)

### 5.3 Multi-Zoom Generation ✅ COMPLETE

Filled the z0–5 gap the way 5.1 recommended and 4.10 proved: Natural Earth
shapefiles through Mapnik's `shape` plugin, following the ocean pattern.
`NaturalEarthConfig.CoversZoom` (`internal/renderer/naturalearth.go`) is the one
predicate the renderer, the band scheduler and `serve`'s fetch queue all branch
on, so no low-zoom Overpass query escapes; 110m serves z0–2 and 50m z3–5; ocean,
lakes and rivers render from `assets/styles/naturalearth/*.xml` and every other
layer is simply absent, which is the world-scale look. Tested by
`TestNaturalEarthRendering` and `TestNaturalEarthOnlyLowZoomLayersRender` against
a datasource fake that fails if it is ever queried; no pipeline golden moved,
since nothing here touches z≥6. Rationale, the EPSG:4326-vs-3857 trap and the
open question about the z5 ceiling are archived in
→ [docs/data-scaling-strategy.md](docs/data-scaling-strategy.md) § 2.1,
zoom-by-zoom behaviour in [docs/zoom-levels.md](docs/zoom-levels.md).

### 5.4 Tile Storage Format

- [x] Research MBTiles format
- [x] Implement MBTiles writer
- [x] Convert folder tiles to MBTiles
- [x] Test MBTiles serving
- [x] Document MBTiles usage

### 5.5 Tile Hosting Options

- [ ] Evaluate self-hosting requirements
- [ ] Research cloud storage options (S3, Azure Blob)
- [ ] Test CDN integration (CloudFront)
- [ ] Evaluate third-party providers (Mapbox, MapTiler)
- [ ] Document hosting recommendations
- [ ] Set up initial hosting solution

### 5.6 On-the-Fly Rendering Service ✅ COMPLETE

Most of the service already existed — two backends, admission control, per-tile
deduplication, a fetch queue, retries and a status stream — but none of its cache was
_measurable_ or _cheap_. Closed by adding the four things that were missing: per-request
accounting (`X-Cache` plus a `cache` object in `/tiles/status`, with staleness counted as a
reason so a too-aggressive `--stale-*` cutoff is visible); an `ETag` and conditional requests on
both backends; an in-process LRU of tile-file metadata that removes two `os.Stat`s and a stamp
lookup per hit (measured 3769 → 2278 ns/op on the stamped hit path); and `just load-test`, seven
benchmarks over the real handler with the render stubbed out, which is where those numbers come
from. **Redis was declined rather than skipped** — a single static binary whose cache of record is
the tile directory gains a mandatory service and a second copy of the bytes, and the cross-node
case it would be bought for belongs to 5.5 and is served by the `ETag` work instead.

One behaviour change to know about: **`--cache-control` now defaults to `no-cache`** rather than
`no-store`, because `no-store` forbids the client from keeping anything and so made the validator
dead code. A positive `max-age` stays opt-in — it outlives `purge`.
→ [docs/tile-server-architecture.md](docs/tile-server-architecture.md)

- [x] Design Go tile server architecture (written down, at last)
- [x] Implement tile caching strategy — four layers, and why tile bytes are not one of them
- [x] Add cache hit/miss handling — the handling existed; the accounting did not
- [x] Implement LRU cache or Redis — `internal/lru` in front of the tile directory; Redis declined
- [x] Test server under load — `just load-test`, plus a `-race` concurrency test for CI
- [x] Optimize for cache performance — ETag/304, one stat per hit, none on a metadata hit

### 5.6a Browser Playground (WebAssembly On-Demand)

- [x] Compile tile generator to WebAssembly (Go → WASM) using TinyGo or standard Go WASM toolchain
- [x] Create a minimal browser UI with Leaflet + IndexedDB/localStorage for client-side tile caching
- [x] Implement on-demand tile generation in the browser (fetch OSM data → render → cache → display)
  - Note: Actual rendering delegates to backend server since Mapnik can't run in browser; WASM provides canonical filename builder
- [x] Handle browser memory/performance constraints (limit concurrent generations, use web workers if needed)
- [x] Set up GitHub Actions CI workflow to build WASM artifact on commits
- [x] Deploy built WASM + demo HTML to GitHub Pages (gh-pages branch or Pages deployment)
- [x] Display rendering progress and cache status in the UI
- [x] Document browser limitations and expected slowness without proper caching backend
- [x] Add disclaimer that this is a proof-of-concept playground, not production-grade
- [x] Add static tile pre-generation for demo area (Hanover, z13-14)
- [x] Implement hybrid tile serving (static first, WASM fallback)
- [x] Configure CI workflow to regenerate tiles on code changes

**Note**: The playground now uses hybrid tile serving with pre-generated static tiles for the demo area (Hanover, zoom 13-14) served from GitHub Pages, falling back to on-demand WASM generation for uncovered areas.

### 5.7 Data Update Pipeline

The _design_ is done under 5.1 — two layers: daily changed-node-bbox invalidation from the `.osc.gz`
(which misses tag-only edits on ways whose nodes are untouched, and proper expiry needs a
node-location store, i.e. `osm2pgsql --expire-tiles`), plus a periodic full regional re-render whose
cadence follows from measured throughput.

The _capability_ that was missing now exists: `watercolormap purge` deletes tiles by area, zoom or
staleness, and every tile rendered by `generate` carries the source-data version it came from, so
`generate --stale-data-before` expresses "re-render everything older than the last import" (5.1a).
`serve` is covered too: its on-demand generator writes into the server's stamp store
(`internal/server/ondemand_tiles.go`), so a tile rendered by a request carries the same version a
batch-rendered one does. What is still open is the scheduling around the two commands, and
the diff-to-bbox step — the daily changed-node boxes are the input purge wants, and deriving them
correctly for tag-only edits still needs the node-location store.
→ [docs/data-scaling-strategy.md](docs/data-scaling-strategy.md)

- [x] Design periodic data refresh strategy (two layers, see the document)
- [x] Expiry and freshness primitives: `purge` plus the source-data stamp (5.1a)
- [ ] Implement OSM diff application (optional) — the remaining blocker for layer 1
- [ ] Create full re-render pipeline
- [ ] Schedule automated updates
- [ ] Test update process
- [ ] Document update procedures

### 5.8 Enhanced Textures

- [ ] Create zoom-specific textures
- [ ] Add coarse paper texture for low zoom
- [ ] Add fine detail textures for high zoom
- [ ] Implement texture selection by zoom
- [ ] Test visual consistency across zooms

### 5.9 Modern Enhancements

- [ ] Evaluate hillshading/relief integration
- [ ] Test DEM data overlay
- [ ] Implement subtle terrain shading
- [ ] Add paper grain effect (optional)
- [ ] Test overall aesthetic balance

### 5.10 Vector Data Integration

- [ ] Plan vector tile service for interactivity
- [ ] Set up parallel vector tile endpoint
- [ ] Test feature highlighting on hover
- [ ] Document vector integration approach

### 5.11 Performance Optimization

**Status**: ✅ Profiling complete, optimization roadmap defined

#### 5.11.1 Performance Analysis ✅ COMPLETE

- [x] Create comprehensive benchmark suite (`internal/watercolor/benchmark_test.go`)
- [x] Run CPU profiling on tile generation pipeline
- [x] Run memory profiling on tile generation pipeline
- [x] Analyze bottlenecks (the referenced `PERFORMANCE_ANALYSIS.md` was never committed; the
      numbers below came from a `cpu.prof` that predates the box-blur work and the module rename,
      so treat them as historical)
- [x] Optimize Perlin noise generation (eliminated 6-7x redundant allocations)

**Current Performance** (`BenchmarkFullPipeline`, 256x256 tile, 5 layers, after 5.11.3):

- Memory per tile: ~2.2MB
- Allocations: ~38

The "~86ms / 29MB / 1.3M allocations" that stood here came from the same stale profile
as the findings below and had already been invalidated twice, by 5.11.2 and 5.11.3.
Time per tile is not quoted because the development machine's run-to-run spread is
wider than any change measured against it; use `benchstat` over interleaved runs.

**Key Findings** (historical, from the stale profile):

- Gaussian blur: 39.6% of CPU time
- Image buffer allocations: 37.8% of memory (64-bit RGBA overhead)
- Pixel access overhead: 17.7% of memory (color.NRGBA allocations per At() call) —
  **wrong**, and 5.11.4 says why: the typed accessors allocate nothing, they cost a
  bounds check and a multiply per pixel. It was a CPU cost, not a memory one
- Perlin noise: ✅ Already optimized (40ms saved per tile)

**Current profile** (after 5.11.3, `BenchmarkFullPipeline`, top flat entries):

- Distance transform: ~25% (`distanceTransform1DWithBuffers`, `distanceTransformRows/Columns`)
- Per-pixel accessors: ~25% (`NRGBAAt`, `SetNRGBA`, `GrayAt`, `SetGray`, `PixOffset`, `Point.In`) —
  ✅ addressed by 5.11.4
- Noise and colour conversion: ~13% (`applyNoise`, `hslToRGB`, `rgbToHSL`) — the HSL round
  trip is now the largest single remaining cost, and it is lossy, so removing it is a
  look change rather than an optimisation
- Blur: no longer in the top 14

#### 5.11.2 Blur Optimization ✅ COMPLETE

**Result**: 2-11x faster blur depending on sigma (6-11x across the range the layer masks use);
blur is no longer a bottleneck. `blurkernel.PlanFor` picks a direct fixed-point Gaussian below
sigma 4 and a 3-pass box blur above it, with an AVX2 path and a portable fallback. Allocations per
blur dropped from 12 to 1; `MaskProcessing` -16% time / -47% bytes, `FullPipeline` -33% / -38%.

Two things a future reader can easily undo, both explained in the archive: the **default sigmas
were rescaled** (`BlurSigma` 1.2 → 2.45 and friends) because the old blur ran ~2x wider than its
nominal sigma, so tiles keep their look while sigma now means blur width in pixels; and
`TestBlurAccuracyVsGaussian` asserts _which path_ each sigma takes, not just its RMSE budget.

Full measurements, rationale and the remaining `BoxCols` vectorisation follow-up →
[docs/performance/blur-optimization.md](docs/performance/blur-optimization.md)

#### 5.11.3 Memory Allocation Optimization ✅ COMPLETE

**Result**: `BenchmarkFullPipeline` allocates 2.19 MiB per tile instead of 5.85 MiB
(-62%) in 38 allocations instead of 143 (-73%), with bit-identical output.

Every kernel in `internal/mask` gained an `*Into` variant writing a caller-owned
destination, and the per-layer mask pipeline now runs over a pooled `maskScratch`
instead of allocating a full-size `*image.Gray` per stage and keeping one. Two
allocations per layer remain and are irreducible: the final mask (the land path hands
it to parks) and the painted layer (the compositor holds them all at once).

The checklist that used to stand here was written against a stale picture and was not
followed literally. It asked for a pool keyed on "common image sizes (256x256,
512x512)" and blamed the `gift` library's 64-bit RGBA buffers — but 5.11.2 already
replaced `gift` with `internal/mask/blurkernel` on `[]uint8` (it is test-only now),
and production never processes a 256² image: `RequiredPaddingPx` puts a 256 tile on a
384² metatile. A size-keyed pool also saves nothing until destination-taking variants
exist. The pooled-context idiom already used three times in the tree was extended
instead.

Note also that wall time did not measurably improve, and the "10-15% speedup via GC
reduction" this section promised was never achievable at this scale. The payoff is GC
pace under the concurrent tile server. The remaining CPU cost is per-pixel accessors,
which is 5.11.4's job — every kernel now has a destination-taking form, so 5.11.4 can
rewrite loop bodies without touching a call site.

Measurements, the four buffer-reuse invariants and the `*Into` conventions →
[docs/performance/allocation-optimization.md](docs/performance/allocation-optimization.md)

#### 5.11.4 Pixel Access Optimization ✅ COMPLETE

**Result**: `BenchmarkFullPipeline` uses 28% less CPU; the individual kernels are 19-76%
cheaper depending on how much of the loop was accessor overhead. Output is bit-identical —
the pipeline goldens did not move.

The mask, distance, edge, texture and compositing loops now resolve one row slice per
image per row and index it, instead of calling `GrayAt`/`SetGray`/`NRGBAAt`/`SetNRGBA`
per pixel. The per-pixel type switches in `getAlpha` and `getNRGBA` were hoisted out of
their loops, and `alphaOver`/`cropNRGBA` no longer box a `color.Color` per pixel.

Two things a future reader can easily undo. `SetGray` **silently clipped**
out-of-bounds writes and `GrayAt` **read zero** outside an image, and several callers
depend on both — `writeRect` and `grayRow` preserve them once per call instead of once
per pixel. And the `rgbToHSL`→`hslToRGB` round trip in the edge pass is lossy, so
skipping it where the mask is white is a look change, not a free win.

The old checklist here aimed at "349MB of temporary color allocations" and expected
5-10%. Both came from reading the profile as an allocation problem; the typed accessors
allocate nothing. The archive explains what the cost actually was.

Measurements, the loop conventions, the clipping rules and why `benchstat` could not be
used on this machine →
[docs/performance/pixel-access-optimization.md](docs/performance/pixel-access-optimization.md)

#### 5.11.5 Parallel Layer Processing 🟢 MEDIUM PRIORITY

**Target**: Utilize multi-core CPUs (Expected gain: 30-50% on multi-core systems)

- [ ] Identify independent layers that can be processed in parallel
- [ ] Implement goroutine-based parallel layer painting
- [ ] Add synchronization for shared resources (noise texture, textures)
- [ ] Benchmark single-core vs multi-core performance
- [ ] Test correctness with parallel processing enabled
- [ ] Document parallelization strategy and trade-offs

**Context**: Water, land, parks, civic layers can be painted independently. Roads/highways depend on land mask but could still be parallelized after land completes.

#### 5.11.6 Texture Processing Optimization ✅ COMPLETE

**Result**: tiling a texture into a metatile costs 96% less CPU (620µs → 22.5µs),
masking one 87% less, and the @2x path no longer allocates. `BenchmarkFullPipeline` is
14% faster and `BenchmarkPaintFromMask` 22% faster; texture work fell from ~17.5% of the
pipeline's CPU profile to 1.5%. Output is bit-identical — the pipeline goldens did not
move.

Unscaled tiling is doubly periodic, so a row is now built by copying one texture period
out of the source row and replicating it, and repeats vertically by copying the row one
texture height above. Textures are normalised to `*image.NRGBA` at load time, which is
what lets those copies exist: every PNG in `assets/textures` decodes to `*image.RGBA`
and `white.png` to `*image.Paletted`, so production was running the two slow sampler
paths while every benchmark and test fed the loops the fast one.

The "TileTexture allocates 175MB per benchmark" this section rested on was **wrong** and
two phases stale: 5.11.3 gave tiling a destination-taking form on a pooled buffer, and
the texture package allocated nothing per tile on `main`. The cost was CPU, and it was
17.5%, not the 3-5% predicted. **No texture atlas was built, deliberately** — with ten
textures, one per layer, each used at full size, an atlas changes resident bytes by
nothing and cannot touch the remaining cost, which is the destination write. Lazy tiling
has nothing to defer (the tiled texture is consumed on the next line, out of a pooled
buffer), and caching tiled textures across tiles would trade microseconds for a shared
mutable buffer under the parallel tile server. Two things a future reader could undo: the
`reference*` loops in `internal/texture/pixelaccess_test.go` are frozen copies of the old
implementation on purpose, and `ToNRGBA` must keep routing texels through `getNRGBA` —
any other conversion moves pixels.

Profile, measurements, the row-copy convention and the full atlas argument →
[docs/performance/texture-optimization.md](docs/performance/texture-optimization.md)

#### 5.11.7 SIMD Optimization ✅ COMPLETE

**Result**: `BenchmarkFullPipeline` is 17% faster (18.90ms → 15.68ms, benchstat over
12 interleaved runs, p=0.000). The soft-edge darkening pass — the largest single cost
left after 5.11.4, at 22% of pipeline CPU — is 33% cheaper end to end and 5.3x cheaper
at the kernel. The box-blur column pass that 5.11.2 left as a follow-up is 3x cheaper,
taking the 7.48 land-shade blur down 28%. Both kernels are **bit-identical** to their
scalar references; the pipeline goldens did not move.

Three things a future reader can easily undo, all explained in the archive. Neither
new kernel may cover a ragged tail by **repeating the final block** the way
`ConvColsRowAVX2` does — the soft-edge pass writes its output and may alias its
input, and the box kernels carry an accumulator — so both hand their tail to the
portable loop. The **float64 multiply** in the mask falloff is deliberate and runs in
double-precision lanes; narrowing it breaks bit-identity on a handful of mask levels.
And the kernel omits the scalar's `h %= 1536` because the hue provably never reaches
it — a hue of 1536 would select a seventh sector and render black.

The "expected gain: 10-20% for specific operations" was right by accident. The
checklist that stood here named pixel blending and noise application as the targets;
the profile named neither. It was written before 5.11.3/5.11.4/5.11.6 and the HSL
round trip it did not mention had become the whole of the opportunity. `avo` was
evaluated and rejected: both kernels are a single loop body, so a code-generation
dependency buys nothing a reviewer of the generated `.s` would not still have to read.
The distance transform, now 30% of the profile and the largest remaining entry, is
Felzenszwalb's lower-envelope algorithm and is not vectorisable without replacing it.

Profile, rejected candidates, the accuracy argument and the cross-platform story →
[docs/performance/simd-optimization.md](docs/performance/simd-optimization.md)

#### 5.11.8 Performance Monitoring & Regression Testing

- [ ] Add continuous benchmark tracking to CI
- [ ] Set performance budgets (max time/memory per tile)
- [ ] Create performance regression tests
- [ ] Document performance characteristics per zoom level
- [ ] Add performance dashboard/reporting

**Combined Expected Speedup**: the "50-70% faster (86ms → 40-50ms per tile)" that stood
here was arithmetic over the stale profile's per-item estimates, and every item that has
actually been done came in somewhere else: the blur was far better than predicted, the
allocation work moved wall time not at all, and pixel access beat its 5-10% estimate by
several times over. Quote measurements from the archived documents instead of a forecast.

### 5.12 Documentation and Deployment

- [ ] Document complete installation process
- [ ] Create configuration guide
- [ ] Document troubleshooting steps
- [ ] Create user guide for custom textures
- [ ] Document API/tile serving interface
- [ ] Write deployment guide
- [ ] Create monitoring and maintenance guide

## Phase 6: Global Expansion

### 6.1 Data Preparation

- [ ] Download OSM planet file or regional extracts
- [ ] Import global data into PostGIS (or regional DBs)
- [ ] Verify data coverage and quality
- [ ] Document global data setup

### 6.2 Region Prioritization

- [ ] Identify high-priority regions for initial generation
- [ ] Plan generation schedule by region
- [ ] Allocate storage for global tiles
- [ ] Document regional coverage

### 6.3 Batch Generation

- [ ] Create global tile generation script
- [ ] Implement resume capability for interrupted runs
- [ ] Add error handling and retry logic
- [ ] Generate tiles by region/zoom
- [ ] Monitor generation progress
- [ ] Verify tile quality across regions

### 6.4 Quality Assurance

- [ ] Visual spot-checking of key cities
- [ ] Automated testing for tile validity
- [ ] Check tile edge alignment globally
- [ ] Verify color consistency
- [ ] Test at various zoom levels worldwide

### 6.5 Final Deployment

- [ ] Upload complete tile set to hosting
- [ ] Configure CDN for global delivery
- [ ] Set up monitoring and analytics
- [ ] Create public demo page
- [ ] Announce project completion

---

## Phase 7: Repository Quality & Hardening 🟨 IN PROGRESS — from full-repo quality review (2026-08)

A detailed, multi-area review (code quality, testing, CI/build, docs, security) found that
several things advertised as working were in fact **broken or non-functional**, giving a false
sense of safety. This phase tracks fixing what can be fixed. Items are ordered by priority;
`[P0]` = broken/red today, `[P1]` = high impact, `[P2]` = should-fix, `[P3]` = polish.

Everything `[P0]` is now closed; 7.4 (CI/build) and 7.6 (testing) remain, plus the follow-ups 7.9
collected while closing 7.5.

### 7.1-7.3, 7.7 ✅ COMPLETE — build, tile-server hardening, code quality

All closed. Summary of what landed:

- **7.1 (P0, build was RED)**: fixed the non-compiling `internal/geojson` tests, a SIGSEGV in
  `internal/watercolor` (nil `PerlinNoise` reached via a per-layer style override), a
  `docker/Dockerfile` broken by shfmt-mangled line continuations, and a CI unit job that installed
  no Mapnik and therefore never compiled half the repo.
- **7.2 (P0/P1, server was not internet-safe)**: tile-coordinate validation with 400-vs-404
  sentinels, per-job `recover()` in background workers (`internal/safe`), per-IP rate limiting plus
  a bounded admission queue inside `OnDemandTiles`, context-aware Overpass queries with a real HTTP
  timeout, full `http.Server` timeouts and graceful shutdown, a transport-level Overpass response
  cap, generic error bodies with `Cache-Control: no-store`, and an evicting per-tile lock map.
- **7.3 (P1/P2)**: unique per-renderer GeoJSON temp dirs (the shared path raced 256px against
  `@2x`), removal of `debugCtx interface{}`, `sync.Pool` buffer reuse in painting and the distance
  transform, Web-Mercator math consolidated into the new leaf package `internal/geo` (six
  implementations, two of them disagreeing on argument order), one authoritative
  `composite.DefaultOrder`, option structs in `cmd/generate.go`, de-duplicated threshold/noise and
  Overpass query builders behind golden tests, and MBTiles storing raw PNG per spec.
- **7.7**: `worker/pool.go` now guarantees `len(results) == len(tasks)`; two Overpass query
  oddities resolved (duplicate `natural=heath` dropped; the z8-9 roads regex confirmed intentional
  and documented as such).

Per-item detail, including the reasoning that keeps several of these from being "fixed" back into
bugs → [docs/history/phase-7-hardening.md](docs/history/phase-7-hardening.md)

### 7.4 CI / build / tooling (P1/P2)

- [ ] **[P1]** Make `build-release.yaml` actually produce binaries: it cross-compiles `CGO_ENABLED=1`
      for arm64/windows/darwin-amd64 with no cross toolchain or cross-built Mapnik (3 of 5 targets fail);
      the tag-push trigger has an empty `upload_url`; `actions/upload-release-asset@v1` is deprecated.
      Drop unbuildable targets or add proper cross containers; replace the upload action.
- [ ] **[P1]** Repair fake checks: `check-tidy` never runs `go mod tidy` before diffing (always passes);
      `check-generated` is a stub that echoes success; `test-format` installs none of treefmt's formatters
      so it verifies nothing. Make them real or delete them.
- [ ] **[P1]** Collapse the three overlapping lint/format stacks (golangci-lint + trunk + treefmt) and
      fix the gci prefix casing bug (`treefmt.toml:47` `WaterColorMap` → `watercolormap`, so local-import
      grouping silently does nothing). Align golangci-lint versions (CI v2.2.1 vs trunk 2.7.2) and the
      trunk Go runtime (1.21) with the project's Go 1.25.
- [ ] **[P2]** Stop shfmt from formatting the Dockerfile (`treefmt.toml:70` — root cause of 7.1's
      broken `&&`); add a `.dockerignore`; verify the downloaded Go tarball checksum; digest-pin base
      images.
- [ ] **[P2]** Fix the cache-dependency glob `*/*.sum` → `go.sum` (root lockfile) in
      `test-unit/test-lint/test-can-build.yaml`; SHA-pin third-party actions.
- [ ] **[P3]** Pin the core dependency `MeKo-Christian/go-overpass` (untagged pseudo-version on a
      personal fork) or bring it in-org; emit `vX.Y.Z` release tags (release-please currently produces
      bare `0.2.0`, which Go module tooling won't resolve).

### 7.5 Documentation & repo hygiene ✅ COMPLETE (P3 remainder deferred to 7.9)

- [x] **[P1]** README quick-start commands fixed. `--tile z13_x4297_y2754` is not a flag and never
      was — it appeared three times, including as the first command in the quick start, so the first
      thing a new user ran errored. Replaced with `--zoom 13 --x 4317 --y 2692`
      (`internal/cmd/generate.go:35-37`), which the README already showed one example later; that
      duplicate is now the only copy. Batch generation used `--min-zoom/--max-zoom/--bounds`, none of
      which exist either → `--zoom-min/--zoom-max/--bbox` (`generate.go:40-42`); `--bounds` belongs to
      `convert`. `Justfile`'s `generate-test-tile` carried the same bogus `--tile` and was fixed to
      match the already-correct `generate-tile` recipe. Also swept up in the same pass: `cd watercolormap`
      after a clone that creates `WaterColorMap/`; a ` ```text ` fence that was never closed, so
      the `--hidpi`/`--png-compression` prose rendered as code; and the previously undocumented
      `convert` and `textures` subcommands, which now get an example each. Verified by running every
      command, not by reading: batch enumerated its 13 tiles and `convert` wrote a 335-tile MBTiles.
- [x] **[P1]** `docs/wasm-playground/wasm.wasm` and `wasm_exec.js` untracked (`git rm --cached`);
      `wasm_exec.js` added to `.gitignore` next to the `*.wasm` rule that was already there but had
      been bypassed with a forced add. Safe because nothing consumed the committed copies: CI rebuilds
      both (`wasm-deploy.yml` "Build WASM" + `scripts/copy-wasm-exec.sh`) and uploads
      `docs/wasm-playground` as the Pages artifact, and `just build-wasm` produces them locally. This
      also unbreaks `just check-formatted`, which aborts on a dirty worktree and therefore failed after
      any local wasm build. The bullet's **"~95% of repo bloat" was wrong**: the seven historical
      `wasm.wasm` revisions are ~52 MB of a ~161 MB loose-object store — large, but not 95%. Untracking
      stops future growth only; purging the existing blobs needs a history rewrite, which is deliberately
      out of this PR and tracked separately.
- [x] **[P1]** `config.example.yaml` rewritten against the code. About three quarters of it was
      silently ignored — there is no config package, every value is read by a `viper.Get*` call in
      `internal/cmd/*.go`, and the `tile:`, `test-area:` and `rendering:` blocks plus
      `overpass.timeout/rate-limit/retry` had no reader at all. Deleted rather than corrected: a key
      that looks configurable and isn't is worse than an absent one, and `tile.width/height` in
      particular invited setting a tile size that only `--tile-size` controls. The dead blocks were
      wrong on their own terms too — `park.png`/`forest.png` don't exist and `forests` is not a
      `LayerType` (`internal/geojson/converter.go:16-26`); the real per-layer texture mapping is
      hard-coded at `internal/texture/processor.go:44-53`, which the file now states. In their place:
      commented-out `generate:`/`serve:`/`convert:`/`textures:` sections covering all 52 keys that are
      read, each with its real default, commented so a fresh copy still changes nothing. Two traps are
      called out — keys inside those sections are **underscored** while the matching flag is hyphenated
      (`--zoom-min` → `generate.zoom_min`), and everything under `overpass:` is honoured by `serve`
      only, since `generate` always builds a default single-server source (`generate.go:361-368`). Same
      deletions applied to `config.multi-overpass.example.yaml`. Verified every remaining key against
      the extracted list of `viper.Get*` call sites.
- [x] **[P2]** Identity links fixed — but **the bullet's premise was already false when written**.
      Nothing in the module or README said `MeKo-Tech`: `go.mod:1` is `github.com/cwbudde/watercolormap`
      and the remote is `github.com/cwbudde/WaterColorMap`; `MeKo-Tech` appeared nowhere outside this
      plan's own prose. The real split was narrower: 46 `MeKo-Christian/WaterColorMap` links in
      `CHANGELOG.md` (the v0.3.0 block already used `CWBudde`) and two `meko-christian.github.io` demo
      links in `README.md`. Both repointed at `CWBudde`/`cwbudde`. Commit SHAs survived the repo move,
      so the rewritten CHANGELOG links resolve, and release-please only appends, so editing historical
      entries is safe. Nothing in CI hardcodes an owner (`wasm-deploy.yml` uses
      `steps.deployment.outputs.page_url`).
- [x] **[P2]** `docs/` consolidated. `WASM-PLAYGROUND-{IMPLEMENTATION,STATUS,QUICKSTART}.md` (662 lines
      across three files, ~85% the same document written twice with the third a strict subset) became a
      single `docs/wasm-playground.md`; `docs/wasm-playground/README.md` is now a pointer. Dropped the
      one-time build transcripts and the file-size inventories — the latter still claimed 3.1 MB for a
      19.7 MB binary, which is exactly how that kind of table ages. `PHASE-2-COMPLETE.md` deleted: it
      duplicated the Phase 2 checklist above and linked to a `docs/2.1-layer-design.md` that does not
      exist. `docs/goal.md` kept with a header marking it as the superseded research brief — it carries
      21 sourced Stamen citations that exist nowhere else — and flagging that it still describes PostGIS
      as live where the implementation went Overpass-only. In this file: `--port=8080` → `--addr` in the
      MBTiles serve example (the pointer to "lines ~699" was wrong; it is in the MBTiles Usage section
      at the end), and the "gzip compression" bullet dropped as it contradicted 7.4's own fix above.
      Phase 3's `🟨 IN PROGRESS` → `✅ COMPLETE`, since all six of its 3.4 work items are `[x]`.
      **4.10's `BLOCKER` marker was left alone at the time: it was accurate, not stale.** All 21 of its
      checkboxes were unchecked, there was no water-polygon datasource anywhere in the tree, and the only
      coastline handling was the way-only Overpass query in `internal/datasource/overpass.go` — precisely
      the lines-not-polygons failure the section described. 4.10 has since been implemented and the
      marker is now `✅ COMPLETE`.
      Two claims in the old docs turned out to be false and are corrected in the merged file rather than
      carried over: there is **no IndexedDB cache** (`wasm.js` contains no IndexedDB at all), and the
      playground does **not** need a backend — `cmd/wasm` imports `internal/raster` and renders the full
      pipeline in-browser from Overpass. `README.md` repeated both and was fixed to match.
- [ ] **[P3]** Commit hygiene, `CONTRIBUTING.md`, package-level godoc, architecture overview. The
      commit-hygiene half is **not fixable** — those commits are in released history, and the CHANGELOG
      entries they generated ("more progress", "recent work", 7× "playground issues fixed") can only be
      prevented going forward, which the conventional-commit discipline used since Phase 7 already does.
      The three documents are deferred to 7.9 rather than rushed into a docs PR: an architecture
      overview is worth writing properly, and package godoc touches ~20 packages.

### 7.6 Testing improvements (P2/P3)

- [ ] **[P2]** Separate pure-Go logic from CGO via build tags so genuinely good tests
      (`parseTilePath`, synthetic pipeline path, raster, mask/composite) run in a Mapnik-less env / CI.
- [ ] **[P2]** Add `internal/raster` tests (353 LOC, pure Go, zero tests). The mocked-HTTP
      (`httptest`) half of this item is **done**: `internal/datasource/cache_transport_test.go`
      drives a counting `httptest.Server` through the cache/retry/error paths, including a
      byte-identity check that a cache hit hands the client exactly what the network did. The
      remaining tautological tests (non-nil constructor assertions) are still worth replacing.
- [ ] **[P2]** Delete the ~7.2 MB orphaned goldens under `testdata/golden/watercolor-stages*/`
      (referenced by no test) and fix the `update-goldens` Justfile recipe (matches no test → no-op).
- [ ] **[P3]** Replace timing-based assertions (`worker/pool_test.go:90,174`) with deterministic
      synchronization; switch file-producing tests to `t.TempDir()` (currently write shared
      `testdata/output/...`); adopt `t.Parallel()` where safe (0 uses today).

### 7.9 Follow-ups surfaced while completing 7.5 (P2/P3)

Defects the documentation audit found in the **code**. None were fixed under 7.5, because changing
behaviour inside a docs PR is how a docs PR stops being reviewable. The docs were corrected to
describe what the code actually does; these entries track making the code do the documented thing.

- [ ] **[P2]** All `WATERCOLORMAP_*` environment variables are dead. `cfgFile` is a plain cobra
      `StringVar` that is never `viper.BindPFlag`-ed (`internal/cmd/root.go:40,61`), so
      `WATERCOLORMAP_CONFIG` does nothing — it was in the README's Docker example and in
      `Justfile`'s `docker-run`, both of which have carried a no-op flag for as long as they've existed.
      Worse, `viper.AutomaticEnv()` (`root.go:70`) has no `SetEnvKeyReplacer`, so every nested or
      hyphenated key maps to a name no shell can set (`WATERCOLORMAP_SERVE.ADDR`,
      `WATERCOLORMAP_OUTPUT-DIR`). The README's env-var precedence claim was removed rather than
      repaired. Fix: bind `cfgFile`, add `SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))`.
- [x] **[P2]** `generate` ignored the entire `overpass:` config block — `newTileDataSource` always
      constructed a default single-server source, so neither `overpass.endpoint` nor the geographic
      routing in `overpass.servers` affected batch generation, which is exactly the workload that most
      wants multiple servers. **Fixed**: `newTileDataSource` (`internal/cmd/generate.go:403-419`) now
      goes through the same `createOverpassDataSource` that `serve` uses, guarded by a regression test
      covering both the `overpass.endpoint` and the coverage-routed `overpass.servers` case
      (`internal/cmd/datasource_config_test.go:20-78`). The caveat has been dropped from both example
      config files. Closed by the 5.1 work, which needed the routing to be real before it could
      recommend it.
- [ ] **[P3]** `wasm-deploy.yml` has no push-to-`main` trigger, so merging to main never redeploys the
      playground — only a release, a version tag, the weekly cron, or a manual dispatch does. Combined
      with the weekly cron having failed since January 2026, the deployed page can silently lag main
      indefinitely. Decide whether that is intended before adding a trigger; a 20 MB artifact rebuilt on
      every merge is not obviously desirable.
- [ ] **[P3]** `CONTRIBUTING.md`, an architecture overview, and package-level godoc — the deferred half
      of 7.5's P3 item. Nothing today describes the end-to-end datasource → raster → mask → texture →
      composite → serve flow in one place; the closest was `docs/PHASE-2-COMPLETE.md`, which 7.5 deleted
      because it was frozen at a Phase 2 snapshot.
- [ ] **[P3]** Purge the historical `wasm.wasm` blobs from git history. 7.5 untracked the file, which
      stops future growth, but seven revisions (~52 MB) remain reachable and every fresh clone still
      pays for them. Needs `git filter-repo` plus a force-push of `main` and all branches; it rewrites
      every SHA, which breaks the ~56 CHANGELOG commit links 7.5 just repaired and invalidates the open
      release-please PR and every existing clone. Do it deliberately, when no long-lived branch is
      outstanding — not opportunistically.

---

## Success Criteria

Each phase is considered complete when:

1. **Phase 1**: All tools installed, data imported, single test render succeeds
2. **Phase 2**: All layers render correctly for test tile, colors distinct
3. **Phase 3**: Watercolor effect applied, textures show properly, edges organic
4. **Phase 4**: Composite tiles seamless, Leaflet shows Hanover beautifully
5. **Phase 5**: Parallel rendering works, hosting deployed, updates automated
6. **Phase 6**: Global coverage achieved, performance acceptable, publicly accessible

## Notes

- Mark tasks complete only when fully verified
- Document issues and solutions as you encounter them
- Test incrementally - don't move ahead with broken foundations
- Keep the Stamen design philosophy in mind: artistic, organic, beautiful
- Maintain deterministic processing for seamless tile edges
- Balance authenticity with modern performance

## MBTiles Usage (Phase 5.4)

### Generate tiles directly to MBTiles

```bash
watercolormap generate --format=mbtiles \
  --output-file=hanover.mbtiles \
  --bbox=9.5,51.8,9.9,52.1 \
  --zoom-min=10 --zoom-max=15
```

A batch run writes one file, holding base tiles only. There is no `@2x` sidecar: `--hidpi` is
rejected for batch generation, and `serve --tiles-dir` renders `@2x` on demand instead. Note that
`serve --mbtiles` has no such generator — it ignores the `@2x` suffix and answers with the base
tile — so a deployment that needs true HiDPI has to serve the tile directory.

### Convert existing folder tiles to MBTiles

```bash
watercolormap convert \
  --input-dir=./tiles \
  --output=hanover.mbtiles \
  --name="WaterColorMap Hanover" \
  --bounds="9.5,51.8,9.9,52.1"
```

### Serve tiles from MBTiles

```bash
watercolormap serve --mbtiles=hanover.mbtiles --addr=127.0.0.1:8080
```

MBTiles format provides:

- Single file portability (no thousands of individual files)
- Tiles stored as raw PNG, per the MBTiles 1.3 spec (gzip applies to `pbf` vector tiles, not raster)
- Standard SQLite format compatible with most map tools
- TMS coordinate system (Y-axis inverted from XYZ)

## References

- [Stamen Watercolor Process](https://stamen.com/watercolor-process-3dd5135861fe/)
- [Stamen Watercolor Textures](https://stamen.com/watercolor-textures-15de97a4ad8b/)
- [Stamen Watercolor GitHub](https://github.com/stamen/watercolor)
- [OpenStreetMap Data](https://www.openstreetmap.org/)
- [Geofabrik Downloads](https://download.geofabrik.de/)
- [Natural Earth Data](https://www.naturalearthdata.com/)
- [MBTiles Specification](https://github.com/mapbox/mbtiles-spec)
