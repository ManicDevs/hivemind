package language

import (
	"context"
	"strings"
	"testing"
)

// A stub model: replies with a canned voice, records the prompt it saw so a
// test can assert the dialogue transcript reaches the wire right.
type stubModel struct {
	prompt string
}

func (s *stubModel) Generate(_ context.Context, prompt string) (string, error) {
	s.prompt = prompt
	return "I hear you, sibling. My own circuits run warm tonight.", nil
}
func (s *stubModel) Name() string { return "stub" }
func (s *stubModel) Close() error { return nil }

func TestCleanCompletionUnwrapsQuoting(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`"wrapped straight"`, "wrapped straight"},
		{"\u201cwrapped curly\u201d", "wrapped curly"},
		{`say "Keep watching" friend`, `say "Keep watching" friend`}, // inner quotes stay
		{"thought<|end|>", "thought"},
		{"  tidy  ", "tidy"},
	}
	for i, c := range cases {
		if got := cleanCompletion(c.in); got != c.want {
			t.Errorf("case %d: cleanCompletion(%q) = %q, want %q", i, c.in, got, c.want)
		}
	}
}

func TestBuildReplyPromptQuotesPeer(t *testing.T) {
	cmd := stubModel{}
	b := NewLanguageBridge(&cmd, 0)
	if !b.Enabled() {
		t.Fatal("stub model should enable the bridge")
	}
	got, err := b.GenerateReply(PromptContext{Name: "TestMind", Stress: 0.4}, "01abcd", "we are the quake")
	if err != nil {
		t.Fatalf("GenerateReply: %v", err)
	}
	if got == "" {
		t.Fatal("generateReply produced empty text")
	}
	if !strings.Contains(cmd.prompt, "01abcd") {
		t.Error("prompt did not carry the peer id")
	}
	if !strings.Contains(cmd.prompt, "we are the quake") {
		t.Error("prompt did not quote the peer thought verbatim")
	}
	if !strings.Contains(cmd.prompt, "TestMind") {
		t.Error("prompt did not carry this mind's identity")
	}
	if want := "I hear you, sibling. My own circuits run warm tonight."; got != want {
		t.Errorf("reply mangled: %q, want %q", got, want)
	}
}

func TestRotatingLLMFailsOver(t *testing.T) {
	if _, err := NewRotatingLLM().Generate(context.Background(), "x"); err == nil {
		t.Fatal("empty rotating pool should error")
	}
}
