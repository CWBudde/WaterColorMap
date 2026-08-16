# AGENTS.md

Orientation for coding agents (and new humans) working in this repository.

WaterColorMap generates Stamen Watercolor–style raster map tiles from
OpenStreetMap data: Mapnik renders clean per-layer masks, the masks are distorted
organically (blur → deterministic Perlin noise → threshold → antialias),
watercolor textures are applied through them, and the layers are composited into
web-ready PNG tiles.

Module: `github.com/cwbudde/watercolormap` (Go 1.25, cgo — Mapnik 3.1+ required).

## Where things live

| Path                            | What it is                                                                     |
| ------------------------------- | ------------------------------------------------------------------------------ |
| `cmd/watercolormap`             | CLI entrypoint (thin `main`)                                                   |
| `internal/cmd`                  | Cobra commands: `generate`, `serve`, `convert`, `purge`, `textures`, `version` |
| `cmd/wasm`                      | Browser playground build (no Mapnik; delegates rendering to a backend)         |
| `internal/datasource`           | Overpass fetching, query builders, caching, retry                              |
| `internal/geojson`              | OSM → GeoJSON conversion                                                       |
| `internal/renderer`             | Mapnik wrapper + multi-pass layer rendering (cgo lives here)                   |
| `internal/mask`                 | Mask ops, blur (`blurkernel`, incl. AVX2 asm), noise, threshold, edges         |
| `internal/texture`              | Texture tiling and tinting                                                     |
| `internal/watercolor`           | Per-layer painting and watercolor styling                                      |
| `internal/composite`            | Layer compositing order and blending                                           |
| `internal/pipeline`             | End-to-end tile generation                                                     |
| `internal/server`               | Tile HTTP server, on-demand generation, admission control, caching             |
| `internal/lru`                  | Bounded LRU with TTL and statistics (leaf package)                             |
| `internal/worker`               | Batch worker pool and progress reporting                                       |
| `internal/checkpoint`           | Batch-run progress file (watermark over the tile enumeration, resume)          |
| `internal/tile`, `internal/geo` | Tile coords and Web-Mercator math (`geo` is a leaf package)                    |
| `internal/mbtiles`              | MBTiles reader/writer, plus the tile delete/vacuum path                        |
| `internal/tilestamp`            | Per-tile source-data stamps (SQLite sidecar); XYZ rows, **not** TMS            |
| `internal/tileformat`           | Tile image format identity (ext, MIME) and encoders (PNG, WebP)                |
| `internal/safe`                 | Panic recovery helpers for background work                                     |
| `assets/`                       | Mapnik layer styles (incl. the z0-5 Natural Earth set) and textures            |

## Common commands

```bash
just build          # build ./bin/watercolormap
just test           # full test suite (needs Mapnik)
just test-purego    # portable (no-assembly) build of the blur kernels
just lint           # golangci-lint
just check          # fmt + lint + test
just serve          # tile server + Leaflet demo, generates missing tiles
just bench-blur     # blur kernel benchmarks
just load-test      # tile-server benchmarks (hit path, dedup, admission)
just update-goldens # regenerate golden images (TestPipelineStages)
```

## Documentation map

Start with `PLAN.md` for **open** work — completed phases have been archived out
of it into the documents below, so anything still listed there is genuinely
outstanding.

**Planning and status**

- [PLAN.md](PLAN.md) — remaining phases and work items
- [README.md](README.md) — user-facing overview, install, quick start
- [SETUP.md](SETUP.md) — environment setup
- [docs/goal.md](docs/goal.md) — long-form project goal and background
- [ELEMENTS.md](ELEMENTS.md) — map elements and styling reference

**Design and reference**

- [docs/watercolor-mask-design.md](docs/watercolor-mask-design.md) — the
  cross-layer mask pipeline the renderer implements (Stamen-aligned). Read this
  before touching `internal/mask` or `internal/watercolor`.
- [docs/3.1-mask-processing-pipeline.md](docs/3.1-mask-processing-pipeline.md) —
  per-stage mask detail; siblings `3.2`–`3.6` cover noise consistency across
  tiles, texture application, edge darkening, layer-specific processing and
  visual quality testing.
- [docs/performance/blur-optimization.md](docs/performance/blur-optimization.md) —
  the blur rewrite: kernel selection, AVX2 path, RMSE budgets, and **why the
  default sigmas were rescaled**. Read before changing any blur sigma.
- [docs/performance/allocation-optimization.md](docs/performance/allocation-optimization.md) —
  the buffer-reuse work: the pooled-context idiom, the `*Into` conventions, and the
  four invariants that keep recycled buffers from leaking stale pixels. Read before
  adding a mask kernel or touching `maskScratch` / `ProcessorContext`.
- [docs/performance/simd-optimization.md](docs/performance/simd-optimization.md) —
  the AVX2 work: which profile entries were vectorised and which were rejected and
  why, the dispatch/fallback pattern every kernel must follow, why both new kernels
  hand their tail to Go, and the argument that makes them bit-identical. Read before
  adding assembly or touching `internal/mask/asm` or `internal/mask/blurkernel/asm`.
- [docs/performance/pixel-access-optimization.md](docs/performance/pixel-access-optimization.md) —
  the row-slice loop convention every pixel kernel now follows, and the two clipping
  behaviours (`writeRect`, `grayRow`) that replaced what `SetGray` and `GrayAt` used to
  do implicitly. Read before writing a loop over `Pix`, and note that the `pixelaccess_test.go`
  reference implementations are frozen copies of the old loops on purpose.
- [docs/performance/parallel-layers.md](docs/performance/parallel-layers.md) — which
  layers are independent, why the paint concurrency defaults to 1, and what makes the
  parallel path deterministic. Read before adding shared state to a paint job, before
  replacing a `sync.Pool` in `internal/watercolor` with a cached instance, and before
  raising `--paint-workers` anywhere.
- [docs/performance/texture-optimization.md](docs/performance/texture-optimization.md) —
  the texture tiling rewrite: why tiling is a row `copy` rather than a per-texel sample,
  why textures are normalised to `*image.NRGBA` at load time, and **why no texture atlas
  was built**. Read before touching `internal/texture` or adding a texture loader.
- [docs/watercolor-tuning.md](docs/watercolor-tuning.md) — the `watercolor:` config
  block: every knob, which of the five pipeline stages it belongs to, and the
  current defaults. Supersedes the stale parameter list in `3.6`.
- [docs/seam-inspection.md](docs/seam-inspection.md) — manual Leaflet checklist for
  tile seams, and what the automated `TestCompositedTileSeams` does and does not
  cover. Use it after touching blur, noise, texture or metatile padding.
- [docs/zoom-levels.md](docs/zoom-levels.md) — what each zoom fetches, which
  dataset answers it, and the zoom-conditioned behaviour that does not live near
  the rule tables (empty-response validation, blur rescaling, band and retry
  thresholds, Mapnik scale tiers). Read before changing any zoom window.
- [docs/MULTI-SERVER-OVERPASS.md](docs/MULTI-SERVER-OVERPASS.md) — multi-endpoint
  Overpass configuration.
- [docs/tile-server-architecture.md](docs/tile-server-architecture.md) — what
  `serve` is: the request path, why admission sits below the cache check, the
  four caching layers and why tile bytes are not one of them, the
  `Cache-Control`/`ETag` policy and its conflict with `purge`, and the load-test
  method. Read before changing the cache-hit path, admission control or the
  per-tile lock.
- [docs/tile-stamps-and-purge.md](docs/tile-stamps-and-purge.md) — the per-tile
  source-data stamp and the `purge` command: what a stamp records, where it
  lives, and why `generate` and `purge` resolve uncertainty in opposite
  directions. Read before changing `internal/tilestamp` or the `--stale-*`
  flags.
- [docs/data-scaling-strategy.md](docs/data-scaling-strategy.md) — how the data
  side scales from one city to a country and what it would cost to go global:
  the regional-Overpass approach, why vector tile input was rejected, measured
  per-tile storage and throughput, and the update pipeline. Read before planning
  any bulk generation run.

**History (completed work, kept for the rationale)**

- [docs/history/phases-1-2-foundation.md](docs/history/phases-1-2-foundation.md) —
  data prep, tooling, and base-layer rendering, including the layer colour map and
  the layer-isolation / edge-buffer fixes everything else depends on.
- [docs/history/phase-7-hardening.md](docs/history/phase-7-hardening.md) —
  the 2026-08 quality review: build repair, tile-server hardening, and code-quality
  work. Several entries record _why_ something is the way it is; check here before
  "simplifying" server admission control, the per-tile lock map, the Overpass query
  rules, or `worker/pool.go` cancellation.

**WASM playground**

- [docs/wasm-playground.md](docs/wasm-playground.md) — the single reference for the
  browser playground. It replaced three overlapping status documents under 7.5;
  `docs/wasm-playground/README.md` is now just a pointer to it.

## Conventions worth knowing

- **Determinism matters.** Noise, texture offsets and mask processing are anchored
  to world coordinates so adjacent tiles have no visible seam. Anything that makes
  output depend on tile identity rather than world position is a bug.
- **Rendering runs on a padded metatile** and is cropped afterwards, so blur and
  edge effects do not clip at tile borders.
- **`internal/geo` imports nothing from `internal/`** — keep it that way, it exists
  to prevent an import cycle.
- **Golden tests** guard the Overpass query builders and the pipeline stages.
  Regenerate deliberately (`just update-goldens`) and audit the diff.
- Keep completed plan sections out of `PLAN.md`: archive the rationale under
  `docs/` and leave a one-paragraph summary plus a link behind.
