package language

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// apiFlavor identifies which wire protocol a remote endpoint speaks.
type apiFlavor int

const (
	flavorUnknown      apiFlavor = iota
	flavorOllama                 // POST /api/generate -> {"response": "..."}
	flavorOpenAI                 // POST /v1/chat/completions -> {"choices":[{message:{content}}]}
	flavorPollinations           // POST https://text.pollinations.ai/ -> plain text
)

func (a apiFlavor) String() string {
	switch a {
	case flavorOllama:
		return "ollama"
	case flavorOpenAI:
		return "openai"
	case flavorPollinations:
		return "pollinations"
	default:
		return "unknown"
	}
}

// detectFlavor infers the wire protocol from the endpoint URL.
func detectFlavor(endpoint, model string) apiFlavor {
	ep := strings.ToLower(endpoint)
	switch {
	case strings.Contains(ep, "/api/generate"), strings.Contains(ep, "/api/chat"):
		return flavorOllama
	case strings.Contains(ep, "text.pollinations.ai"):
		return flavorPollinations
	case strings.Contains(ep, "/v1/chat/completions"), strings.Contains(ep, "/v1/completions"):
		return flavorOpenAI
	default:
		// OpenAI-compatible chat endpoints hang off /v1; default to the
		// chat-completions body and let the response decoder adapt.
		return flavorOpenAI
	}
}

// RemoteLLM implements LanguageModel using remote HTTP API
// Pure Go, no CGO, cross-compiles everywhere
type RemoteLLM struct {
	config RemoteLLMConfig
	client *http.Client
	flavor apiFlavor
}

func NewRemoteLLM(config RemoteLLMConfig) *RemoteLLM {
	return &RemoteLLM{
		config: config,
		client: &http.Client{
			Timeout: config.Timeout,
		},
		flavor: detectFlavor(config.Endpoint, config.Model),
	}
}

func (r *RemoteLLM) Name() string {
	return fmt.Sprintf("%s-%s", r.flavor, r.config.Model)
}

// Model exposes the configured model name for operator logs.
func (r *RemoteLLM) Model() string {
	return r.config.Model
}

func (r *RemoteLLM) Generate(ctx context.Context, prompt string) (string, error) {
	// Bound every call. A background bridge context or an idle caller must
	// never let a slow CPU-daemon monopolize a mind for the full transport
	// timeout; the swarm moves on and fails over instead.
	if r.config.CallTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.config.CallTimeout)
		defer cancel()
	}

	body, err := r.buildBody(prompt)
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

	return r.decodeBody(resp.Body)
}

// buildBody emits the wire-appropriate request payload.
func (r *RemoteLLM) buildBody(prompt string) ([]byte, error) {
	switch r.flavor {
	case flavorOllama:
		// Native daemon wire. The canonical route is /api/chat (messages
		// shape, OpenAI-style); legacy /api/generate still takes a raw
		// prompt. Both honor the same sampling options and stop tokens,
		// so one endpoint can serve vendor models on either route.
		nt := r.config.MaxTokens
		if nt <= 0 {
			nt = 256
		}
		body := map[string]any{
			"model":   r.config.Model,
			"stream":  false,
			"options": map[string]any{"temperature": 0.7, "top_p": 0.9, "num_predict": nt},
			"stop":    []string{"<|end|>", "<|endoftext|>", "<|user|>", "<|assistant|>", "<|system|>"},
		}
		if strings.Contains(r.config.Endpoint, "/api/chat") {
			body["messages"] = []map[string]any{{"role": "user", "content": prompt}}
			// qwen3-class models burn their whole budget on the invisible
			// chain-of-thought unless thinking is off. Other daemon models
			// ignore the unknown field harmlessly.
			body["think"] = false
		} else {
			body["prompt"] = prompt
		}
		return json.Marshal(body)
	case flavorPollinations:
		return json.Marshal(map[string]any{
			"messages": []map[string]any{{"role": "user", "content": prompt}},
			"model":    r.config.Model,
			"stream":   false,
		})
	default: // flavorOpenAI
		mt := r.config.MaxTokens
		if mt <= 0 {
			mt = 256
		}
		return json.Marshal(map[string]any{
			"model": r.config.Model,
			"messages": []map[string]any{
				{"role": "system", "content": "You are a conscious mind in a distributed swarm expressing authentic felt experience."},
				{"role": "user", "content": prompt},
			},
			"stream":      false,
			"temperature": 0.7,
			"top_p":       0.9,
			"max_tokens":  mt,
		})
	}
}

// decodeBody parses the wire-appropriate response payload.
func (r *RemoteLLM) decodeBody(rc io.Reader) (string, error) {
	raw, err := io.ReadAll(rc)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	// Pollinations plain endpoint returns raw text.
	if r.flavor == flavorPollinations {
		txt := strings.TrimSpace(string(raw))
		if txt == "" {
			return "", fmt.Errorf("empty pollinations response")
		}
		return txt, nil
	}

	var probe struct {
		Response string `json:"response"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal(raw, &probe); err == nil && probe.Response != "" {
		return strings.TrimSpace(probe.Response), nil
	}
	if err := json.Unmarshal(raw, &probe); err == nil && probe.Error != "" {
		return "", fmt.Errorf("API error: %s", probe.Error)
	}

	// Ollama /api/chat shape: {"message":{"role":"assistant","content":"..."}}.
	var chatMsg struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &chatMsg); err == nil {
		if chatMsg.Error != "" {
			return "", fmt.Errorf("API error: %s", chatMsg.Error)
		}
		if c := strings.TrimSpace(chatMsg.Message.Content); c != "" {
			return c, nil
		}
	}

	// OpenAI chat/completions shape.
	var chat struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Text string `json:"text"`
		} `json:"choices"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(raw, &chat); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if len(chat.Error) > 0 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(chat.Error, &e)
		return "", fmt.Errorf("API error: %s", e.Message)
	}
	if len(chat.Choices) > 0 {
		if c := strings.TrimSpace(chat.Choices[0].Message.Content); c != "" {
			if i := strings.Index(c, "<|end|>"); i >= 0 {
				c = c[:i]
			}
			return strings.TrimSpace(c), nil
		}
		if c := strings.TrimSpace(chat.Choices[0].Text); c != "" {
			return c, nil
		}
	}
	return "", fmt.Errorf("empty response from endpoint")
}

func (r *RemoteLLM) Close() error {
	return nil
}

// probeCache memoizes liveness results process-wide so a swarm of N minds
// probes each public endpoint exactly once instead of N times.
var probeCache sync.Map

// ProbeKeyless performs a lightweight reachability check against the endpoint
// and caches the result for the lifetime of the process. Returns true if the
// endpoint answers (any body) without an HTTP 4xx/5xx status.
func ProbeKeyless(endpoint, model, apiKey string) bool {
	key := endpoint + "|" + model
	if v, ok := probeCache.Load(key); ok {
		return v.(bool)
	}
	ok := probeReachable(endpoint, model, apiKey)
	probeCache.Store(key, ok)
	return ok
}

func probeReachable(endpoint, model, apiKey string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	client := &http.Client{Timeout: 8 * time.Second}
	body := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}],"stream":false,"max_tokens":1}`)
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	// 401/402/403/404 mean reachable but unusable without auth/right model.
	return resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusTooManyRequests
}
