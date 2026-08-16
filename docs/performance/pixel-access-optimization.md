# Pixel Access Optimization (Phase 5.11.4, complete)

Archived from `PLAN.md`. This file keeps the measurements, the loop conventions the
kernels now follow, and the two clipping behaviours a future reader is most likely to
break.

**Result**: roughly a quarter to three quarters less CPU per kernel, depending on how
much of the loop was accessor overhead to begin with. `BenchmarkFullPipeline` uses
**28% less CPU**. Output is bit-identical: the pipeline goldens did not move.

## What `PLAN.md` got wrong

The section this replaces targeted "349MB of temporary color allocations" and claimed
"every `At()` call allocates a new `color.NRGBA` struct". That is only true of calls
through the `image.Image` **interface**. `GrayAt`, `SetGray`, `NRGBAAt` and `SetNRGBA`
are concrete methods that return values; they allocate nothing. After 5.11.3 the whole
pipeline was down to 38 allocations per tile, so there was no 349MB to reclaim.

What those accessors actually cost is a `Point.In` bounds check and a `PixOffset`
multiply-add **per pixel**, on both the read and the write. In a loop like
`ApplyThresholdInto` — read, compare, write — that is very nearly the entire loop,
which is why it got 76% cheaper.

So the work split in two, with different payoffs:

1. **Interface dispatch**, which really did allocate: `composite.alphaOver` ran
   `color.NRGBAModel.Convert(src.At(x, y))` for every pixel of every layer,
   `pipeline.cropNRGBA` ran `dst.Set(x, y, src.At(...))`, and `mask.getAlpha` /
   `texture.getNRGBA` type-switched per pixel.
2. **Typed accessor loops**, which did not allocate but dominated the CPU profile.

## The loop convention

One row slice per image per row, indexed with no multiply inside the inner loop. This
is the idiom `internal/mask/blurkernel` already used
(`kernel.go:131-150`, `:213-226`) and that `copyGray` (`blur.go:132-136`) follows for
whole-image copies:

```go
w := bounds.Dx()
for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
    srcRow := src.Pix[src.PixOffset(bounds.Min.X, y):][:w]
    dstRow := dst.Pix[dst.PixOffset(bounds.Min.X, y):][:w]
    for i := range dstRow {
        dstRow[i] = ...srcRow[i]...
    }
}
```

Each image gets its own offset: they have different origins and different strides, so a
shared `y*w+x` index is a bug waiting to happen. `grayRow` (`mask/processor.go`) wraps
this, and reslicing to `[:w]` is what lets the compiler drop the per-pixel bounds check.

The arithmetic inside the loops was not touched — same operations, same order, same
`uint8` truncations. This was a rewrite of where the bytes come from, nothing else.

## The two clipping behaviours

`SetGray` / `SetNRGBA` silently drop out-of-bounds writes, and `GrayAt` returns zero
outside an image. Both were load-bearing, and direct indexing would panic or read
garbage instead. Two helpers preserve them, each paying the cost **once** rather than
per pixel:

- **`writeRect(src, dst)`** intersects the two rectangles up front. Needed because a
  destination is routinely a different size from its source: the watercolor processor
  keeps one tile-sized `ctx.edgeMask` and paints masks of any size through it
  (`CreateDistanceEdgeMaskIntoWithContext`), and `ApplyMaskToTextureInto` writes a
  possibly smaller mask into the pooled tile-size `ctx.painted`.
- **`grayRow(m, x, y, w)`** returns `nil` when that run is not wholly inside `m`, and
  the caller falls back to `GrayAt` for that row. Needed because `applyNoiseInto` may
  get a distance map narrower than the mask, and `ApplySoftEdgeMaskInto` a mask
  narrower than the base; both used to read zero there.

`internal/mask/pixelaccess_test.go` runs every converted kernel against destinations
that are the same size, larger, smaller, and offset from the source, so a future kernel
that forgets either helper fails rather than panicking in production.

## Hoisting the type switch

`getAlpha` and `getNRGBA` type-switched on every call. The switch now happens once per
row (`ExtractAlphaMaskInto`) or once per call (`texture.samplerFor`), with the original
accessor loop kept as the `default` branch. The fallback is not decorative: layer images
come from `image.Decode` and textures from `texture/loader.go`, so the concrete type
depends on how the PNG was written.

`texture.sampler` is a struct rather than a closure on purpose. A closure would capture
the texture and allocate once per call, putting back on the per-tile path exactly what
5.11.3 had removed. `TestTextureLoopsDoNotAllocate` pins it at zero.

## Measured

**Read the method before the numbers.** The development machine was running at a load
average of 8-23 on 12 threads throughout, and `benchstat` over wall-clock time was
useless there: an unloaded baseline against a loaded run showed a fictitious +1900%
"regression", and an interleaved wall-clock run showed no significant difference
anywhere with spreads up to ±1600%.

What is reported instead is **user CPU time at a fixed iteration count**
(`-test.benchtime=Nx`), taken from two pre-built test binaries — one at `43d71ff`, one
at `f6abe97` — run alternately so both see the same contention. CPU time is far more
robust to competing processes than wall time.

| benchmark                | pairs | base   | new    | change |
| ------------------------ | ----- | ------ | ------ | ------ |
| `Thresholding`           | 4     | 0.73 s | 0.18 s | −76%   |
| `ApplyNoiseToMask`       | 4     | 0.89 s | 0.24 s | −73%   |
| `MaskProcessing`         | 4     | 1.67 s | 1.06 s | −37%   |
| `EdgeDarkening`          | 4     | 1.12 s | 0.72 s | −35%   |
| `DistanceToIntensity`    | 3     | 1.83 s | 1.18 s | −35%   |
| `PaintFromMask`          | 4     | 2.90 s | 2.07 s | −29%   |
| `FullPipeline`           | 8     | 6.08 s | 4.36 s | −28%   |
| `CreateDistanceEdgeMask` | 3     | 3.40 s | 2.50 s | −26%   |
| `ApplySoftEdgeMask`      | 6     | 3.02 s | 2.45 s | −19%   |
| `Antialiasing`           | 8     | 1.28 s | 1.31 s | ~      |

The spread tracks how much of each loop was accessor overhead. `Thresholding` is a
read, a compare and a write, so removing the accessors removes most of it.
`Antialiasing` is the same loop plus a `smoothstep` per pixel, and the float work
dominates — it did not move, and that is the expected result, not a disappointment.
`ApplySoftEdgeMask` is bounded by the HSL round trip for the same reason.

Allocations are unchanged on the mask side (they were already at 2 per kernel) and the
interface-dispatch allocations are gone from `alphaOver` and `cropNRGBA`.

`PLAN.md` predicted 5-10%. That estimate was made from a profile that attributed the
cost to allocation rather than to the accessors themselves.

## What was deliberately left alone

- **The HSL round trip** in `ApplySoftEdgeMaskInto` is the largest remaining single
  cost, and short-circuiting it where the mask is white looks free, since the darkening
  factor there is 1. It is not: `rgbToHSL` → `hslToRGB` is lossy, so skipping it moves
  pixels. `TestApplySoftEdgeMaskIntoKeepsTheLossyRoundTrip` pins that. Changing it means
  accepting a golden diff, which is a decision, not an optimisation.
- **Dead kernels.** `MultiplyRGBByMask`, `ExtractBinaryMask` and `MinMaskRGBA` have no
  production callers — only tests. They still use the old accessor loops. They are
  deletion candidates for a cleanup pass, not conversion candidates.
- **Cold code**: `internal/texture/generator.go` (the `textures` CLI command),
  `TintTexture` (once per process, from `watercolor/tuning.go`), `internal/raster`
  (WASM-only render path) and `cmd/wasm`'s duplicate `cropNRGBA`.
- Parallelism (5.11.5) and SIMD (5.11.7), which are their own phases.

## The testing problem this created

`TestIntoVariantsMatchAllocatingVariants` compares each `*Into` kernel against its
allocating wrapper — but both wrap the **same** kernel, so once the kernel is rewritten
that test cannot detect the rewrite going wrong. It still earns its place as a call-site
check; it is not an oracle for this kind of change.

The oracle is `pixelaccess_test.go` in `internal/mask`, `internal/texture` and
`internal/composite`: each keeps the previous accessor-based loop body verbatim as a
`reference*` helper and compares whole `Pix` slices. This follows
`blurkernel/kernel_test.go`'s `TestConvKernelsMatchNaive`. Each file was mutation-checked
during the work — a one-character change to the production kernel makes 18 subtests fail.

Keep the references frozen. If a kernel's behaviour is ever meant to change, the
reference has to change in the same commit and the goldens have to move with it, which
is exactly the visibility this is for.
