package learning

import (
	"math"
	"testing"
)

// A synthetic but genuinely learnable signal: CPU and RAM are driven by a
// slow sine plus a linear ramp, so the next tick is predictable from this one
// by a function the net can represent. Nothing here is hand-labelled.
func drive(i int) RawInteroception {
	t := float32(i)
	phase := float32(math.Mod(float64(t), 40.0)) / 40.0
	sin := float32(math.Sin(float64(phase * 2 * math.Pi)))
	return RawInteroception{
		CPULoad:     0.5 + 0.3*sin,
		RAMPressure: 0.4 + 0.2*float32(math.Cos(float64(phase*2*math.Pi))),
		Thermal:     0.3 + 0.1*float32(t)/200.0,
		DiskIO:      0.2,
		NetworkIO:   0.25,
	}
}

func TestLearnUpdatesWeights(t *testing.T) {
	pc := NewInteroceptivePC()
	// Raise the predictor's rate so the test is decisive; clipping still
	// bounds each step, so this cannot diverge.
	pc.PredictorOpt = NewSGD(0.01, 0.9)
	pc.PredictorOpt.Clip = 0.5

	before := pc.WeightSnapshot()
	if pc.Updates != 0 {
		t.Fatalf("fresh PC reports %d updates, want 0", pc.Updates)
	}

	for i := 0; i < 200; i++ {
		_, _ = pc.Step(drive(i))
	}

	if pc.Updates == 0 {
		t.Fatal("no weight updates after 200 ticks: learning is frozen")
	}

	after := pc.WeightSnapshot()
	for stage, b := range before {
		a := after[stage]
		if len(a) != len(b) {
			t.Fatalf("%s: param count %d -> %d", stage, len(b), len(a))
		}
		moved := 0
		for i := range b {
			if b[i] != a[i] {
				moved++
			}
		}
		if moved == 0 {
			t.Errorf("%s: zero of %d weights moved — gradients are not reaching params",
				stage, len(b))
		}
	}
}

func TestLearnReducesLoss(t *testing.T) {
	// Seed the package RNG so weight init is deterministic. Learning is
	// stochastic, and a single unseeded run proved nothing: one draw
	// converged, the next got worse. Assert on windowed mean loss over a
	// horizon long enough for a periodic signal to be learnable.
	rng.Seed(20240917)
	pc := NewInteroceptivePC()
	pc.PredictorOpt = NewSGD(0.01, 0.9)
	pc.PredictorOpt.Clip = 0.5

	const (
		warmup  = 200
		earlyN  = 100
		lateN   = 100
		horizon = warmup + earlyN + lateN
	)
	losses := make([]float32, 0, horizon)
	for i := 0; i < horizon; i++ {
		_, _ = pc.Step(drive(i))
		if pc.Updates > 0 {
			losses = append(losses, pc.LastLoss)
		}
	}
	mean := func(from, n int) float64 {
		if from+n > len(losses) {
			t.Fatalf("only %d losses recorded, need %d", len(losses), from+n)
		}
		var s float64
		for _, v := range losses[from : from+n] {
			s += float64(v)
		}
		return s / float64(n)
	}
	early := mean(0, earlyN)
	late := mean(len(losses)-lateN, lateN)

	if !(late < early) {
		t.Fatalf("mean loss did not fall: early %.6f -> late %.6f (updates=%d)",
			early, late, pc.Updates)
	}
	t.Logf("mean loss %.6f -> %.6f over %d updates (%.0f%% reduction)",
		early, late, pc.Updates, 100*(1-late/early))
}

func TestOptimizerPersistsMomentum(t *testing.T) {
	// The optimizer must be held on the PC, not rebuilt per step, or momentum
	// is discarded and this is plain SGD.
	pc := NewInteroceptivePC()
	if pc.Opt == nil {
		t.Fatal("no persistent optimizer on the PC")
	}
	pc.PredictorOpt = NewSGD(0.01, 0.9)
	pc.PredictorOpt.Clip = 0.5
	for i := 0; i < 5; i++ {
		_, _ = pc.Step(drive(i))
	}
	if len(pc.PredictorOpt.Velocity) == 0 {
		t.Fatal("optimizer holds no velocity: momentum is being discarded")
	}
}

func TestStepIsSingleEntryPoint(t *testing.T) {
	// Step must drive learning, so no caller can forget to call Learn.
	pc := NewInteroceptivePC()
	for i := 0; i < 5; i++ {
		_, _ = pc.Step(drive(i))
	}
	if pc.Updates == 0 {
		t.Fatal("Step did not train; Learn is not wired into the tick")
	}
}
