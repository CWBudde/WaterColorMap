package types

// FilterByBounds returns the features whose geometry bounding box intersects b,
// preserving both the layer split and the order within each layer.
//
// This is what makes band fetching behaviour-preserving rather than merely
// cheaper. A band response is a superset of every member tile's per-tile
// response, and handing that superset straight to a tile render would change
// two things that are not about geometry:
//
//   - The renderer skips a layer entirely when it has zero features, producing
//     an absent layer rather than a blank one. Band data can flip absent into
//     present-but-blank, and those take different paths through painting.
//   - The emptiness check that catches silent Overpass failures is per tile.
//
// Filtering to the tile's own fetch bounds restores both. It cannot lose
// anything: Overpass returns every element whose geometry intersects the query
// box, and geometry-intersects implies bbox-intersects, so this filter is a
// superset of what a per-tile query would have returned.
//
// The residual difference, worth knowing: a feature whose bbox intersects the
// tile while its geometry does not — an L-shaped way — is kept here and would
// not have been fetched per tile. It can only paint inside the metatile via
// stroke half-width across the boundary, which the padding crop removes.
//
// Order is preserved deliberately. Feature order already varies run to run
// (ExtractFeaturesFromOverpassResult ranges over a map), and this must not add
// a second, different source of variation on top of it.
func (fc FeatureCollection) FilterByBounds(b BoundingBox) FeatureCollection {
	return FeatureCollection{
		Water:     filterFeatures(fc.Water, b),
		Rivers:    filterFeatures(fc.Rivers, b),
		Parks:     filterFeatures(fc.Parks, b),
		Roads:     filterFeatures(fc.Roads, b),
		Railroads: filterFeatures(fc.Railroads, b),
		Buildings: filterFeatures(fc.Buildings, b),
		Urban:     filterFeatures(fc.Urban, b),
		Civic:     filterFeatures(fc.Civic, b),
		Land:      filterFeatures(fc.Land, b),
	}
}

// filterFeatures keeps features whose geometry bound intersects b.
//
// A feature with no geometry is kept rather than dropped: it cannot be tested,
// and silently discarding data is the worse failure of the two.
func filterFeatures(features []Feature, b BoundingBox) []Feature {
	if len(features) == 0 {
		return nil
	}

	kept := make([]Feature, 0, len(features))
	for _, f := range features {
		if f.Geometry == nil {
			kept = append(kept, f)
			continue
		}

		bound := f.Geometry.Bound()
		geomBox := BoundingBox{
			MinLon: bound.Min[0],
			MinLat: bound.Min[1],
			MaxLon: bound.Max[0],
			MaxLat: bound.Max[1],
		}
		if b.Intersects(geomBox) {
			kept = append(kept, f)
		}
	}

	if len(kept) == 0 {
		// nil rather than an empty slice, so a filtered-to-nothing layer is
		// indistinguishable from one that was never fetched. That equivalence
		// is what keeps the renderer's absent-layer path intact.
		return nil
	}
	return kept
}
