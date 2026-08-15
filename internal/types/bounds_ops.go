package types

// Union returns the smallest bounding box containing both b and o.
//
// Band fetching builds a band's query box as the union of its member tiles'
// padded fetch bounds, rather than expanding the enclosing ancestor tile.
// The two are not the same: ExpandByFraction pads by a fraction of the box's
// own extent, so an ancestor's padding is several times too wide, and in
// latitude the Mercator stretch makes it wrong rather than merely generous.
// Taking the literal union keeps the per-tile expression the single source of
// truth for how much padding a tile needs.
func (b BoundingBox) Union(o BoundingBox) BoundingBox {
	return BoundingBox{
		MinLon: min(b.MinLon, o.MinLon),
		MinLat: min(b.MinLat, o.MinLat),
		MaxLon: max(b.MaxLon, o.MaxLon),
		MaxLat: max(b.MaxLat, o.MaxLat),
	}
}

// Contains reports whether b fully encloses o. Edges count as contained.
//
// Band routing needs containment rather than the intersection test the
// per-tile router uses: a band box can overlap a regional server's coverage
// while most of its tiles lie outside it, which would answer sixteen tiles
// from a server holding no data for them. Per-tile fetching caps that mistake
// at one tile; banding would not.
func (b BoundingBox) Contains(o BoundingBox) bool {
	return b.MinLon <= o.MinLon && b.MaxLon >= o.MaxLon &&
		b.MinLat <= o.MinLat && b.MaxLat >= o.MaxLat
}

// Intersects reports whether b and o share any area. Touching edges count.
func (b BoundingBox) Intersects(o BoundingBox) bool {
	return b.MinLon <= o.MaxLon && b.MaxLon >= o.MinLon &&
		b.MinLat <= o.MaxLat && b.MaxLat >= o.MinLat
}
