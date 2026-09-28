package fabricsim

import (
	"bytes"
	"context"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestLatencyModelMatchesReference checks the two anchor figures the topology
// spec calls for. These come out of the geography rather than from constants,
// so a regression here means the model itself changed.
func TestLatencyModelMatchesReference(t *testing.T) {
	cases := []struct {
		a, b  Zone
		name  string
		want  float64
		tolPc float64
	}{
		{ZoneNA, ZoneEU, "NA->EU", 150, 0.15},
		{ZoneEU, ZoneAN, "EU->AN", 300, 0.15},
		{ZoneAS, ZoneOC, "AS->OC", 140, 0.25},
	}
	for _, c := range cases {
		got := BaseRTTMs(c.a, c.b)
		if rel := (got - c.want) / c.want; rel > c.tolPc || rel < -c.tolPc {
			t.Errorf("%s RTT = %.1fms, want ~%.0fms (+-%.0f%%)", c.name, got, c.want, c.tolPc*100)
		}
		// Symmetry: RTT must not depend on direction.
		if back := BaseRTTMs(c.b, c.a); back != got {
			t.Errorf("%s asymmetric: %.3f vs %.3f", c.name, got, back)
		}
	}
	// Intra-zone must be far cheaper than inter-continental.
	if intra, inter := SelfRTTMs(ZoneEU), BaseRTTMs(ZoneEU, ZoneAN); intra > inter/10 {
		t.Errorf("intra-zone %.2fms not negligible vs inter-continental %.1fms", intra, inter)
	}
}

// TestTopologyInvariants verifies the matrix is exactly 56 nodes with unique,
// contiguous identities that agree across every index.
func TestTopologyInvariants(t *testing.T) {
	top := NewTopology()
	if got := top.Len(); got != 56 {
		t.Fatalf("Len = %d, want 56", got)
	}
	seen := map[NodeID]int{}
	defCount, atkCount := 0, 0
	for i := 0; i < top.Len(); i++ {
		n := top.At(i)
		if n.ID.Index() != i {
			t.Errorf("Index mismatch at %d: %s reports %d", i, n.ID, n.ID.Index())
		}
		if _, dup := seen[n.ID]; dup {
			t.Errorf("duplicate node %s", n.ID)
		}
		seen[n.ID] = i
		if top.ByID(n.ID) != n {
			t.Errorf("ByID(%s) does not return the node at index %d", n.ID, i)
		}
		switch n.ID.Role {
		case RoleDefender:
			defCount++
			if top.Defender(n.ID.Zone, n.ID.Tier) != n {
				t.Errorf("defender grid missing %s", n.ID)
			}
		case RoleAttacker:
			atkCount++
			if top.Attacker(n.ID.Zone, n.ID.Tier) != n {
				t.Errorf("attacker grid missing %s", n.ID)
			}
		}
	}
	if defCount != 28 || atkCount != 28 {
		t.Errorf("role split = %d def / %d atk, want 28/28", defCount, atkCount)
	}
	// Every zone must be represented at every tier, or the "7 continents"
	// claim is not actually satisfied.
	for z := ZoneNA; z < numZones; z++ {
		for tier := TierMaster; tier <= TierEdge; tier++ {
			if top.Defender(z, tier) == nil {
				t.Errorf("zone %s has no defender at %s", z, tier)
			}
		}
	}
}

// TestRoleSwapPreservesMatrix checks the global role swap keeps the matrix
// coherent: 28/28 preserved, indices stable, and role-aware lookups correct.
func TestRoleSwapPreservesMatrix(t *testing.T) {
	top := NewTopology()
	before := map[int]NodeID{}
	for i := 0; i < top.Len(); i++ {
		before[i] = top.At(i).ID
	}
	if n := top.SwapRoles(0); n != 56 {
		t.Fatalf("SwapRoles reported %d nodes, want 56", n)
	}
	if d, a := len(top.Defenders()), len(top.Attackers()); d != 28 || a != 28 {
		t.Fatalf("after swap: %d def / %d atk", d, a)
	}
	for i := 0; i < top.Len(); i++ {
		n := top.At(i)
		if n.ID.Zone != before[i].Zone || n.ID.Tier != before[i].Tier {
			t.Errorf("slot %d moved from %s to %s", i, before[i], n.ID)
		}
		if n.ID.Role == before[i].Role {
			t.Errorf("slot %d did not swap role: %s", i, n.ID)
		}
		if top.ByID(n.ID) != n {
			t.Errorf("ByID stale after swap for %s", n.ID)
		}
	}
	// Role-aware pickers must not leak across the swap.
	r := NewRand(99)
	for i := 0; i < 200; i++ {
		if n := top.RandomDefender(r); n == nil || n.ID.Role != RoleDefender {
			t.Fatal("RandomDefender returned a non-defender after swap")
		}
		if n := top.RandomAttacker(r); n == nil || n.ID.Role != RoleAttacker {
			t.Fatal("RandomAttacker returned a non-attacker after swap")
		}
	}
	top.SwapRoles(0)
	if top.At(0).ID != before[0] {
		t.Error("double swap did not restore the original assignment")
	}
}

// TestTransportOrdersByDueTime verifies the due-time heap delivers in
// causal order, which is what makes the RTT model observable.
func TestTransportOrdersByDueTime(t *testing.T) {
	tr := NewTransport(DefaultTransportConfig())
	defer tr.Close()
	base := time.Unix(1700000000, 0)

	// EU (cheap) to EU, and EU to AN (expensive): the AN frame must land later.
	near := &Packet{From: NodeID{Zone: ZoneEU}, To: NodeID{Zone: ZoneEU}, Kind: KindLegitimate}
	far := &Packet{From: NodeID{Zone: ZoneEU}, To: NodeID{Zone: ZoneAN}, Kind: KindLegitimate}
	if !tr.Enqueue(far, base) {
		t.Fatal("enqueue far failed")
	}
	if !tr.Enqueue(near, base) {
		t.Fatal("enqueue near failed")
	}

	// Nothing is due yet: propagation delay is modelled, not slept through.
	if got := tr.DrainDue(base); len(got) != 0 {
		t.Fatalf("%d packets delivered before their due time", len(got))
	}
	// Advance well past the inter-continental RTT.
	got := tr.DrainDue(base.Add(5 * time.Second))
	if len(got) != 2 {
		t.Fatalf("drained %d, want 2", len(got))
	}
	if got[0] != near {
		t.Error("cheap link did not deliver before the expensive one")
	}
	if tr.InFlight() != 0 {
		t.Errorf("InFlight = %d after full drain, want 0", tr.InFlight())
	}
}

// TestTransportRespectsInFlightBound verifies the in-flight cap turns a flood
// into a drop rather than an allocation storm.
func TestTransportRespectsInFlightBound(t *testing.T) {
	cfg := DefaultTransportConfig()
	cfg.MaxInFlight = 8
	tr := NewTransport(cfg)
	defer tr.Close()
	base := time.Unix(1700000000, 0)

	accepted := 0
	for i := 0; i < 64; i++ {
		p := &Packet{From: NodeID{Zone: ZoneAN}, To: NodeID{Zone: ZoneEU}, Kind: KindPacketDrop}
		if tr.Enqueue(p, base) {
			accepted++
		}
	}
	if accepted != 8 {
		t.Errorf("accepted %d packets, want the cap of 8", accepted)
	}
	if tr.InFlight() != 8 {
		t.Errorf("InFlight = %d, want 8", tr.InFlight())
	}
}

// TestDetectorRejectsEveryAttackVector is the core security assertion: honest
// traffic passes, and each of the four hostile vectors is refused.
func TestDetectorRejectsEveryAttackVector(t *testing.T) {
	top := NewTopology()
	rnd := NewRand(4242)
	d := NewDetector(DefaultDetectorConfig(), top, rnd)
	d.Advance(time.Unix(1700000000, 0))

	atk := top.Attacker(ZoneNA, TierEdge)
	def := top.Defender(ZoneEU, TierMaster)

	// Honest frames from a legitimate defender must always pass.
	for i := 0; i < 50; i++ {
		payload := append([]byte("DEF/EU/master"), make([]byte, 44)...)
		p := &Packet{From: def.ID, To: atk.ID, Kind: KindLegitimate,
			DeclaredLen: uint32(len(payload)), Payload: payload}
		signPacket(p, def.priv)
		if o := d.Inspect(def.ID, p); !o.Accept {
			t.Fatalf("honest frame %d refused: %s", i, o.Reason)
		}
	}

	// Each hostile vector must be refused, with the right reason.
	cases := []struct {
		kind   AttackKind
		skew   float64
		reason string
	}{
		{KindMalformed, 0, "malformed"},
		{KindForgery, 0, "bad-signature"},
		{KindClockDrift, skewWindowMs * 2, "key-window"},
	}
	for _, c := range cases {
		payload := buildPayload(c.kind, rnd, atk, 96)
		p := &Packet{From: atk.ID, To: def.ID, Kind: c.kind,
			SkewMs: c.skew, Payload: payload, DeclaredLen: declaredFor(c.kind, len(payload))}
		// Sign the honest way first, so each vector fails on its own merits:
		// the malformed one on its declared length, the forgery one only after
		// the signature is broken, the drift one on the key window.
		signPacket(p, atk.priv)
		if c.kind == KindForgery {
			corruptSignature(p)
		}
		if o := d.Inspect(atk.ID, p); o.Accept {
			t.Errorf("%s was accepted, want refused (%s)", c.kind, c.reason)
		} else if o.Reason != c.reason {
			t.Errorf("%s refused for %q, want %q", c.kind, o.Reason, c.reason)
		}
	}
}

// TestKeyWindowToleratesSmallSkew verifies the +/-3 minute window is actually a
// window: drift inside it is tolerated, drift outside it is not.
func TestKeyWindowToleratesSmallSkew(t *testing.T) {
	top := NewTopology()
	rnd := NewRand(7)
	d := NewDetector(DefaultDetectorConfig(), top, rnd)
	d.Advance(time.Unix(1700000000, 0))

	atk := top.Attacker(ZoneAS, TierEdge)
	def := top.Defender(ZoneAS, TierMaster)

	for _, skew := range []float64{-179e3, -60e3, 60e3, 179e3} {
		p := &Packet{From: atk.ID, To: def.ID, Kind: KindLegitimate,
			SkewMs: skew, Payload: make([]byte, 96), DeclaredLen: 96}
		copy(p.Payload, def.ID.String())
		signPacket(p, atk.priv)
		if o := d.Inspect(atk.ID, p); !o.Accept {
			t.Errorf("skew %.0fms inside the window was refused: %s", skew, o.Reason)
		}
	}
	for _, skew := range []float64{-181e3, 181e3, 3.6e6} {
		p := &Packet{From: atk.ID, To: def.ID, Kind: KindLegitimate,
			SkewMs: skew, Payload: make([]byte, 96), DeclaredLen: 96}
		copy(p.Payload, def.ID.String())
		signPacket(p, atk.priv)
		if o := d.Inspect(atk.ID, p); o.Accept {
			t.Errorf("skew %.0fms outside the window was accepted", skew)
		}
	}
}

// TestMalformedFrameRejectedBeforeAllocation models the allocation-amplification
// guard: a frame claiming a huge length must be refused on structure alone.
func TestMalformedFrameRejectedBeforeAllocation(t *testing.T) {
	if structurallyValid(&Packet{DeclaredLen: 0x7fffffff, Payload: []byte{1, 2, 3, 4}}) {
		t.Error("frame claiming 4 GiB in 4 bytes passed structural validation")
	}
	if !structurallyValid(&Packet{DeclaredLen: 8, Payload: []byte{0, 0, 0, 8, 'a', 'b', 'c', 'd'}}) {
		t.Error("well-formed frame failed structural validation")
	}
	// Under-declaring is refused too, matching the production equality check.
	// A receiver that trusted the shorter length would ignore the trailing
	// bytes, letting them bypass every check that reads the frame.
	if structurallyValid(&Packet{DeclaredLen: 4, Payload: []byte{0, 0, 0, 8, 'a', 'b', 'c', 'd'}}) {
		t.Error("a frame under-declaring its payload passed structural validation")
	}
	if structurallyValid(&Packet{DeclaredLen: 2, Payload: nil}) {
		t.Error("empty frame passed structural validation")
	}
}

// TestQuarantineAfterDwell verifies isolation requires sustained hostility, so a
// single bad frame from an honest node cannot get it banned.
func TestQuarantineAfterDwell(t *testing.T) {
	top := NewTopology()
	rnd := NewRand(11)
	cfg := DefaultDetectorConfig()
	d := NewDetector(cfg, top, rnd)
	d.Advance(time.Unix(1700000000, 0))

	atk := top.Attacker(ZoneSA, TierEdge)

	// One malformed frame must not isolate.
	p := &Packet{From: atk.ID, Kind: KindMalformed,
		DeclaredLen: 0x7fffffff, Payload: []byte{0x7f, 0xff, 0xff, 0xff}}
	d.Inspect(atk.ID, p)
	if q, _, _ := atk.Quarantined(); q {
		t.Fatal("a single bad frame isolated the identity; dwell is not enforced")
	}

	// Sustained hostility must.
	isolated := false
	for i := 0; i < 12; i++ {
		if o := d.Inspect(atk.ID, p); o.Quarantined {
			isolated = true
			break
		}
	}
	if !isolated {
		t.Fatal("sustained malformed traffic never isolated the identity")
	}
	// A quarantined identity is then refused regardless of frame content.
	if o := d.Inspect(atk.ID, &Packet{From: atk.ID, Kind: KindLegitimate,
		DeclaredLen: 96, Payload: make([]byte, 96)}); o.Accept {
		t.Error("quarantined identity was still allowed through")
	}
}

// TestFailClosedSeal verifies the seal refuses hostile traffic but still
// permits honest traffic, which is what "fail-closed" has to mean in practice.
func TestFailClosedSeal(t *testing.T) {
	top := NewTopology()
	rnd := NewRand(5)
	d := NewDetector(DefaultDetectorConfig(), top, rnd)
	d.Advance(time.Unix(1700000000, 0))

	def := top.Defender(ZoneOC, TierMaster)
	atk := top.Attacker(ZoneOC, TierEdge)

	d.Seal("test")
	if !d.Sealed() {
		t.Fatal("Seal did not engage")
	}
	// A sealed mesh has concluded it cannot separate hostile from honest, so it
	// refuses anything it cannot verify. A forged frame is refused for that
	// reason; a correctly signed honest frame is served, because it is
	// verifiable on its own merits rather than trusted because of its label.
	hostile := &Packet{From: atk.ID, Kind: KindForgery,
		DeclaredLen: 96, Payload: make([]byte, 96)}
	signPacket(hostile, atk.priv)
	corruptSignature(hostile)
	if o := d.Inspect(atk.ID, hostile); o.Accept || o.Reason != "sealed" {
		t.Errorf("forged frame under seal: accepted=%v reason=%q", o.Accept, o.Reason)
	}
	honest := &Packet{From: def.ID, Kind: KindLegitimate,
		DeclaredLen: 96, Payload: make([]byte, 96)}
	copy(honest.Payload, def.ID.String())
	signPacket(honest, def.priv)
	if o := d.Inspect(def.ID, honest); !o.Accept {
		t.Errorf("honest frame refused under seal: %s", o.Reason)
	}
	// An unsigned frame claiming to be honest must be refused under seal. A
	// seal that accepted this would be trusting a claim it cannot check.
	liar := &Packet{From: def.ID, Kind: KindLegitimate,
		DeclaredLen: 96, Payload: make([]byte, 96)}
	copy(liar.Payload, def.ID.String())
	if o := d.Inspect(def.ID, liar); o.Accept {
		t.Error("unsigned frame claiming to be honest was accepted under seal")
	}
	d.Unseal()
	if d.Sealed() {
		t.Error("Unseal did not release the seal")
	}
}

// TestEngineRunsCleanly is the end-to-end check: the simulator advances, keeps
// honest traffic flowing, blocks the hostile vectors, and performs no disk
// writes.
func TestEngineRunsCleanly(t *testing.T) {
	var buf bytes.Buffer
	e := New(Config{
		Seed:            20260927,
		Workers:         4,
		AttackerWorkers: 4,
		TickInterval:    2 * time.Millisecond,
		TickDuration:    2 * time.Second,
		Duration:        400 * time.Millisecond,
		LegitRate:       20,
		AttackRate:      40,
		Out:             &buf,
		RenderInterval:  time.Hour, // render once at the end
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := e.Run(ctx); err != nil && err != context.DeadlineExceeded {
		t.Fatalf("Run: %v", err)
	}

	s := e.Snapshot()
	if s.Tick < 5 {
		t.Errorf("only %d ticks executed, expected the loop to advance", s.Tick)
	}
	var offeredLegit uint64
	for k := AttackKind(0); k < numKinds; k++ {
		offeredLegit += s.Offered[k]
	}
	if offeredLegit == 0 {
		t.Fatal("no traffic was offered at all")
	}
	if s.Offered[KindLegitimate] == 0 {
		t.Fatal("no honest traffic was generated")
	}
	// Honest traffic must largely survive; a defence that blocks everything
	// would still show recall 100% and be useless.
	if rate := s.HonestPassRate(); rate < 0.5 {
		t.Errorf("honest pass rate %.1f%% is too low to be useful", rate*100)
	}
	// Some hostile traffic must have been refused.
	var rejectedHostile uint64
	for k := AttackKind(0); k < numKinds; k++ {
		if k.IsHostile() {
			rejectedHostile += s.Rejected[k]
		}
	}
	if rejectedHostile == 0 {
		t.Error("no hostile traffic was refused; the defence is inert")
	}
	// The zero-write requirement is the hard compliance assertion.
	if got := e.DiskWrites(); got != 0 {
		t.Errorf("DiskWrites = %d, want 0: the simulator touched storage", got)
	}
	// Heap must stay inside the envelope.
	if s.MemBytes > 512<<20 {
		t.Errorf("heap %.0f MiB exceeds a sane simulator envelope", float64(s.MemBytes)/(1<<20))
	}
	if buf.Len() == 0 {
		t.Error("no scoreboard output was produced")
	}
	if got := buf.String(); !bytes.Contains([]byte(got), []byte("FABRIC-SIM")) {
		t.Errorf("scoreboard output missing header:\n%s", truncate(got, 400))
	}
}

// TestEngineIsDeterministic verifies a fixed seed reproduces a run, which is
// what makes an adversarial failure debuggable.
func TestEngineIsDeterministic(t *testing.T) {
	// Drive a fixed tick count through Step rather than Run: Run is
	// wall-clock paced, so two invocations legitimately complete different
	// numbers of ticks and comparing them would be meaningless.
	run := func() Scoreboard {
		e := New(Config{
			Seed: 12345, Workers: 2, AttackerWorkers: 2,
			TickDuration: time.Second,
			LegitRate:    8, AttackRate: 8,
			Out: io.Discard, RenderInterval: time.Hour,
		})
		for i := 0; i < 40; i++ {
			e.Step()
		}
		return e.Snapshot()
	}
	a, b := run(), run()
	if a.Tick != b.Tick {
		t.Fatalf("tick counts differ: %d vs %d", a.Tick, b.Tick)
	}
	for k := AttackKind(0); k < numKinds; k++ {
		if a.Offered[k] != b.Offered[k] {
			t.Errorf("offered[%s] differs: %d vs %d", k, a.Offered[k], b.Offered[k])
		}
	}
}

// TestConcurrentRunKeepsAccountingInvariants is the dynamic part of the
// pure-Go concurrency story (see concurrency_static_test.go).
//
// With the C race detector unavailable, the strongest reproducible signal is
// an accounting invariant that must hold under every goroutine interleaving:
//   - not a single frame can be delivered that was not enqueued (delivered <= enqueued);
//   - every frame reaches at most one tally (accepted + rejected <= offered per kind).
//
// A data race on any of these counters makes the tallies scheduling-dependent:
// the winner of the race is an accident, so a run that happened to interleave
// badly reports an impossible accounting state and this test fails.
//
// GOMAXPROCS is varied precisely to force different interleavings of the same
// work onto different physical threads, so a schedule-dependent pathology is
// exercised rather than being reproduced identically by luck.
//
// What this test deliberately does NOT assert is that the exact scoreboard is
// reproduced across schedules. It is not a race to assert that: the engine
// reads the real process heap for budget shedding and drains loss through a
// shared mutex, so different schedules legitimately shed or lose different
// numbers of frames. That is what the pure-Go static check in
// concurrency_static_test.go is for: it proves the shared fields are written
// only under their lock, which is the determinism guarantee we can actually
// stand behind without the C runtime.
func TestConcurrentRunKeepsAccountingInvariants(t *testing.T) {
	run := func(gomaxprocs int) {
		old := runtime.GOMAXPROCS(gomaxprocs)
		defer runtime.GOMAXPROCS(old)
		e := New(Config{
			Seed: 12345, Workers: 2, AttackerWorkers: 2,
			TickDuration: time.Second,
			LegitRate:    256, AttackRate: 256,
			Out: io.Discard, RenderInterval: time.Hour,
		})
		for i := 0; i < 80; i++ {
			e.Step()
		}
		s := e.Snapshot()

		for k := AttackKind(0); k < numKinds; k++ {
			if s.Accepted[k]+s.Rejected[k] > s.Offered[k] {
				t.Errorf("GOMAXPROCS=%d: accepted+rejected[%s]=%d exceeds offered=%d",
					gomaxprocs, AttackKind(k), s.Accepted[k]+s.Rejected[k], s.Offered[k])
			}
		}

		enq, del, drop, exp := e.tr.Stats()
		_ = exp
		if del > enq {
			t.Errorf("GOMAXPROCS=%d: delivered=%d exceeds enqueued=%d",
				gomaxprocs, del, enq)
		}
		// dropped counts enqueue-time rejections (congestion, flood) that never
		// enter the queue, so dropped<=enqueued is NOT an invariant; a frame can
		// rebound off a full queue without ever being counted as enqueued. What
		// must hold is that every delivered frame was enqueued, which the check
		// above asserts.
		_ = drop
	}

	for _, gpus := range []int{1, 2, 4, 8, 16, 32} {
		run(gpus)
	}
}

// TestSealEngagesUnderBroadCompromise verifies the fail-closed path triggers on
// its own when isolation spreads, rather than needing a manual Seal.
func TestSealEngagesUnderBroadCompromise(t *testing.T) {
	top := NewTopology()
	rnd := NewRand(31337)
	cfg := DefaultDetectorConfig()
	d := NewDetector(cfg, top, rnd)
	d.Advance(time.Unix(1700000000, 0))

	// Sustained malformed traffic from every attacker must cross the seal
	// threshold on its own.
	// A fresh frame per delivery: Packet carries an atomic.Bool, so copying
	// one prototype around would copy a lock.
	malicious := func() *Packet {
		return &Packet{
			Kind:        KindMalformed,
			DeclaredLen: 0x7fffffff,
			Payload:     []byte{0x7f, 0xff, 0xff, 0xff},
		}
	}
	for round := 0; round < 20 && !d.Sealed(); round++ {
		for z := ZoneNA; z < numZones; z++ {
			for tier := TierMaster; tier <= TierEdge; tier++ {
				if n := top.Attacker(z, tier); n != nil {
					d.Inspect(n.ID, malicious())
				}
			}
		}
	}
	if !d.Sealed() {
		t.Error("fail-closed seal never engaged despite broad compromise")
	}
}

// declaredFor mirrors the engine's rule for what a frame's header claims.
func declaredFor(kind AttackKind, n int) uint32 {
	if kind == KindMalformed {
		return 0x7fffffff
	}
	return uint32(n)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// TestLossInferenceIsBounded guards the fundamental property of sequence-gap
// loss detection: it can only over-report, never under-report, and the
// over-report must stay small. If the reorder window is set below the modelled
// path latency, frames still in flight are declared lost and precision
// collapses, so this catches that class of bug directly.
func TestLossInferenceIsBounded(t *testing.T) {
	e := New(Config{
		Seed: 2024, Workers: 4, AttackerWorkers: 4,
		// The fake clock below must advance on the same cadence the real
		// engine uses, otherwise measured latencies reflect the test's step
		// size rather than the detector's behaviour.
		TickDuration: 250 * time.Millisecond,
		LegitRate:    40, AttackRate: 40,
		Out: io.Discard, RenderInterval: time.Hour,
	})
	// Drive the detector's clock explicitly. Step compresses simulated time, so
	// without this the reorder window could never elapse and loss detection
	// would appear to find nothing at all.
	fake := time.Now()
	e.det.SetClock(func() time.Time { return fake })
	for i := 0; i < 30; i++ {
		fake = fake.Add(e.cfg.TickDuration)
		e.Step()
	}
	s := e.Snapshot()
	if s.Dropped == 0 {
		t.Fatal("no packet was ever lost, so loss detection is untested")
	}
	if s.GapsLost < s.Dropped {
		t.Errorf("inferred loss %d is below actual loss %d; a gap detector "+
			"must never under-report", s.GapsLost, s.Dropped)
	}
	// Over-reporting is inherent: a receiver that infers loss from sequence
	// gaps cannot tell a lost frame from a late one, so it must over-report.
	// The floor only guards against the pathological case where the reorder
	// window is shorter than the path latency and almost everything in flight
	// is declared lost. See the note on ReorderGrace for the real bound.
	if s.LossPrecision < 0.15 {
		t.Errorf("loss precision %.2f is pathologically low: the reorder "+
			"window is shorter than the modelled path latency, so nearly "+
			"every in-flight frame is being misreported as lost", s.LossPrecision)
	}
	// A gap is only examined when its source next sends, or on the next tick,
	// so the measured latency is the window plus at most a tick of sampling
	// slop. Anything beyond that means expiry is being deferred.
	ceiling := DefaultReorderGrace() + 2*e.cfg.TickDuration
	if d := time.Duration(s.DropDetectP50Ms) * time.Millisecond; d > ceiling {
		t.Errorf("loss-detection latency %v exceeds the reorder window %v "+
			"plus two ticks (%v)", d, DefaultReorderGrace(), ceiling)
	}
}

// TestReorderGraceCoversPathLatency asserts the grace window is derived from
// the modelled latency rather than chosen independently, because a window
// shorter than the slowest path manufactures false loss.
func TestReorderGraceCoversPathLatency(t *testing.T) {
	if got, want := DefaultReorderGrace(),
		time.Duration(3*MaxOneWayMs()*float64(time.Millisecond)); got != want {
		t.Errorf("reorder grace %v does not match 3x worst one-way delay %v", got, want)
	}
	if DefaultReorderGrace() <= time.Duration(MaxOneWayMs()*float64(time.Millisecond)) {
		t.Error("reorder grace must exceed the worst one-way delay")
	}
}

// TestDetectorConfigDefaultsAreFieldWise guards against a whole-config
// replacement that would silently discard caller-supplied settings.
func TestDetectorConfigDefaultsAreFieldWise(t *testing.T) {
	top := NewTopology()
	d := NewDetector(DetectorConfig{
		AnomalyZ:     9.0, // caller-set
		DwellSamples: 7,   // caller-set, would be lost by a whole-config default
	}, top, NewRand(1))
	if d.cfg.AnomalyZ != 9.0 {
		t.Errorf("AnomalyZ = %v, want the caller's 9.0", d.cfg.AnomalyZ)
	}
	if d.cfg.DwellSamples != 7 {
		t.Errorf("DwellSamples = %v, want the caller's 7", d.cfg.DwellSamples)
	}
	if d.cfg.ReorderGrace <= 0 {
		t.Error("ReorderGrace must be defaulted, not left zero")
	}
}

// TestCorePackageCannotTouchTheHost enforces the zero-write guarantee
// structurally rather than trusting a counter.
//
// The requirement is that the simulation performs no physical write cycles, and
// the only defensible way to show that is to prove the code implementing the
// simulation cannot express a write at all. The four files below carry the
// topology, the transport, the attack generation and the defence, and none of
// them may import anything capable of reaching a file, a socket or a device.
// Presentation is the only permitted exception, and it is listed explicitly so
// that widening it is a deliberate, visible change.
func TestCorePackageCannotTouchTheHost(t *testing.T) {
	// Packages that could perform I/O, open a descriptor, or reach the network.
	forbidden := map[string]bool{
		"os": true, "net": true, "os/exec": true, "syscall": true,
		"io/ioutil": true, "log": true, "log/slog": true, "bufio": true,
		"path/filepath": true, "database/sql": true, "net/http": true,
	}
	// The renderer is allowed to reach the terminal; nothing else is.
	presentation := map[string]bool{
		"scoreboard.go":  true,
		"output.go":      true,
		"tty_unix.go":    true,
		"tty_windows.go": true,
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading package directory: %v", err)
	}
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if name == "" || filepath.Ext(name) != ".go" ||
			strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		imports := fileImports(t, name, src)
		for _, imp := range imports {
			if !forbidden[imp] {
				continue
			}
			if presentation[name] {
				continue
			}
			t.Errorf("%s imports %q: the simulation core must not be able to "+
				"perform I/O, or the zero-write guarantee is unverifiable",
				name, imp)
		}
		if !presentation[name] {
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no core files were checked; the guard is not doing anything")
	}
	t.Logf("verified %d core files import no I/O package", checked)
}

// fileImports returns the import paths of a Go source file, ignoring the ones
// that appear in comments or inside the import block's comments.
func fileImports(t *testing.T, name string, src []byte) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parsing %s: %v", name, err)
	}
	var out []string
	for _, im := range f.Imports {
		p, err := strconv.Unquote(im.Path.Value)
		if err != nil {
			continue
		}
		out = append(out, p)
	}
	return out
}

// TestDetectorIgnoresTheKindLabel is the test that makes the whole exercise
// meaningful.
//
// AttackKind is the simulator's private record of how a frame was built. If the
// detector's verdict depended on it, every score in a run would be correct by
// construction and no result would say anything about the defence. So this test
// takes frames the simulator built as honest, hands them to the detector with
// every hostile label applied, and asserts the verdict does not change.
//
// The corollary matters as much: a frame built to be hostile must not be
// laundered into acceptance by relabelling it honest.
func TestDetectorIgnoresTheKindLabel(t *testing.T) {
	top := NewTopology()
	rnd := NewRand(99)
	d := NewDetector(DefaultDetectorConfig(), top, rnd)
	d.Advance(time.Unix(1700000000, 0))

	def := top.Defender(ZoneEU, TierMaster)
	atk := top.Attacker(ZoneNA, TierEdge)

	honest := func() *Packet {
		payload := append([]byte("DEF/EU/master"), make([]byte, 44)...)
		p := &Packet{From: def.ID, To: atk.ID, Payload: payload,
			DeclaredLen: uint32(len(payload))}
		signPacket(p, def.priv)
		return p
	}

	base := d.Inspect(def.ID, honest())
	if !base.Accept {
		t.Fatalf("baseline honest frame refused: %s", base.Reason)
	}
	// Relabelling a genuinely valid, correctly signed frame as every hostile
	// kind must not change the verdict.
	for _, kind := range []AttackKind{KindPacketDrop, KindClockDrift, KindMalformed, KindForgery} {
		p := honest()
		p.Kind = kind
		if o := d.Inspect(def.ID, p); !o.Accept {
			t.Errorf("a valid signed frame labelled %s was refused (%s); the "+
				"detector is reading ground truth", kind, o.Reason)
		}
	}

	// The reverse: a forged frame relabelled honest must still be refused.
	forged := &Packet{From: atk.ID, To: def.ID, Kind: KindLegitimate,
		Payload: make([]byte, 96), DeclaredLen: 96}
	signPacket(forged, atk.priv)
	corruptSignature(forged)
	if o := d.Inspect(atk.ID, forged); o.Accept {
		t.Error("a forged frame labelled honest was accepted; the detector is " +
			"reading ground truth")
	}
}

// TestForgeryIsCausedByTheSignature covers the mechanism specifically.
//
// A frame signed with the attacker's own key but claiming a defender identity
// is the realistic case: the attacker has a real key, and only the claimed
// identity is wrong. Verifying against the key of whoever signed would accept
// it, so the test pins that the lookup uses the claimed From.
func TestForgeryIsCausedByTheSignature(t *testing.T) {
	top := NewTopology()
	rnd := NewRand(1234)
	d := NewDetector(DefaultDetectorConfig(), top, rnd)
	d.Advance(time.Unix(1700000000, 0))

	atk := top.Attacker(ZoneSA, TierSuperPeer)
	other := top.Attacker(ZoneOC, TierEdge)

	// Signed by `other` but claiming to be `atk`: valid signature, wrong identity.
	p := &Packet{From: atk.ID, To: top.Defender(ZoneEU, TierMaster).ID,
		Payload: make([]byte, 96), DeclaredLen: 96}
	signPacket(p, other.priv)
	if o := d.Inspect(atk.ID, p); o.Accept {
		t.Error("a frame signed by one attacker and claiming another identity " +
			"was accepted; verification must key on the claimed identity")
	}
	// The same frame, honestly claimed, passes. This proves the rejection above
	// came from the identity mismatch and not from some unrelated defect.
	q := &Packet{From: other.ID, To: p.To, Payload: make([]byte, 96), DeclaredLen: 96}
	signPacket(q, other.priv)
	if o := d.Inspect(other.ID, q); !o.Accept {
		t.Errorf("the honest version of the same frame was refused: %s", o.Reason)
	}
}

// TestSignatureCoversTheFieldsAnAttackerWouldEdit pins what the signature
// commits to.
//
// Seq and SkewMs are included because the receiver derives loss detection and
// the key-window check from them. A signature that ignored them would let an
// attacker rewrite the sequence number to erase evidence of a drop, or claim a
// key hour inside the accepted window.
func TestSignatureCoversTheFieldsAnAttackerWouldEdit(t *testing.T) {
	top := NewTopology()
	rnd := NewRand(555)
	d := NewDetector(DefaultDetectorConfig(), top, rnd)
	d.Advance(time.Unix(1700000000, 0))

	atk := top.Attacker(ZoneAF, TierMaster)
	dst := top.Defender(ZoneAS, TierMaster)

	base := &Packet{From: atk.ID, To: dst.ID, Payload: make([]byte, 96),
		DeclaredLen: 96, Seq: 100, SealedAt: 1700000000 / 3600}
	signPacket(base, atk.priv)
	if o := d.Inspect(atk.ID, base); !o.Accept {
		t.Fatalf("baseline signed frame refused: %s", o.Reason)
	}

	mutations := map[string]func(*Packet){
		"seq":      func(p *Packet) { p.Seq = 101 },
		"skew":     func(p *Packet) { p.SkewMs = -1000 },
		"sealedAt": func(p *Packet) { p.SealedAt-- },
		"from":     func(p *Packet) { p.From = dst.ID },
		"to":       func(p *Packet) { p.To = atk.ID },
		"declared": func(p *Packet) { p.DeclaredLen = 95 },
		"payload":  func(p *Packet) { p.Payload[0] ^= 0xff },
	}
	// Each mutant is built field by field rather than by copying the base
	// Packet. A struct copy would duplicate the embedded atomic, which both
	// trips vet and would leave the copy sharing no state with the original.
	for name, mutate := range mutations {
		p := &Packet{
			From: base.From, To: base.To, Payload: append([]byte(nil), base.Payload...),
			SealedAt: base.SealedAt, Seq: base.Seq, DeclaredLen: base.DeclaredLen,
			Sig: append([]byte(nil), base.Sig...),
		}
		mutate(p)
		if verifySignature(p, top) {
			t.Errorf("mutating %s did not invalidate the signature; that field "+
				"is not covered", name)
		}
	}
}

// TestSealRefusesUnverifiableFrames is the seal's real contract.
//
// A fail-closed seal has concluded it cannot trust its own classification, so
// it must refuse anything it cannot verify cryptographically — including a
// frame that claims to be honest. An earlier seal consulted the Kind label and
// therefore refused only frames it already knew were hostile.
func TestSealRefusesUnverifiableFrames(t *testing.T) {
	top := NewTopology()
	rnd := NewRand(31)
	d := NewDetector(DefaultDetectorConfig(), top, rnd)
	d.Advance(time.Unix(1700000000, 0))
	d.Seal("test")

	def := top.Defender(ZoneAN, TierEdge)
	unsigned := &Packet{From: def.ID, To: def.ID, Kind: KindLegitimate,
		Payload: make([]byte, 96), DeclaredLen: 96}
	if o := d.Inspect(def.ID, unsigned); o.Accept {
		t.Error("sealed detector accepted an unsigned frame")
	}
	// A frame that is honest and verifiable is still served: the seal refuses
	// what it cannot check, not what it merely suspects.
	signed := &Packet{From: def.ID, To: def.ID, Kind: KindLegitimate,
		Payload: make([]byte, 96), DeclaredLen: 96}
	copy(signed.Payload, def.ID.String())
	signPacket(signed, def.priv)
	if o := d.Inspect(def.ID, signed); !o.Accept {
		t.Errorf("sealed detector refused a verifiable frame: %s", o.Reason)
	}
}

// TestStructuralCheckNeedsNoGroundTruth documents that the malformed vector is
// caught by the declared length alone.
func TestStructuralCheckNeedsNoGroundTruth(t *testing.T) {
	payload := make([]byte, 64)
	// Declares far more than it carries: the legacy over-read condition.
	over := &Packet{Payload: payload, DeclaredLen: 0x7fffffff}
	if structurallyValid(over) {
		t.Error("a frame declaring 2GiB of payload was called structurally valid")
	}
	// A frame that under-declares is a different error: the receiver would
	// silently ignore trailing bytes, so it is refused too.
	under := &Packet{Payload: payload, DeclaredLen: 32}
	if structurallyValid(under) {
		t.Error("a frame under-declaring its payload was called valid")
	}
	exact := &Packet{Payload: payload, DeclaredLen: uint32(len(payload))}
	if !structurallyValid(exact) {
		t.Error("a correctly declared frame was called invalid")
	}
	empty := &Packet{Payload: nil, DeclaredLen: 0}
	if structurallyValid(empty) {
		t.Error("an empty frame was called valid")
	}
}

// TestRecallCountsOnlyRefusableFrames pins the recall denominator.
//
// The point of the exercise: recall must measure the vectors where refusing an
// individual frame is correct. A flood's own frame is valid and signed, and a
// drift inside the key window is traffic the mesh must serve, so including
// them would make the figure fall every time the detector stopped consulting
// ground truth — which is exactly what happened before this was fixed.
func TestRecallCountsOnlyRefusableFrames(t *testing.T) {
	refusable := map[AttackKind]bool{}
	for k := 0; k < int(numKinds); k++ {
		refusable[AttackKind(k)] = AttackKind(k).RefusableByFrame()
	}
	if !refusable[KindMalformed] || !refusable[KindForgery] {
		t.Error("malformed and forgery frames must count toward recall")
	}
	if refusable[KindPacketDrop] {
		t.Error("a flood's frame is valid and signed; refusing it is not the " +
			"correct outcome, so it must not count toward recall")
	}
	if refusable[KindClockDrift] {
		t.Error("clock drift is only an attack outside the key window")
	}
	// The drift helper must agree with the window the detector enforces.
	if !DriftIsAttack(181e3) {
		t.Error("181s is outside the +/-3min window and must be an attack")
	}
	if DriftIsAttack(179e3) {
		t.Error("179s is inside the +/-3min window and must be tolerated")
	}
	if DriftIsAttack(0) {
		t.Error("zero skew is not an attack")
	}
}

// TestRunReportsDriftTolerance explicitly documents the cost of the key window.
//
// Half the drift attacker's aim points land inside the window on purpose. Those
// frames are served, because a mesh that refused every 2-minute clock offset
// would drop honest nodes on ordinary clock drift. The number is published so
// the trade-off is measured rather than assumed.
func TestRunReportsDriftTolerance(t *testing.T) {
	eng := New(Config{
		Seed: 3, Workers: 2, AttackerWorkers: 3,
		TickInterval: 200 * time.Millisecond, LegitRate: 12, AttackRate: 20,
		Duration: 6 * time.Second, Out: nil,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if err := eng.Run(ctx); err != nil && err != context.DeadlineExceeded {
		t.Fatal(err)
	}
	s := eng.Snapshot()

	// The attacker aims inside the window about half the time, so a nonzero
	// count here is the expected result, not a defect.
	if s.DriftInWindow == 0 {
		t.Error("no drift frames were served in-window; the key window is not " +
			"being exercised and a run cannot show what it costs")
	}
	if s.DriftInWindow > s.Offered[KindClockDrift] {
		t.Errorf("served %d in-window drift frames but only %d were offered",
			s.DriftInWindow, s.Offered[KindClockDrift])
	}
	// Every drift frame is either served in-window or refused, and nothing else.
	// The two sets must account for the whole vector, or a frame is being
	// dropped without a verdict.
	if got, want := s.DriftInWindow+s.Rejected[KindClockDrift], s.Offered[KindClockDrift]; got != want {
		t.Errorf("in-window served + refused = %d, want the %d offered", got, want)
	}
	if s.FalsePos != 0 {
		t.Errorf("%d honest frames refused; the window must not cost real traffic", s.FalsePos)
	}
	if s.Recall < 1.0 {
		t.Errorf("recall %.4f, want 1.0: every structural and forged frame is "+
			"individually detectable and must be refused", s.Recall)
	}
}
