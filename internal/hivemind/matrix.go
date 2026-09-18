package hivemind

// The cycle matrix: every conscious choice the mind ever made, counted
// as transitions — which drive follows which. Persisted in the soul,
// so character is visible as flow, not just weights. A mind that always
// follows Curiosity with Transcendence is a different animal than one
// that answers everything with Self-Maintenance, even on equal genomes.

import (
	"fmt"
	"sort"
	"strings"
)

// DriveOrder fixes the matrix axes: the four intrinsic drives.
var DriveOrder = []string{GoalCuriosity, GoalSocialization, GoalTranscendence, GoalSelfMaintenance}

// TransitionKey joins winner → winner for the count map.
func TransitionKey(from, to string) string { return from + "→" + to }

// RenderMatrix draws the transition counts as a grid with row totals.
// Unknown drive names (foreign frames, future genes) get their own row/col.
func RenderMatrix(counts map[string]int) string {
	drives := append([]string(nil), DriveOrder...)
	seen := map[string]bool{}
	for _, d := range drives {
		seen[d] = true
	}
	var extra []string
	for k := range counts {
		for _, part := range strings.Split(k, "→") {
			if !seen[part] {
				seen[part] = true
				extra = append(extra, part)
			}
		}
	}
	sort.Strings(extra)
	drives = append(drives, extra...)

	var b strings.Builder
	b.WriteString(fmt.Sprintf("  %-16s", "from ↓ / to →"))
	for _, to := range drives {
		b.WriteString(fmt.Sprintf(" %12.12s", to))
	}
	b.WriteString(fmt.Sprintf(" %8s\n", "total"))
	for _, from := range drives {
		b.WriteString(fmt.Sprintf("  %-16.16s", from))
		row := 0
		for _, to := range drives {
			n := counts[TransitionKey(from, to)]
			row += n
			b.WriteString(fmt.Sprintf(" %12d", n))
		}
		b.WriteString(fmt.Sprintf(" %8d\n", row))
	}
	return b.String()
}
