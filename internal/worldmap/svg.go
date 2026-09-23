// The all-native SVG engine: a real geographic world map with coastline
// outlines, great-circle link arcs, a live day/night terminator computed
// from solar position, node tooltips, and pulse animations. Pure
// string-builder construction — no external process, no JS runtime.
package worldmap

import (
	"fmt"
	"math"
	"strings"
	"time"
)

type geoLL struct{ lon, lat float64 }

// CityLL maps every city in the topology to its real-world lat/lon.
var CityLL = map[string]geoLL{
	// EU
	"rotterdam": {4.48, 51.92},
	"london":    {-0.13, 51.51},
	"frankfurt": {8.68, 50.11},
	"paris":     {2.35, 48.86},
	"amsterdam": {4.90, 52.37},
	"berlin":    {13.40, 52.52},
	// NA
	"new-york":      {-74.01, 40.71},
	"chicago":       {-87.63, 41.88},
	"san-francisco": {-122.42, 37.77},
	"toronto":       {-79.38, 43.65},
	"dallas":        {-96.80, 32.78},
	"seattle":       {-122.33, 47.61},
	// AS
	"tokyo":     {139.69, 35.68},
	"singapore": {103.82, 1.35},
	"seoul":     {126.98, 37.57},
	"mumbai":    {72.88, 19.08},
	"dubai":     {55.27, 25.20},
	"jakarta":   {106.85, -6.21},
	// SA
	"sao-paulo":    {-46.63, -23.55},
	"buenos-aires": {-58.38, -34.60},
	"lima":         {-77.04, -12.05},
	"bogota":       {-74.07, 4.71},
	// AF
	"johannesburg": {28.05, -26.20},
	"lagos":        {3.38, 6.52},
	"cairo":        {31.24, 30.04},
	"nairobi":      {36.82, -1.29},
	// OC
	"sydney":    {151.21, -33.87},
	"auckland":  {174.76, -36.85},
	"melbourne": {144.96, -37.81},
	"brisbane":  {153.03, -27.47},
	// AN
	"mcmurdo":        {166.67, -77.85},
	"amundsen-scott": {0.0, -90.0},
}

// contFallback is the representative anchor used when a node's city
// cannot be resolved (gazes, unknown peers, orbital strays).
var contFallback = map[string]geoLL{
	"eu": {10, 50}, "na": {-95, 40}, "as": {100, 35},
	"sa": {-60, -15}, "af": {20, 5}, "oc": {145, -30}, "an": {0, -80},
}

var contColors = []string{
	"#4fc3f7", "#ffb74d", "#81c784", "#ba68c8", "#ef5350", "#ffd54f", "#4db6ac",
}

var continentOrder = []string{"eu", "as", "af", "na", "sa", "oc", "an"}

func contIndex(c string) int {
	for i, code := range continentOrder {
		if code == c {
			return i
		}
	}
	return 0
}

// cityOf extracts the trailing city slug from a node name like
// "eu-master-rotterdam" → "rotterdam". Gazes and unknowns return "".
func cityOf(name string) string {
	parts := strings.Split(name, "-")
	if len(parts) < 3 {
		return ""
	}
	return strings.Join(parts[2:], "-")
}

// nodeLL resolves a node's real coordinates: its city when known, else
// the continent anchor, else the Greenwich null island.
func nodeLL(name, cont string) geoLL {
	if city := cityOf(name); city != "" {
		if ll, ok := CityLL[city]; ok {
			return ll
		}
	}
	if ll, ok := contFallback[cont]; ok {
		return ll
	}
	return geoLL{0, 0}
}

func shortLabel(s string) string {
	if len(s) > 14 {
		return s[:14] + "…"
	}
	return s
}

// graticuleSVG draws the lat/lon grid every 30° with degree labels on
// the edges — the map reads as a charted planet, not a diagram.
func graticuleSVG(w, h float64) string {
	var b strings.Builder
	// Latitude lines every 30°.
	for lat := -60.0; lat <= 60.0; lat += 30 {
		_, y := project(0, lat, w, h)
		fmt.Fprintf(&b, `<line x1="0" y1="%.1f" x2="%.1f" y2="%.1f" stroke="#131c30" stroke-width="0.8"/>`, y, w, y)
		label := "0°"
		if lat > 0 {
			label = fmt.Sprintf("%.0f°N", lat)
		} else if lat < 0 {
			label = fmt.Sprintf("%.0f°S", -lat)
		}
		fmt.Fprintf(&b, `<text x="4" y="%.1f" fill="#2a3a5c" font-size="9">%s</text>`, y-3, label)
	}
	// Equator emphasised.
	_, ey := project(0, 0, w, h)
	fmt.Fprintf(&b, `<line x1="0" y1="%.1f" x2="%.1f" y2="%.1f" stroke="#1e2d4a" stroke-width="1.2"/>`, ey, w, ey)
	// Longitude lines every 30°.
	for lon := -150.0; lon <= 150.0; lon += 30 {
		x, _ := project(lon, 0, w, h)
		fmt.Fprintf(&b, `<line x1="%.1f" y1="0" x2="%.1f" y2="%.1f" stroke="#131c30" stroke-width="0.8"/>`, x, x, h)
		label := fmt.Sprintf("%.0f°", lon)
		if lon > 0 {
			label = fmt.Sprintf("%.0f°E", lon)
		} else if lon < 0 {
			label = fmt.Sprintf("%.0f°W", -lon)
		}
		fmt.Fprintf(&b, `<text x="%.1f" y="%d" fill="#2a3a5c" font-size="9" text-anchor="middle">%s</text>`, x, int(h)-6, label)
	}
	// Prime meridian emphasised.
	pmx, _ := project(0, 0, w, h)
	fmt.Fprintf(&b, `<line x1="%.1f" y1="0" x2="%.1f" y2="%.1f" stroke="#1e2d4a" stroke-width="1.2"/>`, pmx, pmx, h)
	return b.String()
}

// coastSVG fills every land ring with a dark landmass colour and strokes
// a brighter coastline — recognisable continents on a dark ocean.
func coastSVG(w, h float64) string {
	var b strings.Builder
	for _, r := range landRings() {
		if len(r) < 3 {
			continue
		}
		b.WriteString(`<path d="`)
		for i, p := range r {
			x, y := project(p.lon, p.lat, w, h)
			if i == 0 {
				fmt.Fprintf(&b, "M%.1f,%.1f", x, y)
			} else {
				fmt.Fprintf(&b, "L%.1f,%.1f", x, y)
			}
		}
		b.WriteString(`Z" fill="#111a2e" stroke="#243b5e" stroke-width="1" stroke-linejoin="round"/>`)
	}
	return b.String()
}

// terminatorSVG draws the live day/night boundary computed from the
// current UTC solar position, plus a subtle night-side shading.
func terminatorSVG(w, h float64, now time.Time) string {
	slon, decl := solarPosition(now)
	pts := terminatorPoints(slon, decl, 3)
	if len(pts) < 2 {
		return ""
	}
	var b strings.Builder

	// Night polygon: terminator path closed along the pole on the
	// anti-subsolar side.
	antiLon := slon + 180
	if antiLon > 180 {
		antiLon -= 360
	}
	antiLat := -decl
	closeNorth := false
	// Find terminator latitude at the anti-subsolar meridian.
	dlon := (antiLon - slon) * degRad
	d := decl * degRad
	if math.Abs(d) < 1e-6 {
		d = 1e-6
	}
	termAtAnti := math.Atan2(-math.Cos(dlon), math.Tan(d)) * radDeg
	if antiLat > termAtAnti {
		closeNorth = true
	}
	b.WriteString(`<path d="`)
	for i, p := range pts {
		x, y := project(p.lon, p.lat, w, h)
		if i == 0 {
			fmt.Fprintf(&b, "M%.1f,%.1f", x, y)
		} else {
			fmt.Fprintf(&b, "L%.1f,%.1f", x, y)
		}
	}
	// Close along map edge.
	edgeLat := -90.0
	if closeNorth {
		edgeLat = 90.0
	}
	_, ey := project(180, edgeLat, w, h)
	_, ey2 := project(-180, edgeLat, w, h)
	fmt.Fprintf(&b, "L%.1f,%.1f L%.1f,%.1f Z", w, ey, 0.0, ey2)
	b.WriteString(`" fill="#040710" fill-opacity="0.45"/>`)

	// Terminator glow line.
	b.WriteString(`<path d="`)
	for i, p := range pts {
		x, y := project(p.lon, p.lat, w, h)
		if i == 0 {
			fmt.Fprintf(&b, "M%.1f,%.1f", x, y)
		} else {
			fmt.Fprintf(&b, "L%.1f,%.1f", x, y)
		}
	}
	b.WriteString(`" fill="none" stroke="#ffd479" stroke-width="1.4" stroke-opacity="0.7" stroke-dasharray="6 4"/>`)

	// Subsolar marker.
	sx, sy := project(slon, decl, w, h)
	fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="5" fill="#ffd479" fill-opacity="0.9"/>`, sx, sy)
	fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="9" fill="none" stroke="#ffd479" stroke-width="1" stroke-opacity="0.5"/>`, sx, sy)
	return b.String()
}

// linkArcsSVG draws every gaze↔node link as a great-circle arc with an
// animated dash flow — traffic visibly streaming across the planet.
func linkArcsSVG(w, h float64, gx, gy float64, linked map[string]bool, posOf map[string]nodePos) string {
	var b strings.Builder
	for name, p := range posOf {
		if !linked[name] {
			continue
		}
		pts := greatCirclePoints(geoLL{lon: p.lon, lat: p.lat}, geoLL{lon: 0, lat: 0}, 24)
		// Build path, handling dateline unwrap already done in geo.go.
		b.WriteString(`<path class="linkflow" d="`)
		for i, pt := range pts {
			x, y := project(pt[0], pt[1], w, h)
			if i == 0 {
				fmt.Fprintf(&b, "M%.1f,%.1f", x, y)
			} else {
				fmt.Fprintf(&b, "L%.1f,%.1f", x, y)
			}
		}
		// Finish at the gaze hub.
		fmt.Fprintf(&b, "L%.1f,%.1f", gx, gy)
		b.WriteString(`" fill="none" stroke="#3a6aaf" stroke-width="1.3" stroke-opacity="0.75" stroke-dasharray="5 5"/>`)
	}
	return b.String()
}

// nodePos is the internal projection record for one rendered node.
type nodePos struct {
	lon, lat float64
	x, y     float64
	cont     string
	role     string
	alive    bool
}

// styleSVG emits the CSS animations embedded in the SVG itself —
// live-node pulse, link dash flow, tooltip styling.
func styleSVG() string {
	return `<style>
@keyframes hmpulse{0%,100%{r:var(--r);opacity:1}50%{r:calc(var(--r) * 1.6);opacity:0.55}}
.hmlive{animation:hmpulse 2.4s ease-in-out infinite}
@keyframes hmflow{to{stroke-dashoffset:-40}}
.linkflow{animation:hmflow 1.4s linear infinite}
text{user-select:none}
.hmnode{cursor:default}
.hmnode:hover circle{filter:brightness(1.6)}
.hmnode:hover text{fill:#fff}
</style>`
}

// tooltip builds a native SVG <title> so browsers show a hover box
// with the node's full identity and status.
func tooltip(name, role, cont string, alive bool, lives int, fitness float64) string {
	status := "alive"
	if !alive {
		status = "resting"
	}
	return fmt.Sprintf("%s · %s · %s · %s · lives=%d · fitness=%.2f",
		name, role, strings.ToUpper(cont), status, lives, fitness)
}

// RenderSVG paints one complete world map image: coastlines, graticule,
// day/night terminator, great-circle link arcs, nodes at real city
// coordinates with tooltips, continent labels, legend, and clock.
func RenderSVG(nodes []NodeView, links []string, gazeName, now string) string {
	const W, H = 1200.0, 700.0
	t0 := time.Now()
	if now != "" {
		if parsed, err := time.Parse("15:04:05", now); err == nil {
			t0 = time.Date(t0.Year(), t0.Month(), t0.Day(),
				parsed.Hour(), parsed.Minute(), parsed.Second(), 0, time.UTC)
		}
	}

	byCont := map[string][]NodeView{}
	for _, n := range nodes {
		byCont[n.Continent] = append(byCont[n.Continent], n)
	}

	// Project every node to its real coordinates.
	posOf := map[string]nodePos{}
	for cont, members := range byCont {
		for _, n := range members {
			ll := nodeLL(n.Name, cont)
			x, y := project(ll.lon, ll.lat, W, H)
			role := "peer"
			if IsMaster(n.Name) {
				role = "master"
			}
			if strings.HasPrefix(strings.ToLower(n.Name), "gaze") {
				role = "gaze"
			}
			posOf[n.Name] = nodePos{
				lon: ll.lon, lat: ll.lat, x: x, y: y,
				cont: cont, role: role, alive: n.Alive,
			}
		}
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="%.0f" height="%.0f" viewBox="0 0 %.0f %.0f" font-family="ui-monospace,Menlo,Consolas,monospace">`, W, H, W, H))
	b.WriteString(styleSVG())
	// Ocean.
	b.WriteString(`<rect x="0" y="0" width="100%" height="100%" fill="#0a0e1a"/>`)
	b.WriteString(graticuleSVG(W, H))
	b.WriteString(coastSVG(W, H))
	b.WriteString(terminatorSVG(W, H, t0))

	// Continent labels with node counts.
	contLabels := map[string]geoLL{
		"eu": {15, 55}, "na": {-100, 55}, "as": {105, 50},
		"sa": {-60, -30}, "af": {20, 15}, "oc": {140, -25}, "an": {0, -75},
	}
	for _, cont := range continentOrder {
		members := byCont[cont]
		if len(members) == 0 {
			continue
		}
		ll, ok := contLabels[cont]
		if !ok {
			continue
		}
		cx, cy := project(ll.lon, ll.lat, W, H)
		col := contColors[contIndex(cont)%len(contColors)]
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" text-anchor="middle" fill="%s" font-size="14" font-weight="bold" letter-spacing="2" opacity="0.9">%s</text>`, cx, cy-18, col, strings.ToUpper(cont))
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" text-anchor="middle" fill="%s" font-size="9" opacity="0.6">%d nodes</text>`, cx, cy-6, col, len(members))
	}

	// Great-circle links from gaze hub to every linked node.
	gx, gy := W/2, H/2
	linked := map[string]bool{}
	for _, l := range links {
		linked[l] = true
	}
	b.WriteString(linkArcsSVG(W, H, gx, gy, linked, posOf))

	// Nodes with tooltips and pulse animation on live ones.
	byName := map[string]NodeView{}
	for _, n := range nodes {
		byName[n.Name] = n
	}
	// Deterministic draw order: dead first, then peers, masters last.
	order := make([]string, 0, len(posOf))
	for name := range posOf {
		order = append(order, name)
	}
	// Simple stable sort by role priority then name.
	roleRank := func(r string) int {
		switch r {
		case "gaze":
			return 0
		case "peer":
			return 1
		case "master":
			return 2
		}
		return 3
	}
	for i := 0; i < len(order); i++ {
		for j := i + 1; j < len(order); j++ {
			pi, pj := posOf[order[i]], posOf[order[j]]
			ri, rj := roleRank(pi.role), roleRank(pj.role)
			if !pi.alive {
				ri = -1
			}
			if !pj.alive {
				rj = -1
			}
			if rj < ri || (rj == ri && order[j] < order[i]) {
				order[i], order[j] = order[j], order[i]
			}
		}
	}
	for _, name := range order {
		p := posOf[name]
		r := 7.0
		if p.role == "master" {
			r = 12
		} else if p.role == "gaze" {
			r = 8
		}
		col := contColors[contIndex(p.cont)%len(contColors)]
		nodeFill := col
		strokeCol := "#ffffff"
		if !p.alive {
			// Resting nodes: dim but still clearly on the map — never
			// ocean-dark, or whole continents "disappear".
			nodeFill = "#3d5a80"
			strokeCol = "#8eb1d9"
		}
		nv := byName[name]
		liveClass := ""
		if p.alive {
			liveClass = ` class="hmlive" style="--r:` + fmt.Sprintf("%.0f", r) + `"`
		}
		fmt.Fprintf(&b, `<g class="hmnode"><title>%s</title>`, tooltip(name, p.role, p.cont, p.alive, nv.Lives, nv.Fitness))
		fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="%.1f" fill="%s" stroke="%s" stroke-width="1.5"%s/>`, p.x, p.y, r, nodeFill, strokeCol, liveClass)
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" text-anchor="middle" fill="#cfe3ff" font-size="9">%s</text>`, p.x, p.y+r+11, shortLabel(name))
		b.WriteString(`</g>`)
	}

	// Gaze hub at map centre.
	fill := "none"
	stroke := "#ffd479"
	fmt.Fprintf(&b, `<g class="hmnode"><title>gaze · observer · equal peer</title><circle cx="%.1f" cy="%.1f" r="10" fill="%s" stroke="%s" stroke-width="2"/><text x="%.1f" y="%.1f" text-anchor="middle" fill="%s" font-size="10">gaze</text></g>`, gx, gy, fill, stroke, gx, gy+24, stroke)

	// Legend, bottom-left.
	lx, ly := 16.0, H-56
	b.WriteString(`<g font-size="10">`)
	fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="6" fill="#81c784" stroke="#fff" stroke-width="1.2"/><text x="%.1f" y="%.1f" fill="#cfe3ff">master</text>`, lx+6, ly, lx+16, ly+3)
	fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="4" fill="#4fc3f7" stroke="#fff" stroke-width="1"/><text x="%.1f" y="%.1f" fill="#cfe3ff">peer</text>`, lx+6, ly+16, lx+16, ly+19)
	fmt.Fprintf(&b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="#ffd479" stroke-width="1.4" stroke-dasharray="6 4"/><text x="%.1f" y="%.1f" fill="#cfe3ff">day/night line</text>`, lx+1, ly+32, lx+13, ly+32, lx+16, ly+35)
	fmt.Fprintf(&b, `<path d="M%.1f,%.1f L%.1f,%.1f" stroke="#3a6aaf" stroke-width="1.3" stroke-dasharray="5 5" fill="none"/><text x="%.1f" y="%.1f" fill="#cfe3ff">great-circle link</text>`, lx+1, ly+48, lx+13, ly+48, lx+16, ly+51)
	b.WriteString(`</g>`)

	// Clock + observer, bottom-right.
	if now != "" {
		fmt.Fprintf(&b, `<text x="%d" y="%d" fill="#5f7aa8" font-size="10" text-anchor="end">%s · %s · UTC solar line live</text>`, int(W)-12, int(H)-12, now, gazeName)
	}
	b.WriteString(`</svg>`)
	return b.String()
}
