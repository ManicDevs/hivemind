package worldmap

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

// BrokerStatus mirrors one row of the relay's broker matrix health. It is
// deliberately a copy of the wire shape rather than a shared import: the
// relay is a separate binary, and the map must degrade to "no infra" if the
// relay's schema ever moves.
type BrokerStatus struct {
	Name       string `json:"name"`
	Host       string `json:"host"`
	URL        string `json:"url"`
	Connected  bool   `json:"connected"`
	Outbound   uint64 `json:"outbound"`
	Inbound    uint64 `json:"inbound"`
	Duplicates uint64 `json:"duplicates"`
	Uptime     string `json:"uptime,omitempty"`
	LastError  string `json:"last_error,omitempty"`
}

// MeshStats is the aggregate block the relay publishes alongside the rows.
type MeshStats struct {
	Brokers       int    `json:"brokers"`
	Live          int    `json:"live"`
	Outbound      uint64 `json:"outbound"`
	Inbound       uint64 `json:"inbound"`
	Duplicates    uint64 `json:"duplicates"`
	Topic         string `json:"topic_prefix"`
	Bidirectional bool   `json:"bidirectional"`
}

// RelayHealth is the relay's /healthz envelope, reduced to the fields the
// map needs.
type RelayHealth struct {
	OK        bool           `json:"-"`
	Reachable bool           `json:"-"`
	Status    string         `json:"status"`
	Uptime    string         `json:"uptime"`
	Stats     MeshStats      `json:"mesh"`
	Brokers   []BrokerStatus `json:"brokers"`
	LastSeen  time.Time      `json:"-"`
}

// TotalTraffic is everything that has crossed the mesh in either direction.
// A relay that has connected but never carried a byte is not "working", and
// the map must not pretend otherwise.
func (h *RelayHealth) TotalTraffic() uint64 {
	var t uint64
	for _, b := range h.Brokers {
		t += b.Outbound + b.Inbound
	}
	return t
}

// RelayPoller watches one relay's health endpoint on an interval, keeping
// the last good snapshot. A relay that stops answering leaves the previous
// state to expire rather than reporting a fabricated "down".
type RelayPoller struct {
	url    string
	client *http.Client

	mu     sync.RWMutex
	health RelayHealth
}

// NewRelayPoller targets a relay base URL (scheme://host:port). An empty
// URL yields a poller that is permanently disabled.
func NewRelayPoller(relayURL string) *RelayPoller {
	p := &RelayPoller{
		url: relayURL,
		client: &http.Client{
			Timeout: 3 * time.Second,
			Transport: &http.Transport{
				IdleConnTimeout:     30 * time.Second,
				MaxIdleConnsPerHost: 1,
			},
		},
	}
	p.health.Reachable = false
	return p
}

// Enabled reports whether there is a relay to watch.
func (p *RelayPoller) Enabled() bool { return p != nil && p.url != "" }

// Run polls until ctx-like stop channel closes. It never blocks a caller for
// longer than the HTTP timeout.
func (p *RelayPoller) Run(stop <-chan struct{}) {
	if !p.Enabled() {
		return
	}
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	p.Poll()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			p.Poll()
		}
	}
}

// Poll fetches one health sample. A failure marks the snapshot unreachable
// but keeps the last known broker rows so the map can grey them out rather
// than blink them away.
func (p *RelayPoller) Poll() {
	if !p.Enabled() {
		return
	}
	resp, err := p.client.Get(p.url + "/healthz")
	if err != nil {
		p.mu.Lock()
		p.health.Reachable = false
		p.health.LastSeen = time.Now()
		p.mu.Unlock()
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		p.mu.Lock()
		p.health.Reachable = false
		p.health.LastSeen = time.Now()
		p.mu.Unlock()
		return
	}
	var h RelayHealth
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		p.mu.Lock()
		p.health.Reachable = false
		p.health.LastSeen = time.Now()
		p.mu.Unlock()
		return
	}
	h.OK = h.Status == "ok"
	h.Reachable = true
	h.LastSeen = time.Now()
	p.mu.Lock()
	p.health = h
	p.mu.Unlock()
}

// Health returns the last snapshot.
func (p *RelayPoller) Health() RelayHealth {
	if p == nil {
		return RelayHealth{}
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.health
}

// brokerAnchorLL gives every broker a stable, geographically-plausible
// anchor point on the map. The relay genuinely sits in the WAN between
// continents, so the brokers are spread across longitudes rather than
// stacked at one hub. Anchors are derived from the host name so a broker
// keeps its spot between refreshes.
func brokerAnchorLL(host string, i, n int) (lon, lat float64) {
	if n <= 1 {
		return 33.75, 0
	}
	// Deterministic spread: hashed longitude nudged by a golden-ratio walk
	// so two brokers never land on the same spot and each keeps its place
	// between refreshes.
	const golden = 137.508
	seed := 0
	for _, c := range host {
		seed = (seed*31 + int(c)) & 0x7fffffff
	}
	lon = float64(seed%721) - 360.0
	lat = -45 + float64((seed/721)%90)
	lon += float64(i) * golden / float64(n)
	lon = math.Mod(lon+180, 360) - 180
	return lon, lat
}

// BrokerLabel is the short form shown on the map.
func BrokerLabel(host string) string {
	if host == "" {
		return "?"
	}
	// Strip the leading label for compactness: broker.emqx.io → emqx.io
	if i := strings.IndexByte(host, '.'); i >= 0 && i+1 < len(host) {
		return host[i+1:]
	}
	return host
}
