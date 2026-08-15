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

The five items above were inherited verbatim from `docs/goal.md`, a research brief written before
the implementation existed and assuming PostGIS. They could not be closed as written: the code went
Overpass-only, and `internal/cmd/datasource_config_test.go:83` actively asserts that `"postgis"` is
rejected. They are answered instead against what the code is, staged honestly — concrete for
Niedersachsen → Germany, with global coverage costed rather than planned.
→ [docs/data-scaling-strategy.md](docs/data-scaling-strategy.md)

Three things the work measured that change decisions elsewhere in this plan:

- **The Overpass fetch is ~71% of per-tile wall clock** (2.24 s of 3.16 s, z13, local instance,
  6 runs, cross-checked against a pass-through proxy). 5.11's optimisation roadmap targets the
  ~18% render slice. That does not make 5.11 wrong, but the source side is where a bulk run's
  time actually goes — hence the response cache below.
- **WebP q80 is a 9.2× reduction** — but see 5.1a: the encoder that shipped is _lossless_
  (pure Go, no cgo) and measures **1.21×** on the same tiles. The lossy lever remains open.
  Original measurement: (124 KB → 13.4 KB mean over 20 tiles, z5–z17; damage ~4/255
  mean per channel, and the tiles are label-free by policy so there is no text to smear).
  This is a larger storage lever than any choice of zoom ceiling.
- **There are no cheap empty tiles in PNG, confirmed outside the city**: a Sahara tile with
  _zero_ OSM features still costs 108 KB, and a mid-Pacific ocean tile 127 KB — above the Hanover
  mean. The texture and noise fill every pixel. In WebP the empty tile drops to 6 KB.

Landed alongside the document, because both are load-bearing for the workflow it recommends:

- **MBTiles resume.** `--format=mbtiles` re-rendered everything on every run: the skip-existing
  check stat'd the _folder_ path even when output went through a `TileWriter`. Now
  `pipeline.TileProber` (an optional interface, so a writer that cannot answer simply omits it)
  plus `mbtiles.Writer.HasTile`. Every failure mode degrades to _render_, never to _skip_ — a
  false skip leaves a permanent hole, a false render costs seconds.
- **On-disk Overpass response cache**, `internal/datasource/cache.go` — a caching
  `http.RoundTripper` under the go-overpass client, **default off**. It sits there rather than
  wrapping the datasource because `NewFetchQueue` takes `*OverpassDataSource` concretely and
  `ondemand_tiles.go` type-asserts on it, so a decorator would silently strip `serve` of its
  fetch queue. The key is a hash of endpoint + query text and contains **no tile identity**,
  which is what keeps it a pure performance change under the world-position rule. It caches the
  verbatim upstream bytes, so nothing downstream can observe it. One measured consequence:
  `@2x` padding is computed in world pixels, so the 512px query is byte-identical to the 256px
  one and an on-demand `@2x` render in `serve` reuses the base tile's cached response — while
  that entry is still live — instead of refetching the metatile. (The `--hidpi` batch pass this
  originally described is gone; see the on-demand item in 5.1a.)
  Its `httptest` tests also discharge 7.6's "mocked-HTTP Overpass tests" item.

**A finding worth acting on, since acted on**: the public `overpass-api.de` `406 Not Acceptable`
long attributed to rate limiting was not rate limiting. The server rejects Go's default
`User-Agent: Go-http-client/1.1`; the identical query from `curl` returns 200 in ~0.5 s.
`go-overpass`'s `httpPost` never sets a UA. The fix did not need the dependency: a
`RoundTripper` in `internal/datasource/useragent.go` sets the header below the client, so the
public API is now a usable fallback for coverage gaps. See 5.1a's first item.

### 5.1a Follow-ups surfaced by the scaling analysis

Filed rather than smuggled into the 5.1 work, in rough value order.

- [x] **[P1]** Set a `User-Agent` on the Overpass client (see above). Done in
      `internal/datasource/useragent.go`, as a `RoundTripper` beside the existing limit and cache
      transports rather than as a patch to `go-overpass`: the request is built inside the client
      where the call site cannot reach it, and this layer also covers the per-server clients
      `MultiOverpassDataSource` builds, with no dependency release needed. Overridable via
      `overpass.user_agent`, globally or per server. The public API is now a usable fallback,
      which is what the routing recommendations assumed.
- [x] **[P1]** WebP output end to end — `--image-format webp` on `generate` and `serve`, with
      `internal/tileformat` owning format identity and encoding, and PNG kept as the default
      everywhere.

      **The 9.2× did not survive contact with the encoder.** That figure is _lossy_ q80; the
      encoder chosen here is pure-Go `nativewebp`, which is VP8L, i.e. lossless. Re-measured over
      the same 689 tiles: **1.21×** (122,326 B → 101,181 B), consistent z5–z17, never larger on
      any tile, and ~4× slower to encode. The gap is the same fact as "no cheap empty tiles" —
      the texture and noise fill every pixel, so there is nothing for a lossless codec to
      collapse. `docs/data-scaling-strategy.md` § 3 is corrected in place rather than left to
      mislead.

      Lossless bought the absence of cgo: `GOOS=js` and the release matrix build with no build
      tags. **The lossy lever is still open and still worth ~9×** — `Encoder` is an interface, so
      it is one more implementation plus a decision about round-trip damage.

      Two guards came with it, both of the "false skip leaves a permanent hole" family: `mbtiles`
      refuses to reopen a non-empty tileset under a different format (it rewrites metadata on
      open, and `HasTile` is format-blind), and `serve` 404s the extension it is not configured
      for rather than serving one format's bytes under the other's name.

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
      `out geom` returns unclipped geometry, so a motorway crossing a block is transferred once
      per tile; one query per block transfers it once. At the default 4×4, Germany's 237,424 z14
      queries become ~15k.

      **Two corrections to what this item said.** First, "must stop at z15" does not apply:
      `buildTileQuery` picks its rules from the zoom alone, so every tile in a *same-zoom* band
      emits identical query text apart from the bbox. The `landuse` → `building` switch at z16
      invalidates reusing a **parent's** data for its children across zooms, which is a different
      technique and not this one. (The line reference was also stale — those rules are at
      `overpass.go:486-505`.) Second, 8×8 is not a safe band size: one padded z13 tile measured
      ~3 MB, so a 64-tile block lands past the 64 MiB response cap. 4×4 is the default, and the
      real guard is adaptive rather than a zoom ceiling — any band failure splits into quadrants
      and retries, bottoming out at ordinary per-tile fetches, so a failing tile still fails as
      itself with the error it always had.

      A band's data is **sliced to each tile's own fetch bounds** before rendering. That is not an
      optimisation: the renderer skips a zero-feature layer entirely, and handing a tile its
      neighbours' features would flip absent layers into present-but-blank ones. The emptiness
      check stays per tile too — an empty slice at z8–13 falls back to a real per-tile fetch
      rather than approximating the policy. `TestBandFetchRendersIdenticalTiles` pins the result:
      byte-identical output, on data that genuinely differs (9 features in the band, 6 in the
      slice).

      One hazard found and closed: multi-server routing matches on *intersection*, which at band
      scale could answer sixteen tiles from a server holding data for one corner. Band routing
      requires **containment** and splits otherwise.

- [x] **[P3]** Sort features by OSM ID in `ExtractFeaturesFromOverpassResult`. Both element
      loops now walk `slices.Sorted(maps.Keys(…))` rather than the raw `map[int64]*…`, so **the
      same tile renders byte-identically twice**. What the old behaviour cost: mean deviation
      0.014/255, max 36, on 0.01% of channels of a z12 Hanover tile — a handful of pixels where
      draw order flipped on an antialiased edge. Small in the image, decisive for testing, since
      it put a tolerance floor under every PNG-level regression test.

      Ascending OSM ID is arbitrary _as a painting order_; only its stability matters, which is
      why `TestExtractFeatureOrderIsByOSMID` names the choice rather than leaving a later
      refactor to swap it silently. The response cache was never implicated — two cached runs
      differed by the same 0.016 as two uncached ones — and it goes on storing raw JSON rather
      than the extracted collection, now for its own reasons: one serialization format to keep
      in sync with the decoder, and no stored collection that could outlive a change to the
      extraction rules.

      The synthetic pipeline goldens are unaffected (`syntheticDataSource` involves no map
      iteration). The two Hannover cases move and need `just update-goldens-hannover` from a
      machine with network.
- [ ] **[P3]** A tile purge command, and a source-data version stamp on rendered tiles. Without
      one, skip-existing treats any existing PNG as valid forever and mtime is the only staleness
      proxy — which is what keeps 5.7 open.
- [ ] **[P3]** Streaming tile enumeration and a checkpoint file. `worker/pool.go` buffers the whole
      task list (`make(chan Task, len(tasks))`) — 317,618 structs for Germany z0–14.

### 5.2 Parallel Tile Rendering

- [x] Implement worker pool for tile generation (`internal/worker/pool.go`)
- [x] Add goroutine-based parallel processing (configurable worker count, defaults to NumCPU)
- [x] Implement database connection pooling (N/A - Overpass API, generators are per-worker)
- [x] Add progress tracking and logging (`internal/worker/progress.go`)
- [x] Test parallel rendering performance (unit tests in `internal/worker/pool_test.go`)
- [x] Optimize worker count (defaults to `runtime.NumCPU()`)
- [x] Add batch CLI command (`--bbox`, `--zoom-min`, `--zoom-max`, `--workers`, `--progress`)

### 5.3 Multi-Zoom Generation

Natural Earth still exists only in this line and in `docs/goal.md` — there is no code anywhere.
5.1 recommends filling z0–5 exactly the way the ocean was filled in 4.10: Natural Earth ships
shapefiles, Mapnik's `shape` plugin is installed, and `shapeindex` plus zoom-based dataset
selection are already implemented and proven (`internal/renderer/ocean.go`, `Justfile:204-243`).
That is also the answer to the question "vector tile input" was being asked.
→ [docs/data-scaling-strategy.md](docs/data-scaling-strategy.md)

- [ ] Define zoom range strategy (0-5: Natural Earth, 6-9: country, 10+: OSM)
- [ ] Implement zoom-specific data filtering
- [ ] Create generalized rendering for low zooms
- [ ] Test rendering at each zoom range
- [ ] Document zoom level characteristics

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

### 5.6 On-the-Fly Rendering Service

- [ ] Design Go tile server architecture
- [ ] Implement tile caching strategy
- [ ] Add cache hit/miss handling
- [ ] Implement LRU cache or Redis
- [ ] Test server under load
- [ ] Optimize for cache performance

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
cadence follows from measured throughput. The _capability_ is what stays open: there is no purge
command and no source-data version stamp on a rendered tile.
→ [docs/data-scaling-strategy.md](docs/data-scaling-strategy.md)

- [ ] Design periodic data refresh strategy
- [ ] Implement OSM diff application (optional)
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

**Current Performance** (256x256 tile, 5 layers):

- Time per tile: ~86ms
- Memory per tile: ~29MB
- Allocations: 1.3M

**Key Findings** (historical, from the stale profile):

- Gaussian blur: 39.6% of CPU time
- Image buffer allocations: 37.8% of memory (64-bit RGBA overhead)
- Pixel access overhead: 17.7% of memory (color.NRGBA allocations per At() call)
- Perlin noise: ✅ Already optimized (40ms saved per tile)

**Current profile** (after 5.11.2, `BenchmarkFullPipeline`, top flat entries):

- Distance transform: ~25% (`distanceTransform1DWithBuffers`, `distanceTransformRows/Columns`)
- Per-pixel accessors: ~25% (`NRGBAAt`, `SetNRGBA`, `GrayAt`, `SetGray`, `PixOffset`, `Point.In`) —
  this is what 5.11.4 is about, and it is now the largest single theme
- Noise and colour conversion: ~13% (`applyNoise`, `hslToRGB`, `rgbToHSL`)
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

#### 5.11.3 Memory Allocation Optimization 🟡 HIGH PRIORITY

**Target**: Reduce per-tile memory from 29MB → <20MB (Expected gain: 10-15% speedup via GC reduction)

- [ ] Implement buffer pool for common image sizes (256x256, 512x512)
- [ ] Add buffer reuse in blur operations (avoid creating new buffers per call)
- [ ] Profile memory allocations after buffer pooling
- [ ] Measure GC impact reduction
- [ ] Document buffer pool usage patterns

**Context**: gift library creates 64-bit RGBA buffers (745MB allocated per benchmark), 4x overhead vs 8-bit buffers. Pooling and reuse can dramatically reduce allocation pressure.

#### 5.11.4 Pixel Access Optimization 🟡 HIGH PRIORITY

**Target**: Eliminate 349MB of temporary color allocations (Expected gain: 5-10% speedup)

- [ ] Identify all hot paths using `image.At()` method
- [ ] Replace with direct Pix slice access where possible
- [ ] Implement batch pixel operations to amortize allocations
- [ ] Profile allocation reduction
- [ ] Verify correctness with visual regression tests

**Context**: Every `At()` call allocates a new color.NRGBA struct. Direct slice access via `img.Pix` is allocation-free.

#### 5.11.5 Parallel Layer Processing 🟢 MEDIUM PRIORITY

**Target**: Utilize multi-core CPUs (Expected gain: 30-50% on multi-core systems)

- [ ] Identify independent layers that can be processed in parallel
- [ ] Implement goroutine-based parallel layer painting
- [ ] Add synchronization for shared resources (noise texture, textures)
- [ ] Benchmark single-core vs multi-core performance
- [ ] Test correctness with parallel processing enabled
- [ ] Document parallelization strategy and trade-offs

**Context**: Water, land, parks, civic layers can be painted independently. Roads/highways depend on land mask but could still be parallelized after land completes.

#### 5.11.6 Texture Processing Optimization 🟢 LOW PRIORITY

**Target**: Reduce texture tiling overhead (Expected gain: 3-5% speedup)

- [ ] Implement texture atlas (single large texture, UV mapping)
- [ ] Add lazy texture tiling (on-demand vs upfront)
- [ ] Profile texture operation performance
- [ ] Measure memory reduction from atlas approach

**Context**: TileTexture allocates 175MB per benchmark. Texture atlasing could reduce allocations and improve cache locality.

#### 5.11.7 SIMD Optimization 🟢 FUTURE ENHANCEMENT

**Target**: Accelerate pixel-level operations (Expected gain: 10-20% for specific operations)

- [ ] Research Go SIMD libraries (avo, gonum)
- [ ] Identify SIMD-friendly operations (pixel blending, noise application)
- [ ] Implement SIMD versions of hot functions
- [ ] Benchmark SIMD vs scalar performance
- [ ] Ensure cross-platform compatibility

**Context**: Bulk pixel operations (noise blending, edge darkening) could benefit from SIMD. Lower priority due to implementation complexity.

#### 5.11.8 Performance Monitoring & Regression Testing

- [ ] Add continuous benchmark tracking to CI
- [ ] Set performance budgets (max time/memory per tile)
- [ ] Create performance regression tests
- [ ] Document performance characteristics per zoom level
- [ ] Add performance dashboard/reporting

**Combined Expected Speedup**: 50-70% faster (86ms → 40-50ms per tile)

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
