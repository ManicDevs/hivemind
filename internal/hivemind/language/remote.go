package language

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// RemoteLLM implements LanguageModel using remote HTTP API
// Pure Go, no CGO, cross-compiles everywhere
type RemoteLLM struct {
	config RemoteLLMConfig
	client *http.Client
}

func NewRemoteLLM(config RemoteLLMConfig) *RemoteLLM {
	return &RemoteLLM{
		config: config,
		client: &http.Client{
			Timeout: config.Timeout,
		},
	}
}

func (r *RemoteLLM) Name() string {
	return fmt.Sprintf("remote-%s", r.config.Model)
}

func (r *RemoteLLM) Generate(ctx context.Context, prompt string) (string, error) {
	reqBody := map[string]any{
		"model":       r.config.Model,
		"prompt":      prompt,
		"stream":      false,
		"temperature": 0.7,
		"top_p":       0.9,
		"max_tokens":  256,
		"stop":        []string{"<|end|>", "<|endoftext|>", "<|user|>", "<|assistant|>", "<|system|>"},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", r.config.Endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if r.config.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.config.APIKey)
	}
	for k, v := range r.config.Headers {
		req.Header.Set(k, v)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var respBody struct {
		Response string `json:"response"`
		Error    string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&respBody); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if respBody.Error != "" {
		return "", fmt.Errorf("API error: %s", respBody.Error)
	}

	return strings.TrimSpace(respBody.Response), nil
}

func (r *RemoteLLM) Close() error {
	return nil
}
