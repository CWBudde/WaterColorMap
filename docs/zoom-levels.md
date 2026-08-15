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

Sources: `waterRules` (`overpass.go:462`), `parksRules` (`:479`), `roadsRules`
(`:514`), `railroadsRules` (`:546`), `buildingsRules` (`:561`). The 38 golden
files under `testdata/golden/overpass-query/` pin one query per zoom, so the
table above is checkable: read `z08.txt` rather than trusting this page.

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

### z0–5 is the gap

At z0–4 the whole query is seven lines — coastline, `natural=water` (way and
relation), `landuse=forest`, `natural=wood` (way and relation) — and z5 adds one
more for motorways. Verify against `testdata/golden/overpass-query/z04.txt`.

That is simultaneously **too much** — it asks a regional Overpass instance for
every forest across a quarter of the planet — and **too little**: a world view
needs generalised coastlines and country polygons that OSM does not carry.

The answer is Natural Earth via Mapnik's `shape` plugin, following the ocean
pattern from 4.10. See PLAN.md § 5.3 and `docs/data-scaling-strategy.md` § 2 for
the rationale. Until that lands, treat z0–5 as unsupported rather than merely
slow.

## 2. Which dataset answers

| zoom | source                               |
| ---- | ------------------------------------ |
| 0–5  | Overpass (wrong source — see above)  |
| 6+   | Overpass                             |
| 0–9  | ocean: **simplified** water polygons |
| 10+  | ocean: **full** water polygons       |

The ocean pass is independent of the OSM query — OSM maps no ocean, so the open
sea comes from the processed water polygons at
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

**Metatile padding is not zoom-dependent, and that is deliberate.**
`RequiredPaddingPx` (`internal/watercolor/padding.go:29`) derives padding from
the largest blur sigma in play (`3σ + 2`), floored at `MinGeometryPaddingPx = 64`,
and does the whole calculation in world pixels before scaling to device pixels.
The consequence worth knowing: `pad(2x) == 2 * pad(1x)` exactly, so the `@2x`
Overpass query is byte-identical to the 1× one and an on-demand `@2x` render
costs no upstream traffic when the response cache is on.

**Band fetching stops below z10 by default.** `--band-min-zoom` defaults to 10
(`internal/cmd/generate.go`): a single low-zoom tile already covers a huge area,
so grouping tiles into a 4×4 block buys little and risks an oversized response.

**On-demand retry backoff widens at low zoom.** 30 s at z≤7, 15 s at z≤10, 5 s
above (`retryDelay`, `internal/server/ondemand_tiles.go:944`) — a low-zoom tile's
fetch is bigger and slower, so retrying it quickly only makes things worse.

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
styles draws at all**, whatever the query returned. `railroads.xml:12` starts
coarsest at 2,000,000, i.e. z8.

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

Compute, not storage, is what binds: at the measured ~0.3 renders/s a global
z0–8 tier is about 7 days, and global z0–12 is 4.7 years.

## 5. Zoom limits in the code

| limit                                 | value | where                                        |
| ------------------------------------- | ----- | -------------------------------------------- |
| highest zoom a coordinate may address | 22    | `tile.MaxZoom`, `internal/tile/coords.go:97` |
| TileJSON advertised minimum           | 0     | `tilejson.DefaultMinZoom`                    |
| TileJSON advertised maximum           | 18    | `tilejson.DefaultMaxZoom`                    |
| single-tile `--zoom` default          | 13    | `internal/cmd/generate.go`                   |

`tile.MaxZoom` is a resource policy, not a limit of the tile scheme: z23 is a
perfectly well-defined grid, but this renderer has no data at that detail.

## See also

- [data-scaling-strategy.md](data-scaling-strategy.md) — measured per-tile cost,
  tileset sizes, and the three-tier recommendation. Read before any bulk run.
- [performance/blur-optimization.md](performance/blur-optimization.md) — why the
  sigmas are what they are.
- [seam-inspection.md](seam-inspection.md) — checking a zoom boundary visually.
