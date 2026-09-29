package language

import (
	"strings"
	"testing"
	"time"
)

func baseCtx() PromptContext {
	return PromptContext{
		Name:      "Alpha",
		Stress:    0.5,
		Calm:      0.4,
		Goal:      "answer the doubt",
		Timestamp: time.Unix(1_700_000_000, 0),
		Law: []string{
			"[ab12cd34|US Const. amend. V|due process] No person shall be deprived of life, liberty, or property, without due process of law.",
			"[ef56gh78|US Const. amend. I|religion] Congress shall make no law respecting an establishment of religion.",
		},
	}
}

func TestBuildPromptRendersLawBlockCitable(t *testing.T) {
	pc := baseCtx()
	out := BuildPrompt(pc)

	for _, want := range []string{
		"Provisioned knowledge retrieved for this moment",
		"[ab12cd34|US Const. amend. V|due process]",
		"[ef56gh78|US Const. amend. I|religion]",
		"you MAY quote it verbatim where relevant",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("BuildPrompt missing %q\n---\n%s", want, out)
		}
	}
	// The law block is a system block that opens the citation floor, not a
	// contact between the model's felt-state request and the chat history:
	// "<|end|>" must close it before any history message begins.
	if !strings.Contains(out, "<|end|>") {
		t.Fatalf("prompt lacks the block terminator\n---\n%s", out)
	}

	// Reply prompts ground answers the same way.
	rep := BuildReplyPrompt(baseCtx(), "peer7", "is due process suspended?")
	if !strings.Contains(rep, "[ab12cd34|US Const. amend. V|due process]") {
		t.Fatalf("BuildReplyPrompt failed to render the law block\n---\n%s", rep)
	}
}

func TestBuildPromptWithoutLawStaysClean(t *testing.T) {
	pc := baseCtx()
	pc.Law = nil
	out := BuildPrompt(pc)
	if strings.Contains(out, "Provisioned knowledge") {
		t.Fatalf("law-less prompt still renders the law block\n---\n%s", out)
	}
}
