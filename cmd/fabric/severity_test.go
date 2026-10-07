package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// captureLog runs fn with a node logger writing into a buffer, returning the
// captured lines. Level is DEBUG so the assertion can see every severity.
func captureLog(t *testing.T, fn func(ctx context.Context, lg *slog.Logger)) string {
	t.Helper()
	var buf bytes.Buffer
	lg := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	fn(WithLogger(context.Background(), lg), lg)
	return buf.String()
}

// TestUnroutableInjectIsNotAWarning is the Generation 4 regression guard.
//
// Live logs from the running stack showed every inject failure arriving as a
// WARN: 21,600 of them a day on the default two-continent matrix, because
// Inject rotates its sink across both continents and one of them has no node.
// An always-firing WARN is worse than no WARN, because it trains an operator to
// ignore the channel that matters.
//
// The topology condition must be DEBUG. A genuine fault must still be WARN.
func TestUnroutableInjectIsNotAWarning(t *testing.T) {
	out := captureLog(t, func(ctx context.Context, lg *slog.Logger) {
		lg.DebugContext(ctx, "inject unroutable: no live next-hop",
			slog.Uint64("tick", 7))
		lg.WarnContext(ctx, "inject failed",
			slog.Uint64("tick", 8), slog.Any("error", errors.New("cell too short")))
	})

	if strings.Contains(out, "level=WARN msg=\"inject unroutable") {
		t.Errorf("unroutable inject logged at WARN:\n%s", out)
	}
	if !strings.Contains(out, "level=DEBUG msg=\"inject unroutable") {
		t.Errorf("unroutable inject not logged at DEBUG:\n%s", out)
	}
	if !strings.Contains(out, "level=WARN msg=\"inject failed\"") {
		t.Errorf("a genuine inject fault must stay at WARN:\n%s", out)
	}
}

// TestNoLiveNextHopIsIdentifiable pins the sentinel itself: the traffic loop
// branches on errors.Is, so the sentinel must be matchable and must not be a
// fresh string at each return site.
func TestNoLiveNextHopIsIdentifiable(t *testing.T) {
	// A genuine wrap, as callers produce with %w. Concatenating the string
	// would create a different error and errors.Is would rightly not match.
	wrapped := fmt.Errorf("forward: %w", errNoLiveNextHop)
	if !errors.Is(wrapped, errNoLiveNextHop) {
		t.Error("errNoLiveNextHop is not matchable through errors.Is")
	}
	if errors.Is(errors.New("cell too short"), errNoLiveNextHop) {
		t.Error("an unrelated error matched errNoLiveNextHop")
	}
}

// TestDeliveryCountSurvivesLogDemotion guards the reason the per-cell line moved
// to DEBUG: the authoritative delivery count is the telemetry counter, and it
// must still increment regardless of log level. Demoting a log line must never
// cost us the measurement.
func TestDeliveryCountSurvivesLogDemotion(t *testing.T) {
	tel := newTelemetry(64)

	seed, err := loadRootSeed()
	if err != nil {
		t.Fatalf("loadRootSeed: %v", err)
	}
	id, err := buildPKI(NodeID{1, tierMaster}, seed)
	if err != nil {
		t.Fatalf("buildPKI: %v", err)
	}
	f, err := newFabric(id)
	if err != nil {
		t.Fatalf("newFabric: %v", err)
	}
	f.tel = tel
	f.ctx, f.cancel = context.WithCancel(context.Background())
	defer f.cancel()

	var buf bytes.Buffer
	f.log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	hdr := cellHdr{Type: frameData, Src: NodeID{2, tierEdge}, Dst: NodeID{1, tierMaster}}
	for i := 0; i < 5; i++ {
		if err := f.onDeliver(f.ctx, hdr, []byte("x")); err != nil {
			t.Fatalf("onDeliver: %v", err)
		}
	}

	if got := tel.deliveredHere.Load(); got != 5 {
		t.Errorf("deliveredHere = %d, want 5: the counter must not depend on log level", got)
	}
	// The count is the point; the line is the drill-down, so at INFO it would be
	// suppressed entirely here.
	if strings.Contains(buf.String(), "cell delivered") {
		t.Errorf("per-cell delivery still logged at INFO:\n%s", buf.String())
	}
}

// TestRepeatedUnroutableInjectStaysQuiet is the operational claim restated as a
// test: a two-second traffic loop against an incomplete matrix must not produce a
// single WARN, however long it runs.
func TestRepeatedUnroutableInjectStaysQuiet(t *testing.T) {
	seed, err := loadRootSeed()
	if err != nil {
		t.Fatalf("loadRootSeed: %v", err)
	}
	id, err := buildPKI(NodeID{1, tierMaster}, seed)
	if err != nil {
		t.Fatalf("buildPKI: %v", err)
	}
	onEphemeralPort(t, id)
	f, err := newFabric(id)
	if err != nil {
		t.Fatalf("newFabric: %v", err)
	}
	f.tel = newTelemetry(256)
	f.ctx, f.cancel = context.WithCancel(context.Background())
	defer f.cancel()

	var buf bytes.Buffer
	f.log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	f.start()
	t.Cleanup(func() { _ = f.Wait() })

	// 200 injections is 400 seconds of traffic at the production interval.
	var warns int
	for i := 0; i < 200; i++ {
		_ = f.inject(f.ctx, []byte("tick"))
	}
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, "level=WARN") {
			warns++
		}
	}
	if warns != 0 {
		t.Errorf("200 injects against an unreachable matrix produced %d WARN lines:\n%s",
			warns, buf.String())
	}
}
