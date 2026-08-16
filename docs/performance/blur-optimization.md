# Blur Optimization (Phase 5.11.2, complete)

Archived from `PLAN.md`. The blur rewrite is done and merged; this file keeps the
measurements, the rationale, and the two things a future reader is most likely to
break.

**Result**: 2–11× faster blur depending on sigma (6–11× across the range the
layer masks actually use). Blur is no longer a bottleneck.

Delivered:

- Re-measured the old "39.6% Gaussian blur" figure — it was stale.
  `gift.GaussianBlur` had already been replaced by a 3-pass box blur, and the
  profile the number came from predated that change.
- Benchmarked alternatives against a true Gaussian at realistic sizes (256, and
  the 384 padded metatile) and realistic sigmas (0.35–4.9).
- Implemented the selected kernels in `internal/mask/blurkernel`.
- Added an AVX2 assembly path with a portable fallback.
- Added quality tests measuring RMSE against a true Gaussian.
- Regenerated golden stage images.

## What was actually wrong

The box blur was not faster than the Gaussian it replaced (960µs vs 918µs at
sigma 1.2 on a 256 tile), and it was inaccurate. `radius := int(sqrt(12σ²/3 + 1))`
applied three times blurred roughly twice as hard as the nominal sigma at every
setting, and up to four times at sigma 0.35. Its inner loops were branchy, and
its vertical pass walked columns, touching one byte per cache line.

## What replaced it

`blurkernel.PlanFor` picks the kernel from sigma. Below sigma 4 it convolves a
true Gaussian directly with 16-bit fixed-point weights; above it, a 3-pass box
blur with three distinct radii.

The direct path exists because a box approximation cannot represent sigma below
~0.8 at all — the narrowest non-trivial box is already sigma 0.82 — which is
where most of this renderer's sigmas live.

The direct path's two directions — the horizontal (row) and vertical (column)
passes of the separable convolution — share one kernel shape: a weighted sum at
fixed offsets from a base pointer. `ConvRows` gets that shape by materialising a
replicated-border copy of each row first, so a single AVX2 implementation in
`internal/mask/blurkernel/asm/amd64` (`ConvColsRowAVX2`) serves both directions,
dispatched on `cpu.X86.HasAVX2`, with a portable Go fallback for other
architectures, `-tags purego`, and js/wasm. The box path has no assembly
implementation and does not use this kernel.

## Measured

`just bench-blur`, i7-1255U, 384 padded metatile, at the sigmas the renderer now
produces. The old blur was flat in sigma — a fixed six box passes — at 2.14–2.22ms
for every sigma measured, so one baseline column covers all rows. `gift` is the
true-Gaussian reference.

| sigma | old blur | gift   | new    | vs old |
| ----- | -------- | ------ | ------ | ------ |
| 0.99  | ~2150µs  | 2520µs | 198µs  | 10.9×  |
| 1.41  | ~2150µs  | 3417µs | 263µs  | 8.2×   |
| 2.45  | ~2150µs  | 3916µs | 345µs  | 6.2×   |
| 3.43  | ~2150µs  | 8424µs | 543µs  | 4.0×   |
| 7.48  | ~2150µs  | 8679µs | 1081µs | 2.0×   |

Sigma 7.48 gains least: it is the only production sigma left on the box path,
which had no assembly implementation. Vectorising `BoxCols` was the obvious
follow-up if blur ever mattered again.

**Done in 5.11.7.** `BoxCols` now dispatches its two inner loops to AVX2 kernels,
which took the 7.48 blur from 987µs to 707µs (−28%) with bit-identical output.
`BoxRows` stays scalar and always will: its window slides along x, so the running
sum is a serial dependency across columns. See
[simd-optimization.md](simd-optimization.md).

Allocations per blur dropped from 12 to 1 (a pooled `BlurContext` holds the
scratch buffers). At the pipeline level (`benchstat`, 8 runs each, vs `main`):

| benchmark        | time | bytes/op | allocs/op |
| ---------------- | ---- | -------- | --------- |
| `MaskProcessing` | −16% | −47%     | −36%      |
| `FullPipeline`   | −33% | −38%     | −31%      |

These are with the rescaled sigmas below, which roughly double the kernel widths;
before the rescale `MaskProcessing` was −42%. The blur itself is 8–11× faster
either way — the pipeline numbers are smaller because blur was never the whole of
it.

## Accuracy

Within 0.2 levels RMSE on the direct path, and 0.8 to 1.6 on the box path, which
drifts with sigma and is at its worst at the 7.48 land shade. The old
implementation was never measured against a true Gaussian at all.

`TestBlurAccuracyVsGaussian` pins both budgets **and** asserts which path each
sigma takes, so moving `maxConvRadius` cannot silently reassign a case to the
wrong budget.

## Default sigmas were rescaled to keep the look

Because the old blur ran about twice as wide as its nominal sigma, the sigma
values in `DefaultParams` had been tuned by eye against that. They are now set to
the widths that were actually being rendered:

| parameter                 | old     | new  |
| ------------------------- | ------- | ---- |
| `BlurSigma`               | 1.2     | 2.45 |
| `AntialiasSigma`          | 0.5     | 1.41 |
| `ShadeSigma`              | 3.5     | 7.48 |
| per-layer `MaskBlurSigma` | 0.7     | 1.41 |
| per-layer `MaskBlurSigma` | 0.9/1.1 | 2.45 |

Tiles therefore look as they did before, while sigma now means blur width in
pixels. Without this rescale the map rendered visibly crisper, and thin railway
and highway lines — which the over-blur used to push under the threshold —
started surviving it.

One side effect: `ShadeSigma` 7.48 needs a radius of 23, which puts the land
shade blur on the box path, the one with no assembly implementation.

## Known gap, not fixed here

`testdata/golden/watercolor-stages/` and `watercolor-stages-hannover/` are
orphaned. The `TestWatercolorStagesGolden` tests they belong to no longer exist,
so those PNGs (including `04_blur.png`) are not an active regression guard. The
`update-goldens` recipes that referenced those dead test names have been
repointed at `TestPipelineStages`, but the stale directories are still there.
Deleting them is tracked in PLAN.md § 7.6.
