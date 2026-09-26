package language

import (
	"context"
	"fmt"
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
	response = strings.TrimSpace(response)
	// Remove stop tokens
	for _, stop := range []string{"<|end|>", "<|endoftext|>", "<|user|>", "<|assistant|>", "<|system|>"} {
		response = strings.TrimSuffix(response, stop)
	}

	if response == "" {
		return "", nil
	}

	// Add to history
	b.addToHistory("assistant", response)

	b.lastSpoke = time.Now()
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

// FormatInteroceptionForLog creates a human-readable summary of interoceptive state
func FormatInteroception(ctx PromptContext) string {
	return fmt.Sprintf(
		"Stress:%.2f Pain:%.2f Calm:%.2f Arousal:%.2f Surprise:%.2f Loneliness:%.2f Awe:%.2f Entropy:%.2f",
		ctx.Stress, ctx.Pain, ctx.Calm, ctx.Arousal, ctx.Surprise, ctx.Loneliness, ctx.Awe, ctx.Entropy,
	)
}
