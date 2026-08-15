# Watercolor Mask Design (Stamen-aligned)

This is the design that Phase 3 delivered and that the renderer ships today. It
was moved out of `PLAN.md` once every work item was done, because it documents
_how the mask pipeline works_, not what is left to do.

Companion notes with the per-stage detail:

- [3.1 Mask processing pipeline](3.1-mask-processing-pipeline.md)
- [3.2 Noise consistency across tiles](3.2-noise-consistency-across-tiles.md)
- [3.3 Texture application](3.3-texture-application.md)
- [3.4 Edge darkening effect](3.4-edge-darkening-effect.md)
- [3.5 Layer-specific processing](3.5-layer-specific-processing.md)
- [3.6 Visual quality testing](3.6-visual-quality-testing.md)

## Why the design changed

The first implementation processed each layer independently using its own alpha
mask. The Stamen process instead relies on **cross-layer mask construction** —
land is derived by _inverting_ a combined "non-land" mask — and reuses
progressively blurred masks for additional effects.

The v1 pipeline (blur → noise → threshold → antialias per layer, texture tiling
and tinting with the mask as alpha, edge-darkening halo from mask blur
differencing) lived in `internal/mask/processor.go`, `internal/mask/edge.go`,
`internal/texture/processor.go` and `internal/watercolor/processor.go`. Its gaps
versus Stamen were:

- no explicit "water + roads" union mask used as the foundation;
- no explicit inversion step deriving the land mask from that union;
- no reuse of even-more-blurred masks as multiplicative/overlay shading layers
  per feature category.

## Core mask logic (alpha-only)

All masks are **single-channel alpha masks** (grayscale 0–255) derived only from
the rendered layer PNG's alpha channel.

Base masks:

- `waterMask` := alpha(layer=water)
- `roadsMask` := alpha(layer=roads)

Combined non-land mask (union):

- `nonLandMask` := max(waterMask, roadsMask)

**As shipped, this union has eight inputs, not two.** `buildMasks`
(`internal/pipeline/generator.go`) unions water, rivers, roads, railroads,
highways, urban, civic and buildings — every layer that should punch a hole in
land, not just the two the original Stamen note names. The two-input form above
is the shape of the idea; the eight-input form is what the land mask is actually
inverted from.

Fuzzy boundary mask (the Stamen step):

1. `blur1` := GaussianBlur(nonLandMask)
2. `noisy` := blur1 + PerlinNoise (same channel)
3. `hard` := Threshold(noisy) → hard black/white (transparent/opaque)
4. `aa` := Antialias(hard)

Invert for land:

- `landMask` := invert(aa) — everything not water/roads becomes the textured land
  region.

Antialiasing strategy, simplest first: a small blur kernel (sigma ≈ 0.3–0.8)
after the threshold; supersampling at 2× and downsampling was the fallback if
quality demanded it.

## Using the mask for texture and shading

Land texture application:

1. Tile/tint the land texture (globally aligned)
2. Apply `landMask` as alpha

Land darkening / pigment accumulation, reusing the same foundation mask:

1. `landShadeMask` := GaussianBlur(landMask, larger sigma)
2. Use `landShadeMask` as a black/transparent overlay, multiplied/overlaid onto
   the painted land.

This is the "keep blurring and reuse as a darkening overlay" idea: it derives
from the same mask field, so it stays consistent across tiles.

## Applying the same logic to other layers

Other layers keep the same building blocks but must get their **masking
relationships** right before painting:

- `parksMask` := alpha(parks) AND max(`landMask`, alpha(urban), alpha(civic))
- `civicMask` := alpha(civic) MINUS max(roads, railroads, highways)
- `urbanMask` := alpha(urban) MINUS max(roads, railroads, highways)
- `waterMask` := alpha(water)
- `roadsMask` := alpha(roads)

Two of these are worth explaining, because the obvious "AND `landMask`" version
is wrong once civic and urban are in the non-land union above:

- **Civic and urban are not intersected with land.** They are already subtracted
  _from_ land, so intersecting them with the result would leave them nearly
  empty. `paintAreaLayers` instead subtracts only the road/rail/highway union
  from them, which is what keeps roads drawn on top of an area rather than
  buried under it.
- **Parks are constrained to land _plus_ urban and civic**, not to land alone,
  for the same reason: a park inside a built-up area sits on a hole in the land
  mask, so `AND landMask` would erase exactly the parks that matter most.

Each layer then gets: blur → noise → threshold → antialias; texture application
with the final mask as alpha; and optionally a further-blurred copy reused as a
layer-specific darkening overlay.

## Work completed

- Explicit mask composition ops (alpha extraction, union/max, intersect/min,
  invert) with unit tests
- A cross-layer mask construction step that runs before any layer is painted
- The land pipeline switched to `landMask := invert(process(nonLandMask))`
  instead of land's own alpha
- Parks and civic constrained to land (AND `landMask`)
- A test verifying land is fully excluded where water or roads are present
- Blur/noise/threshold parameters retuned after the behaviour change

> **Note on sigmas**: the default sigma values were rescaled again during the
> blur rewrite, because the old blur ran about twice as wide as its nominal
> sigma. See [performance/blur-optimization.md](performance/blur-optimization.md).
