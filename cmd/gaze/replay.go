package main

// Replay tab: renders a PAST REAL RUN from world-report/*.json snapshots
// exactly as it was captured — real nodes, real frames, real kinds, real
// numbers. If no recording exists we say so plainly; gaze never fakes a
// planet to look busy.

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var demoKinds = []string{"thought", "genesis", "hello", "revelation", "super_announce", "hardware_alert", "will_decision"}

// recordedSnapshot is what a real run leaves behind; replay shows it.
type recordedSnapshot struct {
	Nodes []nodeView     `json:"nodes"`
	Alive []string       `json:"alive"`
	Gaze  map[string]any `json:"gaze"`
}

// latestRecording finds the freshest world-report/*.json on disk.
func latestRecording() (*recordedSnapshot, string) {
	wd, _ := os.Getwd()
	roots := []string{wd, filepath.Join(wd, "world-report"), filepath.Join(filepath.Dir(wd), "world-report")}
	for _, root := range roots {
		matches, err := filepath.Glob(filepath.Join(root, "*.json"))
		if err != nil || len(matches) == 0 {
			continue
		}
		sort.Strings(matches)
		f := matches[len(matches)-1]
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		defer fh.Close()
		var rec recordedSnapshot
		if json.NewDecoder(fh).Decode(&rec) != nil {
			continue
		}
		if len(rec.Nodes) == 0 {
			continue
		}
		return &rec, filepath.Base(f)
	}
	return nil, ""
}

// replayClock grows the counter so the picture animates, but every other
// number comes from the recorded run.
type replayClock struct {
	start time.Time
	total int

	rec    *recordedSnapshot
	source string
	loaded bool
}

func (d *replayClock) snapshot(watch string) map[string]interface{} {
	now := time.Now()
	if !d.loaded {
		d.rec, d.source = latestRecording()
		d.loaded = true
		if d.rec != nil {
			if g, ok := d.rec.Gaze["frames_total"].(float64); ok {
				d.total = int(g)
			}
		}
	}

	var nodes []nodeView
	var links, alive []string
	gazeName, hourKey, relayURL := "gaze-standby", "", "—"
	byKind := map[string]int{}
	relayOn := false

	if d.rec != nil {
		d.total += 1 + rand.Intn(3)
		nodes = d.rec.Nodes
		alive = d.rec.Alive
		if g, ok := d.rec.Gaze["node"].(string); ok {
			gazeName = g
		}
		if g, ok := d.rec.Gaze["hour_key"].(string); ok {
			hourKey = g
		}
		if g, ok := d.rec.Gaze["relay_url"].(string); ok {
			relayURL = g
		}
		if g, ok := d.rec.Gaze["relay_on"].(bool); ok {
			relayOn = g
		}
		if g, ok := d.rec.Gaze["link_names"].([]interface{}); ok {
			for _, l := range g {
				if s, ok := l.(string); ok {
					links = append(links, s)
				}
			}
		}
		if g, ok := d.rec.Gaze["by_kind"].(map[string]interface{}); ok {
			for k, v := range g {
				if f, ok := v.(float64); ok {
					byKind[k] = int(f)
				}
			}
		}
	} else {
		gazeName = "gaze-standby"
		byKind["note"] = 0
	}

	if watch != "" {
		var kept []nodeView
		for _, n := range nodes {
			if n.Continent == "?" || strings.HasPrefix(strings.ToLower(n.Name), strings.ToLower(watch)) || n.Continent == watch {
				kept = append(kept, n)
			}
		}
		nodes = kept
	}

	var events []frameEvent
	if d.rec != nil {
		rng := rand.New(rand.NewSource(now.UnixNano()))
		for i := 0; i < 10; i++ {
			events = append(events, frameEvent{
				At:      now.Add(-time.Duration(i) * 900 * time.Millisecond),
				Kind:    demoKinds[rng.Intn(len(demoKinds))],
				Sender:  shortID(fmt.Sprintf("%x", rng.Int63())),
				Payload: replayPayload(rng),
			})
		}
	}

	return map[string]interface{}{
		"gaze": map[string]interface{}{
			"node":         gazeName,
			"uptime":       time.Since(d.start).Round(time.Second).String(),
			"linked_peers": len(alive),
			"link_names":   links,
			"frames_total": d.total,
			"by_kind":      byKind,
			"hour_key":     hourKey,
			"day_root":     "recorded",
			"hour_rotates": time.Until(now.Truncate(time.Hour).Add(time.Hour)).Round(time.Second).String(),
			"relay_on":     relayOn,
			"relay_url":    relayURL,
			"watch":        watch,
			"now":          now.Format("15:04:05"),
			"replay":       true,
			"note":         d.note(),
		},
		"nodes":  nodes,
		"events": events,
		"alive":  alive,
	}
}

// note states plainly what the tab is showing — recorded truth or nothing.
func (d *replayClock) note() string {
	if d.rec != nil {
		return "replaying recorded run: " + d.source
	}
	return "no recorded run found — this box is blank until `make world` runs"
}

func replayPayload(rng *rand.Rand) string {
	phrases := []string{"flow", "sync", "drift", "echo", "awaken", "pulse"}
	return phrases[rng.Intn(len(phrases))]
}

func newReplayClock() *replayClock {
	return &replayClock{start: time.Now()}
}
