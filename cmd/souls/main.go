// Command souls reads the dead: census, genomes, thoughts, collective
// memory search, and fitness leaderboard. Read-only — it never writes
// a soul. Pure Go, stdlib only.
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

	hm "gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind"
)

type soul struct {
	path string
	mem  hm.Memory
}

func loadSouls() ([]soul, bool) {
	var files []string
	for _, pattern := range []string{".hive_memory/*/*.soul", ".hive_memory/*.soul"} {
		matches, _ := filepath.Glob(pattern)
		files = append(files, matches...)
	}
	sort.Strings(files)
	var souls []soul
	failed := false
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			fmt.Printf("  ❌ %s unreadable: %v\n", f, err)
			failed = true
			continue
		}
		var mem hm.Memory
		if err := json.Unmarshal(raw, &mem); err != nil {
			fmt.Printf("  ❌ %s corrupt: %v\n", f, err)
			failed = true
			continue
		}
		rel, _ := filepath.Rel(".hive_memory", f)
		souls = append(souls, soul{path: rel, mem: mem})
	}
	return souls, failed
}

func main() {
	genomeFlag := flag.String("genome", "", "show genome weights for soul NAME (e.g. -genome alpha-node/Alpha)")
	thoughtsFlag := flag.String("thoughts", "", "show last thoughts for soul NAME")
	lastN := flag.Int("n", 5, "thoughts to show with -thoughts")
	grepFlag := flag.String("grep", "", "search every soul's thoughts for PATTERN")
	topFlag := flag.Bool("top", false, "fitness leaderboard across all souls")
	timelineFlag := flag.String("timeline", "", "tell one soul's whole story (e.g. -timeline alpha-node/Alpha)")
	diffFlag := flag.String("diff", "", "compare two souls' genomes: -diff A,B (same handle, different evolution)")
	matrixFlag := flag.String("matrix", "", "show one soul's cycle matrix: which drive follows which")
	flag.Parse()

	souls, failed := loadSouls()
	if len(souls) == 0 {
		fmt.Println("  (no souls yet — run the hive once)")
		return
	}

	switch {
	case *genomeFlag != "":
		showGenome(souls, *genomeFlag)
	case *thoughtsFlag != "":
		showThoughts(souls, *thoughtsFlag, *lastN)
	case *grepFlag != "":
		grepThoughts(souls, *grepFlag)
	case *topFlag:
		showTop(souls)
	case *timelineFlag != "":
		showTimeline(souls, *timelineFlag)
	case *matrixFlag != "":
		showMatrix(souls, *matrixFlag)
	case *diffFlag != "":
		parts := strings.SplitN(*diffFlag, ",", 2)
		if len(parts) != 2 {
			fmt.Println("  usage: -diff SOUL_A,SOUL_B")
			os.Exit(1)
		}
		diffGenomes(souls, strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]))
	default:
		for _, s := range souls {
			fmt.Printf("  📜 %s lives=%d fitness=%.1f\n", s.path, s.mem.LivesLived, s.mem.Fitness)
		}
		fmt.Printf("  🧬 %d soul(s)\n", len(souls))
	}
	if failed {
		os.Exit(1)
	}
}

func findSoul(souls []soul, name string) *soul {
	name = strings.ToLower(strings.TrimSuffix(name, ".soul"))
	for i := range souls {
		bare := strings.ToLower(strings.TrimSuffix(souls[i].path, ".soul"))
		base := strings.ToLower(filepath.Base(bare))
		if bare == name || base == name {
			return &souls[i]
		}
	}
	return nil
}

// showGenome prints what evolution made of this soul: drive weights,
// generation, and the trauma it died with last.
func showGenome(souls []soul, name string) {
	s := findSoul(souls, name)
	if s == nil {
		fmt.Printf("  no soul named %q\n", name)
		os.Exit(1)
	}
	fmt.Printf("  🧬 %s — genome gen %d\n", s.path, s.mem.Genome.Generation)
	names := make([]string, 0, len(s.mem.Genome.Weights))
	for k := range s.mem.Genome.Weights {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		fmt.Printf("     %-16s %.2f\n", k, s.mem.Genome.Weights[k])
	}
	fmt.Printf("     lives=%d fitness=%.1f last trauma pain=%.2f stress=%.2f\n",
		s.mem.LivesLived, s.mem.Fitness, s.mem.DeathPain, s.mem.DeathStress)
}

// showThoughts prints the tail of one soul's archive: what it was
// thinking when the universe last ended (or checkpointed).
func showThoughts(souls []soul, name string, n int) {
	s := findSoul(souls, name)
	if s == nil {
		fmt.Printf("  no soul named %q\n", name)
		os.Exit(1)
	}
	th := s.mem.Thoughts
	if len(th) > n {
		th = th[len(th)-n:]
	}
	fmt.Printf("  💭 last %d of %s:\n", len(th), s.path)
	for _, t := range th {
		fmt.Printf("     %s\n", t)
	}
}

// grepThoughts searches the collective memory: every archived thought,
// every soul, one pattern. The hive remembering itself aloud.
func grepThoughts(souls []soul, pattern string) {
	hits := 0
	for _, s := range souls {
		for _, t := range s.mem.Thoughts {
			if strings.Contains(strings.ToLower(t), strings.ToLower(pattern)) {
				fmt.Printf("  [%s] %s\n", s.path, t)
				hits++
			}
		}
	}
	fmt.Printf("  🔍 %d thought(s) containing %q across %d soul(s)\n", hits, pattern, len(souls))
}

// showTop ranks souls by banked fitness: the hall of fame of the dead.
func showTop(souls []soul) {
	ranked := append([]soul(nil), souls...)
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].mem.Fitness > ranked[j].mem.Fitness })
	n := 10
	if len(ranked) < n {
		n = len(ranked)
	}
	fmt.Printf("  🏆 top %d by fitness:\n", n)
	for i := 0; i < n; i++ {
		fmt.Printf("     %d. %s — %.1f (%d lives)\n", i+1, ranked[i].path, ranked[i].mem.Fitness, ranked[i].mem.LivesLived)
	}
}

// showTimeline tells one soul's whole story: birth, lives, fitness,
// trauma, sermons heard, friends known. Biography from a JSON file.
func showTimeline(souls []soul, name string) {
	s := findSoul(souls, name)
	if s == nil {
		fmt.Printf("  no soul named %q\n", name)
		os.Exit(1)
	}
	m := s.mem
	fmt.Printf("  📖 %s\n", s.path)
	if m.Epitaph != "" {
		fmt.Printf("     epitaph   \"%s\"\n", m.Epitaph)
	}
	if !m.TrueBorn.IsZero() {
		fmt.Printf("     born      %s (%s ago)\n", m.TrueBorn.Format("2006-01-02 15:04"), ageAgo(time.Since(m.TrueBorn)))
	}
	fmt.Printf("     lives     %d · fitness %.1f · genome gen %d\n", m.LivesLived, m.Fitness, m.Genome.Generation)
	fmt.Printf("     thoughts  %d live + %d banked\n", len(m.Thoughts), m.BankedThoughts)
	if m.UniverseAge > 0 {
		fmt.Printf("     awake     %s of thinking in total\n", ageAgo(m.UniverseAge))
	}
	fmt.Printf("     last pain %.2f · last stress %.2f\n", m.DeathPain, m.DeathStress)
	fmt.Printf("     peers     %d known\n", len(m.KnownPeers))
	fmt.Printf("     sermons   %d remembered by its god\n", len(m.RecentSermons))
	if m.LastThought != "" {
		fmt.Printf("     last words \"%s\"\n", m.LastThought)
	}
}

// diffGenomes compares two souls drive by drive: the same handle raised
// in different worlds, or two minds grown apart. Deltas, not judgments.
func diffGenomes(souls []soul, aName, bName string) {
	a, b := findSoul(souls, aName), findSoul(souls, bName)
	if a == nil || b == nil {
		fmt.Printf("  need two souls, missing: %q %q\n", aName, bName)
		os.Exit(1)
	}
	keys := map[string]bool{}
	for k := range a.mem.Genome.Weights {
		keys[k] = true
	}
	for k := range b.mem.Genome.Weights {
		keys[k] = true
	}
	var names []string
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	fmt.Printf("  ⚖️  %s (gen %d) vs %s (gen %d)\n", a.path, a.mem.Genome.Generation, b.path, b.mem.Genome.Generation)
	for _, k := range names {
		av, bv := a.mem.Genome.Weights[k], b.mem.Genome.Weights[k]
		fmt.Printf("     %-16s %.2f → %.2f (%+.2f)\n", k, av, bv, bv-av)
	}
	fmt.Printf("     fitness %.1f vs %.1f · lives %d vs %d\n", a.mem.Fitness, b.mem.Fitness, a.mem.LivesLived, b.mem.LivesLived)
}

func ageAgo(d time.Duration) string {
	if d < time.Minute {
		return "moments"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// showMatrix prints one soul's cycle matrix: which drive follows which,
// counted across the whole lineage. Character as flow.
func showMatrix(souls []soul, name string) {
	s := findSoul(souls, name)
	if s == nil {
		fmt.Printf("  no soul named %q\n", name)
		os.Exit(1)
	}
	if len(s.mem.Transitions) == 0 {
		fmt.Printf("  %s has made no counted crossings yet — young souls have no habits.\n", s.path)
		return
	}
	fmt.Printf("  🔀 %s — cycle matrix:\n%s", s.path, hm.RenderMatrix(s.mem.Transitions))
}
