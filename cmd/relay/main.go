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

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"gitlab.torproject.org/cerberus-droid/hivemind/internal/infra"
)

// ── hivemind relay: our own message bus ──────────────────────────────
// Our own message bus: compatible API, zero limits, our hardware.
// POST a ciphertext blob, GET /json streams it to subscribers.
// No auth, no quota, no third-party dependency.
//
// Optional MQTT bridge: when RELAY_MQTT is set, every message also
// publishes to a public MQTT broker (no account, no daily quota).
// This gives cross-WAN failover without any third-party relay.

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
	subChans []chan message // active SSE subscribers
}

type relay struct {
	mu     sync.RWMutex
	topics map[string]*topic
	mqtt   mqtt.Client
}

func newRelay() *relay {
	return &relay{topics: make(map[string]*topic)}
}

func (r *relay) getTopic(name string) *topic {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.topics[name]
	if !ok {
		t = &topic{
			messages: make([]message, 0, 1024),
			subChans: make([]chan message, 0),
		}
		r.topics[name] = t
	}
	return t
}

// connectMQTT connects to a public MQTT broker. No account needed
// unless the endpoint matrix supplies credentials (e.g. FreeMQTT).
func (r *relay) connectMQTT(broker string) {
	opts := mqtt.NewClientOptions().
		AddBroker(broker).
		SetClientID(fmt.Sprintf("hivemind-relay-%d", time.Now().UnixNano())).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(10 * time.Second)

	if ep, ok := infra.Lookup(broker); ok {
		if ep.Username != "" {
			opts.SetUsername(ep.Username)
			opts.SetPassword(ep.Password)
		}
	}

	r.mqtt = mqtt.NewClient(opts)
	if token := r.mqtt.Connect(); token.Wait() && token.Error() != nil {
		log.Printf("⚠️  MQTT connect failed: %v (HTTP-only mode)\n", token.Error())
	} else {
		log.Printf("📡 [MQTT] Bridged to %s — unlimited pub/sub backup\n", broker)
	}
}

// mqttPublish sends a message to the MQTT broker on the same topic.
func (r *relay) mqttPublish(topicName string, msg message) {
	if r.mqtt == nil || !r.mqtt.IsConnected() {
		return
	}
	payload, _ := json.Marshal(msg)
	r.mqtt.Publish("hive/"+topicName, 1, false, payload)
}

// POST /{topic} — accept a ciphertext blob, store it
func (r *relay) handlePublish(w http.ResponseWriter, req *http.Request) {
	topicName := strings.TrimPrefix(req.URL.Path, "/")
	if topicName == "" || topicName == "json" || topicName == "healthz" {
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
	// Keep last 10000 messages per topic (our bus, our limits)
	if len(t.messages) > 10000 {
		t.messages = t.messages[len(t.messages)-10000:]
	}
	// Fan out to local SSE subscribers
	for _, ch := range t.subChans {
		select {
		case ch <- msg:
		default: // slow subscriber, drop
		}
	}
	t.mu.Unlock()

	// Bridge to MQTT (no account, no quota, cross-WAN backup)
	r.mqttPublish(topicName, msg)

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

	// Register as a subscriber
	subCh := make(chan message, 128)
	t.mu.Lock()
	t.subChans = append(t.subChans, subCh)
	t.mu.Unlock()

	// Cleanup on disconnect
	defer func() {
		t.mu.Lock()
		for i, ch := range t.subChans {
			if ch == subCh {
				t.subChans = append(t.subChans[:i], t.subChans[i+1:]...)
				break
			}
		}
		t.mu.Unlock()
	}()

	// Send existing messages since `since`
	t.mu.RLock()
	for _, msg := range t.messages {
		if msg.Time >= since {
			line, _ := json.Marshal(msg)
			fmt.Fprintf(w, "%s\n", line)
			flusher.Flush()
		}
	}
	lastIdx := len(t.messages)
	t.mu.RUnlock()

	// Long-poll: new messages arrive via channel
	timeout := time.After(5 * time.Minute)
	for {
		select {
		case <-req.Context().Done():
			return
		case <-timeout:
			return
		case msg := <-subCh:
			line, _ := json.Marshal(msg)
			fmt.Fprintf(w, "%s\n", line)
			flusher.Flush()
			_ = lastIdx // used in the historical send above
		}
	}
}

// GET /healthz — health check
func (r *relay) handleHealth(w http.ResponseWriter, req *http.Request) {
	r.mu.RLock()
	n := len(r.topics)
	r.mu.RUnlock()
	mqttOK := r.mqtt != nil && r.mqtt.IsConnected()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok",
		"topics": n,
		"mqtt":   mqttOK,
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

	// Optional MQTT bridge: RELAY_MQTT="broker.hivemq.com" (hostname only)
	if broker := os.Getenv("RELAY_MQTT"); broker != "" {
		if ep, ok := infra.Lookup(infra.NormalizeHost(broker)); ok {
			if ep.TCPPort > 0 {
				broker = "tcp://" + ep.Host + ":" + strconv.Itoa(ep.TCPPort)
			} else {
				broker = ep.Host
			}
		}
		r.connectMQTT(broker)
	} else {
		// Auto-fallback: walk preferred TARGET INFRASTRUCTURE DATA MATRIX
		// entries (Prefer=true skips hosts that time out from this network).
		for _, ep := range infra.Preferred() {
			if ep.TCPPort <= 0 {
				continue
			}
			broker := "tcp://" + ep.Host + ":" + strconv.Itoa(ep.TCPPort)
			opts := mqtt.NewClientOptions().
				AddBroker(broker).
				SetClientID(fmt.Sprintf("hivemind-%d", time.Now().UnixNano())).
				SetAutoReconnect(true).
				SetConnectRetry(true).
				SetConnectRetryInterval(5 * time.Second).
				SetKeepAlive(30 * time.Second)
			if ep.Username != "" {
				opts.SetUsername(ep.Username)
				opts.SetPassword(ep.Password)
			}

			client := mqtt.NewClient(opts)
			if token := client.Connect(); token.WaitTimeout(3*time.Second) && token.Error() == nil {
				r.mqtt = client
				log.Printf("📡 [MQTT] Auto-connected to %s (%s) — unlimited backup relay\n", ep.Name, broker)
				break
			}
		}
		if r.mqtt == nil {
			log.Printf("📡 [MQTT] No public broker reachable — HTTP-only mode\n")
		}
	}

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
