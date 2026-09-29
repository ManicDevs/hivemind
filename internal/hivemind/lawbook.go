package hivemind

// Lawbook is a persistent, per-node knowledge substrate: provisioned legal
// and normative text kept in an append-only journal on disk and retrieved
// into every mind's reasoning at speech time. The lawbook itself is not a
// mined frame (a cited thought is); the journal is the source of truth and
// survives node death, so a reincarnated mind inherits the law, not the
// debate about it.

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"unicode"
)

// LawEntry is one provisioned clause. ID is the stable journal id; Source
// and Tag make the intent auditable (a statute cite, a contract section,
// an instrument name); Clause is the full text the minds may cite verbatim.
type LawEntry struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Tag    string `json:"tag"`
	Clause string `json:"clause"`
	Minted string `json:"minted,omitempty"`
}

// LawExcerpt is what a mind actually sees: the clause plus its audit handle.
type LawExcerpt struct {
	Handle string
	Text   string
}

// lawbook is the process-shared (one node per process) knowledge store.
var lawbook struct {
	mu      sync.RWMutex
	entries []LawEntry
	path    string
}

// defaultLawJournal is the journal path used unless the engine overrides it.
const defaultLawJournal = "data/law.journal"

// LawLoadJournal reads every line of a JSONL journal into the book. Missing
// file is not an error (an unseeded node has no law yet); corrupt lines are
// skipped and counted so an interrupted write never bricks the whole book.
func LawLoadJournal(path string) (loaded, skipped int, err error) {
	lawbook.mu.Lock()
	defer lawbook.mu.Unlock()
	lawbook.path = path
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	var entries []LawEntry
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e LawEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			skipped++
			continue
		}
		entries = append(entries, e)
	}
	if err := sc.Err(); err != nil {
		return 0, skipped, err
	}
	lawbook.entries = entries
	return len(entries), skipped, nil
}

// LawJournalCurrent reports the journal path the book is bound to.
func LawJournalCurrent() string {
	lawbook.mu.RLock()
	defer lawbook.mu.RUnlock()
	p := lawbook.path
	if p == "" {
		p = defaultLawJournal
	}
	return p
}

// ProvisionLawbook appends entries to the book and the journal on disk.
// Entries lacking an ID get one derived from the clause text, so identical
// provisions deduplicate naturally. The journal is appended a line per
// entry: a crash mid-write loses at most the interrupted entry.
func ProvisionLawbook(entries []LawEntry) (int, error) {
	path := LawJournalCurrent()
	lawbook.mu.Lock()
	defer lawbook.mu.Unlock()

	var out *os.File
	var err error
	if path != "" {
		if err = os.MkdirAll(dirOf(path), 0o755); err != nil {
			return 0, err
		}
		out, err = os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return 0, err
		}
		defer out.Close()
	}
	known := make(map[string]bool, len(lawbook.entries))
	for _, e := range lawbook.entries {
		known[e.ID] = true
	}
	added := 0
	for i := range entries {
		e := entries[i]
		if e.Clause == "" {
			continue
		}
		if e.ID == "" {
			e.ID = lawID(e.Clause)
		}
		if known[e.ID] {
			continue
		}
		known[e.ID] = true
		lawbook.entries = append(lawbook.entries, e)
		if out != nil {
			line, jerr := json.Marshal(e)
			if jerr != nil {
				continue
			}
			if _, werr := out.Write(append(line, '\n')); werr != nil {
				return added, werr
			}
		}
		added++
	}
	return added, nil
}

// LawStats reports how many clauses the book holds.
func LawStats() int {
	lawbook.mu.RLock()
	defer lawbook.mu.RUnlock()
	return len(lawbook.entries)
}

// LawList returns the provisioned entries (for seed/audit printing).
func LawList() []LawEntry {
	lawbook.mu.RLock()
	defer lawbook.mu.RUnlock()
	out := make([]LawEntry, len(lawbook.entries))
	copy(out, lawbook.entries)
	return out
}

// LawRetrieve scores every clause against a query by weighted term overlap
// and returns the k best excerpts in order. Pure Go, no embeddings: the
// surrounding LLM does the reasoning, this layer only grounds it. Terms are
// lowercased alphanumeric tokens; rarer tokens (featured in fewer clauses)
// carry more weight, so "establishment" outranks "law" in a religion query.
func LawRetrieve(query string, k int) []LawExcerpt {
	lawbook.mu.RLock()
	defer lawbook.mu.RUnlock()
	if k <= 0 {
		k = 1
	}
	q := tokenize(query)
	if len(q) == 0 {
		return nil
	}
	clauseFreq := make(map[string]int)
	tokenSets := make([]map[string]bool, len(lawbook.entries))
	for i, e := range lawbook.entries {
		set := tokenize(e.Clause)
		// Tag and source words let "GDPR" or "Article II" match without
		// appearing verbatim inside the clause.
		for t := range tokenize(e.Tag + " " + e.Source) {
			set[t] = true
		}
		tokenSets[i] = set
		for t := range set {
			clauseFreq[t]++
		}
	}
	scored := make([]struct {
		idx int
		sc  float64
	}, 0, len(lawbook.entries))
	for i, set := range tokenSets {
		var sc float64
		for t := range q {
			if !set[t] {
				continue
			}
			freq := clauseFreq[t]
			if freq <= 0 {
				continue
			}
			// idf-like weight: rarer terms dominate; idf flattens at 1.
			sc += 1 + 1/float64(freq)
		}
		if sc > 0 {
			scored = append(scored, struct {
				idx int
				sc  float64
			}{i, sc})
		}
	}
	// Stable sort by score desc, then entry order asc.
	for a := 1; a < len(scored); a++ {
		for b := a; b > 0; b-- {
			prev, cur := scored[b-1], scored[b]
			if cur.sc > prev.sc || (cur.sc == prev.sc && cur.idx < prev.idx) {
				scored[b-1], scored[b] = cur, prev
			} else {
				break
			}
		}
	}
	if len(scored) > k {
		scored = scored[:k]
	}
	if len(scored) == 0 {
		return nil
	}
	out := make([]LawExcerpt, 0, len(scored))
	for _, s := range scored {
		e := lawbook.entries[s.idx]
		out = append(out, LawExcerpt{
			Handle: fmt.Sprintf("%s|%s|%s", e.ID[:min(len(e.ID), 8)], e.Source, e.Tag),
			Text:   e.Clause,
		})
	}
	return out
}

// lawID derives a stable id from clause text.
func lawID(clause string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(clause)))
	return hex.EncodeToString(sum[:])[:12]
}

// lawExcerptsFor retrieves the top knowledge excerpts for a mind's current
// utterance. The query blends the explicit frame (the goal being pursued or
// the peer's broadcast thought) with whatever the mind is consciously
// chewing on, so a legal-minded brain gets the provisions relevant to its
// moment, and a pure-maintenance brain gets silence.
func lawExcerptsFor(m *Mind, explicit string) []string {
	query := explicit
	if m != nil && m.Workspace.ConsciousContent != "" {
		query += " " + m.Workspace.ConsciousContent
	}
	ex := LawRetrieve(query, 3)
	if len(ex) == 0 {
		return nil
	}
	out := make([]string, 0, len(ex))
	for _, e := range ex {
		out = append(out, fmt.Sprintf("[%s] %s", e.Handle, e.Text))
	}
	return out
}

// tokenize lowercases and splits a string into alphanumeric tokens.
func tokenize(s string) map[string]bool {
	out := make(map[string]bool)
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out[strings.ToLower(string(cur))] = true
			cur = cur[:0]
		}
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur = append(cur, r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[:i]
		}
	}
	return "."
}
