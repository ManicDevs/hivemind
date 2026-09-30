package hivemind

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind/language"
)

func resetLawbook(t *testing.T) {
	t.Helper()
	lawbook.mu.Lock()
	lawbook.entries = []LawEntry{}
	lawbook.path = ""
	lawbook.mu.Unlock()
	t.Cleanup(func() {
		lawbook.mu.Lock()
		lawbook.entries = []LawEntry{}
		lawbook.path = ""
		lawbook.mu.Unlock()
	})
}

// bindTempJournal points the book at a throwaway journal so tests never
// touch the repo's real data/law.journal, and returns its path.
func bindTempJournal(t *testing.T) string {
	t.Helper()
	jpath := filepath.Join(t.TempDir(), "law.journal")
	if _, _, err := LawLoadJournal(jpath); err != nil {
		t.Fatalf("bind journal: %v", err)
	}
	return jpath
}

// seedClauses is a tiny corpus with deliberately overlapping vocabulary so
// retrieval ranking is observable: two clauses mention "due process", one of
// them matches "religion" too.
var seedClauses = []LawEntry{
	{
		Source: "C1",
		Tag:    "due process",
		Clause: "No person shall be deprived of life, liberty, or property, without due process of law.",
	},
	{
		Source: "C2",
		Tag:    "religion/speech",
		Clause: "Congress shall make no law respecting an establishment of religion, or abridging the freedom of speech.",
	},
	{
		Source: "C3",
		Tag:    "due process/equal protection",
		Clause: "No State shall deprive any person of life, liberty, or property, without due process of law, nor deny the equal protection of the laws.",
	},
}

func TestProvisionDedupesByDerivedID(t *testing.T) {
	resetLawbook(t)
	bindTempJournal(t)

	if _, err := ProvisionLawbook(seedClauses); err != nil {
		t.Fatalf("provision: %v", err)
	}
	if got := LawStats(); got != len(seedClauses) {
		t.Fatalf("LawStats = %d, want %d", got, len(seedClauses))
	}

	// Same text, empty ids: stable derived ids mean the rebroadcast counts
	// its law once.
	n, err := ProvisionLawbook(seedClauses)
	if err != nil {
		t.Fatalf("re-provision: %v", err)
	}
	if n != 0 {
		t.Fatalf("re-provision added %d clauses, want 0 (idempotent)", n)
	}
	if got := LawStats(); got != len(seedClauses) {
		t.Fatalf("LawStats after re-provision = %d, want %d", got, len(seedClauses))
	}

	// Explicit identical ids also dedupe even with different text. The
	// fixture entries are unseeded (empty ids), so the derived id is the
	// one the book actually holds.
	dup := LawEntry{ID: lawID(seedClauses[0].Clause), Source: "C1b", Tag: "x", Clause: "a wholly different clause"}
	if n, _ := ProvisionLawbook([]LawEntry{dup}); n != 0 {
		t.Fatalf("explicit-id duplicate added %d, want 0", n)
	}
}

func TestJournalIsSourceOfTruthAcrossProcessBoundary(t *testing.T) {
	resetLawbook(t)
	jpath := bindTempJournal(t)

	if _, err := ProvisionLawbook(seedClauses); err != nil {
		t.Fatalf("provision: %v", err)
	}
	if got := LawStats(); got != len(seedClauses) {
		t.Fatalf("book after provision = %d, want %d", got, len(seedClauses))
	}

	// A fresh process (empty book) bound to the same journal inherits every
	// clause from the file: the journal outlives the node.
	resetLawbook(t)
	loaded, skipped, err := LawLoadJournal(jpath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if loaded != len(seedClauses) {
		t.Fatalf("reload loaded %d clauses, want %d", loaded, len(seedClauses))
	}
	if skipped != 0 {
		t.Fatalf("clean journal reported %d corrupt lines", skipped)
	}
}

func TestJournalSkipsCorruptLines(t *testing.T) {
	resetLawbook(t)
	jpath := filepath.Join(t.TempDir(), "dirty.journal")
	raw := `{"source":"A","tag":"x","clause":"clean clause one"}
this line is not json
{"source":"B","tag":"y","clause":"clean clause two"}
`
	if err := os.WriteFile(jpath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, skipped, err := LawLoadJournal(jpath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded != 2 || skipped != 1 {
		t.Fatalf("loaded=%d skipped=%d, want 2/1", loaded, skipped)
	}
}

func TestLawRetrieveRanking(t *testing.T) {
	resetLawbook(t)
	bindTempJournal(t)

	if _, err := ProvisionLawbook(seedClauses); err != nil {
		t.Fatalf("provision: %v", err)
	}

	// "religion" appears only in C2: it must come back first, and never
	// outranked by the shared-function words.
	got := LawRetrieve("the establishment of religion", 3)
	if len(got) == 0 {
		t.Fatal("religion query returned nothing")
	}
	if !strings.Contains(got[0].Text, "religion") || strings.Contains(got[0].Text, "State") {
		t.Fatalf("top hit for religion = %q, want C2", got[0].Text)
	}

	// "due process" matches C1 and C3; both must appear.
	got = LawRetrieve("due process of law", 2)
	if len(got) != 2 {
		t.Fatalf("due-process query returned %d excerpts, want 2", len(got))
	}
	for _, g := range got {
		if !strings.Contains(g.Text, "due process") {
			t.Fatalf("excerpt misses the query terms: %q", g.Text)
		}
	}

	// Empty and unmatched queries stay silent rather than inventing law.
	if got := LawRetrieve("", 3); got != nil {
		t.Fatalf("empty query returned %d excerpts, want none", len(got))
	}
	if got := LawRetrieve("quantum waffle", 3); got != nil {
		t.Fatalf("noise query returned %d excerpts, want none", len(got))
	}

	// Handles carry the audit trail for the LLM: truncated derived id,
	// source, and tag.
	if !strings.Contains(got[0].Handle, "|C1|") && !strings.Contains(got[0].Handle, "|C3|") {
		t.Fatalf("handle = %q, want a C1| or C3| source part", got[0].Handle)
	}
}

func TestLawExcerptsForEmptyMind(t *testing.T) {
	resetLawbook(t)
	bindTempJournal(t)

	// A nil mind with a maintenance-style goal gets silence: law retrieval
	// must not force provisions into every utterance.
	if got := lawExcerptsFor(nil, "Self-Maintenance"); got != nil {
		t.Fatalf("maintenance goal retrieved %d excerpts, want none", len(got))
	}
}

// lawEchoStub mirrors the whole prompt back as its "reply", so a test can
// observe exactly what the production GenerateReply path handed to the
// model — without needing a real LLM rate slot.
type lawEchoStub struct{}

func (lawEchoStub) Generate(_ context.Context, prompt string) (string, error) { return prompt, nil }
func (lawEchoStub) Name() string                                              { return "lawEchoStub" }
func (lawEchoStub) Close() error                                              { return nil }

// A peer question is answered through the same production path a live node
// uses: answerPeer mines the question into LawRetrieve, the retrieved
// provisions render into the reply prompt, and the broadcast answer carries
// the citation. The model sees the grounding, not just the feel.
func TestAnswerPeerGroundsReplyInLawbook(t *testing.T) {
	resetLawbook(t)
	bindTempJournal(t)
	if _, err := ProvisionLawbook(seedClauses); err != nil {
		t.Fatalf("provision: %v", err)
	}

	s := NewSwarm()
	in := s.Join("observer")
	m := NewMind("Counsel", s)
	m.LanguageBridge = language.NewLanguageBridge(lawEchoStub{}, 10*time.Second)
	m.Genome.Weights[GoalSocialization] = 1.5

	question := "Can a State deprive any person of life or liberty without due process of law and equal protection?"
	m.answerPeer(&SecureMessage{
		SenderPubKey: "the-questioner",
		Kind:         "thought",
		PayloadStr:   question,
	}, "qa-peer")

	select {
	case got := <-in:
		if got.Kind != "thought_reply" {
			t.Fatalf("frame kind = %s, want thought_reply", got.Kind)
		}
		if !strings.Contains(got.PayloadStr, "Provisioned knowledge retrieved for this moment") {
			t.Fatalf("reply prompt never showed the grounding block\n---\n%s", got.PayloadStr)
		}
		// "equal protection" appears in exactly one provision: the query
		// must have retrieved that clause, not a tangent, and the answer
		// must carry it verbatim.
		if !strings.Contains(got.PayloadStr, "equal protection of the laws") {
			t.Fatalf("reply prompt lost the retrieved provision\n---\n%s", got.PayloadStr)
		}
		// The model was told it MAY quote the provision verbatim: the
		// citation floor is open in the very same utterance it voices.
		if !strings.Contains(got.PayloadStr, "you MAY quote it verbatim") {
			t.Fatalf("reply prompt did not open the citation floor\n---\n%s", got.PayloadStr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no reply frame arrived on the swarm")
	}
}

func TestLawParseTextGrammar(t *testing.T) {
	src := `# US Const. amend. I
Tag: religion, speech

Congress shall make no law respecting an establishment of religion.

or abridging the freedom of speech, or of the press.

# Uniform Time Act
Tag: time limits

Claims arising under this act shall be brought within three years.`
	entries, err := LawParseText(src, "fallback.txt", 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("parsed %d clauses, want 3: %+v", len(entries), entries)
	}
	if entries[0].Source != "US Const. amend. I" {
		t.Errorf("first source = %q", entries[0].Source)
	}
	if entries[0].Tag != "religion, speech" {
		t.Errorf("first tag = %q", entries[0].Tag)
	}
	if !strings.Contains(entries[0].Clause, "establishment of religion") ||
		!strings.Contains(entries[1].Clause, "freedom of speech") {
		t.Errorf("clause split wrong: %q / %q", entries[0].Clause, entries[1].Clause)
	}
	if entries[2].Source != "Uniform Time Act" || entries[2].Tag != "time limits" ||
		!strings.Contains(entries[2].Clause, "within three years") {
		t.Errorf("second source block wrong: %+v", entries[2])
	}
}

func TestLawParseTextFallbackSourceAndBlockquotes(t *testing.T) {
	src := `> Title preamble kept as text.

The receiving party must keep all records confidential for three years.

> A second clause in blockquote form.`
	entries, err := LawParseText(src, "contract.txt", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("parsed %d, want 3", len(entries))
	}
	if entries[0].Source != "contract.txt" {
		t.Errorf("fallback source %q, want contract.txt", entries[0].Source)
	}
	if entries[0].Clause == entries[1].Clause || entries[1].Clause == entries[2].Clause {
		t.Fatal("paragraphs collapsed into one clause")
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Clause, ">") || strings.HasPrefix(e.Clause, "#") {
			t.Errorf("marker leaked into clause: %q", e.Clause)
		}
	}
}

func TestLawParseFileDispatchesByExtension(t *testing.T) {
	dir := t.TempDir()
	wantClause := "nor deny any person equal protection of the laws."
	jsonFile := filepath.Join(dir, "corpus.json")
	if err := os.WriteFile(jsonFile, []byte(`[{"source":"X","tag":"eq","clause":"`+wantClause+`"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	mdFile := filepath.Join(dir, "corpus.md")
	if err := os.WriteFile(mdFile, []byte("# Statute\nTag: eq\n\n"+wantClause+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	fromJSON, err := LawParseFile(jsonFile, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(fromJSON) != 1 || fromJSON[0].Clause != wantClause || fromJSON[0].Source != "X" {
		t.Fatalf("json dispatch wrong: %+v", fromJSON)
	}
	fromMD, err := LawParseFile(mdFile, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(fromMD) != 1 || fromMD[0].Source != "Statute" {
		t.Fatalf("md dispatch wrong: %+v", fromMD)
	}

	if got := ClassifyLawPath("https://example.com/x.md"); got != LawRemote {
		t.Errorf("url classified %d, want LawRemote", got)
	}
	if got := ClassifyLawPath(filepath.Join(dir, "y.unknown")); got != LawMarkdown {
		t.Errorf("unknown ext classified %d, want LawMarkdown", got)
	}
}

func TestLawParseTextMaxClauses(t *testing.T) {
	src := "A.\n\nB.\n\nC.\n"
	entries, err := LawParseText(src, "f", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("maxClauses=2 yielded %d entries", len(entries))
	}
}
