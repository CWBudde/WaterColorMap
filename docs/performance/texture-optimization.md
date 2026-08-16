# Texture Processing Optimization (Phase 5.11.6, complete)

Archived from `PLAN.md`. This file keeps the profile that decided the scope, what the
tiling loops do now, the measurements, and why the texture atlas the plan asked for was
not built.

**Result**: tiling a texture into a metatile costs **96% less CPU** (620µs → 22.5µs for
the benchmark's worst realistic case), applying a mask to it **87% less**, and the
magnified (@2x) path no longer allocates. End to end, `BenchmarkFullPipeline` is
**14% faster** and `BenchmarkPaintFromMask` **22% faster**. Texture work fell from
~17.5% of the pipeline's CPU profile to 1.5%. Output is bit-identical: the pipeline
goldens did not move.

## What `PLAN.md` got wrong

The section this replaces said "TileTexture allocates 175MB per benchmark" and proposed
a texture atlas to fix it. Both halves were stale by two phases.

`TileTexture` allocated nothing on the per-tile path at all. 5.11.3 gave it a
destination-taking `TileTextureInto` and pointed it at the pooled `ProcessorContext`
buffer, and the memory profile of `BenchmarkFullPipeline` on `main` confirms it: of
518 MB of allocation across the whole run, the texture package accounts for **zero**.
The 175 MB figure came from the same pre-5.11.2 profile that PLAN's other stale numbers
came from.

The one real allocation left was in the _magnified_ path, which built three per-column
scratch slices (9216 B, 3 allocations) on every call despite being named `*Into`. It is
pooled now.

What texture work actually cost was **CPU**, and considerably more than the 3-5% the
plan estimated. `BenchmarkFullPipeline` on `main`, top entries by cumulative time:

| function                 | cum   |
| ------------------------ | ----- |
| `TileTextureScaledInto`  | 10.0% |
| `ApplyMaskToTextureInto` | 7.5%  |

of which `sampler.at` was 7.5% flat and `texture.mod` a further 1.6%. Per destination
pixel the loops ran a modulus, a `sampler.at` call, an `image.Point.In` bounds check and
a `PixOffset` multiply-add — to move four bytes.

## The profile also exposed a production-only cost

Both the pipeline benchmark and `pixelaccess_test.go` feed the tiling loops an
`*image.NRGBA`. Production never does. Every PNG in `assets/textures` is a truecolor
image without an alpha channel, so `image.Decode` returns `*image.RGBA`; `white.png` —
the paper base composited under every tile — is paletted, so it returns
`*image.Paletted`, whose only reader is the `image.Image.At` interface method.

So the benchmark measured the cheapest of the three type paths while production ran the
two expensive ones, one of them the fully generic
`color.NRGBAModel.Convert(img.At(x, y))` fallback. Measured on a 1024² texture into a
384² metatile, before this work:

| texture type      | tile a metatile |
| ----------------- | --------------- |
| `*image.NRGBA`    | 620 µs          |
| `*image.RGBA`     | 780 µs          |
| `*image.Paletted` | 1126 µs         |

That is the gap `ToNRGBA` closes.

## What changed

### Textures are normalised at load time

`texture.ToNRGBA` converts a decoded texture to `*image.NRGBA` once, in
`LoadDefaultTextures` and `LoadEmbeddedDefaultTextures`. It routes each texel through
`getNRGBA` — the same conversion the sampling loops would have applied — so the result
is bit-identical to sampling the original, and it preserves the bounds origin because
the tiling offsets are taken relative to it.

The cost is one pass per texture at startup and ~3 MB extra resident memory for
`white.png` (paletted 1 MB → NRGBA 4 MB); the other nine textures were already 4 MB
each as `*image.RGBA`. The payoff is that every production texture then takes the fast
path below instead of the generic one.

`sampler` also gained an `*image.RGBA` case, so a caller that passes an unconverted
texture (the `textures` CLI command, a test) still avoids the per-texel type switch.

### Unscaled tiling is a memory move, not a sample

Laying a texture down 1:1 is doubly periodic. Destination row `y` reads source row
`(offsetY+y) mod height`, and every row reads _the same_ source row, rotated by
`offsetX mod width`. So `tileUnscaledInto`:

- builds one row by copying at most one texture period out of the source row — two
  copies when the rotation wraps — and then doubling that period across the rest of the
  row;
- copies a row wholesale from the row `height` above it, once one full vertical period
  has been laid down.

`fillTexelRow` does the row build and is shared with `ApplyMaskToTextureInto`, which
fills RGB the same way and then writes the mask over the alpha channel in a second
contiguous pass. In the pipeline that call receives the already-tiled texture, which is
`*image.NRGBA` and exactly tile-sized, so a row is one `copy` plus one strided store.

For the shipped 1024² textures on a 384² metatile neither shortcut ever repeats — one
period is wider and taller than the whole destination — so the win there is purely the
disappearance of the per-pixel sampler call and its modulus. For a small texture (the
pipeline benchmark's 8×8) the replication does the work instead, and both cases land at
roughly the same cost per metatile.

### The magnified path

Unchanged arithmetic — the bilinear blend with the wrap _inside_ the interpolation is
load-bearing (see the comment on `TileTextureScaledInto`) and was not touched. Only its
three per-column scratch slices moved into a `sync.Pool`, which is what the `*Into`
convention requires and what `TestTextureLoopsDoNotAllocate` now pins at zero for
`scale=2` as well.

## Measured

Two test binaries, one built at `main` (`7ebf18b`) with this branch's benchmark file
copied in, one at the branch tip, run **alternately** so both see the same machine
contention, `benchstat` over 6 pairs × 300 iterations. Unlike 5.11.4, wall-clock
`benchstat` was usable this time: the machine was quiet and the spreads stayed inside
±22%.

`internal/texture`, 1024² texture into a 384² destination:

| benchmark                             | base      | new      | change           |
| ------------------------------------- | --------- | -------- | ---------------- |
| `TileTextureInto/NRGBA`               | 620.2 µs  | 22.5 µs  | −96.4% (p=0.002) |
| `TileTextureIntoSmall` (8² texture)   | 618.0 µs  | 15.1 µs  | −97.6% (p=0.002) |
| `TileTextureInto/RGBA`                | 779.5 µs  | 580.3 µs | −25.6% (p=0.002) |
| `TileTextureInto/Paletted`            | 1.126 ms  | 1.096 ms | ~ (p=0.180)      |
| `ApplyMaskToTextureInto/TiledNRGBA`   | 628.6 µs  | 82.4 µs  | −86.9% (p=0.002) |
| `ApplyMaskToTextureInto/RGBA`         | 799.4 µs  | 620.7 µs | −22.4% (p=0.002) |
| `TileTextureScaledInto/NRGBA`         | 2.661 ms  | 2.712 ms | ~ (p=0.240)      |
| `TileTextureScaledInto/RGBA`          | 3.462 ms  | 3.098 ms | −10.5% (p=0.002) |
| `TileTextureScaledInto/*` allocations | 9216 B, 3 | 0 B, 0   | −100%            |

The `RGBA` and `Paletted` rows are the _unnormalised_ cases: they improve only by what
the `*image.RGBA` sampler case and the dropped modulus buy, because a non-NRGBA source
cannot be moved with `copy`. In production those rows no longer occur — `ToNRGBA` turns
them into the `NRGBA` row, so the real before/after for the paper base is
**1126 µs → 22.5 µs** and for a layer texture **780 µs → 22.5 µs**.

`internal/watercolor`, same method, 8 pairs × 60 iterations:

| benchmark       | base     | new      | change           |
| --------------- | -------- | -------- | ---------------- |
| `FullPipeline`  | 18.48 ms | 15.91 ms | −13.9% (p=0.000) |
| `PaintFromMask` | 2.562 ms | 1.992 ms | −22.3% (p=0.000) |

Allocations per tile are unchanged (28 per `FullPipeline` iteration); there were none
left in the texture path to remove. Note that both figures _understate_ the production
gain, because `BenchmarkFullPipeline` uses 8×8 `*image.NRGBA` textures rather than 1024²
`*image.RGBA` ones.

After the change, the only texture entry left in the `FullPipeline` CPU profile is
`ApplyMaskToTextureInto` at 1.5% flat. `TileTextureScaledInto` no longer appears in the
top 60.

## Why there is no texture atlas

The plan asked for an atlas (one large texture, UV mapping), lazy tiling, and a
measurement of the memory the atlas saves. None of the three survives contact with the
profile:

- **An atlas saves no memory here.** It is a fix for many small textures wasting bind
  slots and page space. This pipeline has ten textures, one per layer, each used at full
  size by exactly one layer. Packing them into one image changes the total resident bytes
  by nothing and adds a UV indirection to every texel fetch.
- **An atlas saves no time here.** The remaining cost is the _destination_ write — 590 KB
  per metatile per layer — which no source-side layout can remove. Cache locality on the
  source side is already ideal: one contiguous `copy` per row.
- **Lazy tiling has nothing to defer.** The tiled texture is consumed in full by
  `ApplyMaskToTextureInto` on the very next line, and its buffer is already pooled
  (`ProcessorContext.tiledTex`), so tiling on demand would do the same work at a later
  moment.
- **Caching tiled textures across tiles is a trap.** It looks attractive — offsets are
  world-anchored, so `offsetX mod textureWidth` repeats — but at 22.5 µs to build one, a
  cache would trade a memcpy for a lookup plus a residency policy plus a shared mutable
  buffer under the parallel tile server, in exchange for microseconds. Determinism is
  anchored to world coordinates (see `AGENTS.md`); a cache keyed on anything coarser
  would break tile seams silently.

## What was deliberately left alone

- **Fusing tiling and masking.** `paintFromFinalMaskWithContext` tiles into
  `ctx.tiledTex` and then masks that into `ctx.painted`, two passes over the metatile
  where one would do. Worth perhaps another 1% now that the passes are memory moves, and
  it would mean editing `internal/watercolor/processor.go`, which 5.11.5 is rewriting.
  Revisit after 5.11.5 lands, not before.
- **`TintTexture`** still uses `SetNRGBA` per pixel. It runs once per process, from
  config loading, over a texture that is then normalised anyway. 5.11.4 already listed
  it as cold code; it stays that way.
- **The magnified path's bilinear blend**, which is float work per texel and dominates
  the @2x case. Making it fixed-point would move pixels, so it is a look change and a
  golden diff, not an optimisation.
- **`internal/texture/generator.go`** (the `textures` CLI command) — cold.

## Determinism

The oracle is `internal/texture/pixelaccess_test.go`, which keeps the pre-5.11.4
accessor loops verbatim as `reference*` helpers and compares whole `Pix` slices. This
phase extended its matrix with the three cases the new fast paths introduce: an
`*image.RGBA` texture, an `*image.Paletted` one, and a texture **larger than the
destination** in both axes (the production shape, and the one where neither the row
replication nor the row reuse ever repeats). `TestToNRGBAIsBitIdentical` separately pins
that tiling and masking a converted texture produce the same bytes as tiling and masking
the original.

Keep those references frozen, as 5.11.4 asked. Above them, `TestPipelineStages`
renders from the real `assets/textures` — including the paletted `white.png` — and its
goldens did not move, which is what makes the load-time conversion safe.
