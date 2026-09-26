package worldmap

import (
	"strings"
	"testing"
)

// The relay fan has two regimes, and both have to stay honest:
//
//   - with broker telemetry, the infrastructure layer draws one marker per
//     real broker and arcs only where bytes moved (covered in infra_test.go);
//   - without telemetry, a configured relay still gets the legacy single-hub
//     fan, because a configured endpoint is a real socket even if the relay
//     is not answering /healthz right now.
//
// What neither regime may do is draw a relay that was never configured.
func TestLegacyRelayFanRidesWhenNoTelemetry(t *testing.T) {
	nodes := []NodeView{
		{Name: "alpha-node", Continent: "EU"},
		{Name: "beta-node", Continent: "NA"},
		{Name: "gaze-node", Continent: "EU", Alive: true},
	}
	got := RenderSVG(nodes, []string{"alpha-node", "beta-node"}, "gaze-node", "12:00:00", "tcp://broker.emqx.io:1883")

	for _, want := range []string{"relayflow", `stroke="#2dd4bf"`, `stroke-dasharray="2 3"`} {
		if !strings.Contains(got, want) {
			t.Errorf("configured relay without telemetry should still draw the hub fan, missing %q", want)
		}
	}
}

func TestNoRelayConfiguredDrawsNoRelay(t *testing.T) {
	nodes := []NodeView{
		{Name: "alpha-node", Continent: "EU", Alive: true},
		{Name: "gaze-node", Continent: "EU", Alive: true},
	}
	// An empty relay URL is honest silence: no hub, no fan.
	got := RenderSVG(nodes, nil, "gaze-node", "12:00:00", "")
	if strings.Contains(got, "relayflow") {
		t.Error("no relay configured must not draw a relay fan")
	}
}

func TestBrokerTelemetrySupersedesLegacyFan(t *testing.T) {
	nodes := []NodeView{
		{Name: "eu-master-rotterdam", Continent: "eu", Alive: true},
		{Name: "na-edge-toronto", Continent: "na", Alive: true},
	}
	h := RelayHealth{
		Reachable: true,
		Brokers: []BrokerStatus{
			{Host: "broker.emqx.io", Connected: true, Outbound: 4, Inbound: 1},
		},
	}
	got := RenderSVGWithInfra(nodes, []string{"eu-master-rotterdam"}, "gaze-eu", "12:00:00", "tcp://broker.emqx.io:1883", h)

	// Real telemetry present → the per-broker layer is drawn and the old
	// single-hub fan is not, so the map never shows two truths at once.
	if !strings.Contains(got, "BROKER MESH") {
		t.Error("broker telemetry should render the infrastructure layer")
	}
	if strings.Contains(got, "relayflow") {
		t.Error("broker telemetry must supersede the legacy single-hub fan")
	}
	if !strings.Contains(got, "↑4 ↓1") {
		t.Error("per-broker counters should carry real traffic numbers")
	}
}

func TestRelayNodeIsInTheArcEngine(t *testing.T) {
	// The relay is a class-actor, not a prop: it must be baked into posOf
	// with its own role so the shared arc engine routes it, exactly like it
	// does for peers. This is why the legacy fan can use linkArcsSVG.
	nodes := []NodeView{{Name: "eu-master-rotterdam", Continent: "eu", Alive: true}}
	got := RenderSVG(nodes, nil, "gaze-eu", "12:00:00", "tcp://broker.emqx.io:1883")
	if !strings.Contains(got, "<path") {
		t.Error("no arcs rendered at all")
	}
	// Sanity: the document is well-formed enough to parse as a whole.
	if !strings.HasPrefix(got, "<svg") || !strings.HasSuffix(strings.TrimSpace(got), "</svg>") {
		t.Error("rendered map is not a complete SVG document")
	}
}
