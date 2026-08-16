# Parallel Layer Painting (Phase 5.11.5, complete)

Archived from `PLAN.md`. This file keeps the dependency analysis, the
synchronisation design, the measurements, and the things a future reader is most
likely to get wrong about the default.

**Result**: painting a metatile's ten layers is **2.7x faster** with nine paint
workers than with one (78.9 ms → 29.5 ms, −62.6%), and a whole synthetic tile —
Mapnik render, mask build, paint, composite, PNG encode — goes from ~137 ms to
~84 ms (−39%). Output is bit-identical at every worker count, and the goldens did not
move.

**But the default is 1**, and that is the point of the design rather than caution. See
"Why the default is serial" below before raising it.

## Which layers are independent, and why

The paint stage has a shallower dependency graph than `PLAN.md` assumed. It claimed
"roads/highways depend on the land mask but could still be parallelized after land
completes". They do not. Roads, railroads and highways are painted from **their own**
alpha masks; they appear in the land computation only as part of the non-land union,
which `buildMasks` has already produced before any paint starts.

Everything a paint job reads exists before the first job starts:

- `rawLayers` — the Mapnik output, read-only from here on.
- the `maskSet` from `buildMasks`, including `nonLandUnion`.
- `watercolor.Params`, including the one shared Perlin noise texture, and the tinted
  textures the `Tuner` built once at generator construction.

Exactly one layer consumes another layer's output: **parks** is clipped to the land
mask, which the land paint returns alongside the painted land. So the stage is one
wave of nine independent jobs followed by parks:

| wave | layers                                                                   |
| ---- | ------------------------------------------------------------------------ |
| 1    | land, water, rivers, roads, railroads, highways, urban, civic, buildings |
| 2    | parks (needs the land mask)                                              |

Land is dispatched first because it gates that tail and because it is the single most
expensive job — it is the only layer painted from the union of all the others, so its
mask is the densest.

`buildMasks` itself stays serial. It is alpha extraction and a max over eight masks,
which is a small fraction of the stage and would need its own result plumbing.

## The synchronisation design

There is almost none, and that is deliberate: the shared state was already safe.

- **Scratch buffers.** `maskScratch` and `ProcessorContext` are _not_ safe to share,
  but nothing shares them — 5.11.3 put both behind a `sync.Pool` (`maskScratchPool`,
  `processorContextPool`), so every worker draws its own and returns it. Adding
  goroutines needed no change here at all. This is the invariant to protect: a future
  "optimisation" that replaces either pool with a single cached instance turns a
  correct pipeline into a data race.
- **Textures and the noise texture** are read-only for the whole tile.
- **The painted layers** go into a mutex-guarded `paintedSet`. Insertion order never
  reaches a tile: the compositor reads the map back in `composite.DefaultOrder`.
- **`DebugContext.Capture`** was already mutex-guarded. What was not deterministic was
  `SortedStages`, which sorted on `ZOrder` alone with an unstable sort — and three
  stages claim ZOrder 14, two claim 15, two claim 18. It now breaks ties by name.
- **Errors** are collected into a slice indexed by job and the earliest failing job
  _in list order_ is returned, not the first to arrive. A broken layer therefore
  reports the same message at any concurrency.
- **Panics** go through `safe.Do`. Nothing above a bare `go` recovers, so without it
  one malformed layer would take a whole tile server down instead of failing one tile.

The one visible behavioural difference: the serial path stops at the first failure,
the parallel path lets the jobs already in flight finish. A paint failure aborts the
tile either way.

## Determinism

`TestPaintWorkersProduceIdenticalTiles` renders the synthetic golden tile at 1, 2, 4,
9 and 14 workers and requires the encoded bytes to be identical to the serial run. It
exists because `TestPipelineStages` only ever exercises one setting, so a
schedule-dependent result would otherwise pass the entire suite. Run it under `-race`
as well; that is what covers the buffer sharing.

Nothing in a paint job reads a clock, a map iteration order, or a counter shared with
another job, so there is no floating-point reassociation or accumulation-order hazard
to worry about here — each layer is computed by exactly the same code as before, just
on a different thread.

## Why the default is serial

`GeneratorOptions.PaintWorkers` defaults to 1, and `AutoPaintWorkers(tilesInFlight)`
returns `GOMAXPROCS / tilesInFlight`, clamped to the wave size.

Layer parallelism and tile parallelism draw on the same cores, and the callers were
already saturating the machine at the tile level:

- `generate --workers` defaults to one worker per CPU.
- the tile server admits `MaxConcurrentGenerations` renders at once, defaulting to the
  CPU count, and a browser viewport asks for ~20 tiles in one burst.

Multiplying the two would only add scheduler pressure and per-worker scratch buffers.
The auto split therefore comes out as 1 for a normal batch run and a normal server,
and hands the whole budget to the cases where the outer parallelism genuinely cannot
fill the machine: a single-tile `generate`, a `--workers 1` batch, and a server pinned
to one or two generations at a time. `generate --paint-workers` and
`serve --paint-workers` (config `generate.paint_workers`, `serve.paint_workers`)
override it; an explicit value wins outright, including one that oversubscribes.

So the honest summary of what this bought: **latency for one tile, not throughput for
many**. A batch run over a city was already using every core and is unchanged.

## Measured

`benchstat`, `BenchmarkPaintAllLayers`, ten synthetic layers on a 384² metatile (the
size production actually paints — `RequiredPaddingPx` puts a 256 tile on one), 8 runs
of 30 iterations each, interleaved in a single process, i7-1255U (12 threads,
`GOMAXPROCS` 11), load average below 1.

| workers | sec/op      | vs serial  | B/op      | vs serial |
| ------- | ----------- | ---------- | --------- | --------- |
| 1       | 78.88m ± 4% | —          | 9.80 MiB  | —         |
| 2       | 47.67m ± 3% | −39.6%     | 11.39 MiB | +16%      |
| 4       | 37.86m ± 4% | −52.0%     | 12.28 MiB | +25%      |
| 6       | 31.38m ± 3% | −60.2%     | 12.94 MiB | +32%      |
| 9       | 29.49m ± 5% | **−62.6%** | 14.56 MiB | +49%      |

All differences are significant at p=0.000, n=8. Unlike 5.11.4, this machine was quiet
enough for wall-clock `benchstat` to work — and wall clock is the only thing that can
measure this change, since parallelism does not reduce the work, it spreads it. CPU
time, the metric 5.11.4 fell back to, is flat or slightly worse here by construction.

End to end, `Generate` on the synthetic tile (20 iterations, 6 runs each, means):
**137 ms → 84 ms**, spreads 131–143 and 82–85. The gap between −62.6% on the stage and
−39% on the tile is the serial remainder: Mapnik rendering, `buildMasks`, compositing
and PNG encoding.

Returns fall off sharply after four workers. This is a hybrid CPU (2 performance cores
plus 8 efficiency cores), the nine jobs are very unequal — land is far the largest —
and the parks tail is serial. Six is the sweet spot on this machine; nine buys another
6%.

The memory column is the real trade-off, and it has to be read for what it is: `B/op`
is the number of bytes _allocated_ over one paint, summed, not the peak live heap.
`benchstat` cannot report a peak, and no peak was measured here — so read the +49% as
"nine workers allocate half as much again", not as "the process needs half as much
memory again".

The mechanism behind the rise does say something about the peak, though. Each
concurrent worker checks its own `maskScratch` and `ProcessorContext` out of the pools
instead of handing one set around, so the pools end up holding one set per worker
rather than one in total, and each set is a few hundred KB on a 384² metatile. That is
a few MB of extra live memory at nine workers, on top of the layer results that were
always live. On a server it multiplies with the number of tiles in flight, which is
the second reason the default divides the budget rather than stacking on top of it.
Anyone sizing a server for a specific tile mix should measure RSS on that mix rather
than extrapolate from this column.

## Also done here

Painting land used to tile the paper texture over the whole metatile and composite
land onto it in order to produce the `11_painted_land_on_canvas` stage capture — for
every tile, including production runs where `dc` is nil and `Capture` discards its
argument. Those two operations together cost about as much as painting a layer, and
land sits on the critical path. They are now inside `if dc != nil`. Worth roughly 2%
of the serial stage and ~2 MiB, which is at the edge of this machine's spread; it is
in because it is free, not because it is large.

## Deliberately not done

- **Parallelism inside a layer** (splitting the blur, distance transform or edge pass
  into horizontal bands). It would help the cases layer parallelism cannot — a tile
  with one dominant layer — but it means threading a worker count through every kernel
  in `internal/mask`, and the boundary handling in the distance transform and the box
  blur is exactly where a band split goes subtly wrong. Layer-level parallelism gets
  most of the win for none of that risk.
- **Overlapping `buildMasks` with painting.** The masks are inputs to every job, so
  only the per-layer alpha extraction could move, and it is a small fraction of a
  stage that is now 29 ms.
- **Starting parks the moment land finishes** rather than after the whole wave. Parks
  is a small layer and the wave is dispatched land-first, so the tail is already
  short; a dependency-aware scheduler would add machinery for a few milliseconds.
- **Overlapping the Mapnik render with painting.** The renderer is cgo and holds its
  own state per pass; this was out of scope and is not obviously safe.
- **Raising the default.** See above. If a future workload is latency-bound rather
  than throughput-bound — a single interactive renderer, say — the knob is there.
