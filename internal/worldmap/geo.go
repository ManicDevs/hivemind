package worldmap

// Geographic helpers: the equirectangular projection shared by every
// renderer, great-circle interpolation for link arcs, and a real
// day/night terminator computed from the current UTC solar position.

import (
	"math"
	"time"
)

const (
	degRad = math.Pi / 180
	radDeg = 180 / math.Pi
)

// project maps lon/lat to SVG pixels with a small margin so coastlines
// and the graticule never touch the border.
func project(lon, lat, w, h float64) (x, y float64) {
	pad := 0.04
	nx := (lon + 180) / 360
	ny := (90 - lat) / 180
	return w * (pad + nx*(1-2*pad)), h * (pad + ny*(1-2*pad))
}

// greatCirclePoints samples the great-circle path between two lon/lat
// points into n+1 Cartesian-projected vertices — the curve a flight
// path actually follows, not a straight line on a flat map.
func greatCirclePoints(a, b geoLL, n int) [][2]float64 {
	if n < 2 {
		n = 2
	}
	toXYZ := func(ll geoLL) (x, y, z float64) {
		lat := ll.lat * degRad
		lon := ll.lon * degRad
		return math.Cos(lat) * math.Cos(lon), math.Cos(lat) * math.Sin(lon), math.Sin(lat)
	}
	x1, y1, z1 := toXYZ(a)
	x2, y2, z2 := toXYZ(b)
	dot := x1*x2 + y1*y2 + z1*z2
	if dot > 1 {
		dot = 1
	}
	if dot < -1 {
		dot = -1
	}
	omega := math.Acos(dot)
	out := make([][2]float64, 0, n+1)
	if omega < 1e-9 {
		out = append(out, [2]float64{a.lon, a.lat})
		out = append(out, [2]float64{b.lon, b.lat})
		return out
	}
	sinOmega := math.Sin(omega)
	for i := 0; i <= n; i++ {
		f := float64(i) / float64(n)
		s1 := math.Sin((1-f)*omega) / sinOmega
		s2 := math.Sin(f*omega) / sinOmega
		x := s1*x1 + s2*x2
		y := s1*y1 + s2*y2
		z := s1*z1 + s2*z2
		// Convert back to lon/lat then project.
		lat := math.Atan2(z, math.Hypot(x, y)) * radDeg
		lon := math.Atan2(y, x) * radDeg
		// Unwrap: keep lon continuous with the previous point so the path
		// does not jump across the dateline mid-segment.
		out = append(out, [2]float64{lon, lat})
	}
	// Second pass: unwrap longitudes for continuous drawing.
	for i := 1; i < len(out); i++ {
		for out[i][0]-out[i-1][0] > 180 {
			out[i][0] -= 360
		}
		for out[i][0]-out[i-1][0] < -180 {
			out[i][0] += 360
		}
	}
	return out
}

// solarPosition returns the subsolar point longitude and solar
// declination for a given instant — enough for a real day/night line.
func solarPosition(t time.Time) (subsolarLon, declination float64) {
	utc := t.UTC()
	hour := float64(utc.Hour()) + float64(utc.Minute())/60 + float64(utc.Second())/3600
	day := utc.YearDay()
	// Fractional year in radians.
	gamma := 2 * math.Pi / 365 * (float64(day-1) + hour/24)
	// Declination in degrees (±23.44° over the year).
	declination = 0.006918 - 0.399912*math.Cos(gamma) + 0.070257*math.Sin(gamma) -
		0.006758*math.Cos(2*gamma) + 0.000907*math.Sin(2*gamma) -
		0.002697*math.Cos(3*gamma) + 0.00148*math.Sin(3*gamma)
	declination *= radDeg
	// Equation of time (minutes) — small correction to solar noon.
	eqTime := 229.18 * (0.000075 + 0.001868*math.Cos(gamma) - 0.032077*math.Sin(gamma) -
		0.014615*math.Cos(2*gamma) - 0.040849*math.Sin(2*gamma))
	// Subsolar longitude: 180° at solar noon, 15° west per hour.
	subsolarLon = 180 - (hour*15 + eqTime)
	for subsolarLon > 180 {
		subsolarLon -= 360
	}
	for subsolarLon < -180 {
		subsolarLon += 360
	}
	return subsolarLon, declination
}

// terminatorPoints samples the day/night terminator across all longitudes.
// Each returned point is (lon, lat) where the sun sits exactly on the
// horizon for the given solar position.
func terminatorPoints(subsolarLon, declination float64, step float64) []geoLL {
	if step <= 0 {
		step = 4
	}
	d := declination * degRad
	if math.Abs(d) < 1e-6 {
		d = 1e-6 // avoid division by zero at equinox; pole-hugging line.
	}
	var pts []geoLL
	for lon := -180.0; lon <= 180.0; lon += step {
		dlon := (lon - subsolarLon) * degRad
		// tan(φ) = −cos(Δλ) / tan(δ)
		phi := math.Atan2(-math.Cos(dlon), math.Tan(d))
		pts = append(pts, geoLL{lon, phi * radDeg})
	}
	return pts
}
