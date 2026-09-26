// The markdown reporter: builds REPORT.md and world.svg from the merged
// telemetry, then persists them with os.WriteFile. Native bytes the whole
// way — no subprocess, no shell, no temporary JSON pipeline.
package worldmap

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ReportPaths holds the persisted artifacts of one render.
type ReportPaths struct {
	Dir      string
	Markdown string
	SVG      string
}

// Report is the fully computed render input.
type Report struct {
	States map[string]*GazeState
	Nodes  []NodeView
	Links  map[string]bool
	Now    string
	// Relay carries the broker-mesh telemetry the relay's /healthz reported.
	// The zero value renders no infrastructure layer, so a report written
	// without a relay never invents one.
	Relay RelayHealth
}

// CountByContinent tallies masters/peers and frame totals per continent.
type ContinentRow struct {
	Masters     int
	Peers       int
	FramesTotal int64
}

// ReportWriter renders the aggregate picture to disk.
type ReportWriter struct {
	Dir string
}

// NewReportWriter targets a directory (created on demand).
func NewReportWriter(dir string) *ReportWriter {
	return &ReportWriter{Dir: dir}
}

// WriteStates persists each continent's /state envelope as a raw JSON
// snapshot (world-report/<cont>.json), the exact feed the gaze replay tab
// reads to animate a past run. Stale snapshots from a dead continent are
// removed so the recording never pretends a silent zone was alive.
func (w *ReportWriter) WriteStates(states map[string]*GazeState) error {
	if err := os.MkdirAll(w.Dir, 0o755); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, c := range ContinentCodes {
		st := states[c]
		if st == nil {
			continue
		}
		seen[c] = true
		raw, err := json.Marshal(st)
		if err != nil {
			continue
		}
		path := filepath.Join(w.Dir, c+".json")
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			return err
		}
	}
	for _, c := range ContinentCodes {
		if seen[c] {
			continue
		}
		_ = os.Remove(filepath.Join(w.Dir, c+".json"))
	}
	return nil
}

// perContinent groups a report into its per-continent rows.
func (w *ReportWriter) perContinent(r Report) map[string]ContinentRow {
	rows := map[string]ContinentRow{}
	for _, c := range ContinentCodes {
		rows[c] = ContinentRow{}
		if st, ok := r.States[c]; ok && st != nil {
			rows[c] = ContinentRow{FramesTotal: st.Gaze.FramesTotal}
		}
	}
	for _, n := range r.Nodes {
		row := rows[n.Continent]
		if IsMaster(n.Name) {
			row.Masters++
		} else {
			row.Peers++
		}
		rows[n.Continent] = row
	}
	return rows
}

// Write persists REPORT.md and world.svg into w.Dir and returns their paths.
func (w *ReportWriter) Write(r Report) (ReportPaths, error) {
	if err := os.MkdirAll(w.Dir, 0o755); err != nil {
		return ReportPaths{}, err
	}

	links := make([]string, 0, len(r.Links))
	for l := range r.Links {
		links = append(links, l)
	}
	sort.Strings(links)

	ag := Aggregated(r.States)
	rows := w.perContinent(r)
	var nodesMaster, nodesPeer int
	for _, n := range r.Nodes {
		if IsMaster(n.Name) {
			nodesMaster++
		} else {
			nodesPeer++
		}
	}

	var kinds []string
	for k := range ag.ByKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	var kindsLine []string
	for _, k := range kinds {
		kindsLine = append(kindsLine, fmt.Sprintf("%s=%d", k, ag.ByKind[k]))
	}

	var b strings.Builder
	b.WriteString("# HIVEMIND — World Run Report\n\n")
	b.WriteString("One system. Seven continents. Real minds, real sockets, real signatures.\n\n")
	b.WriteString("## Live world\n\n")
	fmt.Fprintf(&b, "- **Nodes on the mesh**: %d live — %d masters, %d peers (observers join as equal peers)\n", len(r.Nodes), nodesMaster, nodesPeer)
	fmt.Fprintf(&b, "- **Frames witnessed**: %d\n", ag.FramesTotal)
	fmt.Fprintf(&b, "- **Distinct links seen**: %d\n", len(r.Links))
	fmt.Fprintf(&b, "- **Frame kinds**: %s\n", strings.Join(kindsLine, ", "))
	if ag.HourKey != "" {
		fmt.Fprintf(&b, "- **Hour key**: %s\n", ag.HourKey)
	}
	if ag.DayRoot != "" {
		fmt.Fprintf(&b, "- **Day root**: %s\n", ag.DayRoot)
	}
	b.WriteString("\n## Continents\n\n")
	b.WriteString("| Continent | masters | peers | frame total |\n")
	b.WriteString("|---|---|---|---|\n")
	for _, c := range ContinentCodes {
		row := rows[c]
		fmt.Fprintf(&b, "| **%s** | %d | %d | %d |\n",
			strings.ToUpper(c), row.Masters, row.Peers, row.FramesTotal)
	}
	b.WriteString("\n## Map\n\n")
	b.WriteString("![world map](world.svg)\n\n")
	b.WriteString("## How to read the map\n\n")
	b.WriteString("- Continents sit where they live: **eu/na** north-west, **as/oc** east, **sa/af** south, **an** at the bottom.\n")
	b.WriteString("- Larger filled circles are **super controllers (masters)**; smaller ones are **super peers**.\n")
	b.WriteString("- Dashed golden lines are the observer's own serverless links to the swarm — every line is a real authenticated socket.\n")
	b.WriteString("- Gaze sits at the centre as a silent, equal node; it never routes, never owns, never commands alone.\n")

	mdPath := filepath.Join(w.Dir, "REPORT.md")
	svgPath := filepath.Join(w.Dir, "world.svg")

	if err := os.WriteFile(mdPath, []byte(b.String()), 0o644); err != nil {
		return ReportPaths{}, err
	}
	gazeName, now := "", r.Now
	if ag.GazeName != "" {
		gazeName = ag.GazeName
	}
	if now == "" {
		now = ag.Now
	}
	svg := RenderSVGWithInfra(r.Nodes, links, gazeName, now, "", r.Relay)
	if err := os.WriteFile(svgPath, []byte(svg), 0o644); err != nil {
		return ReportPaths{}, err
	}
	return ReportPaths{Dir: w.Dir, Markdown: mdPath, SVG: svgPath}, nil
}
