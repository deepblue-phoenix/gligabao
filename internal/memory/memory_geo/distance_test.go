package memory_geo

import (
	"math"
	"math/rand"
	"testing"
)

func TestDistanceKm(t *testing.T) {
	for _, c := range []struct {
		lat1, lon1, lat2, lon2, want, tol float64
	}{
		{48.8566, 2.3522, 51.5074, -0.1278, 343.5, 2},    // Paris - London
		{0, 0, 0, 0, 0, 0.001},                           // same point
		{0, 179.5, 0, -179.5, 111.2, 0.5},                // across the antimeridian
		{90, 0, -90, 0, MaxDistanceKm, 5},                // pole to pole
		{40.7128, -74.006, 35.6762, 139.6503, 10838, 20}, // New York - Tokyo
	} {
		if got := DistanceKm(c.lat1, c.lon1, c.lat2, c.lon2); math.Abs(got-c.want) > c.tol {
			t.Errorf("DistanceKm(%v,%v,%v,%v) = %.1f, want %.1f", c.lat1, c.lon1, c.lat2, c.lon2, got, c.want)
		}
	}
}

func inBox(b Box, lat, lon float64) bool {
	if lat < b.MinLat || lat > b.MaxLat {
		return false
	}
	for _, r := range b.Lon {
		if lon >= r.Min && lon <= r.Max {
			return true
		}
	}
	return false
}

// TestBoundingBox checks that no point within the radius is outside the box, near the antimeridian, the poles and the equator.
func TestBoundingBox(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	centers := [][2]float64{{0, 0}, {48.85, 2.35}, {10, 179.9}, {-10, -179.9}, {89.5, 20}, {-89.9, -100}, {70, 175}, {0, 180}, {0, -180}}
	for _, c := range centers {
		for _, r := range []float64{1, 50, 500, 3000, 15000, 25000} {
			b := BoundingBox(c[0], c[1], r)
			if len(b.Lon) == 0 || len(b.Lon) > 2 {
				t.Fatalf("%v r=%v: %d lon ranges", c, r, len(b.Lon))
			}
			for i := 0; i < 3000; i++ {
				lat, lon := math.Asin(2*rng.Float64()-1)*180/math.Pi, rng.Float64()*360-180
				if c[0] > 60 || c[0] < -60 || i%2 == 0 { // concentrate samples near the center
					lat, lon = c[0]+(rng.Float64()-0.5)*2*r/111, c[1]+(rng.Float64()-0.5)*2*r/111
					lat = math.Max(-90, math.Min(90, lat))
					lon = math.Mod(lon+540, 360) - 180
				}
				if DistanceKm(c[0], c[1], lat, lon) <= r && !inBox(b, lat, lon) {
					t.Fatalf("%v r=%v: (%v, %v) is %.1f km away but outside the box %+v", c, r, lat, lon, DistanceKm(c[0], c[1], lat, lon), b)
				}
			}
		}
	}
	if b := BoundingBox(10, 179.9, 50); len(b.Lon) != 2 {
		t.Errorf("antimeridian: want two ranges, got %+v", b.Lon)
	}
	if b := BoundingBox(89.9, 0, 50); len(b.Lon) != 1 || b.Lon[0] != (LonRange{-180, 180}) || b.MaxLat != 90 {
		t.Errorf("pole: %+v", b)
	}
}
