package train

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	hm "gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind"
)

var sampleEntries = []hm.LawEntry{
	{
		ID:     "111111111111",
		Source: "US Const. amend. XIV",
		Tag:    "equal protection",
		Clause: "nor deny to any person within its jurisdiction the equal protection of the laws.",
	},
	{
		ID:     "222222222222",
		Source: "US Const. amend. V",
		Tag:    "due process",
		Clause: "nor be deprived of life, liberty, or property, without due process of law.",
	},
}

func TestBuildDatasetCoversEveryClauseVerbatim(t *testing.T) {
	recs := BuildDataset(sampleEntries)
	if len(recs) == 0 {
		t.Fatal("dataset empty")
	}
	seenText := map[string]int{}
	for _, r := range recs {
		if strings.TrimSpace(r.Instruction) == "" || strings.TrimSpace(r.Response) == "" {
			t.Fatalf("blank pair: %+v", r)
		}
		seenText[r.Response]++
	}
	for _, e := range sampleEntries {
		if seenText[e.Clause] == 0 {
			t.Fatalf("clause %q never appears as ground truth", e.Clause)
		}
	}
}

func TestBuildDatasetDeduplicatesInstructions(t *testing.T) {
	// Two clauses sharing one tag silhouette must not duplicate the same
	// question text.
	recs := BuildDataset([]hm.LawEntry{
		{ID: "a", Tag: "tort", Clause: "first clause about liability."},
		{ID: "b", Tag: "tort", Clause: "second clause about liability."},
	})
	seen := map[string]bool{}
	for _, r := range recs {
		if seen[r.Instruction] {
			t.Fatalf("duplicate instruction %q", r.Instruction)
		}
		seen[r.Instruction] = true
	}
}

func TestBuildDatasetUmbrellaPairCount(t *testing.T) {
	// Each clause yields exactly two instructions (tag-phrase slots), so
	// the dataset size is deterministic and auditable.
	recs := BuildDataset(sampleEntries)
	if len(recs) != 2*len(sampleEntries) {
		t.Fatalf("dataset has %d records, want %d", len(recs), 2*len(sampleEntries))
	}
}

func TestBuildDatasetTaglessClauseFallsBackToNouns(t *testing.T) {
	recs := BuildDataset([]hm.LawEntry{{
		ID:     "c",
		Tag:    "",
		Clause: "The querying party may inspect all records upon reasonable notice.",
	}})
	if len(recs) == 0 {
		t.Fatal("tagless clause produced no instructions")
	}
	for _, r := range recs {
		if strings.Contains(r.Instruction, "<") || r.Instruction == "" {
			t.Fatalf("fallback instruction malformed: %q", r.Instruction)
		}
	}
}

func TestRenderModelfileEmbedsCorpusWithHandles(t *testing.T) {
	content := RenderModelfile(sampleEntries, "llama2:7b", "hivemind-counsel", "")
	for _, want := range []string{
		"FROM llama2:7b",
		"SYSTEM",
		"[11111111|US Const. amend. XIV|equal protection]",
		"equal protection of the laws.",
		"due process of law.",
		"PARAMETER temperature 0.7",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("modelfile missing %q\n---\n%s", want, content)
		}
	}
	if strings.Contains(content, "ADAPTER") {
		t.Fatal("modelfile lists an adapter none was given")
	}
}

func TestRenderModelfileWithAdapterLine(t *testing.T) {
	content := RenderModelfile(sampleEntries, "llama2:7b", "hivemind-counsel", "/tmp/counsel.lora.safetensors")
	if !strings.Contains(content, "ADAPTER /tmp/counsel.lora.safetensors") {
		t.Fatalf("adapter not wired\n---\n%s", content)
	}
}

func TestWriteDatasetIsJSONLDropReadable(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/set.jsonl"
	if err := WriteDataset(path, BuildDataset(sampleEntries)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != len(BuildDataset(sampleEntries)) {
		t.Fatalf("JSONL has %d lines", len(lines))
	}
	for _, l := range lines {
		var r Record
		if err := json.Unmarshal([]byte(l), &r); err != nil {
			t.Fatalf("corrupt line %q: %v", l, err)
		}
	}
}

func TestWriteArtifactsCreateDirs(t *testing.T) {
	dir := t.TempDir()
	if err := WriteDataset(dir+"/nested/a.jsonl", BuildDataset(sampleEntries)); err != nil {
		t.Fatal(err)
	}
	if err := WriteModelfile(dir+"/nested/b.Modelfile", "FROM x\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir + "/nested/b.Modelfile"); err != nil {
		t.Fatal(err)
	}
}

func TestScoreGroundedness(t *testing.T) {
	equalProtection := "nor deny to any person within its jurisdiction the equal protection of the laws."

	cases := []struct {
		name    string
		answers []Answer
		want    int
	}{
		{
			name: "verbatim quote is grounded",
			answers: []Answer{
				{Question: "q", Answer: equalProtection},
			},
			want: 1,
		},
		{
			name: "audit handle counts",
			answers: []Answer{
				{Question: "q", Answer: "See handle 11111111 above."},
			},
			want: 1,
		},
		{
			name: "close paraphrase reuses the provision's words",
			answers: []Answer{
				{Question: "q", Answer: "A State may not deny to any person the equal protection of the laws."},
			},
			want: 1,
		},
		{
			name: "invention is caught",
			answers: []Answer{
				{Question: "q", Answer: "The moon is made of cheese and no clause anywhere says otherwise."},
			},
			want: 0,
		},
		{
			name: "empty answer is never grounded",
			answers: []Answer{
				{Question: "q", Answer: "  "},
			},
			want: 0,
		},
		{
			name: "original prose citing a source with substance is grounded",
			answers: []Answer{
				{Question: "q", Answer: "Per US Const. amend. V, no person shall be deprived of life, liberty, or property, without due process of law."},
			},
			want: 1,
		},
		{
			name: "dangling source cite without substance is not grounded",
			answers: []Answer{
				{Question: "q", Answer: "See US Const. amend. XIV § 1. The moon is cheese."},
			},
			want: 0,
		},
		{
			name: "abbreviated verbatim quote via contiguous run",
			answers: []Answer{
				{Question: "q", Answer: "The rule: 'nor deny to any person within its jurisdiction the equal protection of the laws.'"},
			},
			want: 1,
		},
		{
			name: "invented citation is still caught",
			answers: []Answer{
				{Question: "q", Answer: "Article I, Section 9, Clause 2 says the sun is a motorcycle in June."},
			},
			want: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clauses := []hm.LawEntry{
				{ID: "111111111111", Source: "US Const. amend. XIV", Clause: equalProtection},
				{ID: "222222222222", Source: "US Const. amend. V", Clause: "nor be deprived of life, liberty, or property, without due process of law."},
				{ID: "333333333333", Source: "US Const. art. I § 9", Clause: "The Privilege of the Writ of Habeas Corpus shall not be suspended, unless when in Cases of Rebellion or Invasion the public Safety may require it."},
				{ID: "444444444444", Source: "Contract example §7.2", Clause: "Each party shall indemnify and hold harmless the other party against claims arising under this agreement."},
			}
			sc := ScoreGroundedness(tc.answers, clauses)
			if sc.Grounded != tc.want {
				t.Errorf("grounded = %d, want %d (ungrounded: %+v)", sc.Grounded, tc.want, sc.Ungrounded)
			}
			// The per-answer verdict used by the CLI must agree, so the
			// stamp on the screen never contradicts the scorecard.
			if len(tc.answers) > 0 {
				ga := GroundedAnswer(tc.answers[0], clauses)
				if ga != (tc.want == 1) {
					t.Errorf("GroundedAnswer = %v, want %v", ga, tc.want == 1)
				}
			}
		})
	}
}

func TestCiteSignatures(t *testing.T) {
	cases := []struct {
		text string
		want []string
	}{
		{"Per US Const. amend. V, due process applies.", []string{"amend.v"}},
		{"Article I, Section 9, Clause 2.", []string{"sec.9"}},
		{"Contract example §7.2 survives.", []string{"sec.7.2"}},
		{"US Const. amend. XIV § 1 and amend. XIV guard equal protection.", []string{"amend.xiv", "sec.1"}},
		{"", nil},
	}
	for _, tc := range cases {
		got := citeSignatures(tc.text)
		if !equalStrings(got, tc.want) {
			t.Errorf("citeSignatures(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
	// The clause Source itself must cast to the same slots the answer does,
	// so the adjudicator can compare like for like.
	if sig := provenanceSignatures("US Const. art. I § 9"); !containsString(sig, "sec.9") {
		t.Errorf("provenanceSignatures lost sec.9: %v", sig)
	}
}

func TestMisciteAdjudication(t *testing.T) {
	equalProtection := "No State shall make or enforce any law which shall abridge the privileges or immunities of citizens." +
		" nor deny to any person within its jurisdiction the equal protection of the laws."
	clauses := []hm.LawEntry{
		{ID: "111111111111", Source: "US Const. amend. XIV § 1", Clause: equalProtection},
		{ID: "222222222222", Source: "US Const. amend. V", Clause: "nor be deprived of life, liberty, or property, without due process of law."},
	}

	// Q5-style failure: echoes the equal-protection clause but points the
	// reader at Article I, Section 9.
	if w := Miscite(Answer{Answer: "According to the US Constitution, Article I, Section 9, Clause 2, \"No State shall...deny to any person within its jurisdiction the equal protection of the laws.\""}, clauses); w == "" {
		t.Error("expected MIS-CITE warning for art I § 9 vs equal protection substance")
	}

	// Correctly paired cite: amend V attributed to due-process text.
	if w := Miscite(Answer{Answer: "US Const. amend. V forbids depriving a person of life, liberty, or property without due process of law."}, clauses); w != "" {
		t.Errorf("spurious MIS-CITE warning: %q", w)
	}

	// Survey answer citing two provisions is not a miscite.
	if w := Miscite(Answer{Answer: "Both amend. XIV § 1 and the due-process rule guard persons within a State's jurisdiction."}, clauses); w != "" {
		t.Errorf("survey answer wrongly flagged: %q", w)
	}

	// Ungrounded (invented) answers carry no provenance, hence no adjudication.
	if w := Miscite(Answer{Answer: "Article I, Section 9 says the moon is a motorcycle."}, clauses); w != "" {
		t.Errorf("ungrounded answer wrongly adjudicated: %q", w)
	}
}

func TestGateVerdict(t *testing.T) {
	cases := []struct {
		name     string
		sc       Scorecard
		wantPass bool
	}{
		{"all probes grounded", Scorecard{Total: 2, Grounded: 2}, true},
		{"empty regime refuses", Scorecard{Total: 0, Grounded: 0}, false},
		{"single confabulation refuses", Scorecard{Total: 2, Grounded: 1}, false},
		{"single probe that grounds admits", Scorecard{Total: 1, Grounded: 1}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := GateVerdict(tc.sc); got != tc.wantPass {
				t.Errorf("GateVerdict(%+v) = %v, want %v", tc.sc, got, tc.wantPass)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestBudgetDefaultsAndEnv(t *testing.T) {
	if got := gateProbeBudget(); got != defaultGateProbeBudget {
		t.Fatalf("gate budget default = %v, want %v", got, defaultGateProbeBudget)
	}
	if got := answerBudget(); got != defaultAnswerBudget {
		t.Fatalf("answer budget default = %v, want %v", got, defaultAnswerBudget)
	}

	t.Setenv("HIVEMIND_GATE_BUDGET", "90s")
	if got := gateProbeBudget(); got != 90*time.Second {
		t.Fatalf("gate budget override = %v, want 90s", got)
	}

	t.Setenv("HIVEMIND_ANSWER_BUDGET", "45s")
	if got := answerBudget(); got != 45*time.Second {
		t.Fatalf("answer budget override = %v, want 45s", got)
	}

	// A malformed override must fall back, never panic or zero the bound.
	t.Setenv("HIVEMIND_GATE_BUDGET", "soon")
	if got := gateProbeBudget(); got != defaultGateProbeBudget {
		t.Fatalf("malformed override must fall back, got %v", got)
	}
}
