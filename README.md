# WaterColorMap

Generate Stamen Watercolor–style raster map tiles from OpenStreetMap data — with multi-pass rendering, mask processing, watercolor textures, and seamless compositing.

**[Live Demo](https://cwbudde.github.io/WaterColorMap/)**

WaterColorMap is built for "old-school" raster cartography: we render clean layer masks (Mapnik), distort edges organically (blur + deterministic Perlin noise + threshold), apply seamless watercolor textures, then composite everything into final web-ready tiles.

## Highlights

- Full watercolor tile pipeline (render → masks → textures → composite)
- Deterministic edges across tile boundaries (no seams)
- Multi-pass Mapnik rendering for clean layer isolation
- Built-in textures and Mapnik styles (land/water/parks/civic/roads)
- Fast batch generation with safe caching and `--force` regeneration
- Docker and native Linux workflows

## Requirements

- Linux (tested on Ubuntu 24.04)
- Go 1.25+
- Mapnik 3.1+ (`libmapnik-dev`, `mapnik-utils`, `python3-mapnik`)
- Build tooling: `pkg-config`, `build-essential`
- Optional: [Just](https://github.com/casey/just) for one-liner workflows

## Quick Start (native)

```bash
git clone https://github.com/cwbudde/WaterColorMap.git
cd WaterColorMap

sudo apt update
sudo apt install -y libmapnik-dev mapnik-utils python3-mapnik build-essential pkg-config

cp config.example.yaml config.yaml
just build

# Generate a single tile (Hanover example)
./bin/watercolormap generate --zoom 13 --x 4317 --y 2692
```

More setup details (including troubleshooting) are in [SETUP.md](SETUP.md).

## Quick Start (Docker)

```bash
cp config.example.yaml config.yaml
just docker-build

docker run --rm \
  -v "$PWD/config.yaml:/app/config.yaml:ro" \
  -v "$PWD/tiles:/app/tiles" \
  -v "$PWD/cache:/app/cache" \
  -v "$PWD/assets:/app/assets:ro" \
  -e WATERCOLORMAP_CONFIG=/app/config.yaml \
  watercolormap:latest generate --zoom 13 --x 4317 --y 2692
```

## Usage

Generate a few tiles, then look at them in the built-in Leaflet demo. This is
the whole path, end to end:

```bash
# 1. Generate a small batch around Hanover (zoom 12-13)
./bin/watercolormap generate \
  --bbox "9.65,52.32,9.85,52.43" \
  --zoom-min 12 --zoom-max 13 \
  --allow-failures

# 2. Serve the tiles plus the demo UI
just serve
# equivalently: ./bin/watercolormap serve --addr 127.0.0.1:8080

# 3. Open the demo
#    http://127.0.0.1:8080/demo/
```

`just smoke` does steps 1 and 2 in one go for a 3×3 block at zoom 13.

### Generating tiles

A single tile, by coordinate:

```bash
watercolormap generate --zoom 13 --x 4317 --y 2692
```

A batch, by bounding box (`minLon,minLat,maxLon,maxLat`) and zoom range:

```bash
watercolormap generate \
  --bbox "9.65,52.32,9.85,52.43" \
  --zoom-min 10 --zoom-max 16 \
  --allow-failures
```

Useful flags: `--force` (regenerate existing tiles), `--workers`,
`--hidpi` (also write an `@2x` tile; single-tile mode only — batch runs reject
it and `serve` renders `@2x` on demand instead), `--image-format` (`png` or
`webp`), `--png-compression`, `--folder-structure` (`flat` or `nested`), and
`--format mbtiles` with `--output-file` to write an MBTiles file directly. See
`watercolormap generate --help` for the full list.

Note that `--format` is the output _container_ (folder or MBTiles) and
`--image-format` is the tile _image encoding_.

**Batch runs resume.** An interrupted run can simply be re-issued: tiles that
already exist are skipped, for MBTiles output as well as folder output. Pass
`--force` to re-render them instead.

Planning a run larger than a city? Read
[docs/data-scaling-strategy.md](docs/data-scaling-strategy.md) first — it has the
measured per-tile cost, the tile counts per zoom for a country, and the reason
the Overpass fetch rather than the rendering is what sets your throughput.

### Convert tiles to MBTiles

```bash
watercolormap convert --input-dir ./tiles --output hanover.mbtiles
```

`--output` is required. See `watercolormap convert --help` for the metadata flags (`--name`, `--description`, `--attribution`, `--bounds`).

### Delete tiles

```bash
# What would go? (dry run — this is the default)
watercolormap purge --tiles-dir ./tiles --bbox 9.7,52.3,9.9,52.4 --zoom-min 13

# Actually delete it
watercolormap purge --tiles-dir ./tiles --bbox 9.7,52.3,9.9,52.4 --zoom-min 13 --yes
```

Tiles can also be selected by how stale they are, which reads the stamps
`generate` writes alongside them — the Overpass source-data version, the render
time, and the build that produced the tile:

```bash
# Everything rendered from OSM data older than the last import
watercolormap purge --mbtiles hanover.mbtiles --data-before 2026-08-01T00:00:00Z --yes --compact
```

Pair it with `generate --stale-data-before` to re-render exactly what was
removed. A tile with no stamp is never deleted by a staleness flag, and is
always re-rendered by one: deletion is not undoable, a re-render is.

### Generate textures

```bash
watercolormap textures --textures-dir assets/textures --size 1024
```

Textures are deterministic for a given `--seed`. See `watercolormap textures --help` for the remaining flags.

### Serving tiles

`serve` hosts the tiles and the Leaflet demo, and generates any tile that is
missing on the fly (`--generate-missing`, on by default):

```bash
# Serve a tile folder (defaults to --output-dir)
watercolormap serve --addr 127.0.0.1:8080 --tiles-dir ./tiles

# Or serve an MBTiles file instead
watercolormap serve --mbtiles ./tiles.mbtiles
```

Routes: `/demo/` (Leaflet UI), `/tiles/z{z}_x{x}_y{y}.png`, `/tiles/status`
(JSON) and `/tiles/status/stream` (SSE), `/healthz`.

Cross-origin requests are **off by default**. A page served from another
origin — the WASM playground, a GitHub Pages demo — needs the header switched
on explicitly:

```bash
watercolormap serve --cors-origin '*'          # any origin
watercolormap serve --cors-origin https://example.com
# or: just serve-cors
```

The value is also settable as `serve.cors_origin` in `config.yaml`. An empty
value (the default) sends no `Access-Control-*` headers at all.

#### Caching

Every tile response carries an `ETag` and an `X-Cache` header saying how it was
answered — `HIT`, `HIT-COALESCED` (another request rendered it while this one
waited), `MISS` or `BYPASS`. The running totals, including 304s and re-renders
forced by a `--stale-*` cutoff, are in `/tiles/status` under `cache`.

`--cache-control` defaults to `no-cache`: clients store the tile and revalidate
it, so a repeat view costs a 304 rather than the bytes, and a tile removed by
`purge` is gone on the very next request. Behind a CDN,
`--cache-control 'public, max-age=300, stale-while-revalidate=86400'` is far
faster, at the price that a purged tile stays in caches for up to `max-age`.
`--cache-control no-store` restores the old behaviour of refetching everything.

An in-process cache of tile metadata (`--tile-meta-cache-entries`,
`--tile-meta-cache-ttl`) keeps a hit from touching the filesystem at all. Its TTL
also bounds how long a tile deleted by `purge` can still be served — see
[docs/tile-server-architecture.md](docs/tile-server-architecture.md) for the full
picture.

## Browser Playground (WASM)

There is a minimal browser playground (Leaflet) that renders tiles on demand entirely in the browser — it queries Overpass directly and rasterises in WASM, with no backend. It can be deployed via GitHub Pages.

- Live (GitHub Pages): https://cwbudde.github.io/WaterColorMap/
- Details: [docs/wasm-playground.md](docs/wasm-playground.md)
- Local (build + serve):

```bash
just build-wasm-local
# open http://localhost:8000/wasm-playground/
```

`docs/wasm-playground/wasm.wasm` and `docs/wasm-playground/wasm_exec.js` are build artifacts produced by `just build-wasm`; they are not committed.

Note: the playground uses the pure-Go rasteriser in `internal/raster`, not Mapnik, so its output differs from the tiles `generate` produces. For Mapnik-quality tiles on demand, run the backend server (see [Serving tiles](#serving-tiles)). The playground page is served from a different origin than the tile server, so CORS has to be switched on:

```bash
./bin/watercolormap serve --addr 127.0.0.1:8080 --cors-origin '*'
# or: just serve-cors
```

## Output layout

By default, tiles are written to `./tiles` as PNG files using the naming scheme:

```text
tiles/
  z13_x4297_y2754.png
  z13_x4297_y2754@2x.png        # optional HiDPI output
  tilejson.json                 # written by batch runs
  stamps.db                     # per-tile source-data stamps
```

`stamps.db` records, for each tile, the Overpass source-data version it was
rendered from, when it was rendered, which endpoint answered and which build
produced it. `generate --stale-*` and `purge --data-before` read it. An MBTiles
tileset keeps the same information in a `tile_stamp` table inside the file, and
`convert` carries the stamps of a folder over into the MBTiles file it writes.
`serve` stamps the tiles it renders on demand through the same store, so a
tileset filled in by browsing is selectable by the same flags as one produced by
a batch run.

HiDPI (`@2x`) tiles are produced **on demand** by `watercolormap serve`: request
`z13_x4297_y2754@2x.png` and it renders one. `--hidpi` on `watercolormap generate`
writes a single `@2x` tile alongside its base tile, which is useful for
spot-checking that the two show the same road classes at the same ground width.

It is deliberately **not** available for batch runs. Pre-rendering `@2x` across a
bbox doubles compute and quadruples storage for the whole run, and the on-demand
path already covers the tiles anyone actually requests.

PNG encoding can be tuned via `--png-compression` (`default`, `speed`, `best`, `none`).

### WebP output

`--image-format webp` writes `.webp` tiles instead, on both `generate` and
`serve`:

```bash
watercolormap generate --bbox "9.65,52.32,9.85,52.43" \
  --zoom-min 12 --zoom-max 13 --image-format webp
```

The encoder is **lossless** (VP8L, pure Go, no cgo), so a tile round-trips
pixel-for-pixel. Measured over the 689 rendered tiles in `tiles/`:

| statistic                   | value                    |
| --------------------------- | ------------------------ |
| mean PNG                    | 122,326 B                |
| mean WebP (lossless)        | 101,181 B                |
| **reduction**               | **1.21×**                |
| tiles where WebP was larger | 0 of 689                 |
| encode time                 | ~52 ms vs ~14 ms for PNG |

This is deliberately _not_ the 9.24× figure in
[docs/data-scaling-strategy.md](docs/data-scaling-strategy.md), which was
measured with **lossy** WebP at quality 80. These tiles are close to the worst
case for a lossless codec: the watercolor texture and Perlin noise fill every
pixel, so there is no flat region to collapse. 1.21× is the honest number for
lossless, and it holds at every zoom.

`--webp-effort` (0–6) trades size against time; the default of 4 is where the
curve flattens. Some things worth knowing:

- A tileset is one format. `serve` answers only the extension it holds and
  returns 404 for the other — it never transcodes, and never serves one
  format's bytes under the other's name. That applies to both backends: for
  on-demand folder serving the format is the configured one, and for MBTiles it
  is whatever the file's own metadata declares.
- Reopening an existing MBTiles file with a different `--image-format` is
  refused, because the resume check keys on coordinates alone and would
  otherwise skip every tile already there while relabelling the file. A
  non-empty file that declares no format is assumed to hold PNG, so a PNG run
  resumes it and a WebP run is refused rather than relabelling it.
- Existing PNG tiles are untouched. A WebP run writes different filenames, so it
  neither overwrites nor skips them.

During generation, intermediate layer renders and processed masks may be stored in the cache directory for debugging and faster incremental builds.

## How it works (pipeline)

1. Fetch OSM features for the requested tile (Overpass API)
2. Convert features to GeoJSON per layer (land/water/parks/civic/roads)
3. Render each layer via Mapnik to a clean RGBA mask image (multi-pass)
4. Convert layer images to binary masks and apply the watercolor mask pipeline:
   - Gaussian blur
   - deterministic Perlin noise overlay
   - thresholding + antialias
5. Apply seamless watercolor textures as alpha-masked fills
6. Composite layers in the correct order into a final tile

Mask design and rationale: [docs/watercolor-mask-design.md](docs/watercolor-mask-design.md)
Per-stage detail: [docs/3.1-mask-processing-pipeline.md](docs/3.1-mask-processing-pipeline.md)

A full map of the documentation — design notes, performance write-ups and the
archived records of completed phases — is in [AGENTS.md](AGENTS.md).

## Configuration

Configuration is loaded from:

1. The YAML file given by `--config` (defaults to `./config.yaml`)
2. CLI flags, which override the file

Start with the example file: [config.example.yaml](config.example.yaml)

Note that YAML keys use underscores where the corresponding flag uses hyphens
(`generate.zoom_min` for `--zoom-min`, `serve.tiles_dir` for `--tiles-dir`).

Keys that are actually read:

- `data-source`: OSM data source (default: `overpass`; no other source is implemented)
- `output-dir`: where generated tiles go (default: `./tiles`)
- `verbose`, `log-level`: logging (default level: `info`)
- `overpass.endpoint` / `overpass.servers`: read by `serve` only — `generate` always builds a default single-endpoint Overpass source
- `ocean.*`: the water polygons used for ocean and coastline rendering (see below)
- `natural-earth.*`: the generalised shapefiles used at z0-5 (see below)
- `generate.*`, `serve.*`, `convert.*`, `purge.*`, `textures.*`: mirror the flags of the respective command

### Ocean and coastlines

OpenStreetMap does not map the ocean — the sea is modelled as the absence of
land — so the open sea has to come from somewhere else. Without it, ocean tiles
render as land and coastal tiles come out inverted, with the sea painted tan and
lakes painted blue.

The source is the processed water polygons from
[osmdata.openstreetmap.de](https://osmdata.openstreetmap.de/data/water-polygons.html),
rendered directly through Mapnik's shapefile plugin:

```bash
just fetch-water-polygons              # ~1 GB into ./data (gitignored)
# or, for low zooms only:
just fetch-water-polygons-simplified   # ~120 MB
```

Then point `config.yaml` at them — see the `ocean:` block in
[config.example.yaml](config.example.yaml). Ocean rendering is off until it is
configured; inland tiles render identically either way.

### World and continent zooms (z0-5)

Below z6, OSM is the wrong source twice over: a single z2 tile would ask
Overpass for a quarter of the planet, and ungeneralised coastline at roughly one
pixel per 50 km is detail nobody can see. Those zooms come from
[Natural Earth](https://www.naturalearthdata.com/) instead — generalised
coastlines, lakes and rivers, read through the same Mapnik shapefile plugin:

```bash
just fetch-natural-earth   # ~10 MB into ./data (gitignored)
```

Then point `config.yaml` at it — see the `natural-earth:` block in
[config.example.yaml](config.example.yaml). With it enabled, a tile at or below
`max-zoom` (default 5) is rendered entirely from these shapefiles and makes **no
Overpass request at all**, so the whole low tier generates offline and in
minutes. Coastline, lakes and rivers are all that exists down there; roads and
buildings render absent, which is what a world view should look like.

Like ocean rendering, it is off until configured, and z6 and above are unchanged
either way. [docs/zoom-levels.md](docs/zoom-levels.md) documents the full zoom
stack.

## Development

```bash
just build
just test
just fmt
just lint
just check
```

## Project layout

```text
cmd/watercolormap/              # CLI entry
internal/datasource/            # OSM/Overpass fetching
internal/geojson/               # OSM features → GeoJSON
internal/renderer/              # Mapnik rendering + multi-pass
internal/mask/                  # Watercolor mask processing
internal/tile/                  # z/x/y math + bounds
assets/styles/                  # Mapnik styles
assets/textures/                # Seamless watercolor textures
docs/                           # Design notes and phase docs
```

## Attribution

- Map data: © OpenStreetMap contributors
- Watercolor inspiration: Stamen Design's "Watercolor" process and textures writeups

## License

MIT License — see [LICENSE](LICENSE) for details.
