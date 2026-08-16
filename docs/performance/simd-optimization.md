# SIMD Optimization (Phase 5.11.7, complete)

Archived from `PLAN.md`. This file keeps the profile the work was chosen from,
the candidates that were rejected and why, the accuracy argument, and the
measurements.

**Result**: `BenchmarkFullPipeline` is 17% faster. The soft-edge darkening pass —
the largest single cost left after 5.11.4 — is 33% cheaper end to end and 5.3×
cheaper at the kernel, and the box-blur column pass that 5.11.2 left as a
follow-up is 3× cheaper, taking the 7.48 land-shade blur down 28%.

Both kernels are **bit-identical** to their scalar references, not approximations.
The pipeline goldens did not move.

The plan's "expected gain: 10-20% for specific operations" turned out to be right
for the wrong reason: it predicted a modest gain spread over several operations,
and what happened was a large gain on one operation the 2024 candidate list did
not name at all.

## The profile chose the targets

The plan's candidate list — "pixel blending, noise application, edge darkening" —
predates 5.11.3, 5.11.4 and 5.11.6. Re-profiling `BenchmarkFullPipeline` on
`main` gave this (top flat entries, 300 iterations):

| entry                              | flat |
| ---------------------------------- | ---- |
| `distanceTransform1DWithBuffers`   | 20%  |
| `hslToRGB`                         | 9%   |
| `distanceTransformColumns`         | 8%   |
| `rgbToHSL`                         | 7%   |
| `texture.sampler.at`               | 7%   |
| `ApplySoftEdgeMaskInto` (own code) | 5%   |
| `applyNoiseInto`                   | 4%   |
| `ConvColsRowAVX2` (blur, existing) | 3%   |

`ApplySoftEdgeMaskInto` and the two colour conversions it calls are one thing:
22% of pipeline CPU in a single pass over the image. PLAN 5.11.1 had already
identified the HSL round trip as the largest remaining cost and noted that
_removing_ it would be a look change rather than an optimisation. Vectorising it
is not — the output is unchanged.

## Candidates

**Chosen: the soft-edge pass** (`internal/mask/edge.go`, 22% of pipeline CPU).
Per-pixel integer code with two divisions and a six-way switch in it, applied to
every pixel of every layer twice — once for the shade, once for the edge. Wide,
independent, and exactly reproducible in integer/float lanes. Implemented.

**Chosen: the box-blur column pass** (`blurkernel.BoxCols`, ~2% of pipeline, 45%
of the one blur configuration that uses it). Pre-scoped by
[blur-optimization.md](blur-optimization.md) as the remaining follow-up, small,
and low risk. Implemented.

**Rejected: the distance transform** (30% of pipeline, the largest single entry).
`distanceTransform1DWithBuffers` is Felzenszwalb's lower-envelope algorithm: it
walks a stack of parabolas and pops it in a data-dependent `for` loop, so each
step depends on how many parabolas the previous step removed. There is no lane
structure to exploit without replacing the algorithm, and replacing it is an
accuracy question, not a SIMD one. The surrounding cost in
`distanceTransformColumns` is a strided gather/scatter — a transpose — which is a
cache-blocking problem rather than a vector one. Both are worth revisiting; not
under this heading.

**Rejected: `texture.sampler.at`** (7%). It is a wrapped, scaled texture lookup,
i.e. a gather; AVX2 gathers are barely faster than scalar loads for this access
pattern. It is also the code PR #46 (5.11.6) is rewriting, and two people editing
the same loop is a worse cost than the gain.

**Rejected: `applyNoiseInto`** (4%). Vectorisable, but its arithmetic is `float64`
and must stay `float64` to keep the output identical, which caps it at four lanes
per register; the noise row is read with a wraparound that breaks the contiguous
load; and the adaptive variant calls `smoothstep` per pixel. Best case is around
3% of the pipeline for a kernel about as complex as the soft-edge one.

**Rejected: `distanceToIntensityIntoRect`** (9% cumulative). Most of its cost is
`math.Pow` and `math.Exp`. Reimplementing those in assembly cannot be
bit-identical to Go's, so this would have to be an approximation with an accuracy
budget, on a stage that feeds the mask threshold. Not worth the risk for the size.

**Rejected: `BoxRows`** (the row half of the box blur). Its window slides along x,
so the running sum is a serial dependency from one column to the next. The column
pass has no such dependency, which is exactly why only that half was done.

**Rejected: `avo`.** It generates Go assembly from a Go program, which is worth it
when a kernel needs unrolling, register allocation across many variants, or
generation over a family of types. Neither kernel here is that: both are one loop
body, both fit in the 16 architectural registers with room left, and the existing
`blurkernel` assembly is already hand-written in the same style. Adding a
code-generation dependency and a `go generate` step to the build so that two files
can be written in Go instead of Plan 9 is a bad trade — and the generated `.s` is
what a reviewer has to read either way. `gonum` was never a candidate; it is a
numerical library, not a SIMD facility.

## Dispatch and fallback

Both kernels follow the pattern `blurkernel` established in 5.11.2:

- The portable Go implementation is the reference and is always compiled:
  `softEdgeRowGo`, `boxAccumGo`, `boxColsRowGo`.
- A `//go:build amd64 && !purego` file resolves `cpu.X86.HasAVX2` once into a
  package-level `useAVX2` and dispatches per row.
- A `//go:build !amd64 || purego` file provides the same function names calling
  the portable implementation directly.
- The assembly declarations live in an `asm/amd64` package whose **`decl.go`**
  carries the `!purego` constraint. With the declarations gone the `.s` files have
  nothing to bind to, so `-tags purego` needs no second copy of the build tags.

New in this phase: `internal/mask/asm/amd64` sits next to
`internal/mask/blurkernel/asm/amd64`, and the "every body-less declaration has a
`TEXT` symbol" test moved up to `internal/mask` so that it walks both trees
(`internal/mask/asmdecl_test.go`).

### The tail is not covered the way the blur covers it

`ConvColsRowAVX2` handles a width that is not a multiple of eight by repeating the
final full block, which is safe because it is a pure function of its source. Both
new kernels reject that trick:

- the soft-edge pass writes its output and the caller may pass one buffer as both
  source and destination, so a repeated block would darken those pixels twice;
- the box column kernels carry an accumulator forward, so a repeated block would
  add a source row into it twice.

Both therefore run `floor(w/8)*8` columns in assembly and hand the remainder to
the portable loop. At the 384-wide metatile the remainder is empty anyway.

## Accuracy: bit-identical, and why that is provable here

The soft-edge kernel reproduces `rgbToHSL` → darken → `hslToRGB` exactly. Three
places could have made it approximate, and each is pinned:

1. **Two divisions by a per-lane divisor** (saturation, hue). AVX2 has no integer
   divide, so both go through `VDIVPS`. Both operands are exact in float32 and
   the quotient is at most 256 in magnitude, which leaves the rounding error two
   orders of magnitude below the `1/255` gap that separates a non-integral
   quotient from an integer. Truncation therefore agrees with Go's integer
   division — and by that margin it agrees under any rounding mode, not only the
   default. `VCVTTPS2DQ` truncates toward zero, which is what Go's `/` does for
   the negative hue numerators.
2. **The lightness divide by 65025** is a constant divide, done as `(n/255)/255`:
   a widening fixed-point multiply for the first step and the
   `(x + 1 + x>>8) >> 8` identity for the second. `TestSoftEdgeDivisionMagic`
   proves both over their entire input range rather than arguing the bound.
3. **The mask falloff** is `int(float64(65025 - m*m) * strength)`. It stays in
   double-precision lanes (`VCVTDQ2PD` / `VMULPD` / `VCVTTPD2DQ`) because it is
   the same IEEE double multiply the portable path does; anything narrower would
   disagree on products that sit just under an integer.

The kernel also drops the scalar's trailing `h %= 1536`, which is safe because `h`
can never reach 1536 — the r sector is the only one that wraps, and its quotient
there is at most `-1`, never `0`. That invariant is load-bearing (a hue of 1536
would select sector 6, where every selector bit has been shifted out and the pixel
would come out black) and is stated in the `.s` file next to the code that relies
on it.

The box kernels use the same `BoxReciprocal` multiplier the scalar `boxDiv` uses,
so `TestBoxReciprocalExact` still covers the divide. Their contract holds over a
real window sum, `[0, n*255]`; outside it the scalar wraps its `uint8` conversion
where the assembly saturates its pack, and the differential test builds its
accumulator as an actual window sum for that reason.

Tests:

- `TestSoftEdgeRowMatchesReference` — randomised rows at 19 widths straddling the
  eight-pixel block, six strengths including 0 and 1.
- `TestSoftEdgeRowExhaustiveRGB` — all 16.7M source colours through both paths, at
  two strengths, with the mask level cycling underneath.
- `TestSoftEdgeRowInPlace`, `TestSoftEdgeRowClipping`,
  `TestSoftEdgeRowPreservesAlpha`.
- `TestBoxAccumMatchesReference`, `TestBoxColsRowMatchesReference` — both kernels
  against their references, with and without a row to slide in, empty input
  included. `TestBoxKernelsMatchNaive` already covered `BoxCols` end to end.

Each differential test was mutation-checked: a deliberate one-constant change in
the assembly makes it fail, so it is genuinely exercising the vector path.

## Measured

i7-1255U, `benchstat` over interleaved runs of two prebuilt test binaries. This
machine's run-to-run spread is wide, which is why both pipeline figures are quoted
with their `p` and `n` rather than as single runs.

Kernels, 384-wide row:

| kernel                          | scalar | dispatched | speedup |
| ------------------------------- | ------ | ---------- | ------- |
| soft-edge row                   | 5.0µs  | 0.94µs     | 5.3×    |
| box column slide-and-divide row | 495ns  | 165ns      | 3.0×    |

Pipeline, 12 interleaved runs each:

| benchmark        | main    | this    | change           |
| ---------------- | ------- | ------- | ---------------- |
| `FullPipeline`   | 18.90ms | 15.68ms | −17.1% (p=0.000) |
| `EdgeDarkening`  | 430.5µs | 290.4µs | −32.5% (p=0.000) |
| `MaskProcessing` | 1.287ms | 1.275ms | ~ (p=0.347)      |
| `Antialiasing`   | 69.6µs  | 66.5µs  | ~ (p=0.755)      |

`MaskProcessing` does not move because it does not run the edge pass, and its blur
sigmas are all on the direct path.

Blur, 8 interleaved runs each, 384 metatile:

| sigma | main  | this  | change           |
| ----- | ----- | ----- | ---------------- |
| 0.99  | 196µs | 191µs | ~ (p=0.959)      |
| 1.41  | 225µs | 242µs | ~ (p=0.195)      |
| 2.45  | 317µs | 301µs | ~ (p=1.000)      |
| 3.43  | 380µs | 411µs | ~ (p=0.328)      |
| 7.48  | 987µs | 707µs | −28.4% (p=0.000) |

Only 7.48 is on the box path; the rest are on the direct path and were expected
not to move, which is a useful check that the change is where it says it is.

Allocations are unchanged: `FullPipeline` still allocates 27 objects per tile.
Its `bytes/op` reads about 2.5% higher, with `allocs/op` identical — neither
kernel allocates, and neither adds an object, so this is the pooled scratch of
5.11.3 missing slightly more often now that the loop runs faster and the GC sees a
different pace. It is not a new allocation.

## Cross-platform

| target                    | what runs                                                    |
| ------------------------- | ------------------------------------------------------------ |
| amd64 with AVX2           | the assembly kernels                                         |
| amd64 without AVX2        | the portable Go loops, chosen at init from `cpu.X86.HasAVX2` |
| arm64 and everything else | the portable Go loops (`!amd64` build tag)                   |
| `-tags purego`            | the portable Go loops; the `.s` files are not linked at all  |
| js/wasm                   | the portable Go loops — the browser playground builds this   |

`just test-purego` and `just build-wasm` both pass. There is no NEON kernel: this
renderer's deployment target is x86 servers, and a second hand-written assembly
implementation is a second thing to keep bit-identical. The portable path is not a
degraded mode — it is the reference every test compares against.

## What a future reader could undo

- **The tail is deliberately not covered by repeating the last block.** Copying
  that trick over from `ConvColsRowAVX2` looks like a tidy-up and is a correctness
  bug in both new kernels, for the two different reasons above.
- **The float64 multiply in `softEdgeDarkening` is deliberate.** Narrowing it to
  float32 on either side breaks bit-identity, and only on a few mask levels, which
  is the kind of thing that survives a quick test run.
- **`den > 0` in `rgbToHSL` is unreachable** (`den == 0` implies `delta == 0`,
  which the caller has already special-cased). The kernel relies on that; deleting
  the branch as dead code is fine, adding a different guard there is not.
- **`BoxRows` is scalar on purpose**, not by omission.
