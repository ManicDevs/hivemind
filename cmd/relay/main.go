package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ── hivemind relay: our own message bus ──────────────────────────────
// Drop-in replacement for ntfy.sh: same API, zero limits, our hardware.
// POST a ciphertext blob, GET /json streams it to subscribers.
// No auth, no quota, no third-party dependency.

type message struct {
	ID        string `json:"id"`
	Event     string `json:"event"`
	Title     string `json:"title"`
	Message   string `json:"message"`
	Time      int64  `json:"time"`
	ExpiresAt int64  `json:"expires"`
}

type topic struct {
	mu       sync.RWMutex
	messages []message
	nextID   int
}

type relay struct {
	mu     sync.RWMutex
	topics map[string]*topic
}

func newRelay() *relay {
	return &relay{topics: make(map[string]*topic)}
}

func (r *relay) getTopic(name string) *topic {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.topics[name]
	if !ok {
		t = &topic{messages: make([]message, 0, 1024)}
		r.topics[name] = t
	}
	return t
}

// POST /{topic} — accept a ciphertext blob, store it
func (r *relay) handlePublish(w http.ResponseWriter, req *http.Request) {
	topicName := strings.TrimPrefix(req.URL.Path, "/")
	if topicName == "" || topicName == "json" {
		http.Error(w, "topic required", http.StatusBadRequest)
		return
	}

	body := make([]byte, 64*1024) // 64KB max
	n, err := req.Body.Read(body)
	if err != nil && n == 0 {
		http.Error(w, "empty body", http.StatusBadRequest)
		return
	}
	body = body[:n]

	now := time.Now().Unix()
	t := r.getTopic(topicName)
	t.mu.Lock()
	t.nextID++
	id := fmt.Sprintf("hivemind-%d-%d", now, t.nextID)
	msg := message{
		ID:        id,
		Event:     "message",
		Title:     req.Header.Get("X-Title"),
		Message:   string(body),
		Time:      now,
		ExpiresAt: now + 3600, // 1 hour
	}
	t.messages = append(t.messages, msg)
	// Keep last 1000 messages per topic
	if len(t.messages) > 1000 {
		t.messages = t.messages[len(t.messages)-1000:]
	}
	t.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"id": id})
}

// GET /{topic}/json — stream messages as newline-delimited JSON
func (r *relay) handleSubscribe(w http.ResponseWriter, req *http.Request) {
	path := strings.TrimPrefix(req.URL.Path, "/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 1 || parts[0] == "" {
		http.Error(w, "topic required", http.StatusBadRequest)
		return
	}
	topicName := parts[0]

	// Parse ?since= parameter (seconds ago)
	sinceSec := int64(300) // default 5 minutes
	if s := req.URL.Query().Get("since"); s != "" {
		if v, err := strconv.ParseInt(s, 10, 64); err == nil {
			sinceSec = v
		}
	}
	since := time.Now().Unix() - sinceSec

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	t := r.getTopic(topicName)

	// Send existing messages since `since`
	t.mu.RLock()
	for _, msg := range t.messages {
		if msg.Time >= since {
			line, _ := json.Marshal(msg)
			fmt.Fprintf(w, "%s\n", line)
			flusher.Flush()
		}
	}
	t.mu.RUnlock()

	// Long-poll: hold connection open, send new messages as they arrive
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	timeout := time.After(5 * time.Minute)
	lastIdx := len(t.messages)

	for {
		select {
		case <-req.Context().Done():
			return
		case <-timeout:
			return
		case <-ticker.C:
			t.mu.RLock()
			newMsgs := t.messages[lastIdx:]
			lastIdx = len(t.messages)
			t.mu.RUnlock()

			for _, msg := range newMsgs {
				line, _ := json.Marshal(msg)
				fmt.Fprintf(w, "%s\n", line)
				flusher.Flush()
			}
		}
	}
}

// GET /healthz — health check
func (r *relay) handleHealth(w http.ResponseWriter, req *http.Request) {
	r.mu.RLock()
	n := len(r.topics)
	r.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok",
		"topics": n,
		"uptime": time.Since(startTime).String(),
	})
}

var startTime = time.Now()

func main() {
	port := "8080"
	if p := os.Getenv("RELAY_PORT"); p != "" {
		port = p
	}

	r := newRelay()

	http.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case "POST":
			r.handlePublish(w, req)
		case "GET":
			if strings.HasSuffix(req.URL.Path, "/json") || strings.Contains(req.URL.Path, "/json?") {
				r.handleSubscribe(w, req)
			} else if req.URL.Path == "/healthz" || req.URL.Path == "/healthz/" {
				r.handleHealth(w, req)
			} else {
				r.handleSubscribe(w, req)
			}
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	fmt.Printf("📡 [RELAY] Listening on :%s — zero limits, our bus\n", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
