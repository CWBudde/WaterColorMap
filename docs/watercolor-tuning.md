# Watercolor tuning

This is the reference for the optional `watercolor:` block in `config.yaml`. It
supersedes the "Current Recommended Parameters" list in
[`3.6-visual-quality-testing.md`](3.6-visual-quality-testing.md), which is stale.

If there is no `watercolor:` key in your config, nothing here applies: the
renderer uses `watercolor.DefaultParams` verbatim, with no arithmetic in
between. That is deliberate — it is what keeps the golden images in
`testdata/golden/` byte-identical across releases.

## The pipeline, in the order it runs

Every knob below belongs to exactly one of these five stages. Tuning goes badly
when you reach for a knob from the wrong stage, so it is worth knowing which is
which. The order is literally the order of `processMask` followed by
`paintFromFinalMaskWithContext` in `internal/watercolor/processor.go`.

### 1. Blur — soften the hard vector mask

Mapnik hands us a crisp, aliased polygon mask. Blurring it is what later lets
the threshold cut a wobbly, hand-painted-looking edge instead of a vector one.

| Key                              | Default   | Effect                     |
| -------------------------------- | --------- | -------------------------- |
| `defaults.blur-sigma`            | 2.45      | Global blur radius, in px. |
| `layers.<layer>.mask-blur-sigma` | per layer | Per-layer override.        |

Bigger sigma → rounder, softer, more "bleed"; small features dissolve first.
Smaller sigma → the shape stays recognisable but starts to look like a vector
map again.

> The defaults look oddly precise (2.45, 1.41, 7.48) because they are not
> hand-picked. They are the blur widths the renderer was already producing back
> when the blur applied roughly twice the sigma it was asked for. When that was
> fixed, the sigmas were restated so the tuned appearance survived. Retune them
> freely — but retune them as blur widths in pixels, which is now what they mean.

### 2. Noise — displace the edge so it stops being geometric

Perlin noise is added to the blurred mask before the cut. This is the single
biggest contributor to the watercolor look: without it the threshold produces a
smooth, soap-bubble outline.

| Key                                  | Default   | Effect                                                     |
| ------------------------------------ | --------- | ---------------------------------------------------------- |
| `defaults.noise-scale`               | 30.0      | Perlin feature size in px. Larger = coarser, calmer grain. |
| `defaults.noise-strength`            | 0.28      | 0–1. How far the noise pushes the edge.                    |
| `layers.<layer>.mask-noise-strength` | per layer | Per-layer override.                                        |
| `layers.<layer>.adaptive-noise`      | per layer | Scale noise down near thin features.                       |
| `layers.<layer>.noise-min-dist`      | 2.0       | px from the edge below which noise is minimal.             |
| `layers.<layer>.noise-max-dist`      | 10.0      | px from the edge above which noise is at full strength.    |

`adaptive-noise` exists because full-strength noise fragments anything narrower
than about twice the noise amplitude. Roads, rivers and railways all enable it;
without it a residential street breaks into dashes at z13.

The noise field is anchored to world position, not to the tile. That is what
makes adjacent tiles line up, and it is why `noise-scale` is a length that gets
scaled for hi-DPI rather than a per-tile constant.

### 3. Threshold — cut the soft mask back to a hard one

| Key                             | Default   | Effect               |
| ------------------------------- | --------- | -------------------- |
| `defaults.threshold`            | 50        | 0–255 global cutoff. |
| `layers.<layer>.mask-threshold` | per layer | Per-layer override.  |

Threshold interacts with blur, and this trips people up: raising the threshold
on a heavily blurred mask _shrinks_ the shape, because more of the blur's
falloff now falls below the cut. Layers drawn on top of land use higher
thresholds (parks 120, urban 160, buildings 150) precisely to pull them in a
little so the land underneath still reads.

`defaults.antialias-sigma` (1.41) then smooths the freshly cut edge. It is a
finishing pass, not a shape control — reach for `blur-sigma` if you want a
different shape.

### 4. Edge darkening — the pigment that pools at a wet edge

| Key                            | Default | Effect                                             |
| ------------------------------ | ------- | -------------------------------------------------- |
| `layers.<layer>.edge-strength` | 0.2–0.3 | 0–1. Darkness of the halo. **0 disables it.**      |
| `layers.<layer>.edge-sigma`    | 2.5–3.5 | px. The halo radius is `edge-sigma * 3`.           |
| `layers.<layer>.edge-gamma`    | 8.6–9.3 | Falloff steepness. Higher = tighter, crisper ring. |

There is deliberately **no edge colour key**. The edge pass only reduces HSL
lightness; it has no colour input, so such a key could not do anything.

`edge-gamma` is unitless even though it looks like it should be a distance: the
distance field it consumes has already been normalised by the maximum distance,
so gamma is a curve shape, not a radius. This is why it is not scaled for
hi-DPI while `edge-sigma` is.

`shade-sigma` / `shade-strength` are the same idea at a much larger radius —
broad interior shading rather than an edge ring. Only `land` uses them by
default (sigma 7.48, strength 0.12).

### 5. Paint — the paper texture

The final mask becomes the alpha channel of a tiled paper texture.

| Key                            | Effect                                               |
| ------------------------------ | ---------------------------------------------------- |
| `layers.<layer>.tint.color`    | Hex, e.g. `#8ab4c8`. Recolours this layer's texture. |
| `layers.<layer>.tint.strength` | 0–1. 0 is untinted, 1 is flat colour.                |

Tinting is precomputed once when the config is loaded, not per tile, so a tint
costs nothing at render time.

Note that `rivers` shares the _water_ bitmap. The tint is keyed by layer, not by
texture, so tinting `water` leaves `rivers` alone — tint both if you want both.

## Rules that apply to every key

**Lengths are in pixels at the 256px reference tile size.** They are scaled
automatically for larger tiles — an `@2x` render uses scale 2 — so do not double
them by hand. Unitless values (all the `*-strength` keys, `edge-gamma`, both
thresholds, the booleans) are never scaled.

**Unknown keys and unknown layer names are startup errors,** not silent no-ops.
A typo will stop the run before the first Overpass request rather than quietly
rendering with defaults. Valid layer names are `land`, `water`, `rivers`,
`parks`, `urban`, `civic`, `buildings`, `roads`, `railroads`, `highways`.

**Every problem is reported at once,** with the full key path, so you can fix a
whole file in one pass.

**Sigmas are capped at 20** (at the reference size). This is not arbitrary
caution: the metatile size, and therefore the Overpass fetch bounding box, is
derived from the largest sigma in play. An unbounded sigma lets one config line
quadruple the area fetched and rendered for every tile.

**`mask-blur-sigma: 0` and `mask-noise-strength: 0` are rejected.** In the
renderer those two fields use a `> 0` sentinel meaning "fall back to the global
value", so an explicit zero would mean _inherit_, not _off_ — the opposite of
what anyone typing it intends. Remove the key to inherit, or use a small
positive value. (Changing the sentinel would move the goldens and is tracked
separately.)

## A worked example

```yaml
watercolor:
  defaults:
    blur-sigma: 3.2 # softer overall
    threshold: 60 # ...pulled back in, since more blur means more spread
  layers:
    water:
      tint:
        color: "#7fa8c4"
        strength: 0.3
    rivers:
      tint: # rivers share water's bitmap but not its tint
        color: "#7fa8c4"
        strength: 0.3
    buildings:
      edge-strength: 0 # no halo; let buildings sit flat
```

## Working method

1. Change one stage at a time, starting from blur and moving down. The stages
   compound, and a threshold that looks wrong is usually a blur that changed.
2. Render the same tile before and after:
   `just generate --zoom 13 --x 4317 --y 2692`.
3. Check a 2×2 block, not a single tile — several of these knobs feed the
   padding calculation, and padding bugs only show up at tile borders. See
   [`seam-inspection.md`](seam-inspection.md).
4. Run `go test ./internal/...`. If a golden image moves, the change leaked into
   the no-config path; fix the code rather than regenerating the golden.
