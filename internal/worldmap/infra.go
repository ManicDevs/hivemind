package worldmap

import (
	"fmt"
	"math"
	"strings"
)

// The infrastructure layer draws the relay's broker mesh as first-class
// actors on the world map, driven by the relay's own /healthz telemetry.
//
// The rule this layer exists to enforce: nothing is drawn that is not
// carrying traffic. A broker the relay cannot reach is drawn grey and
// unconnected; a broker with no bytes in either direction is drawn dim. The
// map never implies a wire that the relay did not report.

const (
	brokerLiveColor = "#2dd4bf" // teal — carrying traffic
	brokerIdleColor = "#64748b" // slate — connected, silent
	brokerDeadColor = "#7f1d1d" // dark red — unreachable
	brokerHalo      = "#2dd4bf"
)

// infraLayer renders every broker the relay reported, plus the arcs from
// the mesh hub. Arcs are drawn per direction so a one-way link is visibly
// one-way: outbound flows hub→broker, inbound broker→hub.
func infraLayer(W, H, gx, gy float64, h RelayHealth) string {
	if len(h.Brokers) == 0 {
		return ""
	}
	var b strings.Builder

	// Arcs first, so markers sit on top of their own wires. The mesh hub is
	// anchored geosynchronously — the same point the relay node already
	// uses — so hub arcs and the legacy fan agree on where "the relay" is.
	hub := geoLL{lon: 33.75, lat: 0}
	n := len(h.Brokers)
	for i, br := range h.Brokers {
		lon, lat := brokerAnchorLL(br.Host, i, n)

		color := brokerDeadColor
		if br.Connected {
			color = brokerIdleColor
			if br.Outbound+br.Inbound > 0 {
				color = brokerLiveColor
			}
		}

		// Outbound: hub → broker. Drawn only where bytes went out.
		if br.Outbound > 0 {
			b.WriteString(flowArc(hub.lon, hub.lat, lon, lat, color, 1.0, "out"))
		}
		// Inbound: broker → hub. Drawn only if bytes actually came back,
		// which is the whole point of making the bridge subscribe.
		if br.Inbound > 0 {
			b.WriteString(flowArc(lon, lat, hub.lon, hub.lat, color, 1.0, "in"))
		}
	}

	// Markers and labels on top of the arcs.
	for i, br := range h.Brokers {
		lon, lat := brokerAnchorLL(br.Host, i, n)
		x, y := project(lon, lat, W, H)

		color := brokerDeadColor
		if br.Connected {
			color = brokerIdleColor
			if br.Outbound+br.Inbound > 0 {
				color = brokerLiveColor
			}
		}

		total := br.Outbound + br.Inbound
		if br.Connected && total > 0 {
			// Halo grows with traffic, capped so one busy broker cannot
			// swamp the map.
			r := 4.0 + math.Min(9.0, float64(total)/6.0)
			fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="%.1f" fill="%s" opacity="0.18"/>`, x, y, r*2, brokerHalo)
		}
		fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="7" height="7" rx="1.5" fill="%s" stroke="#0a0e1a" stroke-width="0.8"/>`, x-3.5, y-3.5, color)
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" fill="%s" font-size="8" text-anchor="middle" opacity="0.92">%s</text>`,
			x, y+16, color, escapeXML(BrokerLabel(br.Host)))
		// Counters, so "connected" is visibly different from "working".
		if total > 0 {
			fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" fill="%s" font-size="7" text-anchor="middle" opacity="0.6">↑%d ↓%d</text>`,
				x, y+25, color, br.Outbound, br.Inbound)
		} else if !br.Connected {
			fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" fill="%s" font-size="7" text-anchor="middle" opacity="0.5">unreachable</text>`,
				x, y+25, color)
		}
	}

	// Legend: state what the layer means, in words, on the map itself.
	fmt.Fprintf(&b, `<g opacity="0.92"><rect x="%.1f" y="%.1f" width="212" height="%d" rx="4" fill="#0a0e1a" stroke="#1e293b" stroke-width="1"/>`,
		W-224, 10.0, 20+14*len(h.Brokers)+26)
	fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" fill="#94a3b8" font-size="9" font-weight="bold" letter-spacing="1">BROKER MESH</text>`, W-214, 24.0)
	row := 40.0
	if h.Reachable {
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" fill="%s" font-size="8">%d/%d live · %s%s</text>`,
			W-214, row, brokerLiveColor, liveCount(h), h.Stats.Brokers, arrow(h.Stats.Outbound, h.Stats.Inbound), bidiWord(h))
	} else {
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" fill="%s" font-size="8">relay unreachable — last known</text>`,
			W-214, row, brokerIdleColor)
	}
	row += 15
	for _, br := range h.Brokers {
		color := brokerDeadColor
		state := "down"
		if br.Connected {
			color = brokerIdleColor
			state = "idle"
			if br.Outbound+br.Inbound > 0 {
				color = brokerLiveColor
				state = "live"
			}
		}
		fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="6" height="6" rx="1" fill="%s"/>`, W-214, row-5, color)
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" fill="#cbd5e1" font-size="8">%s %s</text>`,
			W-204, row, escapeXML(BrokerLabel(br.Host)), state)
		row += 14
	}

	return b.String()
}

func liveCount(h RelayHealth) int {
	n := 0
	for _, b := range h.Brokers {
		if b.Connected {
			n++
		}
	}
	return n
}

func arrow(out, in uint64) string {
	s := fmt.Sprintf("%d out", out)
	if in > 0 {
		s += fmt.Sprintf(" / %d in", in)
	}
	return s
}

func bidiWord(h RelayHealth) string {
	if h.Stats.Bidirectional {
		return " · bidirectional"
	}
	return " · publish-only"
}

// flowArc draws a great-circle arc between two geo points. The direction
// tag only affects the dash phase, so the eye can tell an outbound wire
// from an inbound one at a glance.
func flowArc(fromLon, fromLat, toLon, toLat float64, color string, width float64, dir string) string {
	pts := greatCirclePoints(geoLL{lon: fromLon, lat: fromLat}, geoLL{lon: toLon, lat: toLat}, 28)
	var p strings.Builder
	p.WriteString(`<path d="`)
	for i, pt := range pts {
		x, y := project(pt[0], pt[1], 1200, 700)
		if i == 0 {
			fmt.Fprintf(&p, "M%.1f,%.1f", x, y)
		} else {
			fmt.Fprintf(&p, "L%.1f,%.1f", x, y)
		}
	}
	dash := "3 3"
	if dir == "in" {
		dash = "6 4"
	}
	fmt.Fprintf(&p, `" fill="none" stroke="%s" stroke-width="%.1f" stroke-opacity="0.5" stroke-dasharray="%s"/>`,
		color, width, dash)
	return p.String()
}

func escapeXML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}
