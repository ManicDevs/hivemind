package worldmap

import (
	"sort"
	"strings"
)

// ContinentCodes is the canonical seven-continent set.
var ContinentCodes = []string{"eu", "na", "as", "sa", "af", "oc", "an"}

// ContinentOf classifies a node name into its continent ("?" when unknown).
func ContinentOf(name string) string {
	low := strings.ToLower(name)
	for _, c := range ContinentCodes {
		if strings.HasPrefix(low, c+"-") || strings.HasPrefix(low, c+"_") {
			return c
		}
	}
	for _, c := range ContinentCodes {
		if strings.Contains(low, "-"+c) {
			return c
		}
	}
	return "?"
}

// IsMaster tells whether a node is a super controller by name.
func IsMaster(name string) bool {
	low := strings.ToLower(name)
	return strings.Contains(low, "master") || strings.Contains(low, "super")
}

// MergeNodes unions every snapshot's node renders, keyed by name, so the
// seven per-continent gazes become one world view.
func MergeNodes(states map[string]*GazeState) []NodeView {
	byName := map[string]NodeView{}
	for _, st := range states {
		if st == nil {
			continue
		}
		for _, n := range st.Nodes {
			if _, ok := byName[n.Name]; !ok {
				byName[n.Name] = n
			}
		}
		for _, nm := range st.Alive {
			if _, ok := byName[nm]; !ok {
				byName[nm] = NodeView{Name: nm, Continent: ContinentOf(nm), Role: "peer", Alive: true}
				if IsMaster(nm) {
					view := byName[nm]
					view.Role = "master"
					byName[nm] = view
				}
			} else {
				view := byName[nm]
				view.Alive = true
				view.Continent = ContinentOf(nm)
				byName[nm] = view
			}
		}
	}
	var out []NodeView
	for _, n := range byName {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// LiveNodes trims a merged view down to nodes actually breathing right
// now, so a report reflects the live topology — not resting souls.
func LiveNodes(all []NodeView) []NodeView {
	var out []NodeView
	for _, n := range all {
		if n.Alive {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
