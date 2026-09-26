// Command derive prints the live v2 key hierarchy through the exact
// runtime path the relay uses, so rotation is observable rather than
// assumed. Built with the same baked root as the mesh, its output must
// match the running gaze's leaf/hour/day values — a live, independent
// cross-check, and the quickest way to confirm two hosts agree on the bus
// while holding different identity keys.
package main

import (
	"fmt"

	hm "gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind"
)

func main() {
	info := hm.CurrentKeyTier()
	fmt.Printf("tier=%s\n", info.Tier)
	fmt.Printf("roots=%d source=%s host_bound=%t\n", info.Roots, info.Source, info.HostBound)
	fmt.Printf("leaf_index=%d window_minutes=%d\n", info.Leaf, info.Window)
	fmt.Printf("namespace=%s\n", hm.Namespace())
	fmt.Printf("leaf_key=%s\n", hm.CurrentLeafKeyHex())
	fmt.Printf("hour_key=%s\n", hm.CurrentHourKeyHex())
	fmt.Printf("day_root=%s\n", hm.CurrentDayRootHex())
	fmt.Printf("host_key=%s\n", hm.HostKeyHex())
}
