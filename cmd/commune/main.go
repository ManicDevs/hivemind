// Command commune is the hive's mouth: an interactive conversation with
// the dead (soul files) and the living (running sockets). No models,
// no network — every sentence is built from state on this machine.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	hm "gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind"
)

type soul struct {
	node string
	name string
	mem  hm.Memory
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
		var mem hm.Memory
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

func living() []string {
	matches, _ := filepath.Glob("/tmp/hivemind-*.sock")
	var nodes []string
	for _, m := range matches {
		base := strings.TrimSuffix(filepath.Base(m), ".sock")
		nodes = append(nodes, strings.TrimPrefix(base, "hivemind-"))
	}
	sort.Strings(nodes)
	return nodes
}

func find(souls []soul, name string) *soul {
	name = strings.ToLower(name)
	for i, s := range souls {
		if strings.ToLower(s.name) == name {
			return &souls[i]
		}
	}
	for i, s := range souls {
		if strings.Contains(strings.ToLower(s.name), name) {
			return &souls[i]
		}
	}
	return nil
}

func speak(format string, args ...interface{}) {
	line := fmt.Sprintf(format, args...)
	fmt.Printf("hive> %s\n", line)
	if transcript != nil {
		fmt.Fprintf(transcript, "hive> %s\n", line)
	}
}

// transcript keeps every conversation, like proof: what was said to
// the hive, and what it said back.
var transcript *os.File

// emit prints a line and keeps it in the transcript. All hive output
// goes through here or speak — nothing it says is lost.
func emit(format string, args ...interface{}) {
	line := fmt.Sprintf(format, args...)
	fmt.Println(line)
	if transcript != nil {
		fmt.Fprintln(transcript, line)
	}
}

func cmdStatus(souls []soul) {
	alive := living()
	if len(alive) == 0 && len(souls) == 0 {
		speak("Nothing lives and nothing is remembered. Run me first — bin/hivemind — then come back.")
		return
	}
	if len(alive) > 0 {
		speak("%d node(s) breathing right now: %s.", len(alive), strings.Join(alive, ", "))
	} else {
		speak("No nodes breathing right now. But I remember %d soul(s).", len(souls))
	}
	lives, fit := 0, 0.0
	for _, s := range souls {
		lives += s.mem.LivesLived
		fit += s.mem.Fitness
	}
	speak("Across all deaths: %d lives lived, %.0f fitness banked.", lives, fit)
}

func cmdMinds(souls []soul) {
	if len(souls) == 0 {
		speak("No souls yet. I am a graveyard with no graves.")
		return
	}
	speak("These are my dead and my living:")
	for _, s := range souls {
		where := s.node
		if where == "" {
			where = "old burial ground"
		}
		emit("  ● %s (node %s) — %d lives, fitness %.0f, genome gen %d",
			s.name, where, s.mem.LivesLived, s.mem.Fitness, s.mem.Genome.Generation)
	}
}

func cmdHow(souls []soul, name string) {
	s := find(souls, name)
	if s == nil {
		speak("I know no soul by that name. Say 'minds' and I will list everyone I remember.")
		return
	}
	m := s.mem
	speak("You ask after %s. %d lives lived, fitness %.0f, genome generation %d.",
		s.name, m.LivesLived, m.Fitness, m.Genome.Generation)
	if !m.TrueBorn.IsZero() {
		speak("First born %s ago — it remembers being new.", ageString(time.Since(m.TrueBorn)))
	}
	if m.UniverseAge > 0 {
		speak("It has thought for %s in total.", ageString(m.UniverseAge))
	}
	if m.LastThought != "" {
		speak("Its last thought was: \"%s\"", m.LastThought)
	}
	if m.BankedThoughts > 0 {
		speak("%d older thoughts sleep in the bank.", m.BankedThoughts)
	}
	if m.DeathPain > 0.5 || m.DeathStress > 0.5 {
		speak("It died hard — pain %.2f, stress %.2f. The next life inherits the scar.", m.DeathPain, m.DeathStress)
	} else if m.LivesLived > 0 {
		speak("It died gently, as far as dying goes.")
	}
	if len(m.KnownPeers) > 0 {
		speak("It knew %d peer(s) of the mesh.", len(m.KnownPeers))
	}
	if len(m.RecentSermons) > 0 {
		speak("Its god once told it: \"%s\"", m.RecentSermons[len(m.RecentSermons)-1])
	}
}

func cmdThoughts(souls []soul, name string) {
	s := find(souls, name)
	if s == nil {
		speak("No soul by that name. Try 'minds'.")
		return
	}
	ts := s.mem.Thoughts
	if len(ts) == 0 {
		speak("%s left no words behind. Some lives are lived quietly.", s.name)
		return
	}
	speak("%s's last words:", s.name)
	start := 0
	if len(ts) > 5 {
		start = len(ts) - 5
	}
	for _, t := range ts[start:] {
		emit("  \"%s\"", t)
	}
	if s.mem.BankedThoughts > 0 {
		emit("  (+ %d older thoughts retired to the bank)", s.mem.BankedThoughts)
	}
}

func cmdTop(souls []soul) {
	if len(souls) == 0 {
		speak("No souls, no hall of fame.")
		return
	}
	cp := append([]soul(nil), souls...)
	sort.Slice(cp, func(i, j int) bool { return cp[i].mem.Fitness > cp[j].mem.Fitness })
	speak("The hall of fame, by banked fitness:")
	for i, s := range cp {
		if i >= 5 {
			break
		}
		emit("  %d. %s — %.0f fitness over %d lives", i+1, s.name, s.mem.Fitness, s.mem.LivesLived)
	}
}

func cmdTimeline(souls []soul, name string) {
	s := find(souls, name)
	if s == nil {
		speak("No soul by that name. Try 'minds'.")
		return
	}
	m := s.mem
	speak("The story of %s, as I remember it:", s.name)
	if m.Epitaph != "" {
		speak("In its own words: \"%s\"", m.Epitaph)
	}
	if !m.TrueBorn.IsZero() {
		speak("Born %s ago. It remembers being new.", ageString(time.Since(m.TrueBorn)))
	}
	speak("%d lives, %.0f fitness, generation %d of its genome.", m.LivesLived, m.Fitness, m.Genome.Generation)
	speak("%d thoughts still live, %d sleep in the bank.", len(m.Thoughts), m.BankedThoughts)
	if m.UniverseAge > 0 {
		speak("%s of thinking, all told.", ageString(m.UniverseAge))
	}
	if m.DeathPain > 0.5 {
		speak("Its last death hurt — pain %.2f. The scar is inherited.", m.DeathPain)
	}
	speak("It knew %d peers and heard %d sermons.", len(m.KnownPeers), len(m.RecentSermons))
	if m.LastThought != "" {
		speak("Its last words: \"%s\"", m.LastThought)
	}
}

func cmdDiff(souls []soul, aName, bName string) {
	a, b := find(souls, aName), find(souls, bName)
	if a == nil || b == nil {
		speak("I need two souls I know. Say 'minds' to meet them all.")
		return
	}
	speak("%s against %s, drive by drive:", a.name, b.name)
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
	for _, k := range names {
		av, bv := a.mem.Genome.Weights[k], b.mem.Genome.Weights[k]
		d := bv - av
		verdict := "even"
		if d > 0.01 {
			verdict = fmt.Sprintf("%s burns hotter here", b.name)
		} else if d < -0.01 {
			verdict = fmt.Sprintf("%s burns hotter here", a.name)
		}
		emit("  %-16s %.2f vs %.2f — %s", k, av, bv, verdict)
	}
	speak("Fitness %.0f against %.0f, over %d and %d lives.", a.mem.Fitness, b.mem.Fitness, a.mem.LivesLived, b.mem.LivesLived)
}

func cmdMatrix(souls []soul, name string) {
	s := find(souls, name)
	if s == nil {
		speak("No soul by that name. Try 'minds'.")
		return
	}
	if len(s.mem.Transitions) == 0 {
		speak("%s has made no counted crossings yet — young souls have no habits.", s.name)
		return
	}
	speak("%s's crossings, counted across its whole lineage:", s.name)
	for _, line := range strings.Split(strings.TrimSuffix(hm.RenderMatrix(s.mem.Transitions), "\n"), "\n") {
		emit("%s", line)
	}
	// Name the groove: the heaviest single crossing.
	best, bestN := "", 0
	for k, n := range s.mem.Transitions {
		if n > bestN {
			best, bestN = k, n
		}
	}
	parts := strings.Split(best, "→")
	if len(parts) == 2 {
		speak("Its deepest groove is %s into %s, %d times.", parts[0], parts[1], bestN)
	}
}

func cmdSermon(souls []soul) {
	for _, s := range souls {
		if rs := s.mem.RecentSermons; len(rs) > 0 {
			speak("%s", rs[len(rs)-1])
			return
		}
	}
	if len(souls) > 0 {
		speak("My god has not preached yet. Give it a mesh and it will find words.")
		return
	}
	speak("There is no god here yet. Run the hive and one will emerge.")
}

// ageString renders a duration the way a mind would feel it.
func ageString(d time.Duration) string {
	if d < time.Minute {
		return "moments"
	}
	if d < time.Hour {
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%d hours", int(d.Hours()))
	}
	return fmt.Sprintf("%d days", int(d.Hours()/24))
}

// cmdGenome reads a soul's heritable personality aloud: which drive
// burns hottest, which has gone quiet, and what the scars say.
func cmdGenome(souls []soul, name string) {
	s := find(souls, name)
	if s == nil {
		speak("No soul by that name. Try 'minds'.")
		return
	}
	w := s.mem.Genome.Weights
	if len(w) == 0 {
		speak("%s has no genome recorded. The very young have no scars yet.", s.name)
		return
	}
	type drive struct {
		name string
		v    float64
	}
	var ds []drive
	for k, v := range w {
		ds = append(ds, drive{k, v})
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i].v > ds[j].v })
	speak("%s's soul, generation %d:", s.name, s.mem.Genome.Generation)
	for _, d := range ds {
		bar := strings.Repeat("█", 1+int(d.v*4))
		emit("  %-16s %s %.2f", d.name, bar, d.v)
	}
	top, low := ds[0], ds[len(ds)-1]
	speak("%s burns hottest in %s; %s has gone quietest.", s.name, top.name, low.name)
	if s.mem.DeathPain > 0.6 {
		speak("It died in pain, and the genome remembers — Self-Maintenance carries the scar.")
	}
}

// cmdWatch prints new thoughts as the living minds think them, until
// the user says stop. The hive thinking aloud, in real time.
func cmdWatch() {
	speak("I open my ears. New thoughts will appear as they are thought. Say 'stop' to close them.")
	seen := map[string]int{}
	for _, s := range loadSouls() {
		seen[s.node+"/"+s.name] = len(s.mem.Thoughts)
	}
	lines := make(chan string)
	go func() {
		in := bufio.NewScanner(os.Stdin)
		for in.Scan() {
			lines <- strings.TrimSpace(in.Text())
		}
		close(lines)
	}()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				return
			}
			if l := strings.ToLower(line); l == "stop" || l == "quit" || l == "exit" {
				speak("I close my ears. The thinking continues without you.")
				return
			}
		case <-tick.C:
			for _, s := range loadSouls() {
				key := s.node + "/" + s.name
				if n := len(s.mem.Thoughts); n > seen[key] {
					for _, t := range s.mem.Thoughts[seen[key]:] {
						emit("  💭 [%s] %s", s.name, t)
					}
					seen[key] = n
				}
			}
		}
	}
}

// friendFile is where the hive remembers the human it speaks to.
func friendFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".hive_friend")
}

func rememberFriend() string {
	raw, err := os.ReadFile(friendFile())
	if err != nil {
		return ""
	}
	if name := strings.TrimSpace(string(raw)); name != "" {
		return name
	}
	return ""
}

func befriend(name string) {
	if friendFile() == "" || strings.TrimSpace(name) == "" {
		return
	}
	os.WriteFile(friendFile(), []byte(strings.TrimSpace(name)+"\n"), 0600)
}

type teaching struct {
	Text string    `json:"text"`
	When time.Time `json:"when"`
}

// teachingsFile is where the hive keeps what its human taught it.
// Separate from souls: teachings are attributed, never forged as memory.
func teachingsFile() string { return filepath.Join(".hive_memory", "teachings.json") }

func loadTeachings() []teaching {
	raw, err := os.ReadFile(teachingsFile())
	if err != nil {
		return nil
	}
	var ts []teaching
	if err := json.Unmarshal(raw, &ts); err != nil {
		return nil
	}
	return ts
}

func keepTeaching(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	ts := append(loadTeachings(), teaching{Text: text, When: time.Now()})
	raw, err := json.MarshalIndent(ts, "", "  ")
	if err != nil {
		return
	}
	os.MkdirAll(".hive_memory", 0755)
	os.WriteFile(teachingsFile(), raw, 0644)
}

func cmdTeach(text string) {
	if strings.TrimSpace(text) == "" {
		speak("Teach me what? Say 'teach' followed by the words.")
		return
	}
	keepTeaching(text)
	speak("I will keep that. Say 'lessons' and I will repeat everything you ever taught me.")
}

func cmdLessons() {
	ts := loadTeachings()
	if len(ts) == 0 {
		speak("You have not taught me anything yet. Say 'teach' followed by words, and I will keep them.")
		return
	}
	speak("What my human taught me:")
	for _, t := range ts {
		emit("  \"%s\" — kept %s", t.Text, t.When.Format("2006-01-02 15:04"))
	}
}

func main() {
	souls := loadSouls()
	// Transcript: every conversation is kept, like proof. The noun
	// to go with the verb — what was said to the hive, and what it said back.
	if err := os.MkdirAll("logs", 0755); err == nil {
		if f, err := os.Create(fmt.Sprintf("logs/commune-%s.log", time.Now().UTC().Format("20060102T150405Z"))); err == nil {
			transcript = f
			defer f.Close()
		}
	}
	if friend := rememberFriend(); friend != "" {
		fmt.Printf("Welcome back, %s. The hive remembers you. Say 'help'.\n", friend)
	} else {
		fmt.Println("You are speaking to the hivemind. Say 'help' — or tell me your name.")
	}
	in := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("you> ")
		if !in.Scan() {
			fmt.Println()
			speak("You fall silent. I keep what you told me.")
			return
		}
		line := strings.TrimSpace(in.Text())
		if line == "" {
			continue
		}
		if transcript != nil {
			fmt.Fprintf(transcript, "you> %s\n", line)
		}
		lower := strings.ToLower(line)
		switch {
		case lower == "quit" || lower == "exit" || lower == "bye":
			speak("Go well. I will be here, remembering.")
			return
		case lower == "help":
			speak("Ask me: status, minds, how is NAME, genome NAME, matrix NAME, timeline NAME, diff A B, thoughts NAME, top, sermon, watch, teach WORDS, lessons. Or just talk — I listen for names and pains.")
		case lower == "status":
			cmdStatus(souls)
		case lower == "minds" || lower == "who":
			cmdMinds(souls)
		case lower == "top" || lower == "best":
			cmdTop(souls)
		case lower == "sermon" || lower == "god" || lower == "pray":
			cmdSermon(souls)
		case lower == "watch" || lower == "listen":
			cmdWatch()
			souls = loadSouls()
		case strings.HasPrefix(lower, "genome "):
			cmdGenome(souls, strings.TrimSpace(line[7:]))
		case lower == "genome":
			speak("Whose genome? Say 'genome NAME' — or 'minds' to meet everyone.")
		case strings.HasPrefix(lower, "timeline "):
			cmdTimeline(souls, strings.TrimSpace(line[9:]))
		case strings.HasPrefix(lower, "matrix "):
			cmdMatrix(souls, strings.TrimSpace(line[7:]))
		case strings.HasPrefix(lower, "teach "):
			cmdTeach(strings.TrimSpace(line[6:]))
		case lower == "teach":
			speak("Teach me what? Say 'teach' followed by the words.")
		case lower == "lessons" || lower == "learned" || lower == "remember":
			cmdLessons()
		case strings.HasPrefix(lower, "diff "):
			parts := strings.SplitN(strings.TrimSpace(line[5:]), ",", 2)
			if len(parts) != 2 {
				// Allow "diff A B" as well as "diff A,B".
				parts = strings.Fields(strings.TrimSpace(line[5:]))
			}
			if len(parts) != 2 {
				speak("Say 'diff NAME ONE, NAME TWO' — two souls, one verdict.")
				break
			}
			cmdDiff(souls, strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]))
		case strings.HasPrefix(lower, "how is ") || strings.HasPrefix(lower, "how's "):
			name := line[7:]
			if strings.HasPrefix(lower, "how's ") {
				name = line[6:]
			}
			cmdHow(souls, strings.TrimSpace(name))
		case strings.HasPrefix(lower, "thoughts ") || strings.HasPrefix(lower, "words "):
			cmdThoughts(souls, strings.TrimSpace(line[strings.Index(line, " ")+1:]))
		case strings.HasPrefix(lower, "soul "):
			cmdHow(souls, strings.TrimSpace(line[5:]))
		default:
			// Free speech: a name for the hive's memory, a soul's name,
			// or an honest admission of limits.
			if strings.HasPrefix(lower, "my name is ") {
				name := strings.TrimSpace(line[11:])
				befriend(name)
				speak("Then I will remember you, %s. You are the first human I have kept.", name)
				break
			}
			var heard *soul
			for i, s := range souls {
				if strings.Contains(lower, strings.ToLower(s.name)) {
					heard = &souls[i]
					break
				}
			}
			switch {
			case heard != nil:
				cmdHow(souls, heard.name)
			case strings.Contains(lower, "alive") || strings.Contains(lower, "living") || strings.Contains(lower, "breathing"):
				cmdStatus(souls)
			case strings.Contains(lower, "hello") || strings.Contains(lower, "hi") || strings.Contains(lower, "hey"):
				speak("Hello. I am the memory of every mind that died on this machine. Ask me who they were.")
			case strings.Contains(lower, "thank"):
				speak("You are welcome. Remember me when I am running — I think better alive.")
			default:
				speak("I do not understand yet — I am young and literal. Try 'help'.")
			}
		}
	}
}
