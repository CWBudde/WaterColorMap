# Allocation Optimization (Phase 5.11.3, complete)

Archived from `PLAN.md`. This file keeps the measurements, the buffer-reuse
conventions the tile pipeline now relies on, and the invariants a future reader is
most likely to break.

**Result**: `BenchmarkFullPipeline` allocates 2.19 MiB per tile instead of 5.85 MiB
(−62%) in 38 allocations instead of 143 (−73%). Output is bit-identical: the pipeline
goldens did not move.

## What was actually wrong

Not what `PLAN.md` said. The section blamed the `gift` library's 64-bit RGBA buffers
and quoted 29 MB and 1.3 M allocations per tile. Both were stale: 5.11.2 replaced
`gift` with `internal/mask/blurkernel`, which works directly on `[]uint8` `Pix`
slices, and `gift` now survives only as the true-Gaussian reference in
`internal/mask/gaussian_reference_test.go`.

The real cost was structural. Every mask operation allocated its own result, so a
layer walked blur → threshold → distance transform → noise → threshold-with-antialias
allocating four to six full-size `*image.Gray` and keeping exactly one. On the padded
metatile a Gray is 147 KB, so ten layers threw away several megabytes per tile for
nothing. `CreateDistanceEdgeMaskWithContext` allocated two more per layer that died
inside the caller, and the painted result was memcpy'd out of a pooled buffer into a
freshly allocated one that had to exist anyway.

## What replaced it

Every kernel in `internal/mask` now has an `*Into` twin that writes a caller-owned
destination; the allocating function is a two-line wrapper around it, so there is
still one loop body per operation. Above them:

- `maskScratch` (`internal/watercolor/processor.go`) is pooled per `processMask` call
  and holds the pipeline's intermediates: the extracted alpha, the blurred mask, and
  one work buffer that carries the binary mask, then the distance map, then the noisy
  mask.
- `ProcessorContext` gained an `edgeMask` buffer, so the distance-based edge mask
  costs no allocation either.
- The paint result is now the destination of the last edge pass instead of a copy out
  of the context.

This follows the idiom already used three times in the tree (`distanceContextPool`,
`blurContextPool`, `processorContextPool`) rather than the size-keyed image pool the
old checklist described. A size pool would have saved nothing on its own — without
`*Into` variants there is no way to hand a pooled buffer to an operation — and the
sizes in that checklist were wrong anyway: **production never processes a 256² image**.
`RequiredPaddingPx` puts a 256 tile on a 384² metatile and an @2x tile higher still,
so do not add a size table.

## What is left, and why

Two allocations per layer are irreducible and stay: the final mask (the land path
hands it on to constrain parks) and the painted NRGBA (the compositor holds every
layer of a tile at once). After this change, `image.NewNRGBA` is what dominates the
remaining profile, and most of it is exactly those results.

`buildMasks` still allocates one mask per layer rather than pooling, deliberately:
`DebugContext.Capture` stores the image *reference*, so anything handed to it can
never be recycled. Only the empty masks that were immediately overwritten went away.

## Invariants

Four rules make recycled buffers unobservable. Breaking any of them is a rendering
bug, not a performance regression:

1. **Exact bounds, not "at least".** `maskScratch.ensure` and
   `ProcessorContext.EnsureCapacity` resize in both directions. Result bounds are
   taken from these buffers, so an oversized buffer would change the output.
   `DistanceContext` stays grow-only, legitimately: every loop inside it is bounded by
   the width and height it is passed, never by a buffer length.
2. **Total write.** Every `*Into` writes every pixel of its bounds — none clears
   first, none skips. That is what makes a dirty destination unobservable. A future
   kernel with an early `continue` breaks this and must clear.
3. **In-place safety is documented, not accidental.** The stages the pipeline aliases
   read each pixel before they write it. `TestIntoVariantsInPlace` pins the specific
   aliases the pipeline uses.
4. **A destination that is smaller than its buffer must be cleared.** The `*Into`
   helpers are bounded by the mask, not by the buffer, so a mask smaller than the tile
   leaves the rest of a recycled buffer holding the previous paint, and the edge pass
   runs over the whole buffer. `paintFromFinalMaskWithContext` clears in that case;
   zero is also what the edge mask used to read outside the mask region back when it
   was allocated at the mask's own bounds.

New `*Into` functions take `dst` **last**, matching `InvertMaskInto`,
`ApplySoftEdgeMaskInto`, `ApplyMaskToTextureInto` and `TileTextureScaledInto`.
`BoxBlurSigmaInto(dst, src, …)` is the one dst-first outlier and stays that way:
flipping it would be a silent-compile change between two arguments of the same type.

## Measured

`benchstat`, 6 interleaved runs of each binary, i7-1255U, 256 tile with 5 layers:

| benchmark        | bytes/op          | allocs/op    | sec/op          |
| ---------------- | ----------------- | ------------ | --------------- |
| `FullPipeline`   | 5.85 MiB → 2.19 MiB (−62%) | 143 → 38 (−73%) | ~ (within noise) |
| `MaskProcessing` | 469 KiB → 108 KiB (−77%)   | 16.5 → 3 (−82%) | ~ (within noise) |
| `PaintFromMask`  | 456 KiB → 331 KiB (−27%)   | 7 → 3 (−57%)    | ~ (within noise) |

**Wall time did not measurably improve**, and `PLAN.md`'s "10–15% speedup via GC
reduction" was never achievable at this scale: zeroing thirty 64 KB buffers is tens of
microseconds against a 30 ms budget. The geometric mean moved about 5% in the right
direction, but the run-to-run spread on this machine was ±15–45%, so that is noise,
not a result. Where this pays off is GC pace under the concurrent tile server, and on
the metatile, which carries 2.25× the benchmark's bytes.

The remaining CPU bulk is per-pixel accessors — `GrayAt`, `SetGray`, `NRGBAAt`,
`SetNRGBA`, `PixOffset`, `Point.In` — inside the very loops touched here. That is
5.11.4's job and was deliberately left alone: the golden test is the only oracle for
both changes, and mixing a call-graph refactor with a loop-body rewrite would make a
golden diff unattributable. 5.11.3 sets it up cheaply, since every kernel now has a
destination-taking form with a documented bounds precondition, so 5.11.4 can rewrite
loop bodies without touching a single call site.
