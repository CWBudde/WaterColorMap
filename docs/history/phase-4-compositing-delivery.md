# Phase 4: Compositing and Tile Delivery (4.1–4.9)

Archived from `PLAN.md`. These sections are complete; the detail is kept here
because it records _why_ several non-obvious decisions were made. The one part
of Phase 4 that is still open — **4.10 Ocean/Coastline Rendering** — stays in
`PLAN.md`.

Two findings below are still live and are also summarised in `PLAN.md`:
the unresolved `EdgeAlignment` failure (4.4) and the low-zoom viewport
coverage limit of the Hanover set (4.8).

## 4.1 Layer Compositing

- [x] Implement layer compositing engine
- [x] Define correct draw order (water, land, parks, civic, roads)
- [x] Handle layer transparency correctly
- [x] Implement pixel-perfect layer alignment
- [x] Test compositing on single tile
- [x] Verify layer overlap handling

## 4.2 Road Layer Fidelity (per Stamen)

- [x] Make road stroke widths zoom-aware in Mapnik (scale_denominator or per-zoom multiplier) so visual thickness stays consistent on 256/512 px tiles
- [x] Keep road watercolor treatment readable: thinner blur/edge params for linear features, reddish/orange tint that survives compositing
- [x] Add regression test comparing rendered road width/alpha at two zooms to prove scaling works

## 4.3 Labels Policy (Stamen default: none)

- [x] Ship label-free tiles (matches Stamen aesthetic)
- [x] Keep Mapnik styles label-free (current state: no labels)

## 4.4 Seam & Alignment Verification

- [x] Use metatile padding + crop during generation to avoid blur/edge artifacts at tile borders
- [x] Add an integration test rendering adjacent tiles and checking border deltas stay within tolerance (`TestCompositedTileSeams` in `internal/pipeline/seam_test.go` renders a 2×2 block at z13 through `Generator.Generate` and judges each border against an in-tile control step, since composited tiles are grainy everywhere and a fixed per-pixel threshold would measure grain rather than seams)
- [x] Document a quick manual seam inspection checklist (Leaflet) — `docs/seam-inspection.md`, linked from `AGENTS.md`

  **Open, found while running the integration suite against a local Overpass:** the older
  `TestRenderAdjacentTilesWithRealData/EdgeAlignment` (`internal/renderer/multipass_test.go`) fails
  ~12 times. It is the naive version of the check above — a fixed ±60 per-pixel threshold between the
  last pixel column of one tile and the first column of its neighbour. Those are adjacent but
  _different_ pixels, so an antialiased edge crossing the seam legitimately differs; observed gaps
  reach ~120. It fails identically on `837537a`, so it predates the hi-DPI work and is not caused by
  it. Either the tolerance is wrong for raw layer masks or there is a real half-pixel offset — the
  `TestCompositedTileSeams` control-step approach is the pattern to fold it into. Unresolved.

## 4.5 Output Formats & Hi-DPI

- [x] Add `--hidpi`/config toggle to emit 512px `@2x` tiles alongside 256px output
- [x] Ensure watercolor offsets/noise/texture stay globally aligned between 256px and 512px outputs (same world anchoring) — `internal/watercolor/scale.go` (`ScaleForTileSize`, `ApplyScale`, `DefaultParamsForTileSize`), world-space `RequiredPaddingPx`, and `texture.TileTextureScaled`. The offsets were always right; every _length_ they were measured against was a fixed device-px constant, so @2x grain, texture and blur were half the ground size of @1x
- [x] Define the on-disk naming/layout for retina (`@2x`) and document the matching Leaflet config
- [x] Use `png.Encoder` with configurable compression level; keep defaults fast and add a reproducible “best compression” mode
- [x] **Scale Mapnik strokes for @2x** (deferred follow-up found while doing the item above). `stroke-width` in `assets/styles/layers/*.xml` is a fixed device-pixel value (e.g. `highways.xml` motorway `stroke-width="14.0"`, `rivers.xml` `stroke-width="2"`), so a 512 px `@2x` tile drew roads at the same pixel width as the 256 px tile — half as wide in ground terms. Fixed by passing `mapnik.RenderOpts{ScaleFactor: s}` rather than duplicating the stylesheets: new `MapnikRenderer.SetScaleFactor` (`internal/renderer/mapnik.go`), set from `NewMultiPassRenderer` with `watercolor.ScaleForTileSize(tileSize)`. The scale must be threaded in, not derived inside the renderer, because `MapnikRenderer.tileSize` is the padded _metatile_ size (`multipass.go` passes `tileSize + 2*padPx`).

  The stroke width turned out to be the smaller half of the defect. Mapnik multiplies the scale denominator by the scale factor before evaluating `Min`/`MaxScaleDenominator`, and `roads.xml`, `highways.xml` and `railroads.xml` filter on those heavily (tiers at 3000/25000/50000/75000/150000). An `@2x` tile covers the same extent in twice the pixels, so its denominator is _half_ its `@1x` twin's — at z13 that lands across the 75000/50000 boundary, meaning `@2x` could draw road classes the `@1x` tile omitted entirely. One `ScaleFactor` value fixes both; this is exactly Mapnik's retina convention (2× image size + `scale_factor 2`).

  256 px output is unchanged: `ScaleForTileSize(256)` is exactly `1.0`, and go-mapnik normalises both `0` and `1.0` to the same `scale_factor` (`mapnik.go:342-343`, `:350`, `:381`, `:403`), so the 1× path issues a bit-identical Mapnik call. Covered by `TestRoadStrokeScalesWithTileSize` (`internal/renderer/roads_zoom_test.go`), integration-gated like every other Mapnik test in that package.

## 4.6 Leaflet Demo & Local Serving

- [x] Add a dedicated demo server command (prefer `watercolormap serve`) for local viewing and sharing screenshots

- [x] Support serving tiles from the existing flat naming scheme (`tiles/z{z}_x{x}_y{y}.png` and `@2x`)
- [x] Provide a Leaflet demo page served by the same server (no external build tooling)

**Server requirements**

- [x] HTTP server with configurable listen address (default `127.0.0.1:8080`)
- [x] Configurable tile directory (default `./tiles`) and static assets root (default `./docs`)
- [x] Routes:
  - [x] `GET /healthz` → plain `ok`
  - [x] `GET /` → redirect to `/demo/`
  - [x] `GET /demo/` → serve the Leaflet demo page
  - [x] `GET /tiles/...` → serve tile PNGs from disk (with on-demand generation if missing)
- [x] Friendly 404 for missing tiles (include requested z/x/y in the response)
- [x] Correct headers for PNG (`Content-Type: image/png`) and optional dev-friendly caching (`Cache-Control: no-store` by default)
- [x] Optional CORS toggle for tile requests (off by default; useful for embedding the demo elsewhere) — `--cors-origin` / `serve.cors_origin`; `withCORS` is now the sole owner of the headers and the four hardcoded `*` duplicates are gone

**Leaflet demo page requirements**

- [x] Minimal HTML (no build step) at `docs/leaflet-demo/index.html`
- [x] Uses Leaflet via CDN
- [x] Uses the demo server as the tile source (no hard-coded host; derive from `window.location`)
- [x] Tile URL strategy:
  - [x] Default: request tiles using the project's flat file naming scheme
  - [x] HiDPI: support `@2x` tiles via Leaflet `detectRetina` (or a simple DPR switch) when available
- [x] Sane defaults: initial view centered on Hanover, min/max zoom aligned with what we generate (Phase 4.8)
- [x] Attribution included on the map (OSM) and a short note that the style is "Watercolor-inspired"

**Developer ergonomics**

- [x] Add `just serve` to run the server against `./tiles` (and optionally `just demo` as an alias)
- [x] Document quickstart in README: generate a tile set → run server → open browser URL (also fixed the nonexistent `--tile` flag and the `--min-zoom`/`--max-zoom`/`--bounds` names)

**Smoke test / acceptance**

- [x] Generate a small Hanover set (e.g., a 3×3 grid at z13) and verify — run against the local Overpass instance (`docs/local-overpass.md`), 3×3 z13 block around `4317/2692`, base + `@2x`, 18 tiles in 62 s with no warnings:
  - [x] Demo page loads without console errors — required one fix: the page had no `<link rel="icon">`, so the browser requested `/favicon.ico`, which the tile server has no route for and answered 404. That was the only console error. Now an inline `data:` SVG icon, so no request is made
  - [x] Tiles load and pan smoothly — 30/30 tiles loaded at z13 and again at z15, 0 broken, 0 pending; after a real drag-pan (hash moved `52.375/9.732` → `52.335/9.822`) 43/43 loaded, 0 broken
  - [x] HiDPI tiles render when present — the demo switches on `devicePixelRatio >= 2` (`docs/leaflet-demo/index.html:432`). At DPR 2 the server log shows `suffix=@2x` on-demand generations and the page holds 13 tiles with `naturalWidth` 512 drawn at 256 CSS px, i.e. the retina path end to end. Verified independently by URL as well: `z13_x4317_y2692@2x.png` is 512×512
  - [x] Missing tiles are generated on-demand and displayed — 35 tiles generated on demand during browsing (`msg="tile generated on-demand"`), base and `@2x`, all displayed, 0 errors or warnings in the server log
  - [x] Regenerated tiles are cached to disk for subsequent requests — a tile absent from `tiles/` returned in 6.66 s and appeared on disk at the same byte size; the second request for it returned in 3.7 ms

## 4.7 Visual Tuning Controls

- [x] Expose per-layer watercolor params via config with Phase 3 defaults — `internal/watercolor/tuning.go` (`Overrides`/`Tuner`), `watercolor:` block in `config.example.yaml`, threaded through `generate`, `serve` and the batch path. **Scope correction:** "edge colors" do not exist and cannot be exposed — the edge pass only reduces HSL lightness — so the keys are `edge-strength` / `edge-sigma` / `edge-gamma`. `tint` does exist and is now wired (it was dead code before)
- [x] Add golden/snapshot render for a known tile to catch regressions when tuning (`TestPipelineStages` in `internal/pipeline/pipeline_stages_test.go:22`, goldens in `testdata/golden/pipeline-stages/`)
- [x] Document tuning guidance referencing the Stamen process steps (blur → noise → threshold → edge darkening) — `docs/watercolor-tuning.md`, which supersedes the stale parameter list in `docs/3.6-visual-quality-testing.md`

## 4.8 Hanover Coverage Generation

- [x] Add CLI flags for bbox/zoom-range batch generation (reuse `tile.TileRange`) — all present on `generate` (`internal/cmd/generate.go`): `--bbox` (:40), `--zoom-min` (:41), `--zoom-max` (:42), `--workers`/`-w` (:43), `--progress` (:44, default `true`), `--force` (:48). Also available and used by the recipes: `--allow-failures` (:45), `--hidpi` (:50).
- [x] Script batch generation for Hanover with progress logging, `--force`, and resumable output dirs — the `Justfile` provides `prebuild-hannover` (:243) plus the `-quick` (z10–12), `-detailed` (z10–15) and `-full` (z10–16) wrappers (:254–263).
  - bbox: `9.65,52.32,9.85,52.43` (`hannover_bbox`, `Justfile:240`)
  - default zoom range of the recipe: **z10–14**; `just prebuild-hannover 10 15` gives the z10–15 set this phase targets
  - recipe always passes `--hidpi --allow-failures` and forwards extra `*args` (so `--force`, `--workers`, `--progress` can be appended)
  - resume: output dirs are resumable by "skip if the file already exists" — `Generator.GenerateWithData` does `if !force { if _, err := os.Stat(finalPath); err == nil { … return finalPath }}` (`internal/pipeline/generator.go:173`), logging "Tile already exists; skipping". There is no separate state/manifest file; re-running the same recipe simply fills the gaps, and `--force` overrides it.
- [x] **Manual step, done**: `just prebuild-hannover 10 15` run against the local Overpass instance (`docs/local-overpass.md`), then verified in the Leaflet demo. It stays out of tests and CI — it is long-running and needs a reachable Overpass plus a working Mapnik.

  **The produced set**
  - bounds `9.65,52.32,9.85,52.43`, zooms **10–15**, as written to `tiles/tilejson.json` (`center` `9.75,52.375` at z12) — unchanged from the committed TileJSON, so the metadata of 4.9 already described this set
  - **502 base + 502 `@2x` = 1004 tiles**, 0 failed, 9m25s total (4m5s base at 2.0 tiles/s, 5m20s hi-DPI at 1.6 tiles/s), 255 MB on disk
  - per zoom: z10 **2**, z11 **6**, z12 **16**, z13 **36**, z14 **100**, z15 **342** — doubled for `@2x`
  - every `@2x` tile is 512×512

  **Run with `--force`, deliberately.** The plain run finished 502/502 in 5m23s and proved the resume path works — but it skipped ~180 in-bbox base tiles left over from 2025-12-23, i.e. rendered before the railroad/civic layers existed (`9f6b504`) and before the hi-DPI fix (`7f78db9`). Those would have sat next to freshly rendered neighbours and under freshly rendered `@2x` twins, which is exactly what this step is meant to inspect. The recorded set is therefore a single consistent generation.

  **Demo verification** (`serve` on `:8085`, tiles served from disk):
  - all six zooms exercised by clicking the zoom control — tile fetches recorded for z10 through z15. DOM tile counts at the points they were sampled: 36/36 loaded at z13, 36 fetched at z14, 30/30 at z15, 48/48 after a drag-pan at z15, 36/36 back at z13. 0 broken and 0 pending throughout
  - no seams visible at z13 or z15 — the Maschsee, the Mittellandkanal, the motorway ring and the rail corridors all run continuously across tile boundaries
  - console clean: two `[LOG]` lines from the demo itself, no warnings, no errors
  - **caution when reading `location.hash`**: setting it does _not_ move the map (the demo does not listen for `hashchange`), it only gets overwritten on the next map move. A first attempt to sweep the zooms that way silently stayed at z13 the whole time and still produced plausible-looking "36/36 at every zoom" numbers. Drive the zoom control, and confirm what was actually fetched via `performance.getEntriesByType('resource')` — and clear that buffer first, its 250-entry default fills up fast.

  **Known and expected: the set does not fill the viewport at low zoom.** The recipe generates the same small bbox at every zoom, so at z10–z12 a screenful is far larger than 0.2°×0.11° — z10 is 2 tiles for a viewport that wants ~36. The server generated 5 of the surrounding tiles on demand and logged 28 `transient error … context canceled` warnings for others, all outside the bbox: Leaflet aborts the request when the map moves on, which cancels the generation and queues a retry. Nothing inside the bbox was ever generated on demand — the prebuilt set was hit for every tile it covers. If a full-screen low-zoom view is wanted, that needs a wider bbox at z10–z12, not a deeper zoom range.

  **Out of scope but visible in the result**: railways render as heavy solid-black bands and dominate the z13 view; also 155 tiles from other locations remain in `tiles/` from earlier work and are still on the December stylesheet.

## 4.9 TileJSON / Delivery Metadata

- [x] Emit a minimal `tilejson.json` (bounds, min/max zoom, format, tile URL template) for the generated set — new `internal/tilejson` package, written next to the tiles by batch folder generation and served at `GET /tiles/tilejson.json`
- [x] Include required attribution text (Stamen-style / OSM) in the metadata (`© OpenStreetMap contributors · Watercolor-inspired rendering`). The demo carries both halves, but in two places: Leaflet's attribution control holds `© OpenStreetMap contributors` (`docs/leaflet-demo/index.html:434`) and the header panel holds "Watercolor-inspired raster tiles (OSM data)." (`:128`). An earlier note here called the watercolor half open; it is present on the page, just not inside the attribution control
