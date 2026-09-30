package train

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clauses := []hm.LawEntry{
				{ID: "111111111111", Source: "US Const. amend. XIV", Clause: equalProtection},
				{ID: "222222222222", Source: "US Const. amend. V", Clause: "nor be deprived of life, liberty, or property, without due process of law."},
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
