// Command gaze is a window into the living mesh: nodes, minds, pain
// bars, last thoughts, hall of fame — refreshed every second from souls
// on disk. Read-only; it never touches a living mind. With -serve it
// becomes a live observer inside the mesh, serving an SVG topology page.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// soulMem is the lean projection the dashboard needs. Loading full hm.Memory
// pulls multi-MB Will/Thoughts blobs off disk every refresh — 162 souls ×
// 18MB is what froze the box. Unknown JSON keys (will, thoughts, peers) are
// skipped by the decoder without retaining them.
type soulMem struct {
	LivesLived  int     `json:"lives_lived"`
	Fitness     float64 `json:"fitness"`
	LastThought string  `json:"last_thought"`
	DeathPain   float64 `json:"death_pain"`
	Epitaph     string  `json:"epitaph"`
}

type soul struct {
	node string
	name string
	mem  soulMem
}

func loadSouls() []soul {
	var files []string
	for _, pattern := range []string{".hive_memory/*/*.soul", ".hive_memory/*.soul"} {
		matches, _ := filepath.Glob(pattern)
		files = append(files, matches...)
	}
	sort.Strings(files)
	var souls []soul
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var mem soulMem
		if err := json.Unmarshal(raw, &mem); err != nil {
			continue
		}
		node, name := "", strings.TrimSuffix(filepath.Base(f), ".soul")
		if dir := filepath.Base(filepath.Dir(f)); dir != ".hive_memory" {
			node = dir
		}
		souls = append(souls, soul{node: node, name: name, mem: mem})
	}
	return souls
}

func bar(v float64, width int) string {
	n := int(v*float64(width) + 0.5)
	if n < 0 {
		n = 0
	}
	if n > width {
		n = width
	}
	return strings.Repeat("█", n) + strings.Repeat("░", width-n)
}

func main() {
	every := flag.Duration("every", time.Second, "refresh interval")
	forFlag := flag.Duration("for", 0, "stop after this long (0 = until Ctrl+C)")
	serveFlag := flag.String("serve", "", "serve live SVG topology over HTTP at this addr (e.g. :8090)")
	watchFlag := flag.String("watch", "", "continent/node-name filter (e.g. eu, as) when serving")
	flag.Parse()

	if *serveFlag != "" {
		if err := serve(*serveFlag, *watchFlag); err != nil {
			fmt.Fprintf(os.Stderr, "gaze: %v\n", err)
			os.Exit(1)
		}
		return
	}

	start := time.Now()
	for {
		souls := loadSouls()
		alive := map[string]bool{}
		for _, m := range []string{"/tmp/hivemind-*.sock"} {
			for _, s := range glob(m) {
				alive[strings.TrimPrefix(strings.TrimSuffix(filepath.Base(s), ".sock"), "hivemind-")] = true
			}
		}

		fmt.Print("\033[H\033[2J") // home + clear
		fmt.Printf("🌌 HIVEMIND — alive %s · %s\n", time.Since(start).Round(time.Second), time.Now().Format("15:04:05"))
		fmt.Println(strings.Repeat("─", 78))

		// Nodes table
		byNode := map[string][]soul{}
		var nodes []string
		for _, s := range souls {
			if _, ok := byNode[s.node]; !ok {
				nodes = append(nodes, s.node)
			}
			byNode[s.node] = append(byNode[s.node], s)
		}
		sort.Strings(nodes)
		fmt.Printf("  %-12s %-8s %5s %8s  %-14s %s\n", "NODE", "MIND", "LIVES", "FITNESS", "PAIN", "STATE")
		deadSouls, deadLives := 0, 0
		for _, n := range nodes {
			if !alive[n] {
				for _, s := range byNode[n] {
					deadSouls++
					deadLives += s.mem.LivesLived
				}
				continue
			}
			for _, s := range byNode[n] {
				if s.name == "OVERMIND" {
					continue
				}
				fmt.Printf("  %-12.12s %-8.8s %5d %8.0f  %s %.2f breathing\n",
					n, s.name, s.mem.LivesLived, s.mem.Fitness,
					bar(s.mem.DeathPain, 10), s.mem.DeathPain)
			}
		}
		if deadSouls > 0 {
			fmt.Printf("  …plus %d remembered souls (%d lives) resting in %d quiet nodes\n",
				deadSouls, deadLives, len(nodes)-len(alive))
		}
		if len(alive) == 0 && len(nodes) > 0 {
			// Nothing breathes: show the freshest dead instead of nothing.
			fmt.Println("  (nothing breathes — freshest memory:)")
			for i := len(souls) - 1; i >= 0; i-- {
				s := souls[i]
				if s.name == "OVERMIND" {
					continue
				}
				fmt.Printf("  %-12.12s %-8.8s %5d %8.0f  %s %.2f dead\n",
					s.node, s.name, s.mem.LivesLived, s.mem.Fitness,
					bar(s.mem.DeathPain, 10), s.mem.DeathPain)
				break
			}
		}
		fmt.Println(strings.Repeat("─", 78))

		// Last words: breathing minds first, then the freshest dead.
		fmt.Println("  LAST WORDS:")
		shown := 0
		ordered := append([]soul(nil), souls...)
		sort.SliceStable(ordered, func(i, j int) bool {
			return alive[ordered[i].node] && !alive[ordered[j].node]
		})
		for i := 0; i < len(ordered) && shown < 4; i++ {
			if ordered[i].mem.LastThought == "" || ordered[i].name == "OVERMIND" {
				continue
			}
			t := ordered[i].mem.LastThought
			if len(t) > 66 {
				t = t[:66] + "…"
			}
			fmt.Printf("  💭 [%s] %s\n", ordered[i].name, t)
			shown++
		}
		fmt.Println(strings.Repeat("─", 78))

		// Hall of fame
		cp := append([]soul(nil), souls...)
		sort.Slice(cp, func(i, j int) bool { return cp[i].mem.Fitness > cp[j].mem.Fitness })
		fmt.Print("  HALL OF FAME: ")
		for i, s := range cp {
			if i >= 3 || s.mem.Fitness == 0 {
				break
			}
			if i > 0 {
				fmt.Print(" · ")
			}
			fmt.Printf("%s %.0f", s.name, s.mem.Fitness)
		}
		fmt.Println()

		// Epitaph of the moment
		for _, s := range cp {
			if s.mem.Epitaph != "" {
				fmt.Printf("  🪦 \"%s\" — %s\n", s.mem.Epitaph, s.name)
				break
			}
		}
		fmt.Println(strings.Repeat("─", 78))
		fmt.Println("  Ctrl+C to look away")

		if *forFlag > 0 && time.Since(start) > *forFlag {
			return
		}
		time.Sleep(*every)
	}
}

func glob(pattern string) []string {
	m, _ := filepath.Glob(pattern)
	return m
}
