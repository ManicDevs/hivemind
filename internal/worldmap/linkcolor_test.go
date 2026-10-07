package worldmap

import (
	"strings"
	"testing"
)

// TestLinkColorForIsStable pins the property that makes per-instance link
// colours useful: a node must resolve to the same colour on every render.
//
// Without this, a viewer cannot learn that "the purple line is na-toronto" —
// the whole point of tinting per instance instead of leaving every link the
// same shade.
func TestLinkColorForIsStable(t *testing.T) {
	names := []string{
		"na-toronto", "na-brooklyn", "eu-berlin", "eu-paris",
		"sa-paulo", "as-tokyo", "af-cape-town", "an-mcmurdo", "oc-sydney",
	}
	for _, n := range names {
		first := linkColorFor(n)
		for i := 0; i < 100; i++ {
			if got := linkColorFor(n); got != first {
				t.Fatalf("linkColorFor(%q) changed between calls: %q then %q", n, first, got)
			}
		}
	}
}

// TestLinkColorForDistributesNodes verifies the palette is actually exercised:
// nine distinct nodes should not collapse onto one or two colours, which would
// mean the tinting adds no information.
func TestLinkColorForDistributesNodes(t *testing.T) {
	names := []string{
		"na-toronto", "na-brooklyn", "eu-berlin", "eu-paris",
		"sa-paulo", "as-tokyo", "af-cape-town", "an-mcmurdo", "oc-sydney",
	}
	seen := make(map[string]int)
	for _, n := range names {
		seen[linkColorFor(n)]++
	}
	if len(seen) < 3 {
		t.Errorf("only %d distinct link colours across %d nodes: %v", len(seen), len(names), seen)
	}
}

// TestLinkColorForAlwaysReturnsPaletteEntry guards against a hash falling
// outside the palette, which would emit a malformed or empty stroke.
func TestLinkColorForAlwaysReturnsPaletteEntry(t *testing.T) {
	inPalette := make(map[string]bool, len(linkPalette))
	for _, c := range linkPalette {
		inPalette[c] = true
	}
	for _, n := range []string{"", "a", "eu-berlin", "an-master-mcmurdo", strings.Repeat("x", 500)} {
		c := linkColorFor(n)
		if !inPalette[c] {
			t.Errorf("linkColorFor(%q) = %q, which is not in the palette", n, c)
		}
	}
}

// TestLinkColorsDifferFromBrokerTeal guards the two colour languages from
// colliding. Broker arcs are teal (#2dd4bf) and mean "MQTT infrastructure"; a
// node link in the same hue would read as a broker arc.
func TestLinkColorsDifferFromBrokerTeal(t *testing.T) {
	for _, c := range linkPalette {
		if c == brokerLiveColor {
			t.Errorf("link palette contains the broker teal %q; node links and broker arcs would be indistinguishable", c)
		}
	}
}

// TestRenderSVGAppliesPerInstanceLinkColors is the integration check: rendered
// output must actually carry more than one link stroke colour.
func TestRenderSVGAppliesPerInstanceLinkColors(t *testing.T) {
	nodes := []NodeView{
		{Name: "na-toronto", Continent: "na", Role: "peer", Alive: true},
		{Name: "na-brooklyn", Continent: "na", Role: "peer", Alive: true},
		{Name: "eu-berlin", Continent: "eu", Role: "peer", Alive: true},
		{Name: "eu-paris", Continent: "eu", Role: "peer", Alive: true},
		{Name: "sa-paulo", Continent: "sa", Role: "peer", Alive: true},
	}
	// links is the set of node names that get a great-circle path to the hub.
	links := []string{"na-toronto", "na-brooklyn", "eu-berlin", "eu-paris", "sa-paulo"}
	svg := RenderSVG(nodes, links, "gaze-test", "2026-01-01T00:00:00Z", "")

	used := make(map[string]int)
	for _, c := range linkPalette {
		used[c] = strings.Count(svg, `stroke="`+c+`"`)
	}
	distinct := 0
	for _, n := range used {
		if n > 0 {
			distinct++
		}
	}
	if distinct < 2 {
		t.Errorf("rendered map used %d distinct link colours, want >= 2: %v", distinct, used)
	}
	// The old fixed link colour must be gone from link paths.
	if strings.Contains(svg, `stroke="#3a6aaf" stroke-width="1.3" stroke-opacity="0.75"`) {
		t.Error("rendered map still contains the old fixed link stroke")
	}
}

// TestNodeTypeFieldIsUnused documents that the NodeView.Type field carries no
// semantics today.
//
// The broker/MQTT ("mgtt") colour language is owned by the infra layer
// (brokerLiveColor / brokerIdleColor / brokerDeadColor), which is the correct
// owner because those colours encode live/idle/dead *transport* state. A second
// parallel node-type colour mechanism would duplicate that and could drift from
// it, so Type is reserved for a future use rather than half-wired now.
func TestNodeTypeFieldIsUnused(t *testing.T) {
	n := NodeView{Name: "x", Continent: "eu", Role: "peer", Type: "broker", Alive: true}
	if n.Type != "broker" {
		t.Fatalf("Type field does not round-trip: %q", n.Type)
	}
	// The reserved field must not leak into the rendered map: it has no
	// rendering semantics, so it must not appear as a broker arc colour.
	svg := RenderSVG([]NodeView{n}, []string{}, "g", "2026-01-01T00:00:00Z", "")
	if strings.Contains(svg, `stroke="`+brokerLiveColor+`"`) {
		t.Error("a NodeView.Type of \"broker\" produced a broker arc; Type is reserved and must not render")
	}
}
