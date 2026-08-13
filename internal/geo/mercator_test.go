package geo

import (
	"math"
	"testing"
)

var testPoints = [][2]float64{
	{0, 0},           // Null Island
	{9.73, 52.37},    // Hanover
	{-122.42, 37.78}, // San Francisco
	{139.69, 35.69},  // Tokyo
	{-180, -85.05},   // south-west corner of the projected world
	{180, 85.05},     // north-east corner of the projected world
}

// TestMercatorRoundTrip moved here from internal/tile/coords_test.go when the
// projection math was consolidated into this package.
func TestMercatorRoundTrip(t *testing.T) {
	for _, point := range testPoints {
		lon, lat := point[0], point[1]

		x, y := LonLatToMercator(lon, lat)
		lon2, lat2 := MercatorToLonLat(x, y)

		if math.Abs(lon-lon2) > 1e-6 || math.Abs(lat-lat2) > 1e-6 {
			t.Errorf("round-trip failed: (%.6f, %.6f) -> (%.2f, %.2f) -> (%.6f, %.6f)",
				lon, lat, x, y, lon2, lat2)
		}
	}
}

// TestInverseFormulationsAgree pins that the two historical inverse
// formulations produce the same latitude: the atan(exp(y/R)) form that lived in
// internal/tile and the atan(sinh(y)) form that lived in internal/types.
func TestInverseFormulationsAgree(t *testing.T) {
	for _, point := range testPoints {
		lat := point[1]

		my := MercatorY(lat)
		viaExp := func() float64 {
			_, l := MercatorToLonLat(0, my*EarthRadius)
			return l
		}()
		viaSinh := MercatorYToLat(my)

		if math.Abs(viaExp-viaSinh) > 1e-9 {
			t.Errorf("inverse formulations disagree at lat=%.6f: exp form %.12f, sinh form %.12f",
				lat, viaExp, viaSinh)
		}
	}

	// Also sweep the normalized ordinate directly, independent of any latitude.
	for my := -math.Pi; my <= math.Pi; my += 0.05 {
		_, viaExp := MercatorToLonLat(0, my*EarthRadius)
		viaSinh := MercatorYToLat(my)
		if math.Abs(viaExp-viaSinh) > 1e-9 {
			t.Errorf("inverse formulations disagree at mercatorY=%.4f: %.12f vs %.12f",
				my, viaExp, viaSinh)
		}
	}
}

func TestMercatorYMatchesMetreScaling(t *testing.T) {
	for _, point := range testPoints {
		lon, lat := point[0], point[1]
		_, y := LonLatToMercator(lon, lat)
		if got, want := MercatorY(lat)*EarthRadius, y; got != want {
			t.Errorf("MercatorY(%.6f)*EarthRadius = %v, want %v", lat, got, want)
		}
	}
}

func TestClampLat(t *testing.T) {
	tests := []struct {
		in, want float64
	}{
		{0, 0},
		{52.37, 52.37},
		{90, MaxLatMercator},
		{-90, -MaxLatMercator},
		{MaxLatMercator, MaxLatMercator},
		{85.06, MaxLatMercator},
	}
	for _, tt := range tests {
		if got := ClampLat(tt.in); got != tt.want {
			t.Errorf("ClampLat(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}

	// The clamp is what keeps the poles inside the projected world. Unclamped,
	// tan(pi/2) blows up (to +-Inf in exact arithmetic, to a ~1e16 float here),
	// so MercatorY runs far past the +-pi the tile grid can represent.
	if got := MercatorY(90); got <= math.Pi {
		t.Errorf("expected unclamped MercatorY(90) to overshoot pi, got %v", got)
	}
	if got := MercatorY(ClampLat(90)); math.Abs(got-math.Pi) > 1e-6 {
		t.Errorf("MercatorY(ClampLat(90)) = %v, want ~pi", got)
	}
	if got := MercatorY(ClampLat(-90)); math.Abs(got+math.Pi) > 1e-6 {
		t.Errorf("MercatorY(ClampLat(-90)) = %v, want ~-pi", got)
	}
}
