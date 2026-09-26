package main

import (
	"fmt"
	"net/http"
	"sort"
)

// metrics renders the same live snapshot /state already serves as Prometheus
// text (go 1.17+ text exposition v0.0.4). Every number here is the value
// the mesh actually counted — no separate telemetry pipeline, no drift by
// construction. One honest source: the observer's snapshot.
func (o *observer) metrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	s := o.snapshot("")
	g := s["gaze"].(map[string]interface{})
	// "# TYPE" follows the mind-types guide: counter, then gauge.
	fmt.Fprintln(w, "# TYPE hivemind_frames_total counter")
	if v, ok := g["frames_total"].(int64); ok {
		fmt.Fprintf(w, "hivemind_frames_total %d\n", v)
	}
	fmt.Fprintln(w, "# TYPE hivemind_linked_peers gauge")
	if v, ok := g["linked_peers"].(int); ok {
		fmt.Fprintf(w, "hivemind_linked_peers %d\n", v)
	}
	if byKind, ok := g["by_kind"].(map[string]int64); ok {
		keys := make([]string, 0, len(byKind))
		for k := range byKind {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "hivemind_frames_by_kind{kind=%q} %d\n", k, byKind[k])
		}
	}
	fmt.Fprintln(w, "# TYPE hivemind_hour_rotated gauge")
	if hk, ok := g["hour_key"].(string); ok && hk != "" {
		fmt.Fprintln(w, "hivemind_hour_rotated 1")
	}
	if ro, ok := g["relay_on"].(bool); ok && ro {
		fmt.Fprintln(w, "hivemind_relay_live 1")
	}
}
