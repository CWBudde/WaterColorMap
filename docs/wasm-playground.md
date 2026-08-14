# WASM Playground

The playground is a static web page that renders WaterColorMap tiles in the browser. The Go
rendering pipeline is compiled to WebAssembly (`cmd/wasm`), map data comes from the public Overpass
API, and Leaflet handles the map UI. It needs no backend: the whole tile pipeline (raster rendering,
masking, watercolor compositing, PNG encoding) runs in the browser.

Source lives in `docs/wasm-playground/`. The deployed copy is at
<https://cwbudde.github.io/WaterColorMap/>.

## Quick start

```bash
just build-wasm-local
# open http://localhost:8000/wasm-playground/
```

`build-wasm-local` runs `build-wasm` and then serves the whole `docs/` directory on port 8000, which
is why the local URL has a `/wasm-playground/` sub-path. The deployed site does not — see
[Deployment](#deployment).

To build the artifacts without serving:

```bash
just build-wasm     # GOOS=js GOARCH=wasm go build + scripts/copy-wasm-exec.sh
just clean-wasm     # removes wasm.wasm and wasm_exec.js
```

## Components

| File                                     | Role                                                              |
| ---------------------------------------- | ----------------------------------------------------------------- |
| `cmd/wasm/main.go`                       | Go entry point; exports the tile functions to JavaScript          |
| `docs/wasm-playground/index.html`        | Page shell, Leaflet CSS/JS from unpkg, status box                 |
| `docs/wasm-playground/wasm_bootstrap.js` | Loads `wasm.wasm`, starts the Go runtime, waits for the exports   |
| `docs/wasm-playground/wasm.js`           | Leaflet grid layer, Overpass fetching, concurrency limits         |
| `docs/wasm-playground/wasm_exec.js`      | Go's WASM runtime shim (build artifact)                           |
| `docs/wasm-playground/wasm.wasm`         | Compiled Go module, ~20 MB uncompressed (build artifact)          |
| `docs/wasm-playground/static-tiles/`     | Pre-generated tiles produced by CI (not present in a checkout)    |
| `scripts/copy-wasm-exec.sh`              | Locates `wasm_exec.js` in the local Go installation and copies it |
| `.github/workflows/wasm-deploy.yml`      | Builds, generates static tiles, deploys to GitHub Pages           |

`wasm.wasm` and `wasm_exec.js` are **build artifacts**, produced by `just build-wasm`
(`Justfile:43-48`). They are not committed to git — both are listed in `.gitignore`, and CI rebuilds
them on every deploy. A fresh checkout has neither; run `just build-wasm` before serving locally.

`static-tiles/` is likewise generated only in CI and never committed.

The WASM module exports four functions on `globalThis`:

- `watercolorInit()` — no-op readiness probe
- `watercolorOverpassQueryForTile(requestJson)` — returns the Overpass QL query plus tile size,
  padding and the padded bounding box for a `{zoom, x, y, hidpi}` request
- `watercolorRenderTileFromOverpassJSON(requestJson, overpassJson)` — renders and returns
  `{pngBase64, mime, ms}`
- `watercolorGetConcurrency()` — `navigator.hardwareConcurrency`, or 4

## Tile flow

For each tile Leaflet requests, `wasm.js`:

1. **Static tile first.** For non-HiDPI tiles at zoom 13-14 it tries `./static-tiles/{z}/{x}/{y}.png`.
   A hit is used directly; a 404 or network error falls through.
2. **Overpass query.** `watercolorOverpassQueryForTile` builds a query for the tile bounds expanded
   by the padding the watercolor filters need (rendering a padded metatile and cropping back avoids
   seams at tile edges).
3. **Fetch.** The query is POSTed to `https://overpass-api.de/api/interpreter`, limited to two
   concurrent requests, with exponential backoff and jitter on 429/5xx and network errors (5 retries).
4. **Render.** `watercolorRenderTileFromOverpassJSON` rasterises the layers, applies blur, Perlin
   noise, thresholding and textures, composites over the paper texture, crops the padding, and
   returns a base64 PNG. Rendering is limited to `watercolorGetConcurrency()` tiles at a time.
5. **Display.** The PNG is assigned as a `data:` URL. On failure an inline SVG placeholder carrying
   the error is shown instead.

## Caching

There is no persistent client-side tile cache. Caching is whatever the browser applies to the
static-tile and Overpass responses; WASM-rendered tiles live only in the Leaflet layer for the
lifetime of the page, so panning back re-fetches and re-renders them.

`wasm_bootstrap.js` deliberately opts out of caching for the module itself: it fetches `wasm.wasm`
with `cache: "no-store"` and a `?v=<timestamp>` query parameter so a redeploy is picked up without a
hard refresh.

## Deployment

`.github/workflows/wasm-deploy.yml` builds the WASM module, installs Mapnik, builds the native
`watercolormap` binary, pre-generates the Hanover static tiles (bbox `9.60,52.30,9.90,52.50`,
zoom 13-14, seed 1337), and uploads **`docs/wasm-playground` itself** as the Pages artifact via
`actions/upload-pages-artifact`.

Because that directory is the artifact root, the deployed site has no `/wasm-playground/` sub-path:

- Deployed: `https://cwbudde.github.io/WaterColorMap/`
- Local: `http://localhost:8000/wasm-playground/`

This is a common trip hazard. Any link or asset path written for one of the two will be wrong in the
other unless it is relative — keep in-page paths relative (`./static-tiles/...`, `wasm.wasm`).

Workflow triggers:

- `release` of type `created`
- push of a version tag matching `[0-9]+.[0-9]+.[0-9]+(-.*)?`
- `workflow_dispatch` (Actions → "Build and Deploy WASM Playground" → Run workflow)
- `schedule`: weekly, Sunday 02:00 UTC

There is **no push-to-`main` trigger**. Merging to main does not redeploy the playground; cut a
release, push a version tag, or dispatch the workflow manually.

## Limitations

- **Module size.** The WASM binary is ~20 MB uncompressed (GitHub Pages serves it gzipped). First
  load is slow on a cold cache.
- **Overpass dependency.** Every tile outside the static-tile coverage needs a live Overpass query.
  Rate limiting, timeouts and outages surface directly as failed tiles; the client backs off but
  does not queue for later.
- **No persistent cache.** Tiles are re-rendered on every visit and on every pan back.
- **No Mapnik.** Mapnik is a native C++ library and cannot compile to WebAssembly. The browser path
  uses the pure-Go rasteriser (`internal/raster`), so output can differ from the Mapnik-based
  `generate`/`serve` pipeline.
- **Zoom range.** The map is limited to zoom 10-16; static tiles exist only for zoom 13-14,
  non-HiDPI, in the Hanover area.
- **Fixed seed.** The browser renderer uses seed 1 (`defaultSeed` in `cmd/wasm/main.go`), while CI
  generates static tiles with seed 1337, so static and on-demand tiles are not noise-identical.

## Troubleshooting

| Symptom                          | Check                                                                                       |
| -------------------------------- | ------------------------------------------------------------------------------------------- |
| "Error: wasm_exec.js not loaded" | Run `just build-wasm`; `scripts/copy-wasm-exec.sh` must find `wasm_exec.js` in your GOROOT  |
| "Error: WASM exports not ready"  | `wasm.wasm` is stale or failed to start — check the console, rebuild with `just build-wasm` |
| WASM fetch 404 locally           | Serve from `docs/`, not from `docs/wasm-playground/`; `just build-wasm-local` does this     |
| "Overpass error" on every tile   | Overpass is rate-limiting or down; try again later or point at another endpoint             |
| Blank tiles outside Hanover      | Expected without Overpass results; the area may genuinely have no matching features         |
| Deployed page 404s on assets     | A path was written for the local `/wasm-playground/` sub-path; make it relative             |
| Build fails                      | `just clean-wasm` and retry; Go 1.25 is what CI uses                                        |

## References

- [Go WebAssembly](https://github.com/golang/go/wiki/WebAssembly)
- [Leaflet GridLayer](https://leafletjs.com/reference.html#gridlayer)
- [Overpass API](https://wiki.openstreetmap.org/wiki/Overpass_API)
- [actions/upload-pages-artifact](https://github.com/actions/upload-pages-artifact)
- [actions/deploy-pages](https://github.com/actions/deploy-pages)
