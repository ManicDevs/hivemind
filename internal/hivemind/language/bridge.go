package language

import (
	"context"
	"strings"
	"time"
)

// LanguageBridge connects the interoceptive system to language generation
type LanguageBridge struct {
	model       LanguageModel
	enabled     bool
	history     []Message
	maxHistory  int
	lastSpoke   time.Time
	minInterval time.Duration
}

func NewLanguageBridge(model LanguageModel, minInterval time.Duration) *LanguageBridge {
	return &LanguageBridge{
		model:       model,
		enabled:     model != nil,
		history:     make([]Message, 0, 10),
		maxHistory:  10,
		minInterval: minInterval,
	}
}

func (b *LanguageBridge) Enabled() bool {
	return b.enabled && b.model != nil
}

func (b *LanguageBridge) SetModel(model LanguageModel) {
	b.model = model
	b.enabled = model != nil
}

// GenerateThought produces a language thought from interoceptive state and goal
func (b *LanguageBridge) GenerateThought(ctx PromptContext) (string, error) {
	if !b.Enabled() {
		return "", nil // silently skip if no model
	}

	// Rate limiting
	if time.Since(b.lastSpoke) < b.minInterval {
		return "", nil
	}

	// Build prompt from interoceptive state
	prompt := BuildPrompt(ctx)

	// Generate
	response, err := b.model.Generate(context.Background(), prompt)
	if err != nil {
		return "", err
	}

	// Clean up response
	response = cleanCompletion(response)
	if response == "" {
		return "", nil
	}

	// Add to history
	b.addToHistory("assistant", response)

	b.lastSpoke = time.Now()
	return response, nil
}

// GenerateReply answers a remote sibling's broadcast thought in this mind's
// own voice. Unlike GenerateThought it does NOT consume the scheduled
// thought-rate budget (minInterval belongs to statements, not reactions),
// and its words join the same historical context as thoughts so dialogue
// and soliloquy stay in one voice across calls.
func (b *LanguageBridge) GenerateReply(ctx PromptContext, peerShort, peerThought string) (string, error) {
	if !b.Enabled() {
		return "", nil
	}

	prompt := BuildReplyPrompt(ctx, peerShort, peerThought)

	response, err := b.model.Generate(context.Background(), prompt)
	if err != nil {
		return "", err
	}

	response = cleanCompletion(response)
	if response == "" {
		return "", nil
	}

	b.addToHistory("assistant", response)
	return response, nil
}

func (b *LanguageBridge) addToHistory(role, content string) {
	b.history = append(b.history, Message{
		Role:    role,
		Content: content,
		Time:    time.Now(),
	})
	if len(b.history) > b.maxHistory {
		b.history = b.history[len(b.history)-b.maxHistory:]
	}
}

func (b *LanguageBridge) GetHistory() []Message {
	return b.history
}

func (b *LanguageBridge) Close() error {
	if b.model != nil {
		return b.model.Close()
	}
	return nil
}

// cleanCompletion trims whitespace, strips model stop-tokens, and unwraps
// a response that arrived fully quoted (some endpoints wrap replies in
// quotation marks as a single outer pair). Only a wrapping pair is removed,
// so a genuine "word" mid-sentence is untouched.
func cleanCompletion(s string) string {
	s = strings.TrimSpace(s)
	for _, stop := range []string{"<|end|>", "<|endoftext|>", "<|user|>", "<|assistant|>", "<|system|>"} {
		s = strings.TrimSuffix(s, stop)
	}
	s = strings.TrimSpace(s)
	if wrapped(s, '"', '"') || wrapped(s, '\u201c', '\u201d') {
		s = strings.TrimSpace(wrappedContent(s))
	}
	return s
}

// wrapped reports whether s is a single pair of quote runes enclosing the
// whole string. rune-aware: multibyte smart quotes are handled correctly.
func wrapped(s string, open, close rune) bool {
	if len(s) < 2 {
		return false
	}
	rs := []rune(s)
	return rs[0] == open && rs[len(rs)-1] == close
}

// wrappedContent strips one leading and one trailing rune from s (the
// wrapping quote pair).
func wrappedContent(s string) string {
	rs := []rune(s)
	return string(rs[1 : len(rs)-1])
}

// FormatInteroceptionForLog creates a human-readable summary of interoceptive state
