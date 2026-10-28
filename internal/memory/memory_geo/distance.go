package memory_geo

import "math"

// EarthRadiusKm is the mean radius of the Earth, which the distances below use (a sphere: the error against WGS 84 is under 0.5%).
const EarthRadiusKm = 6371.0088

// MaxDistanceKm is half the circumference of the Earth: no two points are farther apart than that.
const MaxDistanceKm = 20015.0

// DistanceKm is the great-circle (haversine) distance in kilometers between two points given in degrees.
func DistanceKm(lat1, lon1, lat2, lon2 float64) float64 {
	p1, p2 := lat1*math.Pi/180, lat2*math.Pi/180
	dp, dl := p2-p1, (lon2-lon1)*math.Pi/180
	a := math.Sin(dp/2)*math.Sin(dp/2) + math.Cos(p1)*math.Cos(p2)*math.Sin(dl/2)*math.Sin(dl/2)
	return 2 * EarthRadiusKm * math.Asin(math.Min(1, math.Sqrt(a)))
}

// LonRange is a range of longitudes in degrees, Min <= Max, both within -180..180.
type LonRange struct{ Min, Max float64 }

// Box is the bounding box of a circle: the latitudes MinLat..MaxLat and one or two longitude ranges (two when the circle crosses
// the antimeridian: -180 and 180 are the same meridian). A circle that contains a pole, or is wider than the Earth, has the one
// range -180..180.
type Box struct {
	MinLat, MaxLat float64
	Lon            []LonRange
}

// BoundingBox returns the smallest box that contains every point within radiusKm of (lat, lon). It is a prefilter for a proximity
// query (every point of the circle is in the box, not every point of the box is in the circle): refine with DistanceKm.
func BoundingBox(lat, lon, radiusKm float64) Box {
	d := radiusKm / EarthRadiusKm // angular radius
	b := Box{MinLat: lat - d*180/math.Pi, MaxLat: lat + d*180/math.Pi}
	full := Box{MinLat: math.Max(b.MinLat, -90), MaxLat: math.Min(b.MaxLat, 90), Lon: []LonRange{{-180, 180}}}
	if b.MinLat <= -90 || b.MaxLat >= 90 || d >= math.Pi/2 {
		return full // the circle holds a pole (or half the Earth): every longitude is in it
	}
	// the widest the circle is in longitude: asin(sin(d) / cos(lat)), not simply d / cos(lat)
	dl := math.Asin(math.Sin(d)/math.Cos(lat*math.Pi/180)) * 180 / math.Pi
	if math.IsNaN(dl) || dl >= 180 {
		return full
	}
	lo, hi := lon-dl, lon+dl
	switch {
	case lo < -180:
		b.Lon = []LonRange{{lo + 360, 180}, {-180, hi}}
	case hi > 180:
		b.Lon = []LonRange{{lo, 180}, {-180, hi - 360}}
	default:
		b.Lon = []LonRange{{lo, hi}}
	}
	return b
}
