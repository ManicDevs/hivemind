package worldmap

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The infra layer's contract: it draws exactly what the relay reported, and
// nothing it did not. These tests pin that.

func sampleHealth() RelayHealth {
	return RelayHealth{
		Reachable: true,
		Status:    "ok",
		Stats: MeshStats{
			Brokers: 3, Live: 2, Outbound: 10, Inbound: 4,
			Topic: "hive/", Bidirectional: true,
		},
		Brokers: []BrokerStatus{
			{Name: "EMQX", Host: "broker.emqx.io", Connected: true, Outbound: 7, Inbound: 3},
			{Name: "HiveMQ", Host: "broker.hivemq.com", Connected: true, Outbound: 3, Inbound: 1},
			{Name: "Dead", Host: "dead.example.org", Connected: false, LastError: "timeout"},
		},
	}
}

func TestInfraLayerEmptyHealthDrawsNothing(t *testing.T) {
	if got := infraLayer(1200, 700, 600, 350, RelayHealth{}); got != "" {
		t.Fatal("an empty health snapshot must draw no infrastructure at all")
	}
}

func TestInfraLayerDrawsOneMarkerPerBroker(t *testing.T) {
	h := sampleHealth()
	svg := infraLayer(1200, 700, 600, 350, h)
	for _, b := range h.Brokers {
		if !strings.Contains(svg, BrokerLabel(b.Host)) {
			t.Errorf("broker %s missing from the map layer", b.Host)
		}
	}
	// 3 broker squares (rx="1.5" marker) plus the legend swatches.
	if n := strings.Count(svg, `rx="1.5"`); n != 3 {
		t.Errorf("expected 3 broker markers, drew %d", n)
	}
}

func TestInfraLayerDrawsArcOnlyWhereTrafficMoved(t *testing.T) {
	h := sampleHealth()
	svg := infraLayer(1200, 700, 600, 350, h)
	// EMQX and HiveMQ both carry traffic, the dead one carries none. Every
	// drawn arc belongs to a broker that actually moved bytes.
	paths := strings.Count(svg, "<path d=")
	// 2 outbound + 2 inbound = 4 arcs.
	if paths != 4 {
		t.Errorf("expected 4 traffic arcs (2 out + 2 in), drew %d", paths)
	}
}

func TestInfraLayerMarksUnreachableBroker(t *testing.T) {
	h := sampleHealth()
	svg := infraLayer(1200, 700, 600, 350, h)
	if !strings.Contains(svg, "unreachable") {
		t.Error("a broker the relay could not reach must be labelled, not silently dropped")
	}
	if !strings.Contains(svg, brokerDeadColor) {
		t.Error("unreachable broker should use the dead colour")
	}
}

func TestInfraLayerDistinguishesLiveFromIdle(t *testing.T) {
	// A connected-but-silent broker is not the same as a working one.
	h := RelayHealth{
		Reachable: true,
		Brokers: []BrokerStatus{
			{Host: "busy.example.org", Connected: true, Outbound: 5},
			{Host: "quiet.example.org", Connected: true},
		},
	}
	svg := infraLayer(1200, 700, 600, 350, h)
	if !strings.Contains(svg, "live") {
		t.Error("a broker carrying traffic should be labelled live")
	}
	if !strings.Contains(svg, "idle") {
		t.Error("a connected-but-silent broker should be labelled idle")
	}
}

func TestInfraLayerOnlyDrawsInboundWhenBytesReturn(t *testing.T) {
	// This is the whole reason the bridge subscribes: an inbound arc may
	// only exist if something actually came back.
	h := RelayHealth{
		Reachable: true,
		Brokers: []BrokerStatus{
			{Host: "out-only.example.org", Connected: true, Outbound: 9, Inbound: 0},
		},
	}
	svg := infraLayer(1200, 700, 600, 350, h)
	if strings.Contains(svg, "↓0") && strings.Contains(svg, "↑9 ↓0") {
		// counter text is fine, but there must be no inbound arc
		t.Log("counter rendered; verifying arc count instead")
	}
	if n := strings.Count(svg, "<path d="); n != 1 {
		t.Errorf("publish-only broker should draw exactly 1 arc, drew %d", n)
	}
}

func TestInfraLayerReportsUnreachableRelay(t *testing.T) {
	h := sampleHealth()
	h.Reachable = false
	svg := infraLayer(1200, 700, 600, 350, h)
	if !strings.Contains(svg, "relay unreachable") {
		t.Error("a dead relay must be declared, not drawn as healthy")
	}
}

func TestLegendStatesDirection(t *testing.T) {
	h := sampleHealth()
	svg := infraLayer(1200, 700, 600, 350, h)
	if !strings.Contains(svg, "4 in") {
		t.Errorf("legend should surface inbound count; svg:\n%s", svg)
	}
	if !strings.Contains(svg, "bidirectional") {
		t.Error("legend should say the bridge is bidirectional when it is")
	}
}

func TestInfraLayerEscapesHostNames(t *testing.T) {
	h := RelayHealth{
		Reachable: true,
		Brokers: []BrokerStatus{
			{Host: `evil.example.org/<script>&"`, Connected: true, Outbound: 1},
		},
	}
	svg := infraLayer(1200, 700, 600, 350, h)
	if strings.Contains(svg, "<script>") {
		t.Fatal("broker host reached the SVG unescaped")
	}
}

func TestBrokerAnchorIsStableAndDistinct(t *testing.T) {
	hosts := []string{"broker.emqx.io", "broker.hivemq.com", "test.mosquitto.org", "iot.coreflux.cloud"}
	seen := map[[2]int]bool{}
	for i, h := range hosts {
		lon1, lat1 := brokerAnchorLL(h, i, len(hosts))
		lon2, lat2 := brokerAnchorLL(h, i, len(hosts))
		if lon1 != lon2 || lat1 != lat2 {
			t.Errorf("anchor for %s is not stable across calls", h)
		}
		if lon1 < -180 || lon1 > 180 {
			t.Errorf("anchor for %s has out-of-range longitude %f", h, lon1)
		}
		if lat1 < -90 || lat1 > 90 {
			t.Errorf("anchor for %s has out-of-range latitude %f", h, lat1)
		}
		seen[[2]int{int(lon1), int(lat1)}] = true
	}
	if len(seen) < 3 {
		t.Errorf("brokers collapsed onto too few points: %d distinct of %d", len(seen), len(hosts))
	}
}

func TestBrokerLabelStripsFirstLabel(t *testing.T) {
	cases := map[string]string{
		"broker.emqx.io":     "emqx.io",
		"test.mosquitto.org": "mosquitto.org",
		"localhost":          "localhost",
		"":                   "?",
	}
	for in, want := range cases {
		if got := BrokerLabel(in); got != want {
			t.Errorf("BrokerLabel(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestRelayHealthHelpers(t *testing.T) {
	h := sampleHealth()
	// EMQX 7+3, HiveMQ 3+1, dead 0 → 14
	if got := h.TotalTraffic(); got != 14 {
		t.Errorf("TotalTraffic=%d, want 14", got)
	}
	var live []BrokerStatus
	for _, b := range h.Brokers {
		if b.Connected {
			live = append(live, b)
		}
	}
	if len(live) != 2 {
		t.Fatalf("connected brokers=%d, want 2", len(live))
	}
	if live[0].Host != "broker.emqx.io" {
		t.Errorf("matrix order should lead with EMQX, got %s", live[0].Host)
	}
}

func TestRelayPollerParsesRealHealthShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Errorf("poller asked for %q, want /healthz", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "ok",
			"uptime": "1m",
			"mesh": map[string]interface{}{
				"brokers": 6, "live": 2, "outbound": 5, "inbound": 3,
				"topic_prefix": "hive/", "bidirectional": true,
			},
			"brokers": []map[string]interface{}{
				{"name": "EMQX", "host": "broker.emqx.io", "connected": true, "outbound": 5, "inbound": 3},
				{"name": "Dead", "host": "x.example.org", "connected": false},
			},
		})
	}))
	defer srv.Close()

	p := NewRelayPoller(srv.URL)
	p.Poll()
	h := p.Health()

	if !h.Reachable {
		t.Fatal("poller did not mark a successful fetch reachable")
	}
	if !h.OK {
		t.Error(`status "ok" should set OK`)
	}
	if h.Stats.Live != 2 || h.Stats.Brokers != 6 {
		t.Errorf("mesh stats not decoded: %+v", h.Stats)
	}
	if !h.Stats.Bidirectional {
		t.Error("bidirectional flag not decoded")
	}
	if len(h.Brokers) != 2 || h.Brokers[0].Host != "broker.emqx.io" {
		t.Errorf("broker rows not decoded: %+v", h.Brokers)
	}
	if h.TotalTraffic() != 8 {
		t.Errorf("TotalTraffic=%d, want 8", h.TotalTraffic())
	}
}

func TestRelayPollerMarksFailureUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := NewRelayPoller(srv.URL)
	p.Poll()
	if p.Health().Reachable {
		t.Error("a 500 must not read as reachable")
	}
}

func TestRelayPollerOnDeadEndpoint(t *testing.T) {
	p := NewRelayPoller("http://127.0.0.1:1") // nothing listens here
	start := time.Now()
	p.Poll()
	if p.Health().Reachable {
		t.Error("a refused connection must not read as reachable")
	}
	if time.Since(start) > 6*time.Second {
		t.Error("poller blocked well past its timeout")
	}
}

func TestRelayPollerDisabledWhenNoURL(t *testing.T) {
	p := NewRelayPoller("")
	if p.Enabled() {
		t.Error("an empty relay URL must yield a disabled poller")
	}
	p.Poll() // must be a no-op, not a panic
	if p.Health().Reachable {
		t.Error("a disabled poller has no health to report")
	}
}

func TestRenderSVGSurvivesInfraLayer(t *testing.T) {
	nodes := []NodeView{
		{Name: "eu-master-rotterdam", Continent: "eu", Role: "master", Alive: true},
		{Name: "na-edge-toronto", Continent: "na", Role: "edge", Alive: true},
	}
	links := []string{"eu-master-rotterdam"}
	svg := RenderSVGWithInfra(nodes, links, "gaze-eu", "12:00:00", "", sampleHealth())
	if !strings.HasPrefix(svg, "<svg") {
		t.Fatal("render did not produce an SVG document")
	}
	if !strings.Contains(svg, "BROKER MESH") {
		t.Error("broker layer missing from the rendered map")
	}
	if !strings.Contains(svg, "</svg>") {
		t.Error("SVG document not closed")
	}
}

func TestRenderSVGWithoutInfraIsUnchanged(t *testing.T) {
	nodes := []NodeView{
		{Name: "eu-master-rotterdam", Continent: "eu", Role: "master", Alive: true},
	}
	svg := RenderSVG(nodes, nil, "gaze-eu", "12:00:00", "")
	if strings.Contains(svg, "BROKER MESH") {
		t.Error("a report with no relay telemetry must not draw a broker layer")
	}
}
