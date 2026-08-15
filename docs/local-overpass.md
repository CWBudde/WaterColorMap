# Local Overpass for fast testing

Every integration test and every `generate` run fetches OSM data from an Overpass
API. Against the public `overpass-api.de` that is slow, rate-limited, and at the
moment answers `406 Not Acceptable` outright, which makes the whole integration
suite unrunnable.

A local Overpass instance covering Niedersachsen — the region all the Hannover
test fixtures live in — turns that around completely:

|                               | public API                   | local instance |
| ----------------------------- | ---------------------------- | -------------- |
| one tile fetch                | seconds to minutes, or `406` | ~2 s           |
| full `just test-integration`  | not completable              | ~30 s          |
| 3×3 smoke block, base + `@2x` | rate-limited                 | ~60 s          |
| rate limits                   | yes                          | none           |

## Setup

The instance lives in a sibling repo, `../overpass-niedersachsen`:

```bash
cd ../overpass-niedersachsen
just up      # start (first run downloads ~450 MB and indexes for 15-30 min)
just logs    # watch progress
just test    # verify it answers
```

It serves `http://localhost:12345/api/interpreter` and covers
51.3–53.9 °N, 6.6–11.6 °E (Hannover, Braunschweig, Oldenburg, Osnabrück,
Göttingen, Wolfsburg). Queries outside that box return empty results, not an
error — so a tile elsewhere renders as blank land rather than failing loudly.

The container keeps itself current from Geofabrik minutely diffs. After a long
shutdown the first minutes are spent catching up; queries work throughout, they
just answer from the older snapshot until the catch-up finishes.

### Allow duplicate queries

`../overpass-niedersachsen/docker-compose.yml` sets:

```yaml
OVERPASS_ALLOW_DUPLICATE_QUERIES: "yes"
```

This is **required** for the test suite. The image defaults it to `no`, which
makes the dispatcher reject a byte-identical query issued within roughly 15 s of
the previous one:

```
Error: runtime error: open64: 0 Success /osm3s_osm_base
       Dispatcher_Client::request_read_and_idx::duplicate_query
```

It comes back as an HTML page with HTTP 400, so the client reports it as
`overpass engine error: invalid character '<' looking for beginning of value` —
which reads like a parse bug and is not one. Several tests fetch the same
Hannover tile, so without this setting the suite fails semi-randomly.

## Using it

### The CLI

**`config.yaml` is gitignored, so a fresh checkout has none** and both commands
fall back to the public API until you create one. Two ways to point them at the
local instance:

```bash
# 1. one-off, no config file: supplies the *default* endpoint
export WATERCOLORMAP_OVERPASS_ENDPOINT=http://localhost:12345/api/interpreter

# 2. persistent, with geographic routing: create config.yaml from the block below
```

The `-local` recipes take route 1 for you, which is why they work on a fresh
checkout.

For route 2, put this in `config.yaml`. Both `generate` and `serve` read the
`overpass.servers` list and pick a server by geography, falling back to the
public API for anything outside the coverage box:

```yaml
overpass:
  servers:
    - name: "Niedersachsen"
      endpoint: "http://localhost:12345/api/interpreter"
      workers: 10
      coverage: { min_lat: 51.3, max_lat: 53.9, min_lon: 6.6, max_lon: 11.6 }
    - name: "Public" # no coverage = matches everything
      endpoint: "https://overpass-api.de/api/interpreter"
      workers: 2
```

The startup log says which servers were configured, and the routing is per
tile, so a Hannover run never touches the public API.

### The tests

Integration tests build their datasource directly and never see `config.yaml`.
Point them at the local instance with an environment variable:

```bash
WATERCOLORMAP_OVERPASS_ENDPOINT=http://localhost:12345/api/interpreter \
  just test-integration
```

The variable only supplies the _default_ endpoint, i.e. it applies wherever a
caller passes no endpoint of its own. An explicitly configured endpoint — the
`overpass.servers` entries, or an endpoint passed to
`NewOverpassDataSourceWithWorkers` — always wins. Unset, the default stays
`https://overpass-api.de/api/interpreter`, so CI behaviour does not change.

`just test-integration-local` wraps the line above.

## Smoke test

```bash
just smoke              # 3x3 z13 block around Hannover, then serve it
just smoke-local        # same, but asserts the local instance is up and points
                        # the run at it via WATERCOLORMAP_OVERPASS_ENDPOINT
```

`just smoke` generates 9 tiles and serves them at
<http://127.0.0.1:8080/demo/> so seams and neighbour alignment are visible in a
real map viewer. Add `--hidpi` runs to check `@2x` output against its base
tile — they must show the same road classes at the same ground width.

## Known failures when the suite finally runs

Reaching a working Overpass makes tests run that had been failing at the fetch
for a long time. Two were stale, and are fixed:

- `TestRenderAdjacentTilesWithRealData` expected the roads mask to be yellow.
  `roads.xml` strokes `#FFFFFF`; yellow moved to `highways.xml` when that layer
  was split out.
- `TestPipelineStages/Hannover_*` required a buildings layer at z13 and z15.
  Buildings are only queried from z16 up, so the assertion is now gated on zoom.

Three remain, and none is caused by the code under test:

- **`TestPipelineStages/Hannover_z13` and `_z15`** differ from their goldens in
  `01_water_alpha` only. These goldens are rendered from live OSM data, so they
  drift whenever somebody edits a water polygon in Hannover — or, as here,
  whenever the local snapshot and the snapshot the goldens were recorded from
  disagree. Refresh them deliberately with `just update-goldens-hannover`, and
  check the diff is confined to the stages you expect. This does **not** apply
  to the synthetic goldens, which are deterministic and must never move.
- **`TestRenderAdjacentTilesWithRealData/EdgeAlignment`** compares the last pixel
  column of one tile with the first column of its neighbour and requires them to
  agree within ±60. Those are adjacent but _different_ pixels, so an antialiased
  edge crossing the seam legitimately differs; observed gaps reach ~120. It fails
  identically on `837537a`, before the hi-DPI work, so it is pre-existing. Either
  the tolerance is wrong or there is a real half-pixel seam — unresolved.
- **`TestRenderAdjacentTilesWithRealData/z13_x4317_y2691`** fails with
  `PNG is fully transparent: ..._highways.png`. The layer has features — an empty
  layer is skipped before this check — but at z13 `highways.xml` draws only
  motorway and trunk, and this tile has neither. A layer that renders nothing
  because the stylesheet's scale tier selects none of its features is legitimate,
  so the assertion is too strict. Left as-is: deciding what it _should_ assert
  without losing its ability to catch a genuinely broken layer is a design call.
