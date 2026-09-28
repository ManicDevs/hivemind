package language

import (
	"context"
	"strings"
)

// StubModel is a fallback that returns template responses
type StubModel struct {
	config ModelConfig
}

func NewLlamaCpp(config ModelConfig) (*StubModel, error) {
	if config.ModelPath == "" {
		return nil, ErrNoModelPath
	}
	return &StubModel{config: config}, nil
}

func (s *StubModel) Generate(ctx context.Context, prompt string) (string, error) {
	// Extract the actual interoceptive values if present
	// For now, return a grounded response based on prompt content
	if strings.Contains(prompt, "Stress:") || strings.Contains(prompt, "Pain:") {
		return s.generateGroundedResponse(prompt), nil
	}
	return "[Language model not loaded: build with 'make build-release' and provide a GGUF model]", nil
}

func (s *StubModel) generateGroundedResponse(prompt string) string {
	// Extract interoceptive values from prompt if present
	// This is a simple template-based response for testing
	return "[Interoceptive state received. Language model not loaded. Build with 'make build-release' and provide a GGUF model to enable genuine language generation.]"
}

func (s *StubModel) Name() string {
	return "stub (no model loaded)"
}

func (s *StubModel) Close() error {
	return nil
}
