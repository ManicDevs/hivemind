//go:build sim

// The adversarial launch gate, development builds only.
//
// `make build-release` does not set the sim tag, so a release world has no
// matrix in it and cannot produce a simulated verdict. The gate is a
// development instrument that happens to be able to block a deployment; it is
// never a source of reported data.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"gitlab.torproject.org/cerberus-droid/hivemind/internal/fabricsim"
)

// simOptions is the world binary's simulator surface. The release build carries
// an inert version of the same type, so main() is written once.
type simOptions struct {
	preflight bool
	duration  time.Duration
	seed      uint64
}

var simOpts simOptions

func registerSimFlags() {
	flag.BoolVar(&simOpts.preflight, "sim-preflight", false,
		"run the 56-node adversarial matrix as a launch gate and exit "+
			"(non-zero if the defence does not hold); no mesh is started")
	flag.DurationVar(&simOpts.duration, "sim-preflight-duration", 20*time.Second,
		"how long the preflight adversarial run lasts")
	flag.Uint64Var(&simOpts.seed, "sim-preflight-seed", 0,
		"preflight seed (0 = fixed default, so the gate is reproducible)")
}

// simPreflight runs the adversarial continental matrix and reports whether the
// defence held. It is the launch gate for a world deployment: the criteria are
// the properties that actually matter operationally, and a breach of any of
// them stops the deployment rather than scrolling past.
func simPreflight(dur time.Duration, seed uint64) error {
	if seed == 0 {
		// A fixed default so the gate is reproducible: a launch decision that
		// depended on an unseeded random draw would not be a gate.
		seed = 0x6869766d
	}
	eng := fabricsim.New(fabricsim.Config{
		Seed:            seed,
		Workers:         4,
		AttackerWorkers: 6,
		TickInterval:    250 * time.Millisecond,
		LegitRate:       24,
		AttackRate:      40,
		Duration:        dur,
		Out:             os.Stdout,
		EnableRoleSwap:  true,
	})
	ctx, cancel := context.WithTimeout(context.Background(), dur+10*time.Second)
	defer cancel()
	if err := eng.Run(ctx); err != nil && err != context.DeadlineExceeded {
		return err
	}
	s := eng.Snapshot()
	fmt.Println()
	fmt.Println("PRE-FLIGHT GATE")
	fmt.Printf("  honest frames pass        %.2f%%\n", s.ConsensusRate*100)
	fmt.Printf("  hostile frames refused    %.2f%%\n", s.Recall*100)
	fmt.Printf("  no honest frame refused   %.2f%%\n", s.Precision*100)
	// Reported because it is the cost of the tolerance, not a defect: these
	// clock-drift frames claimed a skew inside the accepted key window and were
	// served, because refusing them would break tolerance for ordinary drift.
	fmt.Printf("  drift served in-window    %d frames\n", s.DriftInWindow)
	fmt.Printf("  loss detection latency    p50=%dms p99=%dms\n",
		int(s.GapP50Ms+0.5), int(s.GapP99Ms+0.5))
	fmt.Printf("  quarantined identities    %d defender / %d attacker\n", s.QuarDef, s.QuarAtk)
	fmt.Printf("  fail-closed engaged       %v (%s)\n", s.Sealed, s.SealWhy)
	fmt.Printf("  disk writes               %d\n", s.DiskWrites)

	var fail []string
	// Honest traffic must not be collateral damage. A defence that stops the
	// attack by stopping everything is not a defence.
	if s.ConsensusRate < 0.99 {
		fail = append(fail, fmt.Sprintf("consensus %.2f%% below 99%%", s.ConsensusRate*100))
	}
	if s.FalsePos > 0 {
		fail = append(fail, fmt.Sprintf("%d honest frames refused", s.FalsePos))
	}
	// The attack must actually be stopped. Recall covers the vectors where
	// refusing an individual frame is the correct outcome: a structural overrun
	// and a broken signature. A flood is caught by loss detection and a drift
	// inside the key window is traffic the mesh must serve, so neither belongs
	// in this figure.
	if s.Recall < 0.90 {
		fail = append(fail, fmt.Sprintf("recall %.2f%% below 90%%", s.Recall*100))
	}
	// Fail-closed must engage; an attack that never trips the seal is an
	// attack the mesh would keep serving.
	if !s.Sealed {
		fail = append(fail, "fail-closed seal never engaged")
	}
	if s.DiskWrites != 0 {
		fail = append(fail, fmt.Sprintf("%d disk writes", s.DiskWrites))
	}
	if len(fail) > 0 {
		return errors.New("defence did not hold: " + strings.Join(fail, "; "))
	}
	fmt.Println("  RESULT: defence held")
	return nil
}
