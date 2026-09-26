package main

import (
	"encoding/json"
	"fmt"
	hm "gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
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
	mesh   *mqttMesh
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

// deliver stores a message on the local bus and fans it out to every local
// SSE subscriber. It is the single write path for a message regardless of
// whether it arrived over HTTP or came back from a broker, so both routes
// behave identically.
func (r *relay) deliver(topicName string, msg message) {
	t := r.getTopic(topicName)
	t.mu.Lock()
	t.messages = append(t.messages, msg)
	// Keep last 10000 messages per topic (our bus, our limits)
	if len(t.messages) > 10000 {
		t.messages = t.messages[len(t.messages)-10000:]
	}
	for _, ch := range t.subChans {
		select {
		case ch <- msg:
		default: // slow subscriber, drop
		}
	}
	t.mu.Unlock()
}

// inject folds a message that arrived from a broker into the local bus. It
// deliberately does not re-publish: the message is already on the mesh, and
// echoing it back would loop.
func (r *relay) inject(topicName string, msg message) {
	if topicName == "" {
		return
	}
	r.deliver(topicName, msg)
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
	t.mu.Unlock()

	msg := message{
		ID:        id,
		Event:     "message",
		Title:     req.Header.Get("X-Title"),
		Message:   string(body),
		Time:      now,
		ExpiresAt: now + 3600, // 1 hour
	}

	// Local bus first, so an HTTP subscriber sees the message even if every
	// broker is down.
	r.deliver(topicName, msg)

	// Then fan out to the broker mesh (no account, no quota, cross-WAN
	// backup). A dead broker costs one lost copy, not the message.
	if r.mesh != nil {
		// Record the ID as seen before it hits the wire, so the echo coming
		// back from each broker is recognised as our own.
		r.mesh.markSeen(id)
		r.mesh.publish(topicName, msg)
	}

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
		}
	}
}

// GET /healthz — health check, including per-broker mesh telemetry.
//
// This is the feed the world map renders as its infrastructure layer: it
// reports which brokers are actually connected and how much traffic has
// moved each way, so the map can never show a wire that is not carrying.
func (r *relay) handleHealth(w http.ResponseWriter, req *http.Request) {
	r.mu.RLock()
	n := len(r.topics)
	r.mu.RUnlock()

	payload := map[string]interface{}{
		"status": "ok",
		"topics": n,
		"uptime": time.Since(startTime).String(),
	}
	if r.mesh != nil {
		stats := r.mesh.stats()
		payload["mqtt"] = stats.Live > 0
		payload["mesh"] = stats
		payload["brokers"] = r.mesh.snapshot()
	} else {
		payload["mqtt"] = false
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(payload)
}

var startTime = time.Now()

func main() {
	log.Printf("🚀 [RELAY] starting (release build)")
	port := "8080"
	if p := os.Getenv("RELAY_PORT"); p != "" {
		port = p
	}

	r := newRelay()

	// MQTT bridge mesh. RELAY_MQTT pins a single broker; otherwise every
	// Prefer=true entry in the infrastructure matrix joins, and we publish
	// and subscribe across all of them at once.
	mesh := newMQTTMesh(r, os.Getenv("RELAY_MQTT"))
	r.mesh = mesh
	log.Printf("🚀 [RELAY] calling AnnounceKeyPosture")
	hm.AnnounceKeyPosture()
	log.Printf("🚀 [RELAY] AnnounceKeyPosture returned")
	mesh.connectAll(3 * time.Second)

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

	// Bind the configured port; if it's already taken, roll forward to the
	// next free one rather than refusing to start the whole relay. A busy
	// port on a dev box should degrade, not kill the mesh. The listener we
	// bind is the one we serve on — probing a port by opening and closing
	// it would race anything else that grabs it in between.
	ln, err := listenFreePort(port)
	if err != nil {
		log.Fatalf("relay: no free port from %s: %v", port, err)
	}
	bound := ln.Addr().(*net.TCPAddr).Port

	// Publish the port we actually got so `make ports` / stack tooling can
	// report the truth rather than echoing the configured default.
	claimStackPort(bound)

	fmt.Printf("📡 [RELAY] Listening on :%d — zero limits, our bus (%d/%d brokers)\n",
		bound, mesh.live(), len(mesh.peers))
	log.Fatal(http.Serve(ln, nil))
}

// listenFreePort returns a live listener on want, or on the next free port
// above it. The returned listener stays open and is what the caller serves
// on, so there is no window where another process can take the port.
func listenFreePort(want string) (net.Listener, error) {
	base, err := strconv.Atoi(want)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for p := base; p < base+100; p++ {
		ln, err := net.Listen("tcp", ":"+strconv.Itoa(p))
		if err == nil {
			return ln, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("ports %d-%d all busy: %w", base, base+99, lastErr)
}

const stackPortFile = ".stack-relay.port"

// claimStackPort records the port this relay serves on, for `make ports`
// and the stack tooling.
//
// It is a claim, not a write. A second relay -- someone testing an
// opt-out, an ad-hoc run -- would otherwise overwrite the file and the
// tooling would cheerfully report the wrong relay's health as the
// stack's. It claims only when nobody holds the file, or when the recorded
// owner is gone, so the tooling always points at a live relay.
//
// The port and owning pid are written together and renamed into place, so
// a reader never sees a port without its owner.
func claimStackPort(port int) {
	if b, err := os.ReadFile(stackPortFile); err == nil {
		if owner := stackPortOwner(b); owner > 0 && processIsRelay(owner) {
			return // a live relay already owns the tooling
		}
	}
	tmp := fmt.Sprintf("%s.%d.tmp", stackPortFile, os.Getpid())
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return
	}
	fmt.Fprintf(f, "%d\n%d\n", port, os.Getpid())
	_ = f.Close()
	if err := os.Rename(tmp, stackPortFile); err != nil {
		_ = os.Remove(tmp)
	}
}

// stackPortOwner reads the owning pid recorded under the port. A file with
// no pid is a legacy single-line file and reports no owner.
func stackPortOwner(raw []byte) int {
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) < 2 {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(lines[1]))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

// processIsRelay reports whether pid is still a live relay. Checking the
// command name as well as liveness keeps a recycled pid from holding the
// claim hostage.
func processIsRelay(pid int) bool {
	comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(comm)) == "relay"
}
