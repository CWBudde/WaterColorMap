// Package geo holds the spherical Web-Mercator (EPSG:3857) math shared by the
// tile, renderer, raster and types packages. It is a leaf package: it must not
// import anything from internal/, so every other package can depend on it
// without risking an import cycle.
package geo

import "math"

// EarthRadius is the sphere radius used by EPSG:3857, in metres.
const EarthRadius = 6378137.0

// MaxLatMercator is the highest latitude representable in Web Mercator. Beyond
// it the projection diverges (lat=90 maps to +Inf), so the tile scheme cuts the
// world off at this parallel, which makes the projected world square.
const MaxLatMercator = 85.05112878

// ClampLat limits a latitude to the Web Mercator valid range.
func ClampLat(lat float64) float64 {
	if lat > MaxLatMercator {
		return MaxLatMercator
	}
	if lat < -MaxLatMercator {
		return -MaxLatMercator
	}
	return lat
}

// MercatorY is the normalized (unit-sphere) Mercator ordinate for a latitude in
// degrees: log(tan(pi/4 + phi/2)). It ranges over roughly [-pi, pi] across the
// valid latitude band. Callers working in metres multiply by EarthRadius;
// callers working in global pixel space divide by pi.
//
// The latitude is not clamped, so lat=+-90 yields +-Inf; use ClampLat first if
// the input can reach the poles.
func MercatorY(lat float64) float64 {
	latRad := lat * math.Pi / 180.0
	return math.Log(math.Tan(math.Pi/4.0 + latRad/2.0))
}

// LonLatToMercator converts WGS84 lon/lat in degrees to Web Mercator
// (EPSG:3857) x/y in metres.
func LonLatToMercator(lon, lat float64) (x, y float64) {
	x = EarthRadius * lon * math.Pi / 180.0
	y = EarthRadius * MercatorY(lat)
	return x, y
}

// MercatorToLonLat converts Web Mercator (EPSG:3857) x/y in metres back to
// WGS84 lon/lat in degrees. It is the exact inverse of LonLatToMercator.
func MercatorToLonLat(x, y float64) (lon, lat float64) {
	lon = (x / EarthRadius) * 180.0 / math.Pi
	lat = (math.Atan(math.Exp(y/EarthRadius)) - math.Pi/4.0) * 2.0 * 180.0 / math.Pi
	return lon, lat
}

// MercatorYToLat converts a normalized Mercator ordinate (the output of
// MercatorY) back to latitude in degrees. This is the sinh formulation, which
// is algebraically identical to MercatorToLonLat's latitude branch but takes
// its input unscaled by EarthRadius.
func MercatorYToLat(mercatorY float64) float64 {
	return 180.0 / math.Pi * math.Atan(math.Sinh(mercatorY))
}
