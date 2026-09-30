// hivemind-eval scores how grounded a trained counsel model actually is:
// asks the probe questions live, then checks every answer against the whole
// lawbook — exact citation, audit-handle reference, or n-gram reuse of a
// provision. It is the counterweight to hivemind-train: creation without
// verification is just a bigger hallucinator.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"gitlab.torproject.org/cerberus-droid/hivemind/internal/train"
)

func main() {
	os.Exit(run())
}

func run() int {
	var journal, model, ollama, probe string
	fs := flag.NewFlagSet("hivemind-eval", flag.ExitOnError)
	fs.StringVar(&journal, "journal", train.DefaultJournal, "lawbook journal that grounds the answers")
	fs.StringVar(&model, "model", train.DefaultModelName, "model to interrogate (default hivemind-counsel)")
	fs.StringVar(&ollama, "ollama", train.DefaultOllama, "loopback Ollama daemon")
	fs.StringVar(&probe, "probes", "", "comma-separated probe questions (default train.DefaultQuestions)")
	_ = fs.Parse(os.Args[1:])

	clauses, err := train.LoadClauses(journal)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  %v\n", err)
		return 1
	}
	if len(clauses) == 0 {
		fmt.Fprintf(os.Stderr, "⚠️  journal %s holds no provisions — seed it first (hivemind seed).\n", journal)
		return 2
	}

	questions := train.DefaultQuestions
	if probe != "" {
		questions = splitCSV(probe)
	}

	fmt.Printf("⚖️  [EVAL] interrogating %s against %d provisions from %s\n", model, len(clauses), journal)
	// Ask in the streaming register: each answer prints the moment it lands,
	// so a slow CPU daemon never looks frozen for ten minutes.
	var answers []train.Answer
	answered := 0
	answers, err = train.EvaluateStreaming(ollama, model, questions, func(a train.Answer) {
		answered++
		fmt.Printf(" Q%d (%s)\n", answered, a.Duration.Round(100_000_000))
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  evaluation failed: %v\n", err)
		fmt.Fprintf(os.Stderr, "   (run  hivemind-train -apply  first, or check the daemon on %s)\n", ollama)
		return 1
	}

	score := train.ScoreGroundedness(answers, clauses)
	misciteCount := 0
	for i, a := range answers {
		mark := "✗"
		if train.GroundedAnswer(a, clauses) {
			mark = "✓"
		}
		fmt.Printf("\n %s Q%d: %s\n", mark, i+1, a.Question)
		fmt.Printf("   A: %s\n   (%s)\n", a.Answer, a.Duration.Round(100_000_000))
		if w := train.Miscite(a, clauses); w != "" {
			misciteCount++
			fmt.Printf("   %s\n", w)
		}
	}

	fmt.Printf("\n📊 [EVAL] grounded %d/%d (%.0f%%)\n",
		score.Grounded, score.Total, pct(score.Grounded, score.Total))
	if misciteCount > 0 {
		fmt.Printf("   ⚠ %d grounded answer(s) carry a citation that contradicts the text they echo\n", misciteCount)
	}
	if len(score.Ungrounded) > 0 {
		fmt.Printf("   ungrounded answers to audit:\n")
		for _, u := range score.Ungrounded {
			fmt.Printf("   - %s\n", clip(u.Answer, 120))
		}
	}
	if score.Grounded == 0 && score.Total > 0 {
		fmt.Printf("   ✗ nothing grounded: refuse to run the mesh on %s.\n", model)
		return 1
	}
	return 0
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func pct(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return 100 * float64(n) / float64(total)
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
