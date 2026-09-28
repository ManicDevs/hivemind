package main

import (
	"strings"
	"testing"
	"time"
)

// TestDeliveredCountsEveryArrival pins the semantics that replaced the old
// delivery-rate figure.
//
// A sink receives cells injected by every node, so deliveredHere must count
// them all. The count is not divided by anything: the protocol never
// acknowledges a cell back to its origin, so a sender cannot know whether its
// own cell arrived, and any ratio of the two numbers would be fiction.
func TestDeliveredCountsEveryArrival(t *testing.T) {
	tel := newTelemetry(64)
	// Two sinks each receive four cells, from four different origins.
	for sink := 0; sink < 2; sink++ {
		for origin := 0; origin < 4; origin++ {
			tel.recordDelivered()
		}
	}
	if got := tel.deliveredHere.Load(); got != 8 {
		t.Errorf("deliveredHere = %d, want 8", got)
	}
	// injected stays independent of arrivals.
	if got := tel.injected.Load(); got != 0 {
		t.Errorf("injected = %d, want 0: delivering a cell is not injecting one", got)
	}
}

// TestReportHasNoDeliveryRatio is a regression guard for a figure that was
// actively misleading.
//
// An earlier version printed delivery=50.00% on a mesh where every
// cross-continent cell had arrived, because it divided one node's injections by
// a count gathered elsewhere. A percentage that cannot exceed the truth is worse
// than no percentage, so none is printed.
func TestReportHasNoDeliveryRatio(t *testing.T) {
	tel := newTelemetry(64)
	tel.recordInjected()
	tel.recordInjected()
	tel.recordDelivered()
	line := tel.Report()
	if strings.Contains(line, "delivery=") {
		t.Errorf("report reintroduced a delivery ratio: %q", line)
	}
	if !strings.Contains(line, "injected=2") || !strings.Contains(line, "delivered_here=1") {
		t.Errorf("report does not carry both raw counts: %q", line)
	}
}

// TestNewTelemetryClampsCapacity documents the 64-sample floor. Without it a
// small requested window would make percentiles meaningless.
func TestNewTelemetryClampsCapacity(t *testing.T) {
	if got := len(newTelemetry(8).ring); got != 64 {
		t.Errorf("ring capacity = %d, want the floor of 64", got)
	}
	if got := len(newTelemetry(512).ring); got != 512 {
		t.Errorf("ring capacity = %d, want the requested 512", got)
	}
}

// TestReportIsHonestBeforeAnyTraffic checks the report does not present the
// absence of data as a healthy measurement. A node with no samples must say so.
func TestReportIsHonestBeforeAnyTraffic(t *testing.T) {
	tel := newTelemetry(64)
	line := tel.Report()
	if !strings.Contains(line, "no samples yet") {
		t.Errorf("report with no RTT samples does not say so: %q", line)
	}
	if !strings.HasPrefix(line, "[real]") {
		t.Errorf("report is not marked as real data: %q", line)
	}
	if !strings.Contains(line, "link_rtt") {
		t.Errorf("report does not label its latency as link RTT: %q", line)
	}

}

// TestPercentilesOverBoundedRing checks the rolling window, including the
// wrap-around that a long-running node will hit.
func TestPercentilesOverBoundedRing(t *testing.T) {
	const capacity = 64
	tel := newTelemetry(capacity)
	if _, _, _, _, _, n := tel.percentiles(); n != 0 {
		t.Errorf("empty collector reported %d samples", n)
	}

	// Enough samples to wrap the ring several times.
	const total = 500
	for i := 1; i <= total; i++ {
		tel.observeRTT(time.Duration(i) * time.Millisecond)
	}
	p50, p95, p99, lo, hi, n := tel.percentiles()
	if n != capacity {
		t.Errorf("sample count = %d, want the ring capacity %d", n, capacity)
	}
	// Percentiles come from the bounded window, so the oldest samples must
	// have scrolled out: a p50 near 1ms would mean the whole 1..500 history is
	// still in play.
	if p50 < float64(total-capacity) {
		t.Errorf("p50 = %v, want it drawn from the most recent %d of 1..%d; "+
			"the ring is not discarding old samples", p50, capacity, total)
	}
	if p99 < p50 {
		t.Errorf("p99 = %v is below p50 = %v", p99, p50)
	}
	// min and max are lifetime extremes by design, so they span the full range.
	if lo != 1 {
		t.Errorf("lifetime min = %v, want 1", lo)
	}
	if hi != float64(total) {
		t.Errorf("lifetime max = %v, want %d", hi, total)
	}
	if !(p50 <= p95 && p95 <= p99) {
		t.Errorf("percentiles out of order: p50=%v p95=%v p99=%v", p50, p95, p99)
	}
}

// TestObserveRTTIgnoresNegative guards against a clock jump poisoning the
// latency distribution with a nonsensical sample.
func TestObserveRTTIgnoresNegative(t *testing.T) {
	tel := newTelemetry(64)
	tel.observeRTT(-5 * time.Millisecond)
	if _, _, _, _, _, n := tel.percentiles(); n != 0 {
		t.Errorf("negative RTT was recorded; sample count = %d", n)
	}
}

// TestReportCountsMatchObservations cross-checks the rendered line against the
// underlying counters, so a field cannot be reported under the wrong label.
func TestReportCountsMatchObservations(t *testing.T) {
	tel := newTelemetry(64)
	for i := 0; i < 4; i++ {
		tel.recordInjected()
	}
	for i := 0; i < 3; i++ {
		tel.recordDelivered()
	}
	tel.recordInjectError()
	tel.recordAuthFailure()
	tel.recordProtoDrop()
	tel.recordProbeOK()
	tel.recordPeerAdded()
	tel.recordPeerForgot()
	tel.observeRTT(2 * time.Millisecond)

	line := tel.Report()
	for _, want := range []string{
		"injected=4", "delivered_here=3",
		"inject_err=1", "auth_fail=1", "proto_drop=1",
		"probes=1", "peers=+1/-1", "n=1",
		// The windowed/lifetime split has to be visible in the output itself,
		// not only in a comment, or the two kinds of figure read as one.
		"window=", "lifetime",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("report is missing %q\ngot: %s", want, line)
		}
	}
}
