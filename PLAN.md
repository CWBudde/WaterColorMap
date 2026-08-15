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
>   — 4.10 (ocean/coastline) is still open and stays below
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
  needs a wider bbox at those zooms, not a deeper zoom range — and, per 4.10 below,
  correct low-zoom tiles need ocean rendering first.

### 4.10 Ocean/Coastline Rendering 🔴 CRITICAL - BLOCKER FOR LOW ZOOM TILES

**Status**: 🔴 BROKEN - Ocean tiles render as land (tan background) instead of water (blue)

**Problem Summary**:

OpenStreetMap's raw data does **not include ocean polygons**. The ocean is represented as the "absence of land" rather than explicit water features:

- `natural=coastline` are **lines only** (boundaries, not filled areas)
- `natural=water` covers lakes, ponds, bays - **NOT the open ocean**
- `place=sea` are **point labels** for naming seas - **NOT area polygons**
- Ocean is implicit (everything not explicitly tagged as land)

**Current (Broken) Behavior**:

For pure ocean tiles (e.g., z9_x266_y164.png):

1. Query Overpass API for features within tile bounds
2. Overpass returns **NOTHING** (ocean is not mapped)
3. `land.xml` fills tile with TAN background (#C4A574)
4. `water.xml` has no data to render (no blue)
5. **Result**: Ocean appears as LAND (tan) ❌

For coastal tiles with islands:

1. Islands may have `natural=water` polygons (lakes)
2. Lakes render BLUE
3. Surrounding ocean has no data → stays TAN
4. **Result**: Islands appear as blue (water) while ocean appears as tan (land) - **completely backwards** ❌

**Impact**:

- **All ocean tiles at z≤10 are broken** (render as land instead of water)
- **Coastal tiles are inverted** (islands appear as water, ocean as land)
- Completely blocks proper rendering of any region with coastlines or ocean
- Current workaround (fetching `natural=sea` and `place=sea`) does NOT work - these tags don't represent area polygons

**Root Cause**:

The rendering pipeline assumes all features (water, land, parks, etc.) are explicitly present as polygons in OSM data. This works for inland features but fails for oceans because:

1. OSM uses an **implicit ocean model** (ocean = not land)
2. Coastlines are directional lines (water is to the right)
3. Converting coastlines to ocean polygons requires complex processing:
   - Assembling multiple coastline ways into closed rings
   - Determining which side is land vs. water
   - Handling tile boundaries correctly
   - Dealing with islands and multipolygon coastlines

**Proposed Solutions**:

#### Option 1: Use OSM Processed Water Polygons (CHOSEN — see decision below)

**Pros**: Production-ready, used by professional renderers, comprehensive coverage

**Cons**: External dependency, ~500MB-1GB download

**Implementation**:

- [ ] Download processed water polygons from https://osmdata.openstreetmap.de/data/water-polygons.html
- [ ] Add new data source interface for shapefile/GeoPackage reading (alongside Overpass)
- [ ] Integrate water polygons into the data pipeline
- [ ] Query both Overpass (for detailed features) and water polygons (for ocean) per tile
- [ ] Merge results before rendering
- [ ] Test ocean tiles at z5-z10
- [ ] Test coastal tiles with islands
- [ ] Update documentation with water polygon setup instructions

**Files**:

- Water-polygons-split-4326.zip (~500MB) - split into smaller files for tile-based access
- Simplified-water-polygons-split-4326.zip (~50MB) - simplified for low zoom levels

**Data Source Priority**:

1. Use simplified polygons for z ≤ 9
2. Use full polygons for z ≥ 10
3. Use Overpass for detailed inland water features at all zooms

#### Option 2: Detect Ocean Tiles and Synthesize Water Polygons (QUICK FIX)

**Pros**: No external dependencies, works with current architecture

**Cons**: Heuristic-based, may miss edge cases, doesn't solve coastal complexity

**Implementation**:

- [ ] Add ocean tile detection logic in datasource layer
- [ ] If tile query returns zero land features AND tile bounds intersect known ocean areas:
  - Synthesize a water polygon covering the entire tile bounds
  - Add to water feature collection before returning
- [ ] Implement simple coastline detection:
  - If `natural=coastline` ways are present, mark as coastal tile
  - For coastal tiles, don't synthesize full-tile ocean (too complex)
- [ ] Test with pure ocean tiles (North Sea, Atlantic)
- [ ] Test with coastal tiles (verify no false positives)
- [ ] Document limitations (coastal tiles may still have issues)

**Limitations**:

- Doesn't handle complex coastlines (bays, islands, estuaries)
- Requires hardcoding ocean bounding boxes
- Won't work for all edge cases

#### Option 3: Implement Coastline Processing (ADVANCED)

**Pros**: Complete solution using OSM's raw coastline data, no external files

**Cons**: Extremely complex, error-prone, reinvents solved problems

**Implementation**: NOT RECOMMENDED - this is what osmcoastline tool does, and it's complex enough to be its own project.

**Decision (resolved): Option 1 — OSM processed water polygons.**

We take the processed water polygons from <https://osmdata.openstreetmap.de/data/water-polygons.html> as the ocean source. Option 2 is explicitly rejected: hardcoded ocean bounding boxes plus a "zero features means ocean" heuristic would ship a wrong-by-construction rule that we would then have to unpick, and it does nothing for the coastal tiles that are the actual visual problem. Option 3 (reimplementing `osmcoastline`) stays out of scope.

**Why this is a project, not a patch** — three things in the current code have to move before water polygons can render at all:

1. `validateFeatureResponse` (`internal/datasource/overpass.go:627`) currently _errors_ on empty z8–13 tiles: `if zoom >= 8 && zoom <= 13 && totalFeatures == 0 { return fmt.Errorf("%w: zoom %d tile has no features …", ErrEmptyOverpassResponse, zoom) }`. A genuine open-ocean tile in that zoom band is exactly the "zero features" case, so it fails before rendering. Emptiness has to become "empty _and_ not covered by a water polygon" rather than an unconditional error.
2. `renderLayer` (`internal/renderer/multipass.go:167`) skips any layer with zero features — `if len(features) == 0 { result.OutputPath = ""; return result }` (`:191`). Ocean coverage therefore cannot arrive as "no Overpass features + a blue background"; the water polygons must be injected as real features (or as a dedicated ocean layer with its own render path) before this check.
3. **No new Go geometry dependency is needed.** Mapnik reads ESRI shapefiles natively through its `shape` input plugin (present in the local install, `/usr/lib/mapnik/*/input/shape.input`), so the downloaded `.shp` can be pointed at directly from a Mapnik datasource instead of being parsed in Go and re-emitted as GeoJSON. The Go-side work is download/cache management and per-tile layer wiring, not geometry.

**Testing Requirements**:

- [ ] Pure ocean tile rendering (z9_x266_y164 North Sea area)
- [ ] Coastal tile with mainland and ocean (Hamburg area)
- [ ] Island tile (British Isles, Mediterranean islands)
- [ ] Bay/estuary tile (complex coastline)
- [ ] Verify color inversion is fixed (ocean=blue, land=tan)
- [ ] Check tile seams at coastlines
- [ ] Test across zoom levels z5-z12

**Related Code**:

(Pointers below verified against the current tree — the previously listed `buildWaterQuery()` does not exist.)

- `internal/datasource/overpass.go` — `buildTileQuery()` (`:229`) builds the whole tile query from the per-layer rule tables; the water layer is registered at `:315` and its tag rules live in `var waterRules` (`:332`), rendered into Overpass QL lines by `renderRules()` (`:295`). This is where an ocean/water-polygon source would have to be joined in, or deliberately bypassed.
- `internal/datasource/overpass.go` — `validateFeatureResponse()` (`:627`), the empty-tile error described above.
- `internal/datasource/overpass_extract.go` — `isWater()` (`:317`), used at `:101`, `:125` and `:292` to classify extracted tags into the water layer.
- `internal/renderer/multipass.go` — `renderLayer()` (`:167`), zero-feature skip at `:191`.
- `assets/styles/layers/water.xml` - water rendering style
- `assets/styles/layers/land.xml` - background color definition

**References**:

- [OSM Water Polygons](https://osmdata.openstreetmap.de/data/water-polygons.html)
- [OSM Coastline Processing](https://wiki.openstreetmap.org/wiki/Coastline)
- [osmcoastline tool](https://osmcode.org/osmcoastline/)

## Phase 5: Scaling and Modern Improvements

### 5.1 Data Scaling Strategy

- [ ] Plan regional database approach
- [ ] Evaluate vector tile input option
- [ ] Document data management for large regions
- [ ] Plan storage requirements
- [ ] Design data update pipeline

### 5.2 Parallel Tile Rendering

- [x] Implement worker pool for tile generation (`internal/worker/pool.go`)
- [x] Add goroutine-based parallel processing (configurable worker count, defaults to NumCPU)
- [x] Implement database connection pooling (N/A - Overpass API, generators are per-worker)
- [x] Add progress tracking and logging (`internal/worker/progress.go`)
- [x] Test parallel rendering performance (unit tests in `internal/worker/pool_test.go`)
- [x] Optimize worker count (defaults to `runtime.NumCPU()`)
- [x] Add batch CLI command (`--bbox`, `--zoom-min`, `--zoom-max`, `--workers`, `--progress`)

### 5.3 Multi-Zoom Generation

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

Everything `[P0]` is now closed; 7.4 (CI/build), 7.5 (docs/hygiene) and 7.6 (testing) remain.

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

### 7.5 Documentation & repo hygiene (P1/P2)

- [ ] **[P1]** Fix the README quick-start commands — `--tile z13_x4297_y2754` (no such flag; use
      `--zoom N --x N --y N`) at `README.md:39,56,66`, and `--min-zoom/--max-zoom/--bounds` →
      `--zoom-min/--zoom-max/--bbox` at `README.md:78`. The first command every user runs currently errors.
- [ ] **[P1]** Remove the committed 20 MB `docs/wasm-playground/wasm.wasm` (and `wasm_exec.js`) from
      git — pure build artifacts, ~95% of repo bloat; build them in CI for Pages and gitignore `*.wasm`.
- [ ] **[P1]** Rewrite `config.example.yaml` to match the code — the `tile:`, `rendering:`,
      `test-area:` and `overpass.timeout/rate-limit/retry` blocks are read by nothing (silently ignored),
      texture filenames (`park.png`/`forest.png`) don't exist, and `protomaps/openmaptiles` data-sources
      aren't implemented.
- [ ] **[P2]** Resolve the MeKo-Tech vs MeKo-Christian identity split (module/README say `MeKo-Tech`;
      all CHANGELOG links and both demo links say `MeKo-Christian` → likely 404s). Pick one, fix links.
- [ ] **[P2]** Prune/consolidate `docs/` status reports (`PHASE-2-COMPLETE.md` — now superseded by
      `docs/history/phases-1-2-foundation.md`, three overlapping `WASM-PLAYGROUND-*.md`, reconcile
      `PLAN.md` vs `docs/goal.md`); fix the `--port` (→ `--addr`) and MBTiles usage examples in this
      file. The stale Phase 3 "IN PROGRESS" marker is fixed (Phase 3 is complete and archived to
      `docs/watercolor-mask-design.md`); the 4.10 "BLOCKER" marker still needs verifying against
      actual state.
- [ ] **[P3]** Improve commit hygiene (the CHANGELOG inherits "more progress"/"recent work"/7× identical
      "playground issues fixed"); add `CONTRIBUTING.md`, package-level godoc, and an architecture overview.

### 7.6 Testing improvements (P2/P3)

- [ ] **[P2]** Separate pure-Go logic from CGO via build tags so genuinely good tests
      (`parseTilePath`, synthetic pipeline path, raster, mask/composite) run in a Mapnik-less env / CI.
- [ ] **[P2]** Add `internal/raster` tests (353 LOC, pure Go, zero tests) and mocked-HTTP
      (`httptest`) Overpass tests for caching/retry/error paths (current datasource "unit" tests are
      tautological — only assert non-nil constructors).
- [ ] **[P2]** Delete the ~7.2 MB orphaned goldens under `testdata/golden/watercolor-stages*/`
      (referenced by no test) and fix the `update-goldens` Justfile recipe (matches no test → no-op).
- [ ] **[P3]** Replace timing-based assertions (`worker/pool_test.go:90,174`) with deterministic
      synchronization; switch file-producing tests to `t.TempDir()` (currently write shared
      `testdata/output/...`); adopt `t.Parallel()` where safe (0 uses today).

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

For HiDPI tiles, two separate files are created:

- `hanover.mbtiles` (base 256px tiles)
- `hanover@2x.mbtiles` (512px tiles)

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
watercolormap serve --mbtiles=hanover.mbtiles --port=8080
```

MBTiles format provides:

- Single file portability (no thousands of individual files)
- Efficient storage with gzip compression
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
