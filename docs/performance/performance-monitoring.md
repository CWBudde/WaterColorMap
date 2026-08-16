# Performance Monitoring and Regression Testing (Phase 5.11.8, complete)

Archived from `PLAN.md`. This file records what the project now measures
automatically, **what it will fail a build over and what it merely reports**, and
why that line is drawn where it is.

The short version:

| signal              | reproducible?     | treatment                                 |
| ------------------- | ----------------- | ----------------------------------------- |
| `allocs` per tile   | exactly           | **hard gate** in the normal unit-test run |
| bytes per tile      | exactly           | **hard gate** in the normal unit-test run |
| wall clock per tile | no                | catastrophe ceiling only (~40x measured)  |
| benchmark sec/op    | no, on any runner | tracked and reported, never gated         |

## Why not "fail the build if it gets 10% slower"

Because it would be switched off within a month, and everything else in this
document would go with it.

Two of this project's own archive documents already record the problem.
`docs/performance/blur-optimization.md` and
`docs/performance/allocation-optimization.md` both note that the development
machine's run-to-run spread is wider than the effects being measured, and
`docs/performance/pixel-access-optimization.md` § "Measured" gives the concrete
failure: an unloaded baseline compared against a loaded run produced a
**fictitious +1900% "regression"**, and an interleaved wall-clock run showed no
significant difference anywhere with spreads up to ±1600%. `benchstat` over
wall time was unusable there, and that machine is a dedicated laptop.

GitHub-hosted runners are worse. They are shared virtual machines with noisy
neighbours, variable CPU models between jobs, and no frequency guarantees. A
percentage threshold on `sec/op` there does not measure the code.

So the gate is put where the signal actually is.

## What is gated

`internal/watercolor/perfbudget_test.go`, `TestTilePaintBudget`. It paints one
delivered 256 px tile through the five production layers and asserts three
budgets:

| budget          | value       | measured on main | headroom |
| --------------- | ----------- | ---------------- | -------- |
| allocations     | 28          | 22               | +6       |
| allocated bytes | 3 910 000 B | 3 834 560 B      | +2%      |
| wall clock      | 3000 ms     | ~36 ms           | ~40x     |

**It paints 384x384, not 256x256, and that is the whole point of the number.**
`pipeline.Generator.renderLayers` sets `params.TileSize` to the padded metatile
`tileSize + 2*RequiredPaddingPx(params)` before any layer is painted, and every
buffer in the paint path is sized from `params.TileSize`. For the default 256 px
tile that is 384x384 at every zoom (`RequiredPaddingPx` is pinned by
`MinGeometryPaddingPx = 64`; see § "Performance per zoom level"), so a budget
taken on a bare 256 px image would price 44% of the pixels production pays for --
and, worse, would be unable to gate: a whole extra 256x256 gray plane is 65 536 B,
which fits inside any workable percentage headroom on a 1.7 MB base.
`tileBudgetWorkload` therefore repeats the generator's four steps in the
generator's order -- `ApplyScale`, the zoom sigmas, `RequiredPaddingPx`,
`params.TileSize` -- rather than approximating them.

This is the class of regression 5.11.3 fixed. That phase took the tile from 143
allocations to 38, and 5.11.4 took it further; nothing but a test stops the next
refactor from putting a `make` back inside a per-row loop. A per-pixel
allocation costs **thousands** of allocations per tile, so a +6 budget is not a
compromise between sensitivity and stability -- it catches the real failure mode
with three orders of magnitude to spare.

The test costs about 0.5 s and runs in `just test` and in the existing CI
`test-unit` job. No new workflow, no Mapnik install, no extra minutes.

### The four guards that keep it non-flaky

1. **`//go:build !race`.** The race detector allocates on its own account and
   multiplies wall time. Under `-race` the file is not compiled at all, which is
   more honest than a runtime skip: there is no version of these numbers that
   means anything with the detector on.
2. **`testing.Short()` skips it.** A `-short` run is asking for the fast subset;
   a measurement does not belong there.
3. **The garbage collector is switched off for the measurement**
   (`measurePerRun`). This is the load-bearing one. Every scratch buffer in this
   pipeline comes from a `sync.Pool` (`watercolor/processor.go:299,398`,
   `mask/blur.go:13`, `mask/distance.go:13`), and a GC **empties** those pools --
   so a collection landing mid-measurement reappears as several megabyte
   allocations that have nothing to do with the code under test. That is exactly
   why `BenchmarkFullPipeline` reports anywhere between 26 and 28 `allocs/op` on
   one unchanged binary.
4. **`GOMAXPROCS(1)` for the duration of the measurement**, restored afterwards.
   This one is a consequence of the third and was found by measuring the padded
   metatile: `sync.Pool` shards per P, a goroutine that runs for tens of
   milliseconds gets preempted by `sysmon` and can resume on a different P whose
   shard is empty, and the next `Get` then misses and allocates a fresh
   megabyte-sized buffer. With the default 12 Ps the test reported 22 allocs /
   3 834 560 B on most runs and 29 allocs / 4 870 979 B whenever a migration
   landed badly -- a 27% swing that no headroom could have absorbed honestly.
   One P is one shard, so there is nothing to migrate away from. It costs
   nothing in fidelity: the paint path is single-goroutine, so the scheduler
   cannot change the work being measured, and no test in `internal/watercolor`
   calls `t.Parallel()`.

With all four in place, ten consecutive runs of the budget test give **22
allocations and 3 834 560 bytes every time**, bit-identical.

That is the reason a budget test is viable here at all. Had the number wobbled
by ±2 the gate would have needed slack wide enough to be useless -- and the 2%
byte budget in particular only works because the measurement is exact.

`measurePerRun` also restores the collector **and forces one collection** before
returning, so it does not leave ~38 MB of garbage and a shifted GC pace for
whatever test runs next.

### A known flake this work did not create

`TestPaintLayerAllocationBudget` in `internal/watercolor/scratch_test.go`
measures one layer with `testing.AllocsPerRun` and no GC control. Under
`-race` it fails on `main` today, reporting 13, 15 or 16 allocations against a
budget of 12 on consecutive runs -- the pool-clearing effect above, amplified by
the detector's own allocations. It is stable in the normal test run (15 out of
15 passes, with and without `-tags purego`), which is why CI does not see it.
`just check` and the `test-unit` job do not pass `-race`.

It is left alone here only to keep this branch off files that unmerged work is
touching. The fix is the one this document argues for: measure with the
collector off, as `measurePerRun` does, or exclude the file from `-race` builds
as `perfbudget_test.go` does. Whoever next opens `scratch_test.go` should do one
of the two.

### The wall-clock ceiling, and why it is set that loosely

3 s against a measured 36 ms is roughly 40x, and it is checked against the
**fastest** of the ten runs rather than their mean.

Both of those look like giving up, and both were arrived at by measurement.
A mean is dragged upward by whatever else the machine is doing: the same tile
measures ~36 ms with the package alone and roughly twice that while `just test`
has twelve package binaries in flight on this 12-thread laptop. Taking the
minimum removes most of that -- a run can be delayed by contention but can never
finish faster than the work takes -- yet even the minimum rises under that load.
A 2-core hosted runner running the same suite has less headroom again.

So the choice is between a tight ceiling that fires on a busy runner and teaches
everyone to ignore it, and a loose one that only ever fires for cause. At 40x it
still catches what it exists for: an inner loop that went quadratic, or
`blurkernel.PlanFor` falling back to naive convolution. Those are 10x to 1000x
events. Anything finer belongs to the benchmark workflow, which does not gate.

If this ever fires, it is real. Profile it, do not re-run it.

## What is tracked instead

`.github/workflows/bench.yaml`, called from `tests.yaml`. It runs the benchmark
suite on the current revision **and on a base revision, alternately**, and
summarises the pair with `benchstat`. **No measurement it takes can fail a
build** -- there is no threshold anywhere in it. (The job can still go red if
the tooling itself breaks, which is the point: a silently broken benchmark job
is worse than none.)

Three decisions in it are worth keeping:

- **Interleaved, not sequential.** The base and head runs alternate, six rounds
  each, so a slow five minutes on the runner lands on both sides instead of on
  one. This is the same discipline `pixel-access-optimization.md` had to apply by
  hand; `scripts/bench-compare.sh` is that method made repeatable, and it works
  locally too (`just bench-compare main`).
- **No Mapnik.** The job benchmarks only the packages that build without cgo
  (`internal/{watercolor,mask,texture,composite,tile,tileformat}`), which is
  where every optimisation from 5.11.2 through 5.11.7 landed. Installing
  `libmapnik-dev` would add minutes to every run to reach benchmarks that are
  dominated by I/O. `just load-test` covers the server side locally.
- **Not on every PR.** It runs on pushes to `main`, which is the trend line, and
  on pull requests carrying the `benchmark` label, which is how you ask for a
  comparison when you are actually optimising something. Everything else gets
  the 0.5 s allocation gate and nothing more. (`tests.yaml` lists `labeled` in
  its `pull_request` `types` so that applying the label to an already-open PR
  starts the job; without it the default trigger set would ignore a label added
  after the last push.)

Output goes to the job summary (rendered `benchstat` table) and to a 90-day
artifact holding the raw `base.txt` / `head.txt` in Go benchmark format, so any
past comparison can be re-run through a different `benchstat` invocation later.

**Read `allocs/op` and `B/op` first.** Those columns are trustworthy on a shared
runner. A `~` verdict in the `sec/op` table is not evidence of no change; it
usually means the runner was too noisy to tell.

## Reproducing all of it locally

```bash
just bench-budget           # the gated numbers, printed
just bench-ci               # the benchmark set CI tracks
just bench-compare main     # interleaved A/B of the working tree vs main
just bench-zoom             # the per-zoom table below
just bench                  # everything, including the Mapnik-dependent packages
just load-test              # tile-server benchmarks
```

`bench-compare` takes `BENCH_ROUNDS`, `BENCH_TIME`, `BENCH_FILTER`, `BENCH_PKGS`
and `BENCH_OUT` from the environment; see the header of
`scripts/bench-compare.sh`.

## Where the numbers came from

All three budgets were measured on `main` at `636971a`, i7-1255U (12 threads),
go1.26.5, linux/amd64, load average below 1.0:

```
$ go test -count=10 -v -run TestTilePaintBudget ./internal/watercolor/
per tile: 22 allocs, 3834560 B, 34.824ms (fastest of 10)
per tile: 22 allocs, 3834560 B, 36.154ms (fastest of 10)
per tile: 22 allocs, 3834560 B, 35.956ms (fastest of 10)
per tile: 22 allocs, 3834560 B, 36.254ms (fastest of 10)
per tile: 22 allocs, 3834560 B, 36.375ms (fastest of 10)
...                                        (all ten identical to the byte)
```

Five runs taken **while the whole suite was running alongside** gave 22
allocations and 3 834 560 bytes -- the same figures, unchanged -- with a fastest
run of 33.9 to 38.0 ms. The allocation and byte columns have no in-suite
variation at all once `GOMAXPROCS(1)` removes the pool-shard migration; the
wall-clock column still spreads, which is why nothing tight is asserted on it.

The 2-core GitHub runner reports **the same 22 allocations and 3 834 560 bytes**
on the same commit, at 87 ms of wall clock. Two different machines, two
different CPU models, identical to the byte -- which is what licenses a 2% byte
budget rather than a defensive one, and it also puts the 3 s ceiling at 34x the
slowest environment the project actually runs in.

**`PLAN.md` said 38 allocations and 2.2 MiB, and both figures are superseded.**
They came from `BenchmarkFullPipeline` before 5.11.4 and they included
GC-cleared pool refills. That benchmark still measures an unpadded 256x256
buffer, so its 26-28 allocs/op and ~1.77 MB/op are a _smaller_ workload than the
budget test's, not a contradictory measurement of the same one; the per-zoom
benchmark below is the padded-metatile counterpart and reports ~3.88 MB/op. Do
not reconcile any of the three by averaging.

## Updating a budget

Deliberately, in the commit that changes the number, with the new measurement in
the message. The procedure:

1. Run `just bench-budget` **three times** on an idle machine. The allocation and
   byte figures must be identical across all three. If they are not, something
   in the measurement is wrong -- do not raise the budget to cover a number you
   cannot reproduce.
2. Decide whether the increase is intended. "The feature needed one more buffer"
   is a reason. "It went up and I do not know why" is a bug report, not a budget
   change.
3. Set the new budget to the new measurement plus the same headroom that is
   there now: +6 allocations, +2% bytes. Do not add headroom on top of headroom;
   ratcheting a budget upward one crisis at a time is how a gate stops gating.
   The byte headroom must stay below one gray metatile plane (147 456 B at
   384x384, i.e. 3.8% of the current base) or the budget stops being able to
   catch the cheapest real regression there is.
4. Update the measured figures in the comment block at the top of
   `perfbudget_test.go` **and** the table in this document. A budget whose stated
   provenance is stale is worse than no budget.

## Interpreting a failure

**`allocations` over budget.** Almost always a new allocation on a per-layer or
per-pixel path. Find it with:

```bash
go test -run TestTilePaintBudget -memprofile mem.out ./internal/watercolor/
go tool pprof -alloc_objects mem.out
```

Check first whether a kernel lost its `*Into` form or a caller stopped passing a
pooled context -- that is the failure mode 5.11.3's invariants exist to prevent,
and `docs/performance/allocation-optimization.md` lists all four of them.

**`bytes` over budget but allocations unchanged.** A buffer got bigger, not more
numerous. Usually the metatile grew: `RequiredPaddingPx` is driven by the largest
sigma in `Params`, so raising a blur sigma past the 64 px geometry floor widens
every buffer at once. That is a legitimate cost of a look change; note it in the
commit and re-measure.

**Wall clock over the ceiling.** Not a noise result at 40x. Profile it:

```bash
go test -run '^$' -bench BenchmarkFullPipeline -cpuprofile cpu.out ./internal/watercolor/
go tool pprof -top cpu.out
```

**The `bench` job shows a red `sec/op` number.** That is information, not a
verdict. Re-run it, or run `just bench-compare <base>` locally with
`BENCH_ROUNDS=10`, before believing it.

## Performance per zoom level

The measurable answer is smaller than one expects, and worth writing down
precisely because the intuition is wrong.

**The paint stage is flat across zoom.** `ZoomAdjustedBlurSigma` scales
`BlurSigma` and `AntialiasSigma` by 1.4 at z≤11, 1.0 at z12-13 and 0.7 at z≥14
(`internal/watercolor/processor.go:100`). That is a visible look change and a
measurable _nothing_. `just bench-zoom`, five counts at each tier, same
five-layer tile:

| zoom | sigma factor | sec/op         | B/op   | allocs/op |
| ---- | ------------ | -------------- | ------ | --------- |
| 6    | x1.4         | 33.8 - 37.2 ms | 3.88 M | 27-28     |
| 11   | x1.4         | 34.3 - 38.3 ms | 3.88 M | 27-28     |
| 13   | x1.0         | 34.2 - 37.2 ms | 3.88 M | 26-27     |
| 14   | x0.7         | 34.6 - 35.3 ms | 3.89 M | 26-28     |
| 17   | x0.7         | 33.5 - 35.0 ms | 3.89 M | 27-28     |

The whole range is 33.5-38.3 ms, which is inside the run-to-run spread. Blur
stopped being a bottleneck in 5.11.2 (it left the top-14 profile entries
entirely), so a ±40% swing in one sigma no longer moves the total. Memory does
not move either, and cannot: the metatile is 384x384 at every zoom, because
`RequiredPaddingPx` is pinned by `MinGeometryPaddingPx = 64` rather than by the
zoom-scaled sigma. `docs/zoom-levels.md` § 3 says the same thing from the other
direction and warns that a config sigma at the `MaxTunableSigma = 20` ceiling
would break the tie -- at which point low zoom would become the _expensive_ end,
86 px of padding against 64.

**What zoom does change is how many layers get painted.** A layer that Mapnik
renders empty produces no output file, and the generator skips it
(`internal/pipeline/generator.go:805`, `paintSimpleLayer`'s `img == nil` guard).
So the paint stage costs the sum of the layers a zoom actually has features for.
Measured per layer on an **unpadded 256x256** buffer, same fixture:

| layer    | sec/op  | B/op    | allocs/op |
| -------- | ------- | ------- | --------- |
| land     | 4.50 ms | 397 317 | 7         |
| highways | 4.08 ms | 336 666 | 5         |
| water    | 3.88 ms | 333 783 | 5         |
| roads    | 3.42 ms | 336 180 | 5         |
| parks    | 2.96 ms | 335 351 | 5         |

Land is dearest because it is the only layer carrying a distance transform and a
shade pass (`ShadeSigma: 7.48`); it is also the only one that is never absent.

These are 256x256 figures and the production metatile is 384x384, i.e. 2.25x the
pixels, so read the table below as the **shape** of the curve rather than as wall
times to quote: multiply by roughly 2.25 for the delivered per-tile cost, which
is how the five-layer row reaches the ~36 ms the budget test measures. What the
per-layer split is actually for is the ratios, and those do not depend on the
buffer size.

Combining that with the query rule windows in `docs/zoom-levels.md` § 1 gives
the shape of the curve. Layers with features, by zoom (unpadded 256x256):

| zoom  | layers painted                                              | n   | paint stage |
| ----- | ----------------------------------------------------------- | --- | ----------- |
| 0-5   | land, water (ocean folded in), rivers -- from Natural Earth | 3   | ~11 ms      |
| 6-7   | + parks, highways (motorway only); no rivers                | 4   | ~15 ms      |
| 8     | + roads (trunk, primary)                                    | 5   | ~18 ms      |
| 9     | + railroads                                                 | 6   | ~22 ms      |
| 10    | + rivers                                                    | 7   | ~25 ms      |
| 11-13 | + urban landuse                                             | 8   | ~29 ms      |
| 14-15 | + civic                                                     | 9   | ~32 ms      |
| 16+   | buildings replaces urban landuse                            | 9   | ~32 ms      |

The `n` column is read out of the rule tables; the `paint stage` column is that
count against the measured per-layer costs above, **not** an end-to-end
measurement. Treat it as the right shape and the right order of magnitude. It
roughly triples from the world view to the street view, and it stops growing at
z14 because the layer set does.

**None of this is what a tile costs.** The paint stage is tens of milliseconds;
`docs/zoom-levels.md` § 4 measures the full render at ~0.3 renders per second,
i.e. seconds per tile. Overpass fetch and Mapnik rendering dominate by two
orders of magnitude, they vary with feature density rather than with zoom
directly, and neither is what 5.11.2-5.11.7 optimised. Optimising the paint
stage further is optimising 1-2% of a batch run. That is the single most
important thing on this page for anyone planning the next performance phase.

## What was deliberately not built

- **A performance dashboard.** `PLAN.md` asked for one. For a single-maintainer
  repository it is a website to maintain, a data store to keep, and a
  `gh-pages` deploy to debug, all to display a trend line that is mostly runner
  noise. The job summary plus a 90-day artifact of raw Go benchmark output
  answers the question a dashboard would ("did this change move anything?")
  without any of that. If the repository ever grows a team and a
  performance-sensitive SLA, revisit it -- and revisit it with a dedicated
  self-hosted runner, because a dashboard fed by shared runners plots weather,
  not code.
- **`benchmark-action/github-action-benchmark`.** It is the obvious third-party
  choice and it was rejected on the same grounds. Its value is the alert
  threshold and the `gh-pages` chart -- precisely the two things that do not
  survive contact with shared-runner variance. What is used instead is
  `golang.org/x/perf/cmd/benchstat`, pinned, run through `go run`, from the same
  team that maintains the toolchain.
- **A `sec/op` regression gate of any percentage.** Covered above.
- **Benchmarking the Mapnik-dependent packages in CI.** The apt install costs
  more than the measurement is worth, and the numbers would be dominated by
  disk and by the Mapnik build on the runner rather than by this project's code.
- **Per-kernel budgets beyond what already exists.**
  `internal/mask/into_test.go` already pins every `*Into` kernel at zero
  allocations, `internal/texture` and `internal/composite` have their own
  zero-allocation assertions, and `TestPaintLayerAllocationBudget` covers one
  layer. What was missing was a number for the unit the project ships -- a whole
  tile -- and that is the only thing 5.11.8 added.
- **A memory budget on the tile server.** `internal/lru` already reports
  statistics and `docs/tile-server-architecture.md` covers the caching layers.
  A budget there would be measuring cache configuration, not code.

## See also

- [blur-optimization.md](blur-optimization.md), [allocation-optimization.md](allocation-optimization.md),
  [pixel-access-optimization.md](pixel-access-optimization.md) -- the three
  optimisation phases whose gains these budgets exist to keep.
- [../zoom-levels.md](../zoom-levels.md) § 3 for the zoom-conditioned behaviour
  and § 4 for storage and end-to-end throughput per zoom.
- [../data-scaling-strategy.md](../data-scaling-strategy.md) for what a bulk run
  costs.
