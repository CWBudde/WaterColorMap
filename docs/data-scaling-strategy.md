# Data Scaling Strategy (Phase 5.1)

What it takes to go from the current Niedersachsen setup to Germany, and what it
would take to go global. Written to close PLAN.md § 5.1.

Two conventions run through this document:

- **Measured** means a number produced by a run on this branch, with the method
  stated next to it. **Estimated** means arithmetic or a plausible extrapolation.
  Where a number is estimated and could be measured cheaply, it says so.
- Tile counts are computed with the repository's own `tile.TileCount`
  (`internal/tile/coords.go:200`), i.e. exact tile-index rounding over the bbox,
  not an area approximation. This matters — see § 3.

The one number to take away, if you read nothing else: **for a bulk run against a
live Overpass, 71% of wall clock is the fetch** (§ 4). Render optimisation is not
where the time is.

## 1. Regional database approach

### Conclusion

Keep Overpass as the interface. Run a second container from a `germany-latest`
extract beside the existing Niedersachsen one, and route between them by
coverage box. No code changes are required for the routing itself.

### What already ships

`overpass.servers` routing exists and is documented in
[docs/MULTI-SERVER-OVERPASS.md](MULTI-SERVER-OVERPASS.md). Servers are checked in
order and the first whose coverage box intersects the tile wins; a nil-coverage
entry matches everything and acts as the fallback
(`internal/datasource/overpass.go:565-644`).

**Verified on this branch: `generate` honours the block too.** `newTileDataSource`
(`internal/cmd/generate.go:403-420`) routes through the same
`createOverpassDataSource` that `serve` uses (`internal/cmd/serve.go:381`), so
both commands read `overpass.servers` / `overpass.endpoint` identically. The
regression is pinned by `TestNewTileDataSourceHonoursConfiguredServer`
(`internal/cmd/datasource_config_test.go:20-79`), which is table-driven over
`overpass.endpoint`, a single `overpass.servers` entry, and a coverage-routed
entry, and asserts the configured test server actually receives the request while
`WATERCOLORMAP_OVERPASS_ENDPOINT` points at an unreachable address.

PLAN.md § 7.9 carried the older claim that `generate` ignores the block for some
time after it had stopped being true. That bullet is corrected in the same change
as this document; if you meet the claim again anywhere, it is stale.

So a Germany rollout is a config change:

```yaml
overpass:
  servers:
    - name: "Germany"
      endpoint: "http://localhost:12346/api/interpreter"
      workers: 10
      coverage:
        { min_lat: 47.27, max_lat: 55.06, min_lon: 5.87, max_lon: 15.04 }
    - name: "Public" # no coverage = matches everything
      endpoint: "https://overpass-api.de/api/interpreter"
      workers: 2
```

### Costs — all estimated

Niedersachsen is a ~450 MB PBF and takes 15-30 min to initialise
([docs/local-overpass.md](local-overpass.md)); that init time is the one measured
figure here. Germany is roughly ten times the extract:

| resource | Niedersachsen (measured where noted) | Germany (estimated) |
| -------- | ------------------------------------ | ------------------- |
| PBF      | ~450 MB                              | ~4-4.5 GB           |
| disk     | ~5 GB                                | ~30-50 GB           |
| RAM      | a few GB of page cache               | 8-16 GB page cache  |
| init     | 15-30 min (**measured**)             | 3-6 h               |

**These are estimates and should be replaced by measurement.** One real import
produces a `du -sh` on the database directory and a wall time from the container
log; both belong in this table, replacing the estimates, the first time somebody
runs the import.

One thing to get right in the RAM row: Overpass is mmap-based. The requirement is
page cache and an NVMe device, not process heap. A machine with 16 GB and a spinning
disk will be far slower than the number suggests; a machine with 8 GB and NVMe will
be fine. Sizing this as "heap" leads to the wrong purchase.

### Where the regional model breaks

The planet extract is 400 GB+ of PBF and days of initialisation. At that scale the
model stops being worth defending, and for a reason more interesting than size:

**Bulk rendering does not need Overpass's query language.** It needs an indexed
spatial scan — "give me every feature of these classes in this bbox" — repeated
several hundred thousand times. Overpass is built for ad-hoc, expressive,
low-volume queries, and pays for that everywhere. The right tool for the bulk case
is `osm2pgsql` into PostGIS, queried directly by Mapnik.

The Mapnik half of that is already free: `postgis.input` and `pgraster.input` are
**installed** in `/usr/lib/mapnik/3.1/input/` (verified). Nothing needs building.

The seam on the Go side is `pipeline.DataSource` (`internal/pipeline/generator.go`)
— a one-method interface (`FetchTileData`), with an optional
`dataSourceWithBounds` extension. A PostGIS-backed implementation slots in there
without touching the renderer.

Record for the next reader: `internal/cmd/datasource_config_test.go:82-86`
(`TestNewTileDataSourceRejectsUnknownSource`) pins `"postgis"` as an **explicitly
rejected** datasource name. That is a deliberate decision to revisit when the
planet case arrives, not an oversight. Do not "fix" the test by wiring a stub.

### Three defects that would stop a Germany run today

None of these is hypothetical, and omitting them would make this section useless.

**1. No failover in the routing.**
`MultiOverpassDataSource.FetchTileDataWithBounds`
(`internal/datasource/overpass.go:629-645`) takes the **first** coverage match and
returns its error verbatim, wrapped with the server name:

```go
for _, srv := range mds.servers {
    if srv.coverage == nil || intersects(bounds, *srv.coverage) {
        data, err := srv.datasource.FetchTileDataWithBounds(ctx, tile, bounds)
        if err != nil {
            return nil, fmt.Errorf("[%s] %w", srv.name, err)
        }
        return data, nil
    }
}
```

There is no second attempt. A container restart, an OOM, or the duplicate-query
rejection described in [docs/local-overpass.md](local-overpass.md) fails **every
tile inside that coverage box** for as long as it lasts, without ever trying the
nil-coverage public fallback that is sitting right there in the list. Over a
multi-day Germany run this is the single most likely cause of a large hole.

The same loop has a second, quieter property: it is **order-dependent**. A nested
box (say a city-level server inside the Germany box) is unreachable unless it is
listed before the box that contains it. Nothing validates or warns about this.

**2. MBTiles skip-existing — fixed on this branch.**
Previously the generator only checked `os.Stat(finalPath)`, so an MBTiles run had
no way to know a tile was already stored and re-rendered the whole tileset on
resume. That is now a working resume mechanism:

- `pipeline.TileProber` (`internal/pipeline/generator.go:106-114`) is an optional
  interface — `HasTile(z, x, y int) (bool, error)` — deliberately kept out of
  `TileWriter` so every test fake and every future writer is not forced to
  implement it.
- `Generator.tileExists` (`internal/pipeline/generator.go:253-272`) dispatches on
  the output backend: with no `TileWriter` it stats the file on disk, and with one
  it asks the prober. Note what it does **not** do — a writer that is not a prober
  yields "does not exist", so the tile is rendered; it never falls back to the
  filesystem, because the file is not where that writer's output lives. A probe
  error behaves the same way. Both failure modes render rather than skip: a wrong
  skip leaves a permanent hole in the tileset, a wrong render only costs time.
- `mbtiles.Writer.HasTile` (`internal/mbtiles/writer.go:201-240`) scans the
  unflushed write batch first (those rows are not queryable yet), then does a
  single seek against the `UNIQUE tile_index`, flipping y to TMS. The key set is
  deliberately **not** preloaded — a resumed z14 planet tileset would be hundreds
  of millions of keys.

The asymmetry in `tileExists` is the part not to "simplify": every uncertain case
— a writer that is not a prober, a probe that errors — answers **false**, i.e.
render it. A wrong "skip" leaves a permanent hole nothing later in the run will
fill; a wrong "render" costs a few seconds and overwrites the tile with an
identical one.

**3. No checkpoint, and no streaming enumeration — fixed on this branch.**
`Pool.Run` materialises the entire task list up front (`taskCh := make(chan Task,
len(tasks))`), which for Germany z0-14 is 317,618 `Task` structs allocated and
buffered before the first tile renders, plus 317,618 `Result` structs collected
on the way back. Survivable — the structs are small — but it also meant the run
had no notion of progress it could resume from beyond "which tiles already
exist".

`generate`'s non-banded path no longer goes through `Pool.Run`:

- `tile.TilesInBBoxSeq` is the enumeration as an `iter.Seq[Coords]`;
  `TilesInBBox` is now just that sequence collected into a slice, so every
  existing caller and test is unaffected. A producer goroutine feeds a channel of capacity `workers*2` and
  selects on `ctx.Done()`, exactly as the banded producer already did.
- `worker.Config.OnResult` takes the results one at a time, so `RunStream`
  retains none of them. `runTilePool` counts, and keeps the first 50 failures
  for logging. Peak bookkeeping is now the worker count, not the tile count.
- `Pool.Run` is untouched in contract: it ignores `OnResult` entirely, so
  `len(results) == len(tasks)` still holds unconditionally for its callers (see
  `docs/history/phase-7-hardening.md` § 7.7). The banded path, which schedules by
  band rather than by enumeration order, still takes the materialised tile list —
  that list is the price of band grouping, not of the pool.
- Cancellation accounting is preserved the way `reconcileBandResults` preserves
  it: the producer counts what it emitted, and every tile it never got to is
  reported as a failure. An interrupted run cannot exit 0 having rendered part of
  a tileset.

### Resume without re-statting the tileset

`--checkpoint` (off by default; `.watercolormap-checkpoint.json` next to the
run's output when the flag is given without a value — `<output-dir>` for a
folder run, the `--output-file`'s directory for an MBTiles run, which never
creates `<output-dir>`) writes `internal/checkpoint`'s small JSON
file every 2,000 tiles and again on shutdown, through the same
temp-file + fsync + rename discipline as `encodeTileAtomic` — a checkpoint
truncated by the very interrupt it exists to survive would be read by the next
run, which is worse than having none.

What it stores is a **watermark over the enumeration**, not a set of tiles: the
highest index such that every tile below it **succeeded**. Completions arrive out
of order, so a small frontier set holds successes ahead of the watermark until
the gap closes; it is bounded by how far out of order workers can finish, not by
the length of the run. A **failed tile blocks the watermark**, so a resume
re-attempts it — the same rule as `tileExists` above: never skip something that
might not be there. A failure also _bounds_ the frontier: nothing at or beyond
the lowest failed index can ever carry the watermark in this run, so those
successes are dropped rather than accumulated — otherwise one failed tile early
in a planet-sized run would grow the map to the length of the remainder, which is
the allocation this whole path exists to remove.

For MBTiles the watermark waits for durability. `mbtiles.Writer` batches rows and
commits them a transaction at a time, so a successful render is not yet a written
tile; the tracker flushes the writer before publishing a watermark that covers
its buffered rows. Without that, a crash could leave a durable checkpoint over
rows that never reached SQLite, and the resumed run would skip them for good.

Resuming then costs nothing: the run fast-forwards the sequence past `watermark`
items, which is arithmetic, and skip-existing still guards every tile that does
get emitted. That is the point — a resumed z14 country run does not re-stat
hundreds of thousands of tiles before rendering the first new one.

Two refusals, both of the "a wrong success is worse than a wrong failure"
family. A checkpoint whose `run_key` (bbox, zoom range, container format, image
format, suffix, the absolute output target and, for folder runs, the folder
structure) or schema does not match the current run is **ignored loudly**, never
reinterpreted: resuming one bbox's watermark into another's enumeration — or one
database's into an empty second one — would skip tiles nobody ever rendered, and
skip-existing could not catch it because those tiles were never emitted to be
checked. `--force` ignores it too.
And `--checkpoint` is rejected together with `--band-fetch`, because a banded run
renders out of enumeration order and its watermark would mean nothing.

The checkpoint is deleted when the whole range completes with no failures — the
file exists to describe unfinished work.

### Operational rule

**Bulk runs write folders, then `convert` to MBTiles.** Folder output is the
crash-tolerant target: a killed run loses at most one partially written PNG, and
the next run skips everything already on disk with a `stat` per tile. MBTiles
resume now works too (defect 2 above), so this is a preference rather than a
requirement — but the folder path stays the recommendation for a multi-day run,
because it does not hold a SQLite write transaction open across the whole batch.

**Give a multi-day run `--checkpoint`.** Skip-existing alone makes a resumed
Germany z0-14 run pay 317,618 existence probes before its first new tile; the
checkpoint turns that into skipping a counted prefix. The two are complementary,
not alternatives: the checkpoint decides what is not even offered, skip-existing
still guards everything that is.

## 2. Vector tile input: rejected

Reading pre-built MVT (Mapbox Vector Tiles) instead of querying Overpass was
evaluated and rejected. Two reasons, and only these two.

### 1. MVT is pre-clipped, and this project already diagnosed that failure

This is the decisive reason.

MVT encodes geometry clipped to the tile with a small buffer — commonly 64 units
at extent 4096, i.e. about 4 px at a 256 px tile. This project's padding is not a
constant: `watercolor.RequiredPaddingPx` (`internal/watercolor/padding.go`)
derives it from the largest blur sigma in play (`3σ + 2`), floored at
`MinGeometryPaddingPx = 64`, and the renderer crops the padded metatile back
afterwards. 64 px is sixteen times the buffer MVT typically carries, and the
floor exists precisely because polygons that cross a tile boundary need room.

The project has already been here. `internal/datasource/overpass.go:267-282`
records what happened when clipping was tried at the source:

```go
// WARNING: The clipGeomToBbox option is available but should NOT be used due to a known
// Overpass API bug (https://github.com/drolbr/Overpass-API/issues/417) where "out geom(bbox)"
// returns malformed/wrapped geometry for partially-included ways. Visual testing confirmed
// severe rendering artifacts (distorted/wrapped polygons). Only use if the Overpass bug is fixed.
```

The query builders fetch complete, unclipped geometry (`out geom qt`) for exactly
this reason, and 38 golden files pin that behaviour.

**MVT reintroduces by design the failure this project already diagnosed and
rejected at the source.** Not as a bug to be fixed upstream — as the format's
defining property.

### 2. Mapnik 3.1 ships no `mvt` input plugin

Verified against `/usr/lib/mapnik/3.1/input/`, which contains: `csv`, `gdal`,
`geojson`, `ogr`, `pgraster`, `postgis`, `raster`, `shape`, `sqlite`, `topojson`.
No `mvt`.

So the MVT path would be: decode the tile in Go, re-emit GeoJSON, write it to a
temp file, and hand it to Mapnik through `type=ogr` — which is exactly the path
that exists today (`internal/renderer/multipass.go:220-234`). It buys nothing on
the render side. The only thing it changes is where the bytes come from, and that
is the thing § 1 covers better.

### Not a determinism argument

To close this off before somebody re-raises it: **MVT would not affect
determinism.** Noise, texture offsets and mask processing are anchored to world
pixel coordinates, not to tile identity (see AGENTS.md, "Determinism matters", and
`docs/3.2-noise-consistency-across-tiles.md`). Feeding the renderer tile-shaped
input would not make adjacent tiles disagree — the clipping would, but that is
reason 1, and it is a geometry problem, not a determinism problem. Arguing
determinism here is arguing the wrong case and invites a rebuttal that looks like
it settles the question.

This is also **not** PLAN.md § 5.10 "Vector Data Integration", which is a separate
open question about serving vector data for interactivity (hover, click, labels on
the client). Rejecting MVT as a _rendering input_ says nothing about that.

### What to do instead

The zoom range that actually needs different data is z0-5, and PLAN.md § 5.3
already names the answer: Natural Earth. OSM at z0-5 is both too detailed and
missing the generalised coastlines and country polygons a world view needs.

The mechanism is already built and proven — the ocean work did exactly this shape
of thing:

- Natural Earth ships shapefiles, and `shape.input` is installed.
- `shapeindex` sidecar generation is already automated
  (`Justfile:220-243`), and the comment there records why it matters: without the
  `.index` file Mapnik scans the whole shapefile for every tile instead of doing a
  bbox lookup.
- Zoom-based dataset selection is already implemented:
  `OceanConfig.ShapefileForZoom` (`internal/renderer/ocean.go:47-78`) picks the
  simplified water polygons up to `DefaultSimplifiedMaxZoom = 9` and the full set
  above.

Filling § 5.3's z0-5 gap by copying the ocean pattern is the concrete piece of
work this section recommends, and it is the tier that §3's T1 depends on.

**This has since been implemented** (`internal/renderer/naturalearth.go`,
`assets/styles/naturalearth/`, `just fetch-natural-earth`), so the sentence that
used to stand here — "Natural Earth currently appears nowhere in code" — is no
longer true.

### 2.1 What the z0-5 tier turned out to be

`NaturalEarthConfig.CoversZoom` is the single predicate the renderer and the
pipeline both branch on, so the two cannot disagree about where a tile's data
comes from. 110m serves z0-2 and 50m z3-5, the same scale-by-zoom trade
`OceanConfig.ShapefileForZoom` already makes; z6+ is unchanged OSM, and z6-9
needed no restyling because those rules were already coarse.

There is no separate "generalized" style. The tier is a _source_ that carries
three layers — ocean, lakes, rivers, from `assets/styles/naturalearth/*.xml`.
Roads, buildings, parks, civic, urban and railroads resolve to no shapefile and
are therefore absent, and that absence _is_ the world-scale look. Land keeps
painting, since it is the background fill.

Three corrections and findings the doing produced, in rough order of how easy
they are to get wrong again:

- **Natural Earth is EPSG:4326, the water polygons are 3857.** `layers/ocean.xml`
  can declare the map's own merc srs and reproject nothing; the Natural Earth
  styles must declare longlat and let Mapnik reproject. Copying the srs across
  from `layers/` is the obvious mistake and it fails silently.
- **A missing dataset costs its layer, not the tile.** `ShapefileForLayer` returns
  "" for a file that is not on disk, and stands the two scales in for each other
  when only one was downloaded — mirroring `OceanConfig`'s "a wrong-detail
  coastline beats an inverted one". `Validate` is what turns a mistyped
  _directory_ into a startup failure; the two cases are deliberately separate.
- **The z5 ceiling may be one or two zooms too low, and there is now evidence.**
  Measured while verifying this work: `generate --zoom 6` over the Niedersachsen
  extract fails with `overpass response exceeds size limit: over 67108864 bytes`.
  z6-z8 are therefore not merely slow from OSM, they are **unrenderable** against
  the 64 MiB cap. `max-zoom: 8` renders that band from Natural Earth instead and
  works today, so the escape hatch exists; what has not been decided is whether it
  should be the default, since 50m coastline is visibly generalised by z8.
  Deciding it needs a side-by-side at z6-8, not more code.

The fetch skip is enforced in three places, because three schedulers can reach a
tile before the generator does: `renderLayersWithData` (the generator itself),
the band producer in `internal/cmd/generate_bands.go`, and `serve`'s fetch queue
in `internal/server/ondemand_tiles.go`. All three branch on `CoversZoom`, so
`--band-min-zoom` and `natural-earth.max-zoom` may overlap freely without a
low-zoom Overpass query escaping.

## 3. Storage requirements

### Measured: what a tile actually costs

All PNG figures below come from the 689 base tiles and 511 `@2x` tiles in `tiles/`
on this branch (`find tiles -name '*.png'`, sizes in bytes), plus a dedicated
measurement run for the out-of-region and WebP figures.

| statistic          | base PNG  | `@2x` PNG |
| ------------------ | --------- | --------- |
| count              | 689       | 511       |
| mean               | 122,327 B | 361,416 B |
| median             | 122,940 B | —         |
| min                | 84,566 B  | —         |
| max                | 144,122 B | —         |
| ratio to base mean | 1.00      | **2.95×** |

Two things fall out of this that a naive model gets wrong.

**Tile size is essentially zoom-independent.** The spread across the whole sample
— which spans z5 to z17 — is min 84 KB to max 144 KB, a factor of 1.7, with the
median sitting on the mean. There is no "cheap low zoom".

**`@2x` costs about 3× the base tile, not 4×.** Four times the pixels, but the
watercolor output is smooth enough that PNG's filters recover some of it. Useful
when sizing, and the reason the tables below use 375 KB rather than 500 KB.

### Measured: there are no cheap empty tiles, and this was tested properly

The Hanover sample is all city, so it could not settle whether a tile with no
features is cheap. Two tiles outside the region were rendered specifically to
test it:

| tile                       | content                     | PNG size      | vs Hanover mean |
| -------------------------- | --------------------------- | ------------- | --------------- |
| `z6 x3 y32` (mid-Pacific)  | open ocean                  | **127,078 B** | **above** it    |
| `z12 x2195 y1778` (Sahara) | literally zero OSM features | **108,020 B** | 88%             |

The Sahara tile renders as nothing but blank paper texture. It still costs 108 KB.

The reason is structural, not incidental: the watercolor texture and the Perlin
noise fill **every pixel** of every tile. There is no flat region for PNG's
filters and DEFLATE to collapse. An "empty" tile is a full-frame photograph of
paper. This is a direct consequence of the look the project exists to produce, so
it is not going to be optimised away — it has to be planned around.

Practical consequence: **do not model a global tileset as "mostly ocean, mostly
free".** Ocean tiles are, if anything, slightly above average.

### Measured: WebP q80 is a 9.2× reduction — but the shipped encoder is lossless

> **Read § "Two policy levers" before quoting this table.** The 9.24× below is
> **lossy** q80. The encoder that actually shipped is lossless and measures
> **1.21×** on the same content. Both numbers are real; they are answers to
> different questions.

Method: 20 tiles spanning z5-z17, encoded with `github.com/chai2010/webp` v1.4.0,
lossy, quality 80, and cross-checked byte-identical against Pillow 10.3.0 at the
same settings.

| statistic            | value                     |
| -------------------- | ------------------------- |
| mean PNG             | 124,227 B                 |
| mean WebP q80        | 13,446 B                  |
| ratio                | 0.108                     |
| **reduction**        | **9.24×**                 |
| per-zoom ratio range | 0.059 (z5) to 0.142 (z16) |

Round-trip damage, measured per channel against the source PNG: mean deviation
about 4/255; maximum 17-119, concentrated on hard colour edges — road casings
against paper — where 4:2:0 chroma subsampling bites. At 1:1 the difference is not
visible.

Two things make lossy safe here specifically:

- **There are no labels to smear.** The Phase 4 label-free policy means the worst
  case for lossy encoding — small high-contrast text — does not occur.
- The output is already soft-edged by design. Blur and noise are the whole point.

And the finding that matters most for § 3's totals: **the empty Sahara tile drops
to 6,006 B — a ratio of 0.056.** Lossy encoding is what finally makes an empty
tile cheap. PNG never does, at any compression level, because the noise is real
signal.

### Measured: throughput is ~0.3 renders/s

From [docs/local-overpass.md](local-overpass.md): a 3×3 smoke block, base plus
`@2x`, takes ≈60 s against the local Overpass instance. That is 18 renders in 60 s
= **0.3 renders/s**, single machine, whole pipeline including the fetch.

**PLAN.md § 5.11.1's "~86 ms/tile" is not used anywhere in this document.** PLAN.md
itself disowns it at lines 255-257: the profile it came from predates the box-blur
work and the module rename. It is also a _render-only_ figure from a benchmark with
no datasource attached, which — given § 4 — makes it wrong by roughly an order of
magnitude as a throughput estimate for a real run. Using it would make every
schedule below look 25× better than it is.

### Computed: how many tiles, and how many bytes

Rates: 125 KB per base tile, 375 KB per `@2x` tile (rounded up from the measured
122 KB / 361 KB). "cum +@2x" is base plus `@2x` = 500 KB/tile. Counts are exact
tile-index ranges, cross-checked against the repository's own `tile.TileCount`.

**Global**

| z   | tiles at z  | cumulative tiles | cum 1×      | cum +@2×    |
| --- | ----------- | ---------------- | ----------- | ----------- |
| 0   | 1           | 1                | 125 KB      | 500 KB      |
| 4   | 256         | 341              | 42 MB       | 166 MB      |
| 6   | 4,096       | 5,461            | 667 MB      | 2.6 GB      |
| 8   | 65,536      | **87,381**       | **10.4 GB** | **41.7 GB** |
| 9   | 262,144     | 349,525          | 41.7 GB     | 166.7 GB    |
| 10  | 1,048,576   | 1,398,101        | 166.7 GB    | 666.7 GB    |
| 11  | 4,194,304   | 5,592,405        | 666.7 GB    | 2.6 TB      |
| 12  | 16,777,216  | **22,369,621**   | **2.6 TB**  | **10.4 TB** |
| 13  | 67,108,864  | 89,478,485       | 10.4 TB     | 41.7 TB     |
| 14  | 268,435,456 | 357,913,941      | 41.7 TB     | 166.7 TB    |

**Regional**, Niedersachsen (6.6, 51.3 → 11.6, 53.9) and Germany
(5.87, 47.27 → 15.04, 55.06):

| z   | NS at z | NS cum     | NS cum 1×  | DE at z   | DE cum      | DE cum 1×   |
| --- | ------- | ---------- | ---------- | --------- | ----------- | ----------- |
| 8   | 20      | 37         | 4.5 MB     | 70        | 105         | 12.8 MB     |
| 10  | 195     | 288        | 35 MB      | 999       | 1,370       | 167 MB      |
| 12  | 2,793   | 3,806      | 465 MB     | 15,158    | 20,344      | 2.4 GB      |
| 13  | 11,172  | 14,978     | 1.8 GB     | 59,850    | 80,194      | 9.6 GB      |
| 14  | 44,688  | **59,666** | **7.1 GB** | 237,424   | **317,618** | **37.9 GB** |
| 15  | 178,296 | 237,962    | 28.4 GB    | 948,560   | 1,266,178   | 150.9 GB    |
| 16  | 710,580 | 948,542    | 113.1 GB   | 3,790,900 | 5,057,078   | 602.9 GB    |

Multiply the 1× column by 4 for base + `@2x`: Germany z0-14 is 37.9 GB at 1×,
151.5 GB with `@2x`.

**On the counting method.** These are exact tile-index ranges, which is what
`generate --bbox` actually enumerates. An area-based estimate (bbox area in
Mercator × 4^z) undercounts, because the tile range is inclusive of the partially
covered boundary tiles. Measured discrepancy of area-estimate against exact:

| bbox                   | z10   | z12   | z14   | z16   |
| ---------------------- | ----- | ----- | ----- | ----- |
| Germany                | −7.4% | −2.4% | −0.3% | −0.1% |
| Niedersachsen          | −11%  | −0.8% | −0.8% | −0.1% |
| a 0.3°×0.15° city bbox | −70%  | −40%  | −15%  | −5%   |

For a country the two methods converge above z12 and either is fine. **For a
city-sized bbox the area method is unusable** — 15% low at z14 and worse below —
so always count tiles, never estimate area. That is also why the `tiles/` sample
above is a mixed-zoom scatter and not a contiguous block; it is a size sample, not
a coverage count, and should not be read as one.

### The key framing: the storage cliff and the compute wall are not the same wall

The storage cliff is **z11 → z13**: 667 GB, 2.6 TB, 10.4 TB at 1×. That is where a
global tileset stops fitting on hardware you would casually buy.

But **compute binds two zooms earlier.**

Global z0-12 with `@2x` is 22,369,621 × 2 = **44,739,242 renders**. At the measured
0.3 renders/s that is **1,726 days — 4.7 years** on one machine. Even at 10
renders/s, a rate nothing in this project has demonstrated (33× current, and § 4
explains why it is not a rendering problem to begin with), it is **52 days
continuous**.

So for a _global_ tier the binding limit is around **z8**, and the constraint is
compute, not disk. z0-8 is 10.4 GB — trivially storable. It is the 175k renders,
about **7 days**, that sets the ceiling.

### Recommendation: three tiers

- **T1 — global z0-8, pre-rendered.** 87,381 tiles; 10.4 GB at 1×, **41.7 GB**
  with `@2x`; 174,762 renders ≈ **7 days** at current throughput. This is the tier
  Natural Earth (§ 2) should feed, since OSM is the wrong source below z6 anyway.
- **T2 — regional z0-14, per country.** Germany: **317,618 tiles, 37.9 GB** at 1×
  (151.5 GB with `@2x`); 12.3 days of rendering at 0.3/s. Niedersachsen: 59,666
  tiles, 7.1 GB, 2.3 days.

  **z15 is where a region stops being feasible.** Germany z0-15 is 1,266,178 tiles
  — 48.8 days, seven weeks, at 0.3/s — even though the 151 GB (604 GB with `@2x`)
  is entirely buyable. z15 alone is 948,560 tiles, 5.2 weeks. Disk is not what
  stops you.

- **T3 — on-demand above T2's ceiling, backed by the persistent PNG cache.** This
  already works: `internal/server/ondemand_tiles.go` renders a missing tile on
  request, writes it to the tile directory, and serves subsequent requests from
  disk (`serveCachedTile`, gated on `DisableCache`), with per-tile locking,
  per-IP rate limiting and a bounded admission queue in front of it.

The choice of the T2/T3 boundary N is a tuning decision. It is also, as the next
section says, not the biggest lever available.

### Two policy levers larger than the choice of N

They are listed here so they are decided deliberately rather than discovered
late. **Lever 1 is now implemented; lever 2 is still a recommendation.**

**1. Make `@2x` on-demand only.** ✅ **Done.** A batch run used to render every
tile twice — `runHiDPIBatch` was a full second pass over the same tile list — and
`@2x` is 3× the bytes _and_ a second complete render. Dropping it from the bulk
tier was **4× the storage and 2× the compute from a single policy line**:
Germany z0-14 goes from 151.5 GB and 24.6 days to 37.9 GB and 12.3 days. Retina
clients get `@2x` from T3 on demand, which `serve` has always been able to do.

`generate --bbox --hidpi` is now an error rather than a silently smaller run, and
`--hidpi` survives for single tiles, where it costs one extra render and is the
easiest way to check that a `@2x` tile and its base show the same road classes at
the same ground width.

**2. Adopt WebP.** 🟡 **Shipped, but lossless — so the number is 1.21×, not 9.24×.**

`--image-format webp` now encodes and serves WebP end to end. The encoder is
`HugoSmits86/nativewebp`: pure Go, no cgo, which keeps `GOOS=js` and the
cross-platform release matrix building with no build tags. The cost of that
choice is that it is **VP8L only, i.e. lossless**, and the 9.24× above is a
_lossy_ q80 figure.

Re-measured on the same 689 base tiles in `tiles/`, lossless:

| statistic                   | value       |
| --------------------------- | ----------- |
| mean PNG                    | 122,326 B   |
| mean WebP (lossless)        | 101,181 B   |
| ratio                       | 0.827       |
| **reduction**               | **1.21×**   |
| tiles where WebP was larger | 0 of 689    |
| per-zoom ratio range        | 0.819–0.858 |

The gap between 9.24× and 1.21× is the same fact that produced "no cheap empty
tiles": the texture and noise fill every pixel, so there is nothing for a
lossless codec to collapse. A lossless codec can only exploit the _correlation_
in that field, not discard it. (Worth knowing for anyone writing a test around
this: on pure per-pixel uniform noise, lossless WebP is actually **larger** than
PNG. Real tiles are smoother than that, which is why the real direction holds.)

So the storage arithmetic is much weaker than planned: global z0-12 at 1× goes
from 2.6 TB to ~2.15 TB, not to 289 GB; Germany z0-14 from 37.9 GB to ~31.4 GB.
Encoding is also ~4× slower than PNG (52 ms vs 14 ms per 256px tile), which is
under 10% of a ~576 ms render but is a cost rather than the ~50% saving
nativewebp's own README advertises for ordinary images.

**The lossy lever is still available and still worth 9.24×.** The encoder is now
an interface (`internal/tileformat.Encoder`), so a cgo lossy backend is one
additional implementation rather than another pass over the whole codebase. What
it would cost is cgo in the js/wasm and `CGO_ENABLED=0` paths, and a decision
about round-trip damage (~4/255 mean per channel at q80). That decision has not
been made; this is the point at which to make it deliberately.

Applied together, a global z0-12 tier is ~2.15 TB instead of 10.4 TB — of which
the `@2x` lever supplies almost all of the saving. It is still 4.7 years of
compute. Which is the point: **storage is a solved problem and compute is not**,
and no amount of choosing N changes that.

## 4. Data update pipeline

### Measured: where the time actually goes

Tile `z13 x4317 y2692` (Hanover) against the local Overpass instance, 6 runs.
Stage timings taken from the structured-log deltas and independently cross-checked
with a pass-through HTTP proxy in front of Overpass; the two methods agreed within
0.06 s.

| stage                       | mean        | sd      | share of wall | share of per-tile work |
| --------------------------- | ----------- | ------- | ------------- | ---------------------- |
| process startup (one-off)   | 0.31 s      | —       | 9.8%          | — (amortised in batch) |
| **Overpass fetch**          | **2.241 s** | 0.129 s | **71.0%**     | **79.3%**              |
| Mapnik passes + watercolour | 0.576 s     | 0.044 s | 18.2%         | 20.4%                  |
| PNG encode + write          | 0.011 s     | —       | 0.3%          | 0.4%                   |
| **total**                   | **3.159 s** | —       | 100%          | —                      |

The per-tile-work column excludes the 0.31 s process startup, which happens once
and is amortised away in a batch run. That column is the one that matters for
scaling.

The transfer itself, for that one tile: **2,733 bytes of query out, 3,189,067
bytes of JSON back**, one request per tile, to produce a ~131 KB PNG. Roughly 24×
more bytes fetched than emitted.

### This is the headline number of the whole document

**Fetch dominates. Source-side wins matter more than render optimisation.**

A response cache, metatile-band fetching (one Overpass request covering a strip of
tiles instead of one per tile), or a local PostGIS with an indexed spatial scan all
attack the 79%. Faster blur, faster compositing and faster PNG encoding all attack
the 20%.

Stated carefully, because it would be easy to overread: PLAN.md § 5.11's entire
optimisation roadmap targets that 20% slice. That is **not wrong** — the blur
rewrite was real, measured, and 2-11× (see
[docs/performance/blur-optimization.md](performance/blur-optimization.md)), and for
a cached or PostGIS-backed run the render side becomes the whole cost. It is simply
not where the time is for a _bulk run against a live Overpass_, which is the
scenario this document is about. Both statements are true at once; the roadmap
should be read as "what to do after the fetch is fixed", not as "what to do first".

### Band fetching exists on this branch

`--band-fetch`, **off by default**. One Overpass query per square block of
same-zoom tiles instead of one per tile, which is the second of the two levers
against the 79% (the first being the cache below).

The arithmetic: `out geom` returns unclipped geometry, so a motorway crossing a
4×4 block is transferred sixteen times by per-tile fetching. At the default
level of 2, Germany's 237,424 z14 queries become roughly 15,000.

**Why 4×4 and not the 8×8 the plan item assumed.** A band's response scales with
_area_, and the measurement above gives the constant: one padded z13 tile
returned 3,189,067 bytes. Sixty-four of those is 100-200 MB — past
`DefaultMaxResponseBytes` (64 MiB) and past any sensible server-side timeout.
4×4 is roughly 12 MB, which fits with room to spare.

**Why there is no zoom ceiling.** § 5.1a said band fetching "must stop at z15",
because the building rules are not monotone across z16. That constraint is real
but belongs to a different technique: it invalidates reusing a _parent's_ data
for its _children_, across zooms. Band fetching groups tiles of the **same**
zoom, and `buildTileQuery` selects its rules from the zoom alone, so every tile
in a band emits identical query text apart from the bbox literal. The real guard
is adaptive: any band failure — oversized response, timeout, or an area that no
single configured server covers — splits the band into quadrants and retries
them independently, bottoming out at ordinary per-tile fetches. A tile that
genuinely cannot be fetched therefore still fails as itself, with the error it
always had, rather than taking fifteen neighbours down with it.

**Why the data is sliced per tile before rendering.** This is the part that makes
it behaviour-preserving rather than merely cheaper. The renderer skips a layer
entirely when it has zero features, producing an _absent_ layer rather than a
blank one, and those take different paths through painting. Handing a tile its
neighbours' features would flip absent into present-but-blank. Filtering each
tile's slice to its own fetch bounds restores that exactly, and cannot lose
anything: Overpass matches on geometry intersection, which implies bbox
intersection, so the filter is a superset of what a per-tile query would have
returned. The slices share the band's underlying geometry, so this costs feature
headers rather than a copy.

The emptiness check stays per tile for the same reason — it is a per-tile policy,
and a band that is non-empty overall would mask a genuinely empty member while an
empty band would fail every member at once. An empty slice inside z8–13 falls
back to a real per-tile fetch.

`internal/pipeline/band_equivalence_test.go` pins the outcome: a band-fetched
tile renders **byte-identically** to a per-tile one. Not within a tolerance —
identically — on data that genuinely differs (9 features in the band, 6 in the
slice).

One hazard found while building it, and closed: `MultiOverpassDataSource` routes
on bbox _intersection_, so a band box could be answered by a regional server
holding data for one corner of it and nothing else — sixteen tiles silently from
the wrong source. Band routing requires **containment** and splits otherwise.

**Off by default** for the same reason the cache is: it changes what the upstream
sees. The query shape and size change, and an operator running against a shared
or rate-limited instance has to opt into that. Output does not change, which is
what the equivalence test is for.

### The Overpass response cache exists on this branch

`internal/datasource/cache.go` and `internal/datasource/cache_transport.go`, with a
`cache:` block documented in `config.example.yaml`.

What it does:

- Stores **verbatim upstream response bytes**, gzipped, at
  `<dir>/<endpointHash8>/<key[0:2]>/<key>.json.gz`, keyed by endpoint + query text.
  Storing the raw bytes rather than a parsed structure means a hit and a miss feed
  the go-overpass decoder identical input, and there is no second serialisation
  format to keep in sync.
- Lives at the `http.RoundTripper` layer (`cachingTransport`), under go-overpass
  and under `limitedTransport`, so retry, the response-size cap, the worker
  semaphore and `MultiOverpassDataSource` routing all keep working untouched.
  Only JSON-shaped 200s are stored; 429s, 504s and HTML error pages pass through
  unstored so the retry logic keeps seeing them.
- Defaults: `cache/overpass`, 7-day TTL by file mtime, 5 GB budget with
  **oldest-written-first** eviction (one synchronous sweep at open, background
  thereafter). Deliberately not LRU: `Get` does not touch the entry, so a
  frequently read old entry is still evicted before a newly written one. Making it
  a true LRU would mean stamping mtime on every hit, and mtime is also what the
  TTL reads — refreshing it on access would make an entry immortal. Tracking
  access separately is possible but has not been needed; the working set is a
  bbox-and-zoom range, which ages out together.
- Nothing that looks like a failure is stored: only JSON-shaped 200s carrying an
  `elements` array, and never a body carrying a `remark`, which is how Overpass
  reports a timeout while still returning what it collected. `store-empty` is off
  by default, because a 200 with zero elements is also what a silently failing
  instance looks like, and freezing either for a week would turn one bad minute
  into a week of subtly wrong tiles.
- Every read-path failure is a miss, never a failed render.
- Read in exactly one place, `createOverpassDataSource` (`internal/cmd/serve.go:381`),
  which is what structurally prevents the one-command-honours-the-block bug that
  `overpass:` used to have.

**It is off by default, deliberately, because it changes freshness semantics.** A
cached run can render week-old OSM data. That is exactly right for a reproducible
batch and exactly wrong for a `serve` instance somebody expects to reflect recent
edits, and the project cannot pick for you.

**Measured consequence worth knowing:** the `@2x` Overpass query is
**byte-identical** to the 1× query. `RequiredPaddingPx`
(`internal/watercolor/padding.go`) does the whole padding calculation in world
pixels and scales to device pixels only at the end, so `pad(2x) == 2 * pad(1x)`
exactly and the two metatiles cover the same ground. That property is pinned
structurally by `internal/watercolor/scale_test.go:TestRequiredPaddingProportional`
(zooms 10-18). Since the cache key is endpoint + query text and nothing about tile
identity enters it, **an on-demand `@2x` render is served entirely from the base
pass's cache entries** and costs no Overpass traffic at all.

(This was originally recorded as "the cache halves Overpass load on a `--hidpi`
run". With lever 1 above implemented there is no second batch pass to halve; the
same property now pays off on the on-demand path instead.)

**Measured end to end** (4 tiles, z12 Hanover, local Overpass, folder output):
cold cache 5.60 s (0.8 tiles/s), warm cache 1.71 s (2.8 tiles/s) — a **3.3×
speedup**, which is about what a 71%-fetch workload predicts once the fetch goes
to zero.

**On the determinism of that result — since fixed.** When this was measured, the
cached run's tiles were _not_ byte-identical to the uncached run's — but neither
were two uncached runs to each other. Same tile, same data, mean per-channel
deviation across the three comparisons:

| comparison           | mean  | max | channels differing |
| -------------------- | ----- | --- | ------------------ |
| uncached vs uncached | 0.014 | 36  | 0.01%              |
| cached vs cached     | 0.016 | 31  | 0.01%              |
| uncached vs cached   | 0.017 | 36  | 0.01%              |

The near-equality of the three rows was the finding: the cache added no variation
beyond what the pipeline already had. The source was
`ExtractFeaturesFromOverpassResult` ranging over `map[int64]*…` unsorted, so draw
order flipped on a handful of antialiased edges. That extraction now walks both
element maps in **ascending OSM ID order** and the same tile renders
byte-identically twice.

The cache still stores **raw JSON** rather than the extracted `FeatureCollection`,
now for its own reasons rather than this one: no second serialization format to
keep in sync with the decoder, and no stored collection that could outlive a
change to the extraction rules.

Ascending OSM ID is arbitrary _as a painting order_; only its stability matters,
which is why `TestExtractFeatureOrderIsByOSMID` names the choice rather than
leaving a later refactor to swap it silently. The synthetic pipeline goldens are
unaffected (`syntheticDataSource` involves no map iteration); the two Hannover
cases move and need `just update-goldens-hannover` from a machine with network.

### Source freshness is nearly free, and is now recorded

The Niedersachsen container keeps itself current from Geofabrik minutely diffs
([docs/local-overpass.md](local-overpass.md)). So the _data_ is fresh within
minutes at essentially zero operational cost.

**Every tile rendered by `generate` now carries a source-data version.**
`generate` writes a stamp per tile into `internal/tilestamp`: the Overpass
`osm3s.timestamp_osm_base` of the response it rendered from, when it was
rendered, which endpoint answered, and which build of this binary produced it.
The store is a sidecar living with the tiles — an extra `tile_stamp` table
inside the `.mbtiles` file, or `stamps.db` beside `tilejson.json` in a tile
folder. `convert` carries the stamps of a tile folder over into the MBTiles file
it produces, so converting a tileset does not lose its provenance.

`serve` stamps as well: its on-demand generator
(`internal/server/ondemand_tiles.go`) is built with the store the server opened,
one store shared across every tile size, written through per stamp and closed on
the shutdown path. A tileset filled in by browsing is therefore selectable by the
same staleness flags as one produced by a batch run, and `serve --stale-*`
re-renders a cached tile whose stamp says it is out of date.

The timestamp is read from the response body rather than taken from the clock,
which matters as soon as the response cache is on: a cache hit reports the age
of the _data_, not the age of the fetch.

Skip-existing therefore no longer has to mean "does this tile exist". With
`--stale-data-before`, `--stale-rendered-before` or `--stale-renderer-rev`, it
means "is this tile still good", and "re-render everything older than the last
import" is expressible. Without those flags the behaviour is unchanged, and an
uncertain case — no stamp, an unreadable stamp, an unparseable timestamp — always
renders, because a wrong skip leaves a permanent hole.

### Recommended: two layers, neither honest alone

**Layer 1 — incremental expiry from the diffs.**

Take the daily `.osc.gz`, union the bounding boxes of the changed nodes in one
`osmium` pass, expand each by the metatile pad, and hand the resulting boxes to
`watercolormap purge --bbox … --yes`; the next scheduled run's skip-existing
refills them. Cheap, and it uses machinery that already works.

**The real gap, stated plainly: this misses tag-only edits.** A way whose nodes are
untouched but whose tags change — a forest retagged, a road reclassified from
`secondary` to `primary`, a building given a different `building=` value — produces
no changed node and therefore no bbox, so its tiles are never expired. These are
common edits and they are exactly the ones that change how a tile looks.

Doing this properly requires a node-location store, so that a way's geometry can be
recovered from a tag-only change. That is precisely what `osm2pgsql --expire-tiles`
maintains and emits. **This is the second, independent argument for PostGIS** — the
first was bulk-scan performance in § 1. Two unrelated problems with the same
answer is worth noticing.

**Layer 2 — periodic full regional re-render, as a backstop.**

Cadence **derived from measured throughput**, not picked because it sounds tidy:

| scope               | renders | at 0.3/s  | realistic cadence                            |
| ------------------- | ------- | --------- | -------------------------------------------- |
| Germany z0-13       | 80,194  | 3.1 days  | monthly (with the cache on)                  |
| Germany z0-14       | 317,618 | 12.3 days | quarterly at best, until throughput improves |
| Niedersachsen z0-14 | 59,666  | 2.3 days  | monthly                                      |

If z0-14 monthly is a requirement, the throughput has to improve first, and § 4's
measurement says where to get it: the fetch. Do not schedule against a cadence the
machine cannot hold — a re-render that is still running when the next one is due is
worse than no schedule.

### Open decisions, recorded not resolved

- **Snapshot consistency during a multi-day batch.** A Germany z0-14 run takes 12
  days; the source updates minutely throughout. Either accept the drift (tiles at
  opposite ends of the run reflect data 12 days apart, which is invisible in
  practice) or pause the diff updater for the duration (a consistent snapshot, at
  the cost of a long catch-up afterwards). Not decided. Enabling the response cache
  partially forces the first choice, since a cached response freezes that tile's
  input at fetch time.
- **Expiry is now a command.** `watercolormap purge` deletes from a tile folder or
  an MBTiles file, selecting by `--bbox`, zoom range and `--suffix`, or by
  staleness (`--data-before`, `--rendered-before`, `--renderer-rev-not`) read from
  the stamps. It is a dry run unless `--yes` is given, and always prints the count
  and a sample first. What is still done by hand is deriving the bounding boxes
  from a diff — see the layer-1 note above.
- **MBTiles can be updated in place, in both directions.**
  `internal/mbtiles/writer.go` uses `INSERT OR REPLACE INTO tiles`, so overwriting
  an existing tile works; `HasTile` (§ 1) is the read side skip-on-resume needs;
  and `DeleteTile`/`DeleteTiles`/`Vacuum` are the delete side purge needs. A tile
  and its stamp are deleted in one transaction, so a stamp can never outlive the
  tile it describes and make a later run skip a hole.

## 5. A finding worth its own section: the public API 406 is not rate limiting

The measurement run for § 4 turned up the root cause of something this project has
believed for a long time and built policy around.

[docs/local-overpass.md](local-overpass.md) used to open by saying that against the
public `overpass-api.de`, tile fetching is "slow, rate-limited, and at the moment
answers `406 Not Acceptable` outright, which makes the whole integration suite
unrunnable." The 406 had been read as an aggressive form of rate limiting.

**It is not rate limiting.** `overpass-api.de` rejects requests carrying Go's
stdlib default `User-Agent: Go-http-client/1.1` — and requests with an empty UA —
with 406 Not Acceptable. The byte-identical query issued from `curl` returns 200 in
about 0.5 s.

The cause is one missing header in the dependency.
`github.com/cwbudde/go-overpass`'s `httpPost` (`query.go:55-94` in
`v0.0.0-20260418190031-ddf15fac5067`) sets exactly one request header:

```go
req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
```

No `User-Agent`, so `net/http` fills in its default and the server refuses it.

**Evidence.** The client's requests were routed through a pass-through proxy that
added an honest `User-Agent` and changed nothing else — same endpoint, same query
bytes, same method. Both test tiles then fetched **on the first attempt**, in
approximately 5 s and 2 s. No retries, no backoff, no 406.

Three consequences:

1. **It is a small fix, and it did not need the dependency** — set a `User-Agent`
   identifying the project and a contact URL, which is what the Overpass usage
   policy asks for anyway. Patching go-overpass looked like the obvious route, but
   the header can be set below the client instead: `internal/datasource/useragent.go`
   adds a `RoundTripper` beside the existing limit and cache transports, which also
   covers the per-server clients `MultiOverpassDataSource` builds and needs no
   dependency release. Overridable via `overpass.user_agent`.
2. **It makes the public API a viable fallback again.** Every routing
   recommendation in § 1 assumes a nil-coverage public entry that actually works —
   for tiles outside the regional box, and (once § 1's defect 1 is fixed) as
   failover when the local container is down. That fallback was dead on arrival,
   which quietly made the "no failover" defect worse than it looked; with the
   `User-Agent` in place it works, so only defect 1 still stands between it and
   real failover.
3. **`docs/local-overpass.md` needed correcting**, and has been. Its framing was
   not just incomplete, it was misleading: it attributed to server-side rate
   limiting something that was a client-side bug in this stack, which means nobody
   looked for the fix. The local instance is still worth having — 2 s versus 5 s
   per fetch, no shared quota, no politeness budget — but "the public API does not
   work" and "the public API is slower" are very different operational facts.

## What 5.1 does not close

§ 5.1's five bullets are answerable and answered above. Two neighbours are not.

**§ 5.7 Data Update Pipeline stays open.** The update _design_ is closable — § 4
gives the two-layer design, its measured cadences, and names its real gap. The
_capability_ is not: there is no purge command, and no data-version stamp on a
tile, so the design above cannot actually be implemented today without writing
both.

**§ 5.3 Multi-Zoom Generation is closed.** § 2 handed it a concrete plan
(Natural Earth via `shape.input`, following the ocean pattern) and that plan has
since been carried out; see [zoom-levels.md](zoom-levels.md).

Follow-ups this work surfaced, roughly in priority order:

1. **Failover in `MultiOverpassDataSource`** (§ 1, defect 1) — try the next
   matching server, and the nil-coverage fallback, before failing a tile. Blocks
   any multi-day run. Warning on unreachable nested coverage boxes belongs in the
   same change.
2. **`User-Agent` in go-overpass** (§ 5) — one line; makes the public fallback real,
   and makes 1 worth having.
3. **Measure the Germany import** and replace § 1's estimated disk/RAM/init table
   with `du -sh` and a wall time.
4. **Decide the `@2x` policy** (§ 3) — on-demand-only is 4× storage and 2× compute
   from one line.
5. **WebP encoding + serving** (§ 3) — done, but lossless: 1.21× measured, not the
   9.2× a lossy encoder would give. The only thing that makes an
   empty tile cheap.
6. **Natural Earth for z0-5** (§ 2, § 3's T1) — **done.** The ocean pattern was
   copied as recommended: `NaturalEarthConfig` (`internal/renderer/naturalearth.go`)
   selects 110m below z3 and 50m up to z5, three styles under
   `assets/styles/naturalearth/` read them through `shape.input`, and
   `just fetch-natural-earth` downloads and `shapeindex`es them. The part worth
   knowing beyond the recommendation: the generator **skips the Overpass fetch
   entirely** below the ceiling, so the low tier renders offline. See
   [zoom-levels.md](zoom-levels.md).
7. ~~**Tile data-version stamp and a purge command** (§ 4)~~ — done. Stamps are
   written by `generate` into `internal/tilestamp`; `watercolormap purge` selects
   on them. What remains of the update policy is the diff-to-bbox step, which is
   layer 1 above and needs `osm2pgsql --expire-tiles` to handle tag-only edits.
8. **Streaming task enumeration in `worker.Pool`** (§ 1, defect 3) — not needed for
   Germany, needed beyond it; preserve the `len(results) == len(tasks)` invariant.

The 406 framing in `docs/local-overpass.md` and PLAN.md § 7.9's stale
`generate` claim were both corrected in the change that introduced this document,
so neither is listed above.
