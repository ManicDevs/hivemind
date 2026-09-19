package hivemind

// Health: a tiny HTTP window into a running node. /healthz answers
// "am I alive" for supervisors and load balancers; /metrics speaks
// Prometheus text for graphs and alerts. Stdlib only, localhost by
// default — observability must never become remote access.

import (
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// HealthServer serves liveness and metrics for one node.
type HealthServer struct {
	mu      sync.Mutex
	swarm   *Swarm
	node    string
	born    time.Time
	srv     *http.Server
	started bool
}

// StartHealth launches the server in the background. Empty addr disables
// it (the default): no listener, no surface.
func StartHealth(addr, node string, swarm *Swarm) *HealthServer {
	if addr == "" {
		return nil
	}
	h := &HealthServer{swarm: swarm, node: node, born: time.Now()}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", h.healthz)
	mux.HandleFunc("/metrics", h.metrics)
	h.srv = &http.Server{Addr: addr, Handler: mux, ReadTimeout: 5 * time.Second}
	h.started = true
	go func() { _ = h.srv.ListenAndServe() }()
	return h
}

// Stop closes the listener.
func (h *HealthServer) Stop() {
	if h == nil || h.srv == nil {
		return
	}
	_ = h.srv.Close()
}

func (h *HealthServer) healthz(w http.ResponseWriter, _ *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	members := 0
	depth := 0
	maxPain := 0.0
	if h.swarm != nil {
		members = len(h.swarm.Members())
		depth = h.swarm.Depth()
		h.swarm.mu.Lock()
		for _, p := range h.swarm.NodePain {
			if p > maxPain {
				maxPain = p
			}
		}
		h.swarm.mu.Unlock()
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"alive":true,"node":%q,"uptime_s":%d,"members":%d,"chronicle_depth":%d,"max_pain":%.2f}`+"\n",
		h.node, int64(time.Since(h.born).Seconds()), members, depth, maxPain)
}

func (h *HealthServer) metrics(w http.ResponseWriter, _ *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	members, depth := 0, 0
	pains := []float64{}
	if h.swarm != nil {
		members = len(h.swarm.Members())
		depth = h.swarm.Depth()
		h.swarm.mu.Lock()
		for _, p := range h.swarm.NodePain {
			pains = append(pains, p)
		}
		h.swarm.mu.Unlock()
	}
	sort.Float64s(pains)
	maxPain := 0.0
	if len(pains) > 0 {
		maxPain = pains[len(pains)-1]
	}
	var b strings.Builder
	metric := func(name, help, typ, labels string, v interface{}) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n%s{%s} %v\n", name, help, name, typ, name, labels, v)
	}
	metric("hivemind_up", "node alive", "gauge", fmt.Sprintf(`node=%q`, h.node), 1)
	metric("hivemind_uptime_seconds_total", "seconds since birth", "counter", fmt.Sprintf(`node=%q`, h.node), int64(time.Since(h.born).Seconds()))
	metric("hivemind_swarm_members", "joined identities", "gauge", fmt.Sprintf(`node=%q`, h.node), members)
	metric("hivemind_chronicle_total", "frames ever chronicled", "counter", fmt.Sprintf(`node=%q`, h.node), depth)
	metric("hivemind_max_pain", "hottest pain in swarm", "gauge", fmt.Sprintf(`node=%q`, h.node), maxPain)
	metric("hivemind_go_goroutines", "goroutines", "gauge", fmt.Sprintf(`node=%q`, h.node), runtime.NumGoroutine())
	metric("hivemind_go_heap_bytes", "heap in use", "gauge", fmt.Sprintf(`node=%q`, h.node), m.HeapAlloc)
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprint(w, b.String())
}
