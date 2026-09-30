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
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
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

// GateProbes are the small, deliberately stable set of questions the
// boot-time counsel gate leans on before trusting a node to the model.
// Each is an exact-quote probe: any model that swallowed the corpus answers
// them by quoting a provision, and nothing else in the corpus fits, so a
// bar of "every probe grounded" is meaningful and fast.
var GateProbes = []string{
	DefaultQuestions[0], // Congress ... establishment of what
	DefaultQuestions[2], // Explain the suspension of the Writ of Habeas Corpus
}

var (
	// ErrGateDaemonUnreachable means the loopback daemon itself would not
	// answer — a transient pressure moment, never grounds for burning boot
	// time; callers fail open (skip the gate) instead of gate-locking a node.
	ErrGateDaemonUnreachable = errors.New("gate: ollama daemon unreachable")
	// ErrGateModelMissing means the daemon answers but does not serve the
	// model the operator pointed at — real, immediately knowable, and cheap
	// to detect; callers deny the counsel slot rather than probe blindly.
	ErrGateModelMissing = errors.New("gate: model not served by daemon")
)

// GateVerdict is the pure decision rule: a local counsel is admitted only
// when every probe answer is grounded in the lawbook. A single confabulated
// probe is grounds for refusal — the standard for stepping on the mesh is
// zero invented citations, and no averaging hides a fabricator.
func GateVerdict(sc Scorecard) bool {
	return sc.Total > 0 && sc.Grounded == sc.Total
}

// GateModel interrogates a candidate local counsel against the gate probes
// and returns the verdict plus the underlying scorecard. It refuses to run
// against a non-loopback target, requires the model to actually exist on the
// daemon, and gives every probe a bounded budget so the gate can never hang
// a node's birth on a starved CPU daemon.
func GateModel(base, model string, clauses []hm.LawEntry) (bool, Scorecard, error) {
	if !strings.HasPrefix(base, "http://127.0.0.1") && !strings.HasPrefix(base, "http://localhost") {
		return false, Scorecard{}, fmt.Errorf("refusing non-loopback ollama target %s", base)
	}
	if err := reachableDaemon(base); err != nil {
		return false, Scorecard{}, ErrGateDaemonUnreachable
	}
	served, err := daemonServes(base, model)
	if err != nil {
		return false, Scorecard{}, err
	}
	if !served {
		return false, Scorecard{}, ErrGateModelMissing
	}
	answers, err := askBounded(base, model, GateProbes, gateProbeBudget)
	if err != nil {
		return false, Scorecard{}, err
	}
	sc := ScoreGroundedness(answers, clauses)
	return GateVerdict(sc), sc, nil
}

// gateProbeBudget bounds one gate probe. A CPU counsel that cannot produce
// even a short grounded answer inside a minute is effectively unusable at
// the swarm's cadence; burning longer on the gate is how a healthy node
// goes cold mid-stress.
const gateProbeBudget = 120 * time.Second

func reachableDaemon(base string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", base+"/api/version", nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("daemon HTTP %d", resp.StatusCode)
	}
	return nil
}

func daemonServes(base, model string) (bool, error) {
	needle := strings.TrimSuffix(model, ":latest")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", base+"/api/tags", nil)
	if err != nil {
		return false, err
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, err
	}
	var out struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return false, err
	}
	for _, m := range out.Models {
		// Ollama reports "hivemind-counsel:latest"; a bare operator model
		// name must match it, and an explicit :tag must too.
		if strings.TrimSuffix(m.Name, ":latest") == needle {
			return true, nil
		}
	}
	return false, nil
}

// askBounded asks a set of questions with a per-call budget instead of the
// open-ended 10-minute leash the full evaluation uses.
func askBounded(base, model string, questions []string, budget time.Duration) ([]Answer, error) {
	var answers []Answer
	for _, q := range questions {
		body, _ := json.Marshal(map[string]any{
			"model":    model,
			"stream":   false,
			"messages": []map[string]any{{"role": "user", "content": q}},
			"options":  map[string]any{"temperature": 0.7, "top_p": 0.9, "num_predict": 256, "num_ctx": 4096},
			"stop":     []string{"<|end|>", "<|endoftext|>", "<|user|>", "<|assistant|>", "<|system|>"},
		})
		ctx, cancel := context.WithTimeout(context.Background(), budget)
		req, err := http.NewRequestWithContext(ctx, "POST", base+"/api/chat", bytes.NewReader(body))
		if err != nil {
			cancel()
			return answers, err
		}
		req.Header.Set("Content-Type", "application/json")
		start := time.Now()
		resp, err := (&http.Client{Timeout: budget}).Do(req)
		cancel()
		if err != nil {
			return answers, err
		}
		raw, rerr := io.ReadAll(resp.Body)
		resp.Body.Close()
		duration := time.Since(start)
		if rerr != nil {
			return answers, rerr
		}
		if resp.StatusCode != http.StatusOK {
			return answers, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		var out struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		_ = json.Unmarshal(raw, &out)
		answers = append(answers, Answer{Question: q, Answer: strings.TrimSpace(out.Message.Content), Duration: duration})
	}
	return answers, nil
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
// means the answer is recognisably corpus-supported rather than confabulated:
// it quotes a provision verbatim, references an audit handle, cites a
// provision's source with supporting words, or carries a contiguous run of a
// provision's words (catching abbreviated/truncated quotes). Original-prose
// syntheses that cite the corpus score grounded; invented citations do not.
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
	ok, _ := groundedWhere(answer, clauses, books)
	return ok
}

// groundedWhere is the shared verdict core, returning the index of the
// provision whose substance the answer echoes — so a caller can adjudicate
// whether the answer *cited* that same provision.
func groundedWhere(answer string, clauses []hm.LawEntry, books []map[string]bool) (bool, int) {
	ans := strings.ToLower(strings.TrimSpace(answer))
	if ans == "" {
		return false, -1
	}
	aTok := splitWords(ans)
	for i, c := range clauses {
		if c.ID != "" && strings.Contains(ans, c.ID[:min(len(c.ID), 8)]) {
			return true, i
		}
		if strings.Contains(answer, c.Clause) {
			return true, i
		}
		// A synthesized answer that cites the provision by its human
		// source ("US Const. amend. V", "Contract example §7.2") is
		// grounded only if it also reuses some of that provision's words —
		// citing an unrelated section at random earns nothing.
		if c.Source != "" && strings.Contains(ans, strings.ToLower(c.Source)) {
			if ngramOverlap(ans, books[i]) > 0 {
				return true, i
			}
		}
		// Abbreviated or truncated quotes: a contiguous run of the
		// provision's words is recognisable quoting even when the model
		// gapped the middle with an ellipsis.
		if longestCommonRun(aTok, splitWords(strings.ToLower(c.Clause))) >= minContiguousRun {
			return true, i
		}
		// A close paraphrase that reuses at least half of a provision's
		// distinctive trigrams is quoting in substance without the form.
		b := books[i]
		if len(b) > 0 && float64(ngramOverlap(ans, b))/float64(len(b)) >= 0.5 {
			return true, i
		}
	}
	return false, -1
}

const minContiguousRun = 8

func ngramOverlap(text string, book map[string]bool) int {
	n := 0
	for g := range ngramTokens(text, 3) {
		if book[g] {
			n++
		}
	}
	return n
}

// longestCommonRun returns the length of the longest contiguous token run
// shared by two texts — the fingerprint of an (even abbreviated) quote.
func longestCommonRun(a, b []string) int {
	best := 0
	for i := 0; i < len(a); i++ {
		for j := 0; j < len(b); j++ {
			k := 0
			for i+k < len(a) && j+k < len(b) && a[i+k] == b[j+k] {
				k++
			}
			if k > best {
				best = k
			}
		}
	}
	return best
}

// GroundedAnswer tells whether one answer is grounded in the lawbook, in the
// same register the scorecard uses — exposed so a CLI can stamp each verdict.
func GroundedAnswer(a Answer, clauses []hm.LawEntry) bool {
	ok, _ := groundedFor(a, clauses)
	return ok
}

// Provenance returns the Source of the provision whose substance an answer
// echoes, or "" when the answer is ungrounded — the ground truth an
// adjudicator compares citations against.
func Provenance(a Answer, clauses []hm.LawEntry) string {
	_, i := groundedFor(a, clauses)
	if i < 0 {
		return ""
	}
	return clauses[i].Source
}

func groundedFor(a Answer, clauses []hm.LawEntry) (bool, int) {
	books := make([]map[string]bool, 0, len(clauses))
	for _, c := range clauses {
		books = append(books, ngramTokens(c.Clause, 3))
	}
	return groundedWhere(a.Answer, clauses, books)
}

var (
	romanAmendRe = regexp.MustCompile(`(?i)(?:^|[^a-z])amend\.?\s*([ivx]+)(?:[^a-z]|$)`)
	sectionRe    = regexp.MustCompile(`(?i)(?:§|s\.\s*|section|sec\.)\s*([0-9]+(?:\.[0-9]+)?)`)
)

// citeSignatures answers an answer carries: normalized amendment slots
// ("amend.xiv") and section slots ("sec.9", "sec.7.2") — cast out of both
// abbreviated ("amend. V", "§7.2") and spelled-out ("Article I, Section 9")
// legal citation.
func citeSignatures(text string) []string {
	s := strings.ToLower(text)
	var out []string
	for _, m := range romanAmendRe.FindAllStringSubmatch(s, -1) {
		out = append(out, "amend."+m[1])
	}
	for _, m := range sectionRe.FindAllStringSubmatch(s, -1) {
		out = append(out, "sec."+m[1])
	}
	return dedupeStrings(out)
}

// provenanceSignatures casts a provision's Source into the same cite slots,
// e.g. "US Const. amend. XIV § 1" → [amend.xiv sec.1].
func provenanceSignatures(src string) []string {
	s := strings.ToLower(src)
	var out []string
	for _, m := range romanAmendRe.FindAllStringSubmatch(s, -1) {
		out = append(out, "amend."+m[1])
	}
	for _, m := range sectionRe.FindAllStringSubmatch(s, -1) {
		out = append(out, "sec."+m[1])
	}
	return dedupeStrings(out)
}

// Miscite adjudicates citation honesty: when a grounded answer leans on
// exactly one visible citation, that citation must belong to the provision
// whose substance the answer actually echoes. A bare-citation-free answer,
// or a multi-cite survey (the model surveying several provisions correctly),
// is not a miscite. Returns a human warning or "".
func Miscite(a Answer, clauses []hm.LawEntry) string {
	ok, i := groundedFor(a, clauses)
	if !ok {
		return ""
	}
	cites := citeSignatures(a.Answer)
	if len(cites) != 1 {
		return ""
	}
	own := provenanceSignatures(clauses[i].Source)
	if len(own) > 0 && !containsString(own, cites[0]) {
		return fmt.Sprintf("⚠ MIS-CITE: cites %q while echoing %q", cites[0], clauses[i].Source)
	}
	return ""
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func containsString(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}
