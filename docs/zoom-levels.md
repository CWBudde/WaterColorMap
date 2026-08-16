# Zoom level characteristics

What changes as you move up and down the zoom stack: which features are fetched,
which datasets answer, how the render behaves, and where the boundaries actually
are. Written to close PLAN.md § 5.3's "document zoom level characteristics".

Everything here is read out of the code, with the file reference beside it. If
this document and the code disagree, the code is right and this document is a
bug.

## 1. What each zoom fetches

The tile query is assembled from five rule tables — water, parks, roads,
railroads, buildings — in that order (`featureLayers`,
`internal/datasource/overpass.go:444`). Each rule carries a `minZoom` / `maxZoom`
window and is emitted only where it applies (`featureRule.appliesAt`,
`overpass.go:410`). **The zoom alone selects the rules; the bbox only sets the
extent** (`buildAreaQuery`, `overpass.go:356`).

| zoom  | water                       | parks                            | roads                                    | railroads      | buildings                         |
| ----- | --------------------------- | -------------------------------- | ---------------------------------------- | -------------- | --------------------------------- |
| 0–4   | coastline, `natural=water`  | `landuse=forest`, `natural=wood` | —                                        | —              | —                                 |
| 5–7   | ″                           | ″¹                               | motorway                                 | —              | —                                 |
| 8–9   | ″                           | + park, nature_reserve, heath    | + trunk, primary                         | rail (z9+)     | —                                 |
| 10–11 | + `waterway=river` (z10–11) | + `landuse=grass`                | ″                                        | rail           | urban landuse (z11+)              |
| 12–13 | + river\|canal              | ″                                | + secondary, tertiary                    | rail           | urban landuse                     |
| 14–15 | + river\|canal              | + garden, orchard, vineyard      | + residential, unclassified, living_str. | rail           | + civic amenities                 |
| 16    | all `["waterway"]`          | + playground, allotments         | all `["highway"]`                        | + light_rail   | **`["building"]`; landuse drops** |
| 17+   | ″                           | ″                                | ″                                        | + subway, tram | ″                                 |

¹ z5–7 gets forest and wood only; parks proper start at z8.

The z0–5 half of that table is conditional: with the `natural-earth` block
configured those zooms issue no Overpass query at all and render from
shapefiles instead. See § 1's last subsection.

Sources: `waterRules` (`overpass.go:462`), `parksRules` (`:479`), `roadsRules`
(`:514`), `railroadsRules` (`:546`), `buildingsRules` (`:561`). The 38 golden
files under `testdata/golden/overpass-query/` are two per zoom for z0–18: the
plain query (`z08.txt`) and its `-clipped` twin (`z08-clipped.txt`). Both pin
the identical rule selection; they differ in the last line only — `out geom qt;`
against `out geom(<bbox>) qt;`, the `clipGeomToBbox` mode
(`overpass_query_test.go:33-38`). So the table above is checkable: read
`z08.txt` rather than trusting this page.

Two things in that table are easy to get wrong:

- **Roads at z8 include `primary`.** An older comment described z8–9 as
  "motorway + trunk", but the shipped regex has always matched primary too and
  the goldens pin it. Do not "correct" it back (`overpass.go:508-513`).
- **The z16 buildings switch is not monotone.** Urban `landuse` polygons are
  fetched at z11–15 and then _stop_; `["building"]` takes over at z16. Every
  other rule table only ever adds. This is why reusing a **parent's** data for
  its children across zooms is invalid, while same-zoom band fetching stays valid
  — a band groups tiles of one zoom, which all emit identical query text apart
  from the bbox literal (`docs/data-scaling-strategy.md` § 4).

### z0–5 comes from Natural Earth, not from Overpass

The z0–5 rows above describe rules that, with `natural-earth` configured, are
never reached: no query is issued at those zooms at all.

The rules are still worth reading for _why_. At z0–4 the whole query is seven
lines — coastline, `natural=water` (way and relation), `landuse=forest`,
`natural=wood` (way and relation) — and z5 adds one more for motorways; verify
against `testdata/golden/overpass-query/z04.txt`. That is simultaneously **too
much**, since it asks a regional Overpass instance for every forest across a
quarter of the planet, and **too little**, since a world view needs generalised
coastlines that OSM does not carry.

`NaturalEarthConfig.CoversZoom` (`internal/renderer/naturalearth.go:88`) is the
single predicate every caller branches on, so none of them can disagree about
where a tile's data comes from. It short-circuits the fetch in four places,
because there are four ways to reach one:

- the generator, before the datasource is touched (`renderLayersWithData`,
  `internal/pipeline/generator.go:543`) — this is what covers `generate`,
  `generate --bbox` and every batch run at once;
- the band scheduler, which excludes covered zooms whatever `--band-min-zoom`
  says (`internal/cmd/generate_bands.go:112`); a band fetch happens _before_ the
  generator sees the tile, so it needs its own check;
- `serve`'s on-demand fetch queue (`internal/server/ondemand_tiles.go:654`) and
  its retry path (`:1042`).

The tile still renders. Every layer except land is routed to the Natural Earth
shapefiles instead (`renderLayer`, `internal/renderer/multipass.go:206`); land
is excluded because it is the background fill rather than a feature layer, so it
keeps painting at every zoom. The shapefiles go straight to Mapnik's `shape`
plugin, which does its own bbox lookup against the `.index` sidecar
(`renderShapefileLayer`, `multipass.go:337`) — the same mechanism as the ocean
pass, and no geometry work on the Go side.

Three datasets exist down there and no others (`naturalEarthDatasets`,
`naturalearth.go:46`):

| layer  | dataset                        | style                                   |
| ------ | ------------------------------ | --------------------------------------- |
| ocean  | `ne_*_ocean`                   | `assets/styles/naturalearth/ocean.xml`  |
| water  | `ne_*_lakes`                   | `assets/styles/naturalearth/water.xml`  |
| rivers | `ne_*_rivers_lake_centerlines` | `assets/styles/naturalearth/rivers.xml` |

Roads, highways, railroads, buildings, civic and parks resolve to no shapefile
and therefore render absent. No rule switches them off — they simply have no
source down here, and **that absence _is_ the world-scale look**. Ocean is folded
into water before masking exactly as it is at high zoom (`foldOceanIntoWater`,
`internal/pipeline/generator.go:650`), so masks, textures and composite order
downstream are unchanged.

The scale switches once, at z3: 110m serves z0–2, 50m serves z3 upward
(`DefaultNaturalEarthMidScaleMinZoom = 3`, `naturalearth.go:26`; `scaleForZoom`,
`:93`). That is the same trade the ocean pass makes at `DefaultSimplifiedMaxZoom`,
one scale step down — 110m is visibly coarse by z3, and 50m is detail nobody can
resolve at z0–2. If the preferred scale is not on disk the other one stands in
(`ShapefileForLayer`, `:127`), for the ocean pass's reason: a wrong-detail
coastline beats an inverted one. A missing dataset costs its layer, not the tile.

The ceiling is `natural-earth.max-zoom`, default 5
(`DefaultNaturalEarthMaxZoom`, `naturalearth.go:18`;
`internal/cmd/naturalearth_config.go:17`). Raising it is a real option and has
been measured: a z6 Overpass query over a regional extract exceeds the 64 MiB
response cap and fails the tile outright, so z6–z8 are not merely slow from OSM
but unrenderable, and `max-zoom: 8` renders that band from Natural Earth
instead. Whether that should be the default is unsettled, since 50m coastline is
visibly generalised by z8 (`docs/data-scaling-strategy.md` § 2.1).

**None of this is on unless you turn it on.** `Enabled` means only "a directory
is configured" (`naturalearth.go:71`), the zero value is disabled, and with the
`natural-earth` block absent every zoom goes through Overpass exactly as before
— which is the behaviour the z0–5 query rules above describe. Run
`just fetch-natural-earth` (~10 MB, both scales, `Justfile:267`) and point
`natural-earth.dir` at the result. The path is checked at startup rather than at
first use, so a mistyped directory — or one holding no `ne_*.shp` at all — fails
the run before the first tile instead of quietly producing an empty world
(`Validate`, `naturalearth.go:158`). An explicit `enabled: true` with no `dir` is
likewise an error, not a silent fall back to Overpass
(`naturalearth_config.go:45`).

## 2. Which dataset answers

| zoom | source                                                                          |
| ---- | ------------------------------------------------------------------------------- |
| 0–5  | Natural Earth shapefiles — Overpass only if the `natural-earth` block is absent |
| 6+   | Overpass                                                                        |
| 0–5  | ocean: `ne_*_ocean`, when Natural Earth is enabled                              |
| 6–9  | ocean: **simplified** water polygons                                            |
| 10+  | ocean: **full** water polygons                                                  |

The z0–5 ocean row is not a second pass laid over the first: below the ceiling
the ocean layer is intercepted with every other feature layer
(`multipass.go:206`) and never reaches `renderOceanLayer`, so the OSM water
polygons are not consulted there at all. Configuring ocean rendering and leaving
`natural-earth` out puts the water polygons back at z0–5.

The ocean pass is otherwise independent of the OSM query. OSM does map the
boundary, as `natural=coastline` ways — that is what the water rules above fetch
— but it carries no filled ocean polygon, so the open sea comes from the
processed water polygons at
<https://osmdata.openstreetmap.de/data/water-polygons.html>. The switch is
`DefaultSimplifiedMaxZoom = 9` (`internal/renderer/ocean.go:12`): above it the
simplified coastline is visibly coarse, below it the full set costs I/O for
detail nobody can see. Either dataset stands in for the other when only one is
configured — a wrong-detail coastline beats an inverted one
(`OceanConfig.ShapefileForZoom`, `ocean.go:55`).

## 3. Zoom-conditioned behaviour outside the query

These are the parts that surprise people, because none of them lives near the
rule tables.

**Empty responses are only an error at z8–13.** `validateFeatureResponse`
(`overpass.go:932`) treats "no features" as a failed fetch only inside that
window: over land at those zooms there is always _something_, so an empty
response means a timeout or a truncated answer rather than an empty world.
Outside it, empty is legitimate. Band fetching mirrors the same window in
`emptinessIsCheckedAt` (`internal/cmd/generate_bands.go:359`), and configuring
ocean rendering relaxes it further, because an open-sea tile genuinely does come
back empty (`WithEmptyResponsesAllowed`, `overpass.go:219`).

**Blur sigma is rescaled by zoom.** ×1.4 at z≤11, ×1.0 at z12–13, ×0.7 at z≥14
(`watercolor.ZoomAdjustedBlurSigma`, `internal/watercolor/processor.go:94`). The
overview wants softer edges; detail wants sharper ones. Read
`docs/performance/blur-optimization.md` before touching any sigma.

**Metatile padding is scale-proportional; it is zoom-dependent only at large
sigmas.** `RequiredPaddingPx` (`internal/watercolor/padding.go:29`) derives
padding from the largest blur sigma in play (`3σ + 2`), floored at
`MinGeometryPaddingPx = 64`, and does the whole calculation in world pixels
before scaling to device pixels.

Because `Generator.watercolorParams` (`internal/pipeline/generator.go:349`)
applies `ZoomAdjustedBlurSigma` _before_ calling `RequiredPaddingPx`, the zoom
does reach the padding whenever the sigma term beats the 64 px floor. With the
defaults it never does, so padding is a flat 64 px at every zoom — but a config
`blur-sigma` at the `MaxTunableSigma = 20` ceiling gives `3·(20·1.4) + 2 = 86` px
at z≤11 against 64 px at z12+, and the fetch bbox widens with it. Do not read the
flat default as a guarantee.

What _is_ guaranteed is equal world extent across device scales:
`pad(2x) == 2 * pad(1x)` exactly, so at a given zoom the `@2x` Overpass query is
byte-identical to the 1× one and an on-demand `@2x` render costs no upstream
traffic when the response cache is on.

**Band fetching stops below z10 by default.** `--band-min-zoom` defaults to 10
(`internal/cmd/generate.go`): a single low-zoom tile already covers a huge area,
so grouping tiles into a 4×4 block buys little and risks an oversized response.
Natural-Earth-covered zooms are excluded on top of that, whatever
`--band-min-zoom` is lowered to (`generate_bands.go:112`) — nothing stops the two
ranges from overlapping, and a band fetch runs before the generator's own skip
would apply.

**On-demand retry backoff widens at low zoom.** The _base_ delay is 30 s at z≤7,
15 s at z≤10, 5 s above; `retryDelay` then multiplies it by `1 << attempt`
(`internal/server/ondemand_tiles.go:944`), so a z6 tile waits 30 s, 60 s, 120 s
and a z14 one 5 s, 10 s, 20 s. A low-zoom tile's fetch is bigger and slower, so
retrying it quickly only makes things worse. With Natural Earth enabled the
z0–5 end of that scale goes unused: those tiles never enter the fetch queue, so
there is no fetch to retry (`ondemand_tiles.go:1042`).

**Style-level generalisation is Mapnik scale tiers, not geometry simplification.**
There is no `simplify` or `generalize` anywhere in the Go code. Stroke widths and
which classes draw at all are stepped by `Min/MaxScaleDenominator` in
`assets/styles/layers/{roads,highways,railroads}.xml`. Computed from the standard
Web Mercator denominator `559,082,264 / 2^z` at 256 px:

| z   | ≈ denominator | z   | ≈ denominator |
| --- | ------------- | --- | ------------- |
| 0   | 559,000,000   | 10  | 546,000       |
| 2   | 140,000,000   | 12  | 137,000       |
| 4   | 34,900,000    | 14  | 34,100        |
| 5   | 17,500,000    | 16  | 8,500         |
| 6   | 8,700,000     | 18  | 2,100         |
| 8   | 2,180,000     | 20  | 533           |

The coarsest tier in `highways.xml:11-12` is 20,000,000–4,000,000, i.e. z5–z7.
At z4 and below the denominator is past 20 M and **nothing in the road or highway
styles draws at all**, whatever the query returned. `railroads.xml:12-13` starts
coarsest at 2,000,000–1,000,000, i.e. z9: z8 is still ≈2.18 M, just past that
maximum. That agrees with the query rules, which fetch no railway before z9
either.

## 4. Cost per zoom

The one thing to unlearn: **there is no cheap low zoom, and no cheap empty tile.**

Measured over the 689 base tiles in `tiles/`, spanning z5–z17: min 84 KB, max
144 KB, median on the mean at ~122 KB — a factor of 1.7 across twelve zoom
levels. A mid-Pacific z6 tile of open ocean is 127 KB, _above_ the mean; a z12
Sahara tile with literally zero OSM features is 108 KB.

The reason is structural. The watercolor texture and the Perlin noise fill every
pixel of every tile, so there is no flat region for PNG's filters to collapse. An
"empty" tile is a full-frame photograph of paper. This is a direct consequence of
the look the project exists to produce, so it is planned around rather than
optimised away. Full numbers and the tileset-size tables are in
`docs/data-scaling-strategy.md` § 3.

Compute, not storage, is what binds. The unit is the _render_, not the
coordinate, and a coordinate rendered at both scales is two renders. At the
measured ~0.3 renders/s:

| tier  | coordinates | base only  | base + `@2x` |
| ----- | ----------- | ---------- | ------------ |
| z0–8  | 87,381      | ≈3.4 days  | ≈7 days      |
| z0–12 | 22,369,621  | ≈2.4 years | ≈4.7 years   |

Batch runs no longer pre-render `@2x`, so the base-only column is what a bulk
job costs today; the right-hand column is what it used to cost, and still does
if every coordinate is wanted at both scales.

That is the _whole_ render. The watercolor **paint stage** inside it is measured
separately in
[performance/performance-monitoring.md](performance/performance-monitoring.md)
§ "Performance per zoom level", and the headline there is that it is flat across
zoom — the zoom-scaled sigma above does not move it, and the metatile is 384²
everywhere. What varies is how many layers a zoom has features for, roughly 3 at
z0–5 against 9 at z14+, for tens of milliseconds against the seconds a full
render takes. Not duplicated here.

## 5. Zoom limits in the code

| limit                                       | value | where                                                                         |
| ------------------------------------------- | ----- | ----------------------------------------------------------------------------- |
| highest zoom a coordinate may address       | 22    | `tile.MaxZoom`, `internal/tile/coords.go:97`                                  |
| TileJSON advertised minimum                 | 0     | `tilejson.DefaultMinZoom`                                                     |
| TileJSON advertised maximum                 | 18    | `tilejson.DefaultMaxZoom`                                                     |
| single-tile `--zoom` default                | 13    | `internal/cmd/generate.go`                                                    |
| last zoom served from Natural Earth         | 5     | `renderer.DefaultNaturalEarthMaxZoom`, `internal/renderer/naturalearth.go:18` |
| first zoom served from 50m rather than 110m | 3     | `DefaultNaturalEarthMidScaleMinZoom`, `naturalearth.go:26`                    |

`tile.MaxZoom` is a resource policy, not a limit of the tile scheme: z23 is a
perfectly well-defined grid, but this renderer has no data at that detail.

## See also

- [data-scaling-strategy.md](data-scaling-strategy.md) — measured per-tile cost,
  tileset sizes, and the three-tier recommendation. Read before any bulk run.
- [performance/blur-optimization.md](performance/blur-optimization.md) — why the
  sigmas are what they are.
- [performance/performance-monitoring.md](performance/performance-monitoring.md) —
  measured paint-stage cost per zoom, and the per-tile budgets CI enforces.
- [seam-inspection.md](seam-inspection.md) — checking a zoom boundary visually.
