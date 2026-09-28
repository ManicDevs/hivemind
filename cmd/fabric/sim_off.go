//go:build !sim

// The release half of the simulator seam.
//
// A release binary contains this file and not sim_dev.go. Because sim_dev.go is
// the only thing that imports internal/fabricsim, a release build links no part
// of the simulator: no simulated nodes, no synthetic attacks, no modelled
// latency. Every number the node reports comes from its own real socket.
//
// The functions here mirror the method set of simOptions exactly, so main() and
// the traffic loop are written once and compile unchanged in both builds.

package main

import "context"

// simOptions is a release-build no-op.
//
// The struct is kept so that the flags' absence and the call sites' presence are
// the same in both builds; main() does not need to know which one it is in.
type simOptions struct {
	// topology exists so that main() can read simOpts.topology in either build.
	// It is always false here: the release binary has no matrix to print.
	topology bool
}

var simOpts simOptions

// registerSimFlags registers nothing.
//
// There is deliberately no silent acceptance of -sim or FABRIC_SIM here. A
// release binary has no simulator, so accepting the flag and doing nothing would
// be a lie about what the process is doing; instead the flags simply do not
// exist, and a script that passes them fails loudly on an unknown flag.
func registerSimFlags() {}

// printSimTopology reports that the capability is absent.
func printSimTopology() {}

// startFabricSim does nothing, and installs no hooks. f.simGate and f.simWait
// stay nil, so the traffic loop and the shutdown path are untouched.
func startFabricSim(ctx context.Context, f *fabric, self NodeID) {}
