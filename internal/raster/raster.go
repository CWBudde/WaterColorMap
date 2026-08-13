package raster

import (
	"image"
	"image/color"
	"math"

	"github.com/paulmach/orb"
	"golang.org/x/image/vector"

	"github.com/cwbudde/watercolormap/internal/geo"
	"github.com/cwbudde/watercolormap/internal/geojson"
	"github.com/cwbudde/watercolormap/internal/types"
)

type Renderer struct {
	zoom      int
	tileSize  int
	offsetX   int // global pixel space
	offsetY   int // global pixel space
	canvasW   int
	canvasH   int
	fillColor color.NRGBA
}

// NewRenderer creates a renderer that maps lon/lat to a pixel canvas.
// offsetX/offsetY are the top-left pixel of the canvas in global pixel coordinates at the given zoom.
func NewRenderer(zoom int, tileSize int, canvasW int, canvasH int, offsetX int, offsetY int) *Renderer {
	return &Renderer{
		zoom:      zoom,
		tileSize:  tileSize,
		offsetX:   offsetX,
		offsetY:   offsetY,
		canvasW:   canvasW,
		canvasH:   canvasH,
		fillColor: color.NRGBA{R: 0, G: 0, B: 0, A: 255},
	}
}

func (r *Renderer) RenderLayers(fc types.FeatureCollection) map[geojson.LayerType]*image.NRGBA {
	b := image.Rect(0, 0, r.canvasW, r.canvasH)
	water := image.NewNRGBA(b)
	rivers := image.NewNRGBA(b)
	parks := image.NewNRGBA(b)
	urban := image.NewNRGBA(b)
	civic := image.NewNRGBA(b)
	buildings := image.NewNRGBA(b)
	roads := image.NewNRGBA(b)
	railroads := image.NewNRGBA(b)
	highways := image.NewNRGBA(b)

	// Water polygons (lakes, ponds, coastlines)
	for i := range fc.Water {
		r.renderFeature(water, &fc.Water[i], r.getWaterStrokeWidth())
	}

	// Rivers (linear waterways: rivers, streams, canals)
	// Rendered with LineSymbolizer to avoid polygon closing issues
	for i := range fc.Rivers {
		r.renderFeature(rivers, &fc.Rivers[i], r.getRiverStrokeWidth())
	}

	// Parks polygons
	for i := range fc.Parks {
		r.renderFeature(parks, &fc.Parks[i], 0)
	}

	// Urban landuse areas (residential/commercial/industrial/retail)
	// Rendered as areas, with buildings drawn on top at high zoom
	for i := range fc.Urban {
		r.renderFeature(urban, &fc.Urban[i], 0)
	}

	// Civic areas (schools, hospitals, universities, libraries, town halls)
	// Rendered as areas with lilac texture, with buildings drawn on top at high zoom
	for i := range fc.Civic {
		r.renderFeature(civic, &fc.Civic[i], 0)
	}

	// Individual building footprints - only at zoom 16+
	// Buildings are drawn on top of their respective urban/civic areas
	if r.zoom >= 16 {
		for i := range fc.Buildings {
			r.renderFeature(buildings, &fc.Buildings[i], 0)
		}
	}

	// Roads + derived highways
	for i := range fc.Roads {
		f := &fc.Roads[i]
		if r.isHighway(f) {
			r.renderFeature(highways, f, r.getHighwayStrokeWidth())
		} else {
			r.renderFeature(roads, f, r.getRoadStrokeWidth())
		}
	}

	// Railway lines (rail, light_rail, subway, tram)
	for i := range fc.Railroads {
		r.renderFeature(railroads, &fc.Railroads[i], r.getRailroadStrokeWidth())
	}

	return map[geojson.LayerType]*image.NRGBA{
		geojson.LayerWater:     water,
		geojson.LayerRivers:    rivers,
		geojson.LayerParks:     parks,
		geojson.LayerUrban:     urban,
		geojson.LayerCivic:     civic,
		geojson.LayerBuildings: buildings,
		geojson.LayerRoads:     roads,
		geojson.LayerRailroads: railroads,
		geojson.LayerHighways:  highways,
	}
}

func (r *Renderer) isHighway(f *types.Feature) bool {
	if f == nil || f.Properties == nil {
		return false
	}
	hw, isString := f.Properties["highway"].(string)
	if !isString {
		// A missing or non-string highway tag never matches any zoom bracket.
		return false
	}

	// At very low zoom (z <= 7), don't show any highways
	if r.zoom <= 7 {
		return false
	}

	// At low zoom (z8-9), only show motorways and trunks
	if r.zoom <= 9 {
		switch hw {
		case "motorway", "motorway_link", "trunk", "trunk_link":
			return true
		default:
			return false
		}
	}

	// At medium-low zoom (z10-11), add primary roads
	if r.zoom <= 11 {
		switch hw {
		case "motorway", "motorway_link", "trunk", "trunk_link",
			"primary", "primary_link":
			return true
		default:
			return false
		}
	}

	// At medium zoom (z12-14), add secondary roads
	if r.zoom <= 14 {
		switch hw {
		case "motorway", "motorway_link", "trunk", "trunk_link",
			"primary", "primary_link", "secondary", "secondary_link":
			return true
		default:
			return false
		}
	}

	// At high zoom (z15+), also treat tertiary roads as highways
	switch hw {
	case "motorway", "motorway_link", "trunk", "trunk_link",
		"primary", "primary_link", "secondary", "secondary_link",
		"tertiary", "tertiary_link":
		return true
	default:
		return false
	}
}

// getHighwayStrokeWidth returns the stroke width for highways based on zoom level.
// Ensures highways are always visible with 1-2 pixel minimum width.
func (r *Renderer) getHighwayStrokeWidth() int {
	switch {
	case r.zoom <= 9:
		return 1 // Minimum visibility at very low zoom
	case r.zoom <= 11:
		return 2 // Slightly thicker for low-medium zoom
	case r.zoom <= 13:
		return 2 // Medium zoom
	case r.zoom <= 15:
		return 3 // Medium-high zoom
	default:
		return 4 // High zoom (original width)
	}
}

// getRoadStrokeWidth returns the stroke width for regular roads based on zoom level.
// Roads are generally slightly thinner than highways.
func (r *Renderer) getRoadStrokeWidth() int {
	switch {
	case r.zoom <= 11:
		return 1 // Minimum visibility at low zoom
	case r.zoom <= 13:
		return 2 // Medium zoom
	case r.zoom <= 15:
		return 2 // Medium-high zoom
	default:
		return 3 // High zoom (original width)
	}
}

// getRailroadStrokeWidth returns the stroke width for railway lines based on zoom level.
// Railroads are rendered slightly thinner than regular roads.
func (r *Renderer) getRailroadStrokeWidth() int {
	switch {
	case r.zoom <= 11:
		return 1 // Minimum visibility at low zoom
	case r.zoom <= 15:
		return 2 // Medium and medium-high zoom
	default:
		return 3 // High zoom
	}
}

// getWaterStrokeWidth returns the stroke width for water polygons based on zoom level.
// This applies to polygonal water bodies (lakes, ponds, coastlines).
func (r *Renderer) getWaterStrokeWidth() int {
	switch {
	case r.zoom <= 9:
		return 2 // Small water bodies visible at low zoom
	case r.zoom <= 11:
		return 3 // Low-medium zoom
	case r.zoom <= 13:
		return 4 // Medium zoom
	case r.zoom <= 15:
		return 5 // Medium-high zoom
	default:
		return 6 // High zoom (original width)
	}
}

// getRiverStrokeWidth returns the stroke width for linear waterways based on zoom level.
// This applies to rivers and canals (streams excluded at zoom ≤15).
// Wider strokes at low zoom ensure rivers remain visible.
func (r *Renderer) getRiverStrokeWidth() int {
	switch {
	case r.zoom <= 9:
		return 2 // Major rivers only, visible width
	case r.zoom <= 11:
		return 2 // Low-medium zoom
	case r.zoom <= 13:
		return 3 // Medium zoom
	case r.zoom <= 15:
		return 4 // Medium-high zoom - rivers/canals only
	default:
		return 4 // High zoom - includes streams
	}
}

func (r *Renderer) renderFeature(dst *image.NRGBA, f *types.Feature, strokeWidth int) {
	if f == nil {
		return
	}

	switch g := f.Geometry.(type) {
	case orb.Polygon:
		r.fillPolygon(dst, g)
	case orb.MultiPolygon:
		for _, p := range g {
			r.fillPolygon(dst, p)
		}
	case orb.Ring:
		r.fillPolygon(dst, orb.Polygon{g})
	case orb.LineString:
		w := strokeWidth
		if w <= 0 {
			w = 3
		}
		r.strokeLineString(dst, g, w)
	case orb.MultiLineString:
		w := strokeWidth
		if w <= 0 {
			w = 3
		}
		for _, ls := range g {
			r.strokeLineString(dst, ls, w)
		}
	default:
		// ignore points/unknown geometries (e.g. relation placeholders)
	}
}

func (r *Renderer) fillPolygon(dst *image.NRGBA, poly orb.Polygon) {
	if len(poly) == 0 {
		return
	}

	ras := vector.NewRasterizer(r.canvasW, r.canvasH)

	for _, ring := range poly {
		if len(ring) < 3 {
			continue
		}
		// MoveTo/LineTo expect fixed-point 26.6.
		first := true
		for _, pt := range ring {
			x, y := r.lonLatToLocalPx(pt[0], pt[1])
			fx := float32(x)
			fy := float32(y)
			if first {
				ras.MoveTo(fx, fy)
				first = false
			} else {
				ras.LineTo(fx, fy)
			}
		}
		ras.ClosePath()
	}

	src := image.NewUniform(r.fillColor)
	ras.Draw(dst, dst.Bounds(), src, image.Point{})
}

func (r *Renderer) strokeLineString(dst *image.NRGBA, ls orb.LineString, width int) {
	if len(ls) < 2 {
		return
	}
	radius := float64(width) / 2.0
	step := 0.75
	if width >= 5 {
		step = 0.9
	}

	for i := 0; i < len(ls)-1; i++ {
		x0, y0 := r.lonLatToLocalPx(ls[i][0], ls[i][1])
		x1, y1 := r.lonLatToLocalPx(ls[i+1][0], ls[i+1][1])

		dx := x1 - x0
		dy := y1 - y0
		segLen := math.Hypot(dx, dy)
		if segLen == 0 {
			r.drawDisc(dst, x0, y0, radius)
			continue
		}

		steps := int(math.Ceil(segLen / step))
		for s := 0; s <= steps; s++ {
			t := float64(s) / float64(steps)
			x := x0 + dx*t
			y := y0 + dy*t
			r.drawDisc(dst, x, y, radius)
		}
	}
}

func (r *Renderer) drawDisc(dst *image.NRGBA, cx, cy float64, radius float64) {
	minX := int(math.Floor(cx - radius))
	maxX := int(math.Ceil(cx + radius))
	minY := int(math.Floor(cy - radius))
	maxY := int(math.Ceil(cy + radius))

	if minX < 0 {
		minX = 0
	}
	if minY < 0 {
		minY = 0
	}
	if maxX >= r.canvasW {
		maxX = r.canvasW - 1
	}
	if maxY >= r.canvasH {
		maxY = r.canvasH - 1
	}

	r2 := radius * radius
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			dx := (float64(x) + 0.5) - cx
			dy := (float64(y) + 0.5) - cy
			if dx*dx+dy*dy <= r2 {
				i := dst.PixOffset(x, y)
				dst.Pix[i+3] = 255
			}
		}
	}
}

// lonLatToLocalPx maps WGS84 lon/lat to local pixel coordinates on the current canvas.
// It uses WebMercator math in "global pixel" space, then applies the configured offset.
func (r *Renderer) lonLatToLocalPx(lon, lat float64) (float64, float64) {
	n := math.Pow(2, float64(r.zoom))

	// Global pixel space (at this zoom) in [0, n*tileSize)
	globalX := (lon + 180.0) / 360.0 * n * float64(r.tileSize)

	// Clamping keeps the ordinate inside the usable band: near the poles MercatorY
	// blows up (huge magnitudes, -Inf at lat=-90, NaN beyond +-90).
	mercY := geo.MercatorY(geo.ClampLat(lat))
	globalY := (1.0 - mercY/math.Pi) / 2.0 * n * float64(r.tileSize)

	return globalX - float64(r.offsetX), globalY - float64(r.offsetY)
}
