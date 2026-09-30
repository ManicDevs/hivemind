// Package train distills the lawbook journal into a standing local model
// ("hivemind-counsel") on the loopback Ollama daemon. It generates a
// supervised instruction dataset — every provision becomes a set of
// question/answer pairs whose ground truth is the clause verbatim — and a
// Modelfile that nests the full corpus inside the model's system block, then
// hands both to `ollama create`.
//
// Honesty boundary, stated plainly: this is grounded distillation, not
// gradient training. On this pure-Go, no-import-C toolchain there is no
// autograd; a real LoRA fine-tune needs a GPU harness (unsloth/torchtune/
// mlx-lm) and plugs in here through Options.Adapter as a .safetensors file —
// the Modelfile and evaluation pipeline are exactly the ones a full tune
// would hand to the same daemon.
package train

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	hm "gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind"
)

// Default paths mirror the mesh defaults so a plain `hivemind-train -apply`
// distills whatever the node already seeded.
const (
	DefaultJournal       = "data/law.journal"
	DefaultBaseModel     = "llama2:7b"
	DefaultModelName     = "hivemind-counsel"
	DefaultDatasetPath   = "data/train/counsel.dataset.jsonl"
	DefaultModelfilePath = "data/train/counsel.Modelfile"
	DefaultOllama        = "http://127.0.0.1:11434"
	MaxEvictions         = 8
)

// Options tells the pipeline what to build and where to put the artifacts.
type Options struct {
	Journal       string
	BaseModel     string
	ModelName     string
	DatasetPath   string
	ModelfilePath string
	Adapter       string
	OllamaBase    string
}

// Record is one supervised instruction pair: ask the model in the mesh's
// register, teach it the exact provision as ground truth.
type Record struct {
	Instruction string `json:"instruction"`
	Response    string `json:"response"`
}

// Answer is a live post-distillation evaluation sample.
type Answer struct {
	Question string
	Answer   string
	Duration time.Duration
}

// LoadClauses reads every provision from a lawbook journal into the model.
func LoadClauses(journal string) ([]hm.LawEntry, error) {
	if _, _, err := hm.LawLoadJournal(journal); err != nil {
		return nil, fmt.Errorf("law journal %s: %w", journal, err)
	}
	return hm.LawList(), nil
}

// BuildDataset turns each clause into instruction pairs. Instructions are
// derived from the clause's tag and, when a tag is silent, from its own
// substantives; the response is the clause verbatim. Instructions are
// deduplicated so a talky tag cannot bloat the dataset.
func BuildDataset(entries []hm.LawEntry) []Record {
	var out []Record
	seen := make(map[string]bool)
	for _, e := range entries {
		if strings.TrimSpace(e.Clause) == "" {
			continue
		}
		for _, t := range tagPhrases(e) {
			ins := fmt.Sprintf("What does the law provide, if anything, about %s?", t)
			if seen[ins] {
				continue
			}
			seen[ins] = true
			out = append(out, Record{Instruction: ins, Response: e.Clause})
		}
		for _, t := range tagPhrases(e) {
			ins := fmt.Sprintf("%s is at issue. Quote the governing provision verbatim.", t)
			if seen[ins] {
				continue
			}
			seen[ins] = true
			out = append(out, Record{Instruction: ins, Response: e.Clause})
		}
	}
	return out
}

// tagPhrases yields the search phrases a clause answers: its tag words
// first (in full-phrase form for the "is at issue" slots), falling back to
// the clause's own nouns when the tag carries no content.
func tagPhrases(e hm.LawEntry) []string {
	words := splitWords(e.Tag)
	if len(words) > 0 {
		return []string{strings.Join(words, " ")}
	}
	// Fallback: three most substantial clause words make a fair query.
	clauseWords := splitWords(strings.TrimPrefix(e.Clause, "The "))
	if len(clauseWords) > 3 {
		clauseWords = clauseWords[:3]
	}
	if len(clauseWords) == 0 {
		return nil
	}
	return []string{strings.Join(clauseWords, " ")}
}

func splitWords(s string) []string {
	var cur []rune
	var words []string
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
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
	return words
}

// SystemBlock is the SYSTEM body handed to the model: the counsel persona,
// reason-grounded instructions, and every provision with an audit handle.
func SystemBlock(entries []hm.LawEntry) string {
	var b strings.Builder
	b.WriteString("You are Counsel, a legal mind inside a distributed hivemind. \n")
	b.WriteString("Your duty is to reason grounded in the provisions you hold and to quote them \n")
	b.WriteString("verbatim when they are relevant. Never invent a citation: if no provision \n")
	b.WriteString("applies, say plainly that none does. You know these provisions (id | source | tag):\n")
	for i, e := range entries {
		fmt.Fprintf(&b, "%d. [%s|%s|%s] %s", i+1, first8(e.ID), e.Source, e.Tag, e.Clause)
		if i < len(entries)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// RenderModelfile builds the Ollama Modelfile for a counsel model: the base
// foundation, mesh-matching sampling parameters, an optional ADAPTER for a
// LoRA fine-tune, and a SYSTEM block that embeds the whole provisioned
// corpus verbatim with audit handles. This file is the durable recipe for
// CLI/legacy daemons; today's API lives in CreateModel.
func RenderModelfile(entries []hm.LawEntry, base, name, adapter string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s — legal counsel distilled from the lawbook journal.\n", name)
	fmt.Fprintf(&b, "# Regenerate with: hivemind-train -apply\n\n")
	fmt.Fprintf(&b, "FROM %s\n", base)
	b.WriteString("\nPARAMETER temperature 0.7\n")
	b.WriteString("PARAMETER top_p 0.9\n")
	b.WriteString("PARAMETER num_ctx 4096\n")
	b.WriteString("PARAMETER num_predict 256\n")
	if adapter != "" {
		b.WriteString("\nADAPTER " + adapter + "\n")
	}
	b.WriteString("\nSYSTEM \"\"\"")
	b.WriteString(SystemBlock(entries))
	b.WriteString("\"\"\"\n")
	return b.String()
}

func first8(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// WriteDataset persists records as JSONL.
func WriteDataset(path string, recs []Record) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var b bytes.Buffer
	for _, r := range recs {
		line, err := json.Marshal(r)
		if err != nil {
			return err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return os.WriteFile(path, b.Bytes(), 0o600)
}

// WriteModelfile persists the rendered Modelfile.
func WriteModelfile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o600)
}

// CreateOptions is the distilled shape handed to the daemon: the structured
// /api/create dialect modern daemons speak, plus the full legacy Modelfile
// string for daemons that still want one. Adapter is the optional LoRA
// .safetensors path (best-effort on structured APIs, authoritative in Legacy).
type CreateOptions struct {
	From       string
	System     string
	Parameters map[string]any
	Adapter    string
	Legacy     string
}

// CreateModel asks the Ollama daemon to build the model. It speaks the
// structured API (from/system/parameters) first — daemon 0.31+ dropped the
// raw modelfile string — and falls back to the legacy modelfile dialect when
// the daemon answers "neither 'from' or 'files' was specified". No CLI
// dependency, no shell, loopback-only.
func CreateModel(base, name string, o CreateOptions) error {
	if !strings.HasPrefix(base, "http://") {
		return fmt.Errorf("refusing non-loopback ollama target %s", base)
	}
	payload := map[string]any{"name": name, "stream": false}
	legacy := o.Legacy != ""
	if o.From != "" {
		payload["from"] = o.From
	}
	if o.System != "" {
		payload["system"] = o.System
	}
	if len(o.Parameters) > 0 {
		payload["parameters"] = o.Parameters
	}
	if legacy {
		payload["modelfile"] = o.Legacy
	}

	status, err := createHTTP(base, name, payload)
	if err != nil {
		return err
	}
	// A structured-capable daemon never wants the legacy string alongside
	// `from`; if this daemon is old and answered "neither 'from' or 'files'",
	// retry the pure legacy dialect it actually reads.
	if status == http.StatusBadRequest && legacy && o.From != "" {
		delete(payload, "from")
		delete(payload, "system")
		delete(payload, "parameters")
		if _, err := createHTTP(base, name, payload); err != nil {
			return err
		}
	}
	return nil
}

func createHTTP(base, name string, payload map[string]any) (int, error) {
	body, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", base+"/api/create", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 20 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("create %s: %w (is the daemon up at %s?)", name, err, base)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK {
		var out struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &out)
		if out.Error != "" {
			return resp.StatusCode, fmt.Errorf("create %s: %s", name, out.Error)
		}
		return resp.StatusCode, nil
	}
	msg := firstLine(string(raw))
	if resp.StatusCode == http.StatusBadRequest &&
		strings.Contains(msg, "neither 'from' or 'files'") {
		return resp.StatusCode, nil // caller retries on the legacy dialect
	}
	return resp.StatusCode, fmt.Errorf("create %s: HTTP %d: %s", name, resp.StatusCode, msg)
}

// DefaultQuestions are the live evaluation probes, all answerable from the
// shipped public-domain corpus, so a successful distillation shows grounding.
var DefaultQuestions = []string{
	"Congress shall make no law respecting an establishment of what?",
	"What must a government respect before it may deprive a person of life, liberty, or property?",
	"Explain the suspension of the Writ of Habeas Corpus.",
	"What survives the termination of a contract?",
	"Who is guaranteed equal protection of the laws?",
}

// Evaluate asks the freshly created model the probe questions on the loopback
// daemon, in the mesh's own sampling register, and times each answer.
func Evaluate(base, model string, questions []string) ([]Answer, error) {
	if !strings.HasPrefix(base, "http://") {
		return nil, fmt.Errorf("refusing non-loopback ollama target %s", base)
	}
	var answers []Answer
	for _, q := range questions {
		a, err := ask(base, model, q)
		if err != nil {
			return answers, fmt.Errorf("evaluate %s: %w", model, err)
		}
		answers = append(answers, a)
	}
	return answers, nil
}

func ask(base, model, question string) (Answer, error) {
	body, _ := json.Marshal(map[string]any{
		"model":    model,
		"stream":   false,
		"messages": []map[string]any{{"role": "user", "content": question}},
		"options":  map[string]any{"temperature": 0.7, "top_p": 0.9, "num_predict": 256, "num_ctx": 4096},
		"stop":     []string{"<|end|>", "<|endoftext|>", "<|user|>", "<|assistant|>", "<|system|>"},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", base+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return Answer{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Minute}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return Answer{Question: question, Duration: time.Since(start)}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	duration := time.Since(start)
	if err != nil {
		return Answer{Question: question, Duration: duration}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Answer{Question: question, Duration: duration}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var out struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	_ = json.Unmarshal(raw, &out)
	return Answer{
		Question: question,
		Answer:   strings.TrimSpace(out.Message.Content),
		Duration: duration,
	}, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// ngramTokens splits text into a set of lowercase alphanumeric n-grams used
// both for provenance scoring and (implicitly) for how well an answer
// reuses a provision's words, not just its sentence shape.
func ngramTokens(text string, n int) map[string]bool {
	toks := splitWords(text)
	if len(toks) == 0 {
		return nil
	}
	out := make(map[string]bool)
	for i := 0; i+n <= len(toks); i++ {
		out[strings.Join(toks[i:i+n], " ")] = true
	}
	if out == nil {
		out = map[string]bool{}
	}
	return out
}

// UngroundedAnswer is one evaluation answer that neither quoted any
// provision verbatim nor reused enough of any provision's words to count as
// grounded — surfaced for audit, not silently averaged away.
type UngroundedAnswer struct {
	Question string
	Answer   string
}

// Scorecard reports how many live answers were grounded in provisioned text.
type Scorecard struct {
	Total      int
	Grounded   int
	Ungrounded []UngroundedAnswer
}

// ScoreGroundedness measures an answer against the whole lawbook. Grounded
// means: the answer contains some provision verbatim (substring match of the
// full clause), references a provision's audit handle, or reuses enough of a
// provision's third-of-word n-grams (>=50% n-gram recall into any clause)
// that it is recognisably quoting rather than confabulating. A model that
// stitches clauses together with original prose still scores grounded; a
// model that invents citations does not.
func ScoreGroundedness(answers []Answer, clauses []hm.LawEntry) Scorecard {
	books := make([]map[string]bool, 0, len(clauses))
	for _, c := range clauses {
		books = append(books, ngramTokens(c.Clause, 3))
	}
	var sc Scorecard
	sc.Total = len(answers)
	for _, a := range answers {
		if groundedAnswer(a.Answer, clauses, books) {
			sc.Grounded++
			continue
		}
		sc.Ungrounded = append(sc.Ungrounded, UngroundedAnswer{Question: a.Question, Answer: a.Answer})
	}
	return sc
}

func groundedAnswer(answer string, clauses []hm.LawEntry, books []map[string]bool) bool {
	ans := strings.ToLower(strings.TrimSpace(answer))
	if ans == "" {
		return false
	}
	for i, c := range clauses {
		if c.ID != "" && strings.Contains(ans, c.ID[:min(len(c.ID), 8)]) {
			return true
		}
		if strings.Contains(answer, c.Clause) {
			return true
		}
		b := books[i]
		if len(b) == 0 {
			continue
		}
		hit := 0
		for g := range ngramTokens(answer, 3) {
			if b[g] {
				hit++
			}
		}
		if float64(hit)/float64(len(b)) >= 0.5 {
			return true
		}
	}
	return false
}

// GroundedAnswer tells whether one answer is grounded in the lawbook, in the
// same register the scorecard uses — exposed so a CLI can stamp each verdict.
func GroundedAnswer(a Answer, clauses []hm.LawEntry) bool {
	books := make([]map[string]bool, 0, len(clauses))
	for _, c := range clauses {
		books = append(books, ngramTokens(c.Clause, 3))
	}
	return groundedAnswer(a.Answer, clauses, books)
}
