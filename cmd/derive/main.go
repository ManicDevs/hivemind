// Command derive prints THIS machine's live rolling hour key and TODAY's
// TOTD root through the exact runtime path. Built with the same baked
// relay key as the mesh, its output must equal the running gaze's
// hour_key/day_root — a live, independent cross-check of rotation.
package main

import (
	"fmt"

	hm "gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind"
)

func main() {
	fmt.Printf("hour_key=%s\n", hm.CurrentHourKeyHex())
	fmt.Printf("day_root=%s\n", hm.CurrentDayRootHex())
}
