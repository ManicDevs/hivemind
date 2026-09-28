//go:build sim

// This file is the simulator's wiring into the fabric node.
//
// The build tag is load-bearing: `make build-release` does not set it, so a
// release binary does not contain this file, does not import internal/fabricsim,
// and therefore does not contain a single byte of simulated node, simulated
// link or synthetic attack. The development targets that want the matrix build
// with `-tags sim` explicitly.

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"gitlab.torproject.org/cerberus-droid/hivemind/internal/fabricsim"
)

// simOptions holds the simulator's command-line surface.
//
// In a build without the `sim` tag this type still exists (see sim_off.go) with
// the same method set, so main() calls into it unconditionally and the flag set
// is identical in both builds.
type simOptions struct {
	topology bool
	run      bool
	duration time.Duration
	seed     uint64
	verbose  bool
	roleSwap bool
	attach   bool // true once the engine is running
}

var simOpts simOptions

// registerSimFlags adds the simulator flags. Called from main() so that the
// flags are declared in the same place as the rest, while only existing in
// builds that can actually honour them.
func registerSimFlags() {
	flag.BoolVar(&simOpts.topology, "sim-topology", false,
		"print the adversarial matrix model and exit")
	flag.BoolVar(&simOpts.run, "sim", false,
		"run the 56-node adversarial continental simulator alongside this node")
	flag.DurationVar(&simOpts.duration, "sim-duration", 0,
		"stop the simulator after this long (0 = run until signalled)")
	flag.Uint64Var(&simOpts.seed, "sim-seed", 0,
		"simulator seed (0 = derive from the node identity)")
	flag.BoolVar(&simOpts.verbose, "sim-verbose", false,
		"print a full scoreboard table every tick instead of a single scrolling line")
}

// printSimTopology prints the modelled geography and node layout without
// running anything.
func printSimTopology() {
	fmt.Println("HIVEMIND adversarial continental simulator — model (development build)")
	fmt.Println()
	fmt.Print(fabricsim.FormatTable())
	fmt.Println()
	fmt.Println("legend: RTT in milliseconds, derived from great-circle distance at")
	fmt.Println("        ~200,000 km/s in fibre with a 1.85 routing-inflation factor")
	fmt.Println("        calibrated to reproduce ~150 ms NA->EU and ~300 ms Antarctic.")
	fmt.Println()
	top := fabricsim.NewTopology()
	fmt.Printf("matrix: %d nodes = %d defender + %d attacker across %d zones x %d tiers\n",
		top.Len(), fabricsim.MatrixSize, fabricsim.MatrixSize, 7, 4)
	fmt.Print(top.FormatGrid())
	fmt.Println()
	fmt.Printf("key skew window: %.0f ms (a receiver retries the adjacent hour\n",
		fabricsim.SkewWindowMs())
	fmt.Println("within this window when deciding which hour a cell was sealed under)")
	fmt.Println()
	fmt.Println("NOTE: no simulated data is reported by a release build. A release")
	fmt.Println("      binary has no simulator in it and reports only measured traffic.")
}

// startFabricSim launches the matrix, if it was asked for.
//
// It installs two hooks on the node. The first lets the matrix suppress real
// traffic when it reaches its fail-closed seal; the second is the shutdown
// wait. Both are nil in a release build, where nothing steers the node but its
// own observations.
func startFabricSim(ctx context.Context, f *fabric, self NodeID) {
	if !simOpts.run && os.Getenv("FABRIC_SIM") != "1" {
		return
	}
	seed := simOpts.seed
	if seed == 0 {
		// Derive a stable default from the node identity so each node's traffic
		// pattern differs but is reproducible across restarts.
		seed = uint64(self.Continent)<<32 | uint64(self.Tier)<<16 | 0x9e37
	}
	eng := fabricsim.New(fabricsim.Config{
		Seed: seed,
		// These pools share 16 hardware threads with Ollama and the world, so
		// they are deliberately small.
		Workers:         envInt("FABRIC_SIM_WORKERS", 4),
		AttackerWorkers: envInt("FABRIC_SIM_ATTACK_WORKERS", 6),
		TickInterval:    time.Duration(envInt("FABRIC_SIM_TICK_MS", 250)) * time.Millisecond,
		LegitRate:       envInt("FABRIC_SIM_LEGIT", 24),
		AttackRate:      envInt("FABRIC_SIM_ATTACK", 40),
		Duration:        simOpts.duration,
		Out:             os.Stdout,
		Verbose:         simOpts.verbose,
		EnableRoleSwap:  simOpts.roleSwap || os.Getenv("FABRIC_SIM_ROLE_SWAP") == "1",
	})
	simOpts.attach = true
	f.simGate = func() bool { return eng.TrafficAllowed() }
	f.simWait = func() { eng.Wait(3 * time.Second) }

	go func() {
		if err := eng.Run(ctx); err != nil && err != context.Canceled {
			log.Printf("[sim] %v", err)
		}
	}()
	log.Printf("[sim] adversarial matrix enabled: %d nodes, seed=%d",
		eng.Topology().Len(), seed)
}
