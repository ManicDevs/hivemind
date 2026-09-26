package main

// Static SVG world map: delegates to the shared worldmap renderer so
// the live dashboard, /map.svg, and the persisted world.svg all paint
// the exact same geographic world — coastlines, great-circle links,
// day/night terminator, real city coordinates.

import (
	"gitlab.torproject.org/cerberus-droid/hivemind/internal/worldmap"
)

// toWorldNodes converts the gaze's local nodeView into the shared
// worldmap render model — identical JSON shape, one conversion seam.
func toWorldNodes(in []nodeView) []worldmap.NodeView {
	out := make([]worldmap.NodeView, 0, len(in))
	for _, n := range in {
		out = append(out, worldmap.NodeView{
			Name:      n.Name,
			Continent: n.Continent,
			Role:      n.Role,
			Alive:     n.Alive,
			Lives:     n.Lives,
			Fitness:   n.Fitness,
			Pain:      n.Pain,
			NHearts:   n.NHearts,
		})
	}
	return out
}

// renderSVG paints one world map image from a snapshot via the shared engine.
// It is the single entry point for map rendering in the gaze, so the broker
// mesh is wired in exactly one place and cannot drift from the other call
// sites.
func renderSVG(nodes []nodeView, links []string, gazeName, now, relayURL string, h worldmap.RelayHealth) string {
	return worldmap.RenderSVGWithInfra(toWorldNodes(nodes), links, gazeName, now, relayURL, h)
}
