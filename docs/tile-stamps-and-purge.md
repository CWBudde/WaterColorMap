# Tile stamps and `purge`

A tile purge command and a source-data version stamp on rendered tiles landed
together, because neither is useful alone: a stamp nothing reads answers no
question, and a purge with nothing to select on can only delete by geometry.

## What a stamp records

The stamp is Overpass's `osm3s.timestamp_osm_base`, taken from the parsed
response rather than from the clock. **That distinction is the whole point.**
`FetchedAt` says when this process asked; the source timestamp says how old the
data is, and with the response cache on those are wildly different numbers — a
cache hit is fetched now and carries data from whenever the upstream import was.
Taking it from the body means it costs nothing extra and needs no
`storeRawResponse`, whose default stays off. `Source` records the endpoint that
answered, which under multi-server routing is a per-tile fact and the one thing
a stale tile's provenance cannot be reconstructed from later. `RendererRev`
identifies the binary, so a renderer change becomes a re-render selector.

## Where stamps live

Stamps live in `internal/tilestamp`, a SQLite sidecar that goes wherever the
tiles go: an extra `tile_stamp` table inside the `.mbtiles` file (safe — the
spec tolerates extra tables, and `insertMetadata` only clears `metadata`), or
`stamps.db` beside `tilejson.json` in a tile folder.

**Its rows are XYZ, deliberately unlike the `tiles` table sitting next to
them.** `mbtiles.tmsRow` exists because a missed y-flip does not fail, it
answers about the mirrored tile — so rather than repeat that trap in a second
table, the stamp store states one convention once and the single place both meet
(the MBTiles delete path) converts visibly.

Timestamps are stored UTC and fixed width so the table sorts chronologically as
text, but the staleness filters compare _parsed_ values rather than the stored
text: a row written by hand or by another tool need not follow that layout, and
a malformed one must read as "unknown" everywhere rather than sort below every
cutoff and be selected for deletion.

`convert` copies the stamps of the tiles it converts into the MBTiles file it
writes; without that, converting a stamped folder would silently produce a
tileset that answers "unknown" for every tile. `serve` stamps too. Its on-demand
generator is handed the same kind of store, opened once with the server and
shared by every generator it builds — they differ only in tile size and all
write into one tile folder, so a store per generator would put two SQLite
handles, each with its own write buffer, on one file. The store writes through
rather than batching (`Store.SetBatchSize(1)`): batching is sized for a run
producing hundreds of tiles a minute, whereas a server renders a tile now and
then and stays up for weeks, so a buffer would hold stamps in memory
indefinitely and lose all of them to a crash. It is closed — and therefore
flushed — after the on-demand backend stops, on the same graceful-shutdown path
that drains connections. A store that cannot be opened is a warning and nothing
more: the server serves unstamped rather than refusing to start, because a
sidecar is not worth a tile service.

## The stamp key includes the image format

A stamp is addressed by zoom, column, row, suffix **and** image format, and the
schema records that shape as `PRAGMA user_version = 2`. The format is there
because a tile folder may hold `z13_x1_y1.png` and `z13_x1_y1.webp` at once —
`purge` walks exactly such folders, which is why it uses `walkTilesDirectory`
rather than `scanTilesDirectory` — and those are two files, written at two
times, possibly from two different Overpass responses. Keyed without the format,
the second render silently overwrote the first file's provenance, and a
staleness purge then deleted whichever file the surviving stamp did not
describe.

Version 1 — the key without the format, which recorded no version and so reads
back as `user_version 0` — is **refused**, not migrated. Nothing in a version 1
row says which image format its tile was written in, so a migration would have
to guess, and a wrong guess is precisely the bug being fixed: a purge deleting
the file the stamp does not describe. Opening one fails with
`tilestamp.ErrSchemaVersion`, naming the file and saying what to do — the store
is a rebuildable sidecar, so deleting it costs a re-render and nothing else.
`serve` treats that like any other unopenable store and serves unstamped;
`generate` and `purge` stop before acting on a table they would misread.

`purge` matches a folder tile only against the stamp for its own format. An
MBTiles tile names no format, because the `tiles` table holds one row per
z/x/y and there is nothing to disambiguate; any stamp for the coordinate
selects it.

## Generation: skip-existing became a freshness question

Skip-existing keeps its documented asymmetry, extended rather than replaced.
`--stale-data-before`, `--stale-rendered-before` and `--stale-renderer-rev` turn
"does this tile exist" into "is this tile still good"; with none of them set,
nothing consults the store and behaviour is what it was. Every uncertain case
still renders — missing stamp, unreadable store, unparseable timestamp — because
a wrong skip leaves a permanent hole and a wrong render costs seconds. A stamp
write that fails is logged and does not fail the tile: the tile is real, the
stamp is bookkeeping about it.

## `purge`

`watercolormap purge` selects by `--bbox`, zoom range and `--suffix`, or by the
staleness of the stamps, over a folder or an MBTiles file. **Its asymmetry runs
the other way**: an unstamped tile is never selected by a staleness flag,
because deletion is not undoable and an unknown data version is not evidence of
an old one. Dry run is the default, `--yes` deletes, and the count and a sample
are always printed first. Deleting a tile removes its stamp in the same
transaction, so a stamp can never outlive its tile and talk a later run into
skipping a hole.

Two properties of the selection are worth keeping:

- The bounding box is tested **against the tiles that exist**, per zoom level,
  never enumerated into a set. A tileset reaching z22 under a country-sized box
  covers billions of theoretical tiles at that level alone; enumerating them is
  not a slow answer but no answer at all.
- A dry run opens the stamp store **read-only**. Opening it for writing would
  create `stamps.db`, switch the journal mode and create the schema, so a
  command whose whole promise is "this changes nothing" would modify a legacy
  tileset, and would fail outright on read-only storage before printing the
  selection it was asked for.

## Found on the way

`scanTilesDirectory` matched the flat layout only, so `convert` quietly produced
an empty MBTiles file from a `--folder-structure=nested` folder. It now handles
both, and purge reads the same scan.
