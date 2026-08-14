# Manual seam inspection (Leaflet)

Tiles are rendered independently, so every effect that looks at more than one
pixel — blur, noise, texture, edge darkening — is a chance to produce a visible
line where two tiles meet. The automated guard is
`TestCompositedTileSeams` in `internal/pipeline/seam_test.go`: it renders a 2×2
block through the full generator and compares the step across each border with
the step just inside the tiles. It is deliberately blind to anything that does
not touch the border row/column, so a quick manual pass over a real map stays
worth the five minutes.

Do this after changing anything in `internal/mask`, `internal/watercolor`,
`internal/texture`, the metatile padding, or any Mapnik style.

## Generate and serve

```bash
just prebuild-hannover   # a small Hanover tile set
just serve               # http://127.0.0.1:8080
```

Then open <http://127.0.0.1:8080/demo/>.

## What to look for

Zoom the browser to 200–400 % over a tile corner; seams that are invisible at
100 % are still visible on a phone screen.

- **Grain discontinuity at a border.** The paper and watercolor grain must flow
  through the border. A sudden change in grain size or contrast along a perfectly
  straight horizontal or vertical line is the giveaway — real watercolor edges
  are never axis-aligned for 256 pixels.
- **Texture phase jumps.** Follow a large blotch of the paper texture across a
  border. It must continue, not restart. A restart means the texture is being
  anchored to the tile rather than to the world.
- **Edge-darkening halos at borders.** Watercolor layers are darkened at their
  edges on purpose. That darkening must appear only at the edge of a _feature_
  (a lake, a park), never along a tile border. A thin dark or light frame around
  each tile means padding or the crop is wrong.
- **Features that stop at the border.** Roads, rivers and coastlines must line up
  across the seam without a kink or an offset. Diagonals are the sensitive case;
  a half-pixel disagreement shows up as a visible step.
- **`@2x` versus `@1x` at the same location.** Load the same view with retina
  tiles and without (a `@2x`-capable browser, or force `detectRetina` in the demo
  page). The noise and texture must be anchored to the same world position at
  both sizes: they should look like the same map at two resolutions, not two
  different maps. Compare a recognizable blotch, not the overall tone.
- **Zoom levels.** Repeat at the lowest and highest zoom you generate. Blur sigma
  is zoom-adjusted, so a padding problem can appear at one zoom only.

If you find a seam, capture the two tile PNGs and the z/x/y, and reproduce it in
`internal/pipeline/seam_test.go` before fixing it — the fixture there takes
coordinates as constants.

## Phase 4.6 acceptance checklist

Run through this after `just prebuild-hannover` + `just serve`, with the browser
devtools console open:

- [ ] The demo page loads with **no console errors**.
- [ ] Tiles load and **pan smoothly**; no missing tiles left behind while panning.
- [ ] **HiDPI (`@2x`) tiles render** where present, and the map does not fall back
      to blurred upscaled `@1x` tiles.
- [ ] **Missing tiles are generated on demand** — pan to an area that was never
      prebuilt and confirm the tile appears (the server logs the generation).
- [ ] The **second request is served from disk**: reload the same view and confirm
      the tile comes back immediately, with no generation logged, and that the PNG
      now exists under the tile directory.
