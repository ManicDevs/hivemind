//go:build !sim

// The release half of the simulator seam.
//
// A release world has no adversarial matrix, no synthetic attack, and no
// simulated verdict. The gate flags below do not exist, so a script that passes
// them fails loudly on an unknown flag rather than quietly getting a pass.

package main

import "time"

// simOptions is inert in a release build: preflight is always false, so main()
// starts the mesh exactly as it always has.
type simOptions struct {
	preflight bool
	duration  time.Duration
	seed      uint64
}

var simOpts simOptions

// registerSimFlags registers nothing.
func registerSimFlags() {}

// simPreflight cannot fail, because in a release build it is never reached:
// simOpts.preflight is permanently false. It exists only so the call site in
// main() compiles in both builds.
func simPreflight(dur time.Duration, seed uint64) error { return nil }
