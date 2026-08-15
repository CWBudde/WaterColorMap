package composite

import (
	"fmt"
	"image"
	"image/color"
	"math"

	"github.com/cwbudde/watercolormap/internal/geojson"
)

// DefaultOrder defines the bottom-to-top compositing order for watercolor layers.
// It is the single source of truth for layer stacking; callers that composite a
// full tile should pass nil (or DefaultOrder) rather than spelling out an order.
// Urban/civic are below parks so green spaces show on top of developed areas.
// Buildings are on top so individual footprints show over everything at high zoom.
// Layers absent from the layer map are skipped, so partial renderers can reuse it.
var DefaultOrder = []geojson.LayerType{
	geojson.LayerLand,
	geojson.LayerUrban, // Urban landuse areas (lighter lavender)
	geojson.LayerCivic, // Civic areas (schools, hospitals, universities)
	geojson.LayerParks,
	geojson.LayerRivers, // Linear waterways
	geojson.LayerWater,
	geojson.LayerRoads,
	geojson.LayerRailroads,
	geojson.LayerHighways,
	geojson.LayerBuildings, // Buildings on top (darker lavender)
}

// CompositeLayersOverBase stacks watercolor-painted layers into a single tile over a pre-filled base.
// This is used to model "paper" showing through cutouts (e.g., roads as transparent holes).
func CompositeLayersOverBase(
	base image.Image,
	layers map[geojson.LayerType]image.Image,
	order []geojson.LayerType,
	tileSize int,
) (*image.NRGBA, error) {
	if tileSize <= 0 {
		return nil, fmt.Errorf("tile size must be positive")
	}

	if order == nil {
		order = DefaultOrder
	}

	expectedBounds := image.Rect(0, 0, tileSize, tileSize)
	dst := image.NewNRGBA(expectedBounds)

	if base != nil {
		if base.Bounds() != expectedBounds {
			return nil, fmt.Errorf("base bounds %v do not match expected %v", base.Bounds(), expectedBounds)
		}
		copyBase(dst, base)
	}

	for _, layer := range order {
		img := layers[layer]
		if img == nil {
			continue
		}

		if img.Bounds() != expectedBounds {
			return nil, fmt.Errorf("layer %s bounds %v do not match expected %v", layer, img.Bounds(), expectedBounds)
		}

		alphaOver(dst, img)
	}

	return dst, nil
}

// CompositeLayers stacks watercolor-painted layers into a single tile using alpha blending.
// Layers are drawn in the provided order (or DefaultOrder when nil). Each layer must match tileSize.
func CompositeLayers(
	layers map[geojson.LayerType]image.Image,
	order []geojson.LayerType,
	tileSize int,
) (*image.NRGBA, error) {
	if tileSize <= 0 {
		return nil, fmt.Errorf("tile size must be positive")
	}

	if order == nil {
		order = DefaultOrder
	}

	expectedBounds := image.Rect(0, 0, tileSize, tileSize)
	dst := image.NewNRGBA(expectedBounds)

	for _, layer := range order {
		img := layers[layer]
		if img == nil {
			continue
		}

		if img.Bounds() != expectedBounds {
			return nil, fmt.Errorf("layer %s bounds %v do not match expected %v", layer, img.Bounds(), expectedBounds)
		}

		alphaOver(dst, img)
	}

	return dst, nil
}

// copyBase fills dst with base, which the caller has already checked shares dst's
// bounds. The painted paper base is always an *image.NRGBA, so the common case is a
// row-wise memcpy; anything else goes through the colour model as before.
func copyBase(dst *image.NRGBA, base image.Image) {
	bounds := dst.Bounds()

	if src, ok := base.(*image.NRGBA); ok {
		rowLen := 4 * bounds.Dx()
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			copy(
				dst.Pix[dst.PixOffset(bounds.Min.X, y):][:rowLen],
				src.Pix[src.PixOffset(bounds.Min.X, y):][:rowLen],
			)
		}

		return
	}

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			dst.Set(x, y, base.At(x, y))
		}
	}
}

// alphaOver composites src over dst in place.
//
// Painted layers are always *image.NRGBA, so the fast path reads their pixels straight
// out of Pix instead of boxing a color.Color per pixel and converting it. The generic
// path below it is the same arithmetic reached through the interface, and produces
// identical output - alphaOverPixel is the only place the blend is written down.
func alphaOver(dst *image.NRGBA, src image.Image) {
	bounds := dst.Bounds()

	if s, ok := src.(*image.NRGBA); ok && s.Bounds() == bounds {
		rowLen := 4 * bounds.Dx()
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			srcRow := s.Pix[s.PixOffset(bounds.Min.X, y):][:rowLen]
			dstRow := dst.Pix[dst.PixOffset(bounds.Min.X, y):][:rowLen]
			for i := 0; i < rowLen; i += 4 {
				if srcRow[i+3] == 0 {
					continue
				}
				alphaOverPixel(
					color.NRGBA{R: srcRow[i], G: srcRow[i+1], B: srcRow[i+2], A: srcRow[i+3]},
					dstRow[i:i+4:i+4],
				)
			}
		}

		return
	}

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			s, ok := color.NRGBAModel.Convert(src.At(x, y)).(color.NRGBA)
			if !ok || s.A == 0 {
				continue
			}

			off := dst.PixOffset(x, y)
			alphaOverPixel(s, dst.Pix[off:off+4:off+4])
		}
	}
}

// alphaOverPixel blends a non-premultiplied source pixel over the four destination
// bytes in place.
func alphaOverPixel(s color.NRGBA, d []uint8) {
	sa := float64(s.A) / 255.0
	da := float64(d[3]) / 255.0

	outA := sa + da*(1.0-sa)
	if outA == 0 {
		d[0], d[1], d[2], d[3] = 0, 0, 0, 0

		return
	}

	blend := func(srcVal, dstVal uint8) uint8 {
		srcPremult := float64(srcVal) * sa
		dstPremult := float64(dstVal) * da
		outPremult := srcPremult + dstPremult*(1.0-sa)

		return uint8(math.Round(outPremult / outA))
	}

	d[0], d[1], d[2], d[3] =
		blend(s.R, d[0]),
		blend(s.G, d[1]),
		blend(s.B, d[2]),
		uint8(math.Round(outA*255.0))
}
