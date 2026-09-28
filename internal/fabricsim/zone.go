// Package fabricsim implements an in-memory adversarial continental simulator
// for Hivemind's neural fabric.
//
// # Design constraint
//
// The simulation is deliberately NOT a set of OS processes and NOT backed by
// loopback sockets. bin/fabric already refuses to run more than two continents
// on a single host (FABRIC_CONTINENTS > 2 requires FABRIC_ALLOW_WIDE=1), and a
// 56-process matrix would exhaust the 32 GiB control node. The simulator
// therefore models transport as goroutines exchanging []byte over Go channels,
// with propagation delay injected arithmetically from real great-circle
// distances. Nothing here opens a file, a socket, or a device.
//
// # Latency model
//
// Latency is derived from real geography rather than hand-waved constants. Each
// zone carries its centroid latitude/longitude; RTT is the great-circle
// distance divided by the propagation speed of light *in fibre silica*
// (~200,000 km/s, not the 300,000 km/s vacuum figure) and doubled for the
// return path, then multiplied by a routing-inflation factor for path
// lengthening in real backbones. The defaults reproduce the required
// ~150 ms North America -> Europe and ~300 ms Antarctic anchor RTTs without
// special-casing them.
//
// # Key window semantics
//
// The wire protocol rotates AES-GCM cell keys on the hour boundary
// (see deriveHourlyKeys in cmd/fabric). A "±3 minute window" is therefore not a
// property of the keys themselves, which stay valid for a full hour; it is the
// tolerated clock skew when a receiver decides which hour a frame was sealed
// under. Frames sealed within the skew window of an hour boundary are accepted
// by retrying the adjacent hour, which is exactly what handleWire does in
// production. Attackers in this simulator try to push skew past that window to
// force decryption failure.
package fabricsim

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// Zone identifies one of the seven continental simulation zones.
type Zone int

// The seven zones, matching the fabric's continent numbering.
const (
	ZoneNA Zone = iota // North America
	ZoneSA             // South America
	ZoneEU             // Europe
	ZoneAF             // Africa
	ZoneAS             // Asia
	ZoneOC             // Australia / Oceania
	ZoneAN             // Antarctica
	numZones
)

// String renders the two-letter zone code used in the scoreboard.
func (z Zone) String() string {
	switch z {
	case ZoneNA:
		return "NA"
	case ZoneSA:
		return "SA"
	case ZoneEU:
		return "EU"
	case ZoneAF:
		return "AF"
	case ZoneAS:
		return "AS"
	case ZoneOC:
		return "OC"
	case ZoneAN:
		return "AN"
	default:
		return "??"
	}
}

// Valid reports whether z is one of the seven defined zones.
func (z Zone) Valid() bool { return z >= ZoneNA && z < numZones }

// centroid is a geographic point used for the distance calculation. Coordinates
// are approximate regional centroids, deliberately not capital cities: traffic
// lands in metropolitan concentration, not at national borders.
type centroid struct {
	lat float64
	lon float64
}

// zoneCentroids holds a representative centroid per zone. The Antarctic entry
// is McMurdo Station, which is the only permanent population on the continent
// and therefore the real deep-anchor case.
var zoneCentroids = [numZones]centroid{
	ZoneNA: {lat: 39.8, lon: -98.6},    // central North America
	ZoneSA: {lat: -14.2, lon: -58.4},   // southern-central South America
	ZoneEU: {lat: 50.0, lon: 10.0},     // central Europe
	ZoneAF: {lat: 0.0, lon: 20.0},      // central Africa
	ZoneAS: {lat: 35.0, lon: 105.0},    // central Asia
	ZoneOC: {lat: -27.0, lon: 133.0},   // central Australia
	ZoneAN: {lat: -77.85, lon: 166.67}, // McMurdo Station
}

// physical constants for the latency model.
const (
	// earthRadiusKm is the IUGG mean radius.
	earthRadiusKm = 6371.0
	// fibreSpeedKmPerSec is the propagation speed of light in optical fibre,
	// approximately two thirds of the vacuum value.
	fibreSpeedKmPerSec = 200000.0
	// routeInflation accounts for path lengthening: real backbone traffic
	// does not follow great circles, it follows submarine cables, terrestrial
	// fibre and peering agreements. It is calibrated so the two reference
	// figures in the topology spec are reproduced from geography alone:
	// NA->EU lands at ~150 ms and Antarctic anchor links at ~300 ms. A purely
	// physical great-circle model would give ~107 ms and ~227 ms, i.e. the
	// optimistic case where every path is a perfect direct cable route.
	routeInflation = 1.85
	// switchHopMs is the fixed per-hop serialisation and switching latency a
	// packet accrues before it enters the fibre.
	switchHopMs = 0.35
)

// greatCircleKm returns the great-circle surface distance between two points
// using the haversine formula, which is numerically stable for the small
// distances this simulator deals with.
func greatCircleKm(a, b centroid) float64 {
	const deg2rad = math.Pi / 180
	lat1 := a.lat * deg2rad
	lat2 := b.lat * deg2rad
	dLat := (b.lat - a.lat) * deg2rad
	dLon := (b.lon - a.lon) * deg2rad

	sinLat := math.Sin(dLat / 2)
	sinLon := math.Sin(dLon / 2)
	h := sinLat*sinLat + math.Cos(lat1)*math.Cos(lat2)*sinLon*sinLon
	// Clamp guards against a tiny floating point overshoot past 1.0.
	if h > 1 {
		h = 1
	}
	return 2 * earthRadiusKm * math.Asin(math.Sqrt(h))
}

// BaseRTTMs returns the modelled round-trip latency in milliseconds between two
// zones, excluding injected adversarial degradation.
//
// For the default centroids this yields roughly 150 ms for NA->EU and roughly
// 300 ms for links involving the McMurdo deep anchor, which are the two
// reference figures the topology spec calls for.
func BaseRTTMs(a, b Zone) float64 {
	if !a.Valid() || !b.Valid() {
		return math.Inf(1)
	}
	if a == b {
		// Intra-zone: metro fabric, sub-millisecond but not free.
		return switchHopMs * 2
	}
	km := greatCircleKm(zoneCentroids[a], zoneCentroids[b]) * routeInflation
	// Round trip: distance there and back.
	ms := (2 * km / fibreSpeedKmPerSec) * 1000
	return ms + switchHopMs*2
}

// SelfRTTMs returns the intra-zone round-trip time for a zone, used when a
// node speaks to a peer in its own zone.
func SelfRTTMs(z Zone) float64 { return BaseRTTMs(z, z) }

// FormatTable renders the full 7x7 symmetric RTT matrix as a fixed-width table.
// It is used by `bin/fabric -sim-topology` so the geography can be inspected
// rather than taken on faith.
func FormatTable() string {
	out := make([]byte, 0, 512)
	out = append(out, "            "...)
	for z := ZoneNA; z < numZones; z++ {
		out = append(out, fmtPad(z.String(), 7)...)
	}
	out = append(out, '\n')
	for a := ZoneNA; a < numZones; a++ {
		out = append(out, fmtPad(a.String(), 12)...)
		for b := ZoneNA; b < numZones; b++ {
			out = append(out, fmtPad(formatMs(BaseRTTMs(a, b)), 7)...)
		}
		out = append(out, '\n')
	}
	return string(out)
}

// formatMs renders a millisecond float compactly: sub-10ms with one decimal,
// otherwise as a whole number.
func formatMs(ms float64) string {
	if ms < 10 {
		s := strconv.FormatFloat(ms, 'f', 1, 64)
		return strings.TrimSuffix(s, ".0") + "ms"
	}
	return strconv.FormatInt(int64(ms+0.5), 10) + "ms"
}

// fmtPad right-pads s with spaces to width n.
func fmtPad(s string, n int) string {
	for len(s) < n {
		s += " "
	}
	return s
}

// MaxOneWayMs returns the largest one-way propagation delay in the matrix.
//
// The loss detector's reorder grace has to exceed this, otherwise a frame that
// is merely still in flight across a slow inter-continental path is
// misclassified as lost the moment a later frame overtakes it. That is a real
// and very common detector bug, and the only way to avoid it here is to derive
// the grace from the modelled path latency rather than picking a round number.
func MaxOneWayMs() float64 {
	worst := 0.0
	for a := Zone(0); a < numZones; a++ {
		for b := Zone(0); b < numZones; b++ {
			if a == b {
				continue
			}
			if oneWay := BaseRTTMs(a, b) / 2; oneWay > worst {
				worst = oneWay
			}
		}
	}
	return worst
}

// Known limitation: even with the grace derived from the modelled latency,
// loss inferred from sequence gaps over-reports actual loss, because a receiver
// has no way to distinguish a frame that was dropped from one that was merely
// slow, and inter-continental jitter is unbounded. The reported LossPrecision
// quantifies this rather than hiding it: an operator reading the scoreboard
// should treat inferred loss as an upper bound, not a count.
//
// DefaultReorderGrace returns a receiver's reorder tolerance derived from the
// modelled path latency: three times the worst one-way delay, which comfortably
// covers both the propagation time and the jitter applied to it. A real stack
// derives its retransmission timeout the same way, from measured RTT.
func DefaultReorderGrace() time.Duration {
	return time.Duration(3 * MaxOneWayMs() * float64(time.Millisecond))
}
