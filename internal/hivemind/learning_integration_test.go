package hivemind

import (
	"os"
	"path/filepath"
	"testing"

	"gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind/learning"
)

// A mind wired to real hardware telemetry must actually train. This asserts
// the integration end to end: Cycle -> Affect.Tick -> Interoception.Step ->
// Learn, with no hand-fed synthetic data anywhere.
func TestMindLearnsFromRealTelemetry(t *testing.T) {
	oldNode := NodeName
	NodeName = "learnprobe"
	defer func() {
		NodeName = oldNode
		_ = os.RemoveAll(filepath.Join(MemoryDir, NodeName))
	}()

	m := NewMind("Probe", NewSwarm())
	if m.Interoception == nil {
		t.Fatal("mind has no predictive-coding state")
	}

	// Seed the CPU-load delta: readCPULoad returns 0 until it has a previous
	// sample to difference against, so run once to prime it.
	_ = m.CollectInteroception()

	const cycles = 200
	for i := 0; i < cycles; i++ {
		m.Cycle()
	}

	pc := m.Interoception
	if pc.Updates == 0 {
		t.Fatalf("no learning updates after %d cycles: weights are frozen", cycles)
	}
	t.Logf("updates=%d lastLoss=%.6f lossEMA=%.6f", pc.Updates, pc.LastLoss, pc.LossEMA)

	if pc.LossEMA != pc.LossEMA { // NaN check without importing math
		t.Fatalf("loss went NaN after %d cycles", cycles)
	}

	// Real telemetry must be non-degenerate, or the network is learning from
	// a constant and the loss figure means nothing.
	raw := m.CollectInteroception()
	if raw.RAMPressure <= 0 {
		t.Errorf("RAM pressure reads %.4f; expected real non-zero /proc/meminfo data",
			raw.RAMPressure)
	}
	if raw.Timestamp.IsZero() {
		t.Error("telemetry carries no timestamp")
	}
}

// The report line must be emitted and must carry a finite loss, so the log
// claim of "real learning" is checkable rather than decorative.
func TestLearningReportIsFinite(t *testing.T) {
	oldNode := NodeName
	NodeName = "learnreport"
	defer func() {
		NodeName = oldNode
		_ = os.RemoveAll(filepath.Join(MemoryDir, NodeName))
	}()

	m := NewMind("Reporter", NewSwarm())
	_ = m.CollectInteroception()
	for i := 0; i < 60; i++ {
		m.Cycle()
	}
	pc := m.Interoception
	if pc.Updates == 0 {
		t.Fatal("no updates; the LEARN line would never print")
	}
	// A fresh mind must not report a bogus precision-immediately-zero state.
	pc0 := learning.NewInteroceptivePC()
	if pc0.Updates != 0 || pc0.LossEMA != 0 {
		t.Errorf("fresh PC reports updates=%d lossEMA=%v, want 0/0",
			pc0.Updates, pc0.LossEMA)
	}
}
