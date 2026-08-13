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
		for y := expectedBounds.Min.Y; y < expectedBounds.Max.Y; y++ {
			for x := expectedBounds.Min.X; x < expectedBounds.Max.X; x++ {
				dst.Set(x, y, base.At(x, y))
			}
		}
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

func alphaOver(dst *image.NRGBA, src image.Image) {
	bounds := dst.Bounds()

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			s, ok := color.NRGBAModel.Convert(src.At(x, y)).(color.NRGBA)
			if !ok || s.A == 0 {
				continue
			}

			d := dst.NRGBAAt(x, y)

			sa := float64(s.A) / 255.0
			da := float64(d.A) / 255.0

			outA := sa + da*(1.0-sa)
			if outA == 0 {
				dst.SetNRGBA(x, y, color.NRGBA{})
				continue
			}

			blend := func(srcVal, dstVal uint8) uint8 {
				srcPremult := float64(srcVal) * sa
				dstPremult := float64(dstVal) * da
				outPremult := srcPremult + dstPremult*(1.0-sa)
				return uint8(math.Round(outPremult / outA))
			}

			dst.SetNRGBA(x, y, color.NRGBA{
				R: blend(s.R, d.R),
				G: blend(s.G, d.G),
				B: blend(s.B, d.B),
				A: uint8(math.Round(outA * 255.0)),
			})
		}
	}
}
