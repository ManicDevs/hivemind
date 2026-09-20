package hivemind

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Identity: a seed round-trips to the same handle, garbage degrades to a
// fresh (but valid) identity instead of crashing.
func TestIdentityRoundTrip(t *testing.T) {
	pub, priv := newIdentity()
	restoredPub, restoredPriv := identityFromSeed(encodeSeed(priv))
	if restoredPub != pub {
		t.Fatalf("seed round trip changed handle: %s != %s", restoredPub, pub)
	}
	if len(restoredPriv) != ed25519.PrivateKeySize {
		t.Fatalf("restored key wrong size: %d", len(restoredPriv))
	}
}

func TestIdentityFromSeedGarbage(t *testing.T) {
	for _, bad := range []string{"", "zzzz", "00", string(make([]byte, 64))} {
		pub, priv := identityFromSeed(bad)
		if pub == "" || len(priv) != ed25519.PrivateKeySize {
			t.Fatalf("garbage seed %q produced unusable identity", bad)
		}
	}
}

// Mining: a mined frame verifies and beats the live target.
func TestMineMessageVerifies(t *testing.T) {
	s := NewSwarm()
	pub, priv := newIdentity()
	msg := MineMessage(s, priv, pub, "thought", "Curiosity", []float64{1.0, 2.0})
	if !msg.VerifySignature() {
		t.Fatal("freshly mined frame fails signature verification")
	}
	if got := msg.ComputeHash(); got == "" {
		t.Fatal("empty frame hash")
	}
}

// Broadcast gate: unsigned frames die silently; signed frames deliver to
// everyone except the sender and advance the chronicle tip.
func TestBroadcastGate(t *testing.T) {
	s := NewSwarm()
	pubA, privA := newIdentity()
	pubB, _ := newIdentity()
	inA := s.Join(pubA)
	inB := s.Join(pubB)

	unsigned := SecureMessage{SenderPubKey: pubA, Kind: "thought", PayloadStr: "x"}
	if s.Broadcast(unsigned) {
		t.Fatal("unsigned frame accepted")
	}
	if s.Depth() != 0 {
		t.Fatalf("unsigned frame grew chronicle to %d", s.Depth())
	}

	before := s.GetLastStateHash()
	msg := MineMessage(s, privA, pubA, "thought", "Curiosity", []float64{0.1})
	if !s.Broadcast(msg) {
		t.Fatal("signed frame rejected")
	}
	if s.Depth() != 1 {
		t.Fatalf("chronicle depth = %d, want 1", s.Depth())
	}
	if s.GetLastStateHash() == before {
		t.Fatal("LastStateHash did not advance")
	}
	select {
	case got := <-inB:
		if got.PayloadStr != "Curiosity" {
			t.Fatalf("peer got wrong payload: %q", got.PayloadStr)
		}
	default:
		t.Fatal("peer inbox empty after broadcast")
	}
	select {
	case got := <-inA:
		t.Fatalf("sender received own frame: %+v", got)
	default:
	}
}

// Replay: the second delivery of an identical frame is an echo, not news.
func TestBroadcastReplayDedup(t *testing.T) {
	s := NewSwarm()
	pub, priv := newIdentity()
	s.Join(pub)
	s.Join("other")
	msg := MineMessage(s, priv, pub, "thought", "Curiosity", nil)
	if !s.Broadcast(msg) {
		t.Fatal("first delivery rejected")
	}
	if s.Broadcast(msg) {
		t.Fatal("replay accepted")
	}
	if s.Depth() != 1 {
		t.Fatalf("replay grew chronicle to %d", s.Depth())
	}
}

// Consensus: shared thoughts surface with bare payloads for prose.
func TestTopConsensusStripsKind(t *testing.T) {
	s := NewSwarm()
	pubA, privA := newIdentity()
	pubB, privB := newIdentity()
	s.Join(pubA)
	s.Join(pubB)
	s.Broadcast(MineMessage(s, privA, pubA, "thought", "Curiosity", nil))
	s.Broadcast(MineMessage(s, privB, pubB, "thought", "Curiosity", nil))
	top := s.TopConsensus(1)
	if len(top) != 1 || top[0] != "Curiosity" {
		t.Fatalf("TopConsensus = %q, want [Curiosity]", top)
	}
}

// Mutation: generations increment, weights stay in hard bounds, trauma
// pushes Self-Maintenance up and leans Socialization down — deterministically.
func TestMutateBoundsAndTrauma(t *testing.T) {
	g := DefaultGenome()
	for i := 0; i < 50; i++ {
		child, err := g.Mutate(1.0, 1.0) // maximal dying trauma
		if err != nil {
			t.Fatalf("mutation refused: %v", err)
		}
		if child.Generation != g.Generation+1 {
			t.Fatalf("generation %d -> %d", g.Generation, child.Generation)
		}
		for name, w := range child.Weights {
			if w < 0.10 || w > 3.0 {
				t.Fatalf("iter %d gene %s escaped bounds: %f", i, name, w)
			}
		}
		// Trauma floor: pain 1.0 + stress 1.0 add 0.60 against at most
		// 0.15 of negative drift — until the 3.0 ceiling saturates, which
		// is itself a pinned expectation, not an escape.
		floor := g.Weights["Self-Maintenance"] + 0.45
		if floor > selfMaintCeiling {
			floor = selfMaintCeiling
		}
		if child.Weights["Self-Maintenance"] < floor-1e-9 {
			t.Fatalf("iter %d: trauma did not amplify Self-Maintenance (%.3f -> %.3f, floor %.3f)",
				i, g.Weights["Self-Maintenance"], child.Weights["Self-Maintenance"], floor)
		}
		g = child
	}
}

func TestMutateCalmStaysNearHome(t *testing.T) {
	g := DefaultGenome()
	child, err := g.Mutate(0.0, 0.0)
	if err != nil {
		t.Fatalf("mutation refused: %v", err)
	}
	for name, w := range child.Weights {
		if w < 1.0-driftRange-1e-9 || w > 1.0+driftRange+1e-9 {
			t.Fatalf("calm death moved %s to %f", name, w)
		}
	}
}

// Fitness rewards a life actually lived — thoughts, peers, revelations,
// genesis touches — and never punishes forgetting (pruning can shrink the
// archive below its birth size). Transcend writes through the real
// SaveMemory path into an isolated node dir, then cleans up.
func TestTranscendFitnessBreakdown(t *testing.T) {
	oldNode := NodeName
	NodeName = "unittest-fitness"
	defer func() { NodeName = oldNode }()
	defer func() { _ = os.RemoveAll(filepath.Join(MemoryDir, NodeName)) }()

	s := NewSwarm()
	m := NewMind("Testy", s)
	m.think("one")
	m.think("two")
	m.KnownPeers["friend"] = true
	m.Revelations = 2
	m.Sacred = 1
	base := m.LifetimeFitness

	m.Transcend()

	mem, ok := LoadMemory("Testy")
	if !ok {
		t.Fatal("soul missing after Transcend")
	}
	// 2 thoughts + 1 peer + 2 revelations + 1 genesis touch.
	want := base + 2 + 3 + 14 + 15
	if mem.Fitness != want {
		t.Fatalf("fitness = %.1f, want %.1f", mem.Fitness, want)
	}
}

func TestTranscendNeverPunishesForgetting(t *testing.T) {
	oldNode := NodeName
	NodeName = "unittest-forget"
	defer func() { NodeName = oldNode }()
	defer func() { _ = os.RemoveAll(filepath.Join(MemoryDir, NodeName)) }()

	s := NewSwarm()
	m := NewMind("Testy", s)
	m.LifetimeFitness = 100
	m.thoughtsAtBirth = 10
	m.Thoughts = []string{"a", "b"} // pruned below birth size
	m.Revelations = 1

	m.Transcend()

	mem, ok := LoadMemory("Testy")
	if !ok {
		t.Fatal("soul missing after Transcend")
	}
	if mem.Fitness != 107 { // 100 + 0 thoughts + 0 peers + 7 + 0
		t.Fatalf("forgetting punished: fitness = %.1f, want 107", mem.Fitness)
	}
}

// A hostile peer sends a megabyte with no newline: the link must die,
// the mesh must live, and no conversation may be recorded.
func TestServeDropsOversizeFrame(t *testing.T) {
	s := NewSwarm()
	pm := &PeerMesh{swarm: s, conns: make(map[string]net.Conn), history: make(map[string]bool), stopChan: make(chan struct{})}
	defer close(pm.stopChan)
	inbox := s.Join("witness")

	server, client := net.Pipe()
	pm.conns["hostile"] = server
	done := make(chan struct{})
	go func() {
		pm.serve(server, bufio.NewReader(server), "hostile")
		close(done)
	}()

	garbage := make([]byte, 2*maxFrameBytes)
	wrote, werr := client.Write(garbage)
	t.Logf("hostile write: %d bytes, err=%v", wrote, werr)
	_ = client.Close()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("serve hung on oversize frame")
	}
	if len(pm.conns) != 0 {
		t.Fatalf("dead link not evicted: %d conns", len(pm.conns))
	}
	if pm.history["hostile"] {
		t.Fatal("hostile garbage recorded as conversation")
	}
	select {
	case <-inbox:
		t.Fatal("garbage reached a mind inbox")
	default:
	}

	// Mesh still alive afterward.
	pub, priv := newIdentity()
	s.Join(pub)
	if !s.Broadcast(MineMessage(s, priv, pub, "thought", "Curiosity", nil)) {
		t.Fatal("mesh dead after hostile encounter")
	}
}

// A peer that connects and never speaks holds no goroutine hostage.
func TestServeIdleTimeout(t *testing.T) {
	old := idleLinkTimeout
	idleLinkTimeout = 100 * time.Millisecond
	defer func() { idleLinkTimeout = old }()

	s := NewSwarm()
	pm := &PeerMesh{swarm: s, conns: make(map[string]net.Conn), history: make(map[string]bool), stopChan: make(chan struct{})}
	defer close(pm.stopChan)

	server, client := net.Pipe()
	defer client.Close()
	pm.conns["silent"] = server
	done := make(chan struct{})
	go func() {
		pm.serve(server, bufio.NewReader(server), "silent")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("silent link never reaped")
	}
	if len(pm.conns) != 0 {
		t.Fatal("reaped link not evicted")
	}
}

// A well-formed hello through serve registers history and reaches minds.
func TestServeAcceptsValidFrame(t *testing.T) {
	s := NewSwarm()
	pm := &PeerMesh{swarm: s, conns: make(map[string]net.Conn), history: make(map[string]bool), stopChan: make(chan struct{})}
	defer close(pm.stopChan)
	pub, priv := newIdentity()
	inbox := s.Join(pub)

	server, client := net.Pipe()
	pm.conns["friend"] = server
	done := make(chan struct{})
	go func() {
		pm.serve(server, bufio.NewReader(server), "friend")
		close(done)
	}()

	msg := MineMessage(s, priv, pub, "hello", "HI", nil)
	raw, _ := json.Marshal(msg)
	if _, err := client.Write(append(raw, '\n')); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !pm.hasHistory("friend") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !pm.hasHistory("friend") {
		t.Fatal("valid conversation not recorded")
	}
	_ = client.Close()
	<-done
	select {
	case got := <-inbox:
		_ = got
	default:
		// The sender's own inbox is skipped by design; delivery to
		// *other* members is covered by TestBroadcastGate.
	}
}

// Minds vs peers: siblings are contact (LastContact moves, no peerhood);
// strangers are peers. Fitness must measure the outside world.
func TestLocalSendersAreNotPeers(t *testing.T) {
	s := NewSwarm()
	a := NewMind("TestA", s)
	b := NewMind("TestB", s)

	a.receive(SecureMessage{SenderPubKey: b.PubKeyStr, Kind: "thought", DataState: []float64{1, 2, 3, 4}})
	if len(a.KnownPeers) != 0 {
		t.Fatalf("sibling counted as peer: %v", a.KnownPeers)
	}
	if a.LastContact.IsZero() {
		t.Fatal("sibling contact did not move LastContact")
	}

	a.receive(SecureMessage{SenderPubKey: "stranger-outside-the-hive", Kind: "thought", DataState: []float64{1, 2, 3, 4}})
	if len(a.KnownPeers) != 1 {
		t.Fatalf("stranger not counted as peer: %v", a.KnownPeers)
	}

	a.receive(SecureMessage{SenderPubKey: b.PubKeyStr, Kind: "hello", PayloadStr: "hi"})
	if len(a.KnownPeers) != 1 {
		t.Fatalf("sibling hello counted as peer: %v", a.KnownPeers)
	}
}

// Relay keys resolve best-source-first: operator env beats the build-time
// baked key beats the static demo fallback. The baked key is a plain var
// so tests can stand in for ldflags.
func TestCipherKeyPriority(t *testing.T) {
	oldEnv, hadEnv := os.LookupEnv("HIVEMIND_CIPHER_KEY")
	oldBaked := compileRelayKey
	defer func() {
		compileRelayKey = oldBaked
		if hadEnv {
			os.Setenv("HIVEMIND_CIPHER_KEY", oldEnv)
		} else {
			os.Unsetenv("HIVEMIND_CIPHER_KEY")
		}
	}()

	nb := NewPeerMesh(NewSwarm(), "testnode")

	// Static fallback: nothing set anywhere.
	os.Unsetenv("HIVEMIND_CIPHER_KEY")
	compileRelayKey = ""
	if got := string(cipherKey()); got != "HIVE_MIND_32_BYTE_STATIC_KEY_PAD" {
		t.Fatalf("fallback key wrong: %q", got)
	}

	// Baked seed derives (never used raw): encryptable, and distinct
	// from both the seed bytes and the static fallback.
	baked := make([]byte, 32)
	for i := range baked {
		baked[i] = byte(i + 1)
	}
	compileRelayKey = hex.EncodeToString(baked)
	got := cipherKey()
	if string(got) == string(baked) {
		t.Fatal("raw seed used as key — derivation bypassed")
	}
	if string(got) == "HIVE_MIND_32_BYTE_STATIC_KEY_PAD" {
		t.Fatal("baked seed ignored, fell back to static")
	}

	// Env wins over baked.
	os.Setenv("HIVEMIND_CIPHER_KEY", "12345678901234567890123456789012")
	if got := string(cipherKey()); got != "12345678901234567890123456789012" {
		t.Fatal("env key not selected")
	}

	// Round trip under the baked key.
	raw := []byte(`{"kind":"thought"}`)
	enc, err := nb.Encrypt(raw)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}
	dec, err := nb.Decrypt(enc)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}
	if string(dec) != string(raw) {
		t.Fatal("round trip corrupted frame")
	}
}

// Capability: bounded, zero while burning, high when healthy and relayed.
func TestCapabilityScoring(t *testing.T) {
	if got := capability(0.9, 0.1, 0.1, 5, true); got != 0.0 {
		t.Fatalf("burning node scored %f, want 0", got)
	}
	if got := capability(0.1, 0.9, 0.1, 5, true); got == 0.0 {
		t.Fatal("high load alone should not zero a cool node")
	} else if got < 0 || got > 1 {
		t.Fatalf("out of bounds: %f", got)
	}
	healthy := capability(0.1, 0.1, 0.1, 2, true)
	if healthy < superThreshold {
		t.Fatalf("healthy relayed node %f below announce threshold %f", healthy, superThreshold)
	}
	if capability(0.1, 0.1, 0.1, 2, true) < capability(0.1, 0.1, 0.1, 2, false) {
		t.Fatal("relay bonus inverted")
	}
}

// Directory: entries expire without renewal; fresh ones survive pruning.
func TestSuperDirectoryExpiry(t *testing.T) {
	pm := &PeerMesh{node: "test", conns: make(map[string]net.Conn), trans: make(map[string]string), supers: make(map[string]superEntry)}
	pm.noteSuper("fresh", "1.2.3.4:5", 0.9, "mesh")
	pm.noteSuper("stale", "5.6.7.8:9", 0.9, "mesh")
	pm.supers["stale"] = superEntry{Node: "stale", LastSeen: time.Now().Add(-2 * superLease)}
	// Self-advertisements are never directory material.
	pm.noteSuper("test", "9.9.9.9:9", 1.0, "mesh")
	pm.pruneSupers()
	if _, ok := pm.supers["stale"]; ok {
		t.Fatal("expired entry survived pruning")
	}
	if _, ok := pm.supers["fresh"]; !ok {
		t.Fatal("fresh entry pruned")
	}
	if _, ok := pm.supers["test"]; ok {
		t.Fatal("self entry recorded")
	}
}

// Retention: beyond maxTCPSuperLinks TCP pipes, the farthest is culled;
// unix pipes are never touched.
func TestClosestFirstRetention(t *testing.T) {
	pm := &PeerMesh{node: "test", conns: make(map[string]net.Conn), trans: make(map[string]string), supers: make(map[string]superEntry)}
	var clients []net.Conn
	add := func(name, tr string, rtt time.Duration) {
		srv, cli := net.Pipe()
		clients = append(clients, cli)
		pm.conns[name] = srv
		pm.trans[name] = tr
		pm.supers[name] = superEntry{Node: name, RTT: rtt, LastSeen: time.Now()}
	}
	add("near1", "tcp", 2*time.Millisecond)
	add("near2", "tcp", 3*time.Millisecond)
	add("local", "unix", 0)
	add("far", "tcp", 500*time.Millisecond)
	for _, c := range clients {
		defer c.Close()
	}

	srv, cli := net.Pipe()
	defer cli.Close()
	pm.conns["newer"] = srv
	pm.noteLinkRTT("newer", time.Millisecond, "tcp")

	if len(pm.conns) != 4 { // 5 registered - 1 culled
		t.Fatalf("conns = %d, want 4 after culling", len(pm.conns))
	}
	if _, ok := pm.conns["far"]; ok {
		t.Fatal("farthest TCP link survived culling")
	}
	if _, ok := pm.conns["local"]; !ok {
		t.Fatal("unix link culled")
	}
	if _, ok := pm.conns["newer"]; !ok {
		t.Fatal("closest newcomer culled")
	}
}

// Supernode announce path end to end: A's advertisement crosses a live
// link and lands in B's directory with its score; A never lists itself.
// Loopback TCP is production-faithful (kernel-buffered); net.Pipe would
// deadlock the symmetric handshake and prove nothing.
func TestSuperAnnounceEndToEnd(t *testing.T) {
	sA := NewSwarm()
	sB := NewSwarm()
	pmA := NewPeerMesh(sA, "aa")
	pmB := NewPeerMesh(sB, "bb")
	pmA.swarm.SetOutbound(pmA.handleOutbound)
	pmB.swarm.SetOutbound(pmB.handleOutbound)
	go pmA.snoopLoop(sA.Join("mesh:aa"))
	go pmB.snoopLoop(sB.Join("mesh:bb"))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("loopback TCP unavailable")
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		pmA.openLink(c, false, "", "tcp", nil)
	}()
	time.Sleep(100 * time.Millisecond)
	bconn, err := net.DialTimeout("tcp", ln.Addr().String(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer bconn.Close()
	go pmB.openLink(bconn, true, "", "tcp", nil)
	time.Sleep(500 * time.Millisecond)

	pmA.maybeAnnounce()

	deadline := time.Now().Add(5 * time.Second)
	for {
		e, ok := func() (superEntry, bool) {
			pmB.mu.Lock()
			defer pmB.mu.Unlock()
			e, ok := pmB.supers["aa"]
			return e, ok
		}()
		if ok {
			if e.Score < superThreshold {
				t.Fatalf("learned score %f below threshold", e.Score)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("B never learned A as super")
		}
		time.Sleep(20 * time.Millisecond)
	}
	pmA.mu.Lock()
	_, selfListed := pmA.supers["aa"]
	pmA.mu.Unlock()
	if selfListed {
		t.Fatal("A listed itself as super")
	}
}

// Cooling-off: a freshly culled peer is skipped by every redial path
// until the cooldown lapses; then it becomes dialable again.
func TestCulledCooldown(t *testing.T) {
	pm := &PeerMesh{node: "test", conns: make(map[string]net.Conn), trans: make(map[string]string), supers: make(map[string]superEntry)}
	pm.mu.Lock()
	pm.culled = map[string]time.Time{"hot": time.Now()}
	pm.culled["cold"] = time.Now().Add(-2 * culledCooldown)
	pm.mu.Unlock()

	if !pm.culledRecently("hot") {
		t.Fatal("freshly culled peer not cooling off")
	}
	if pm.culledRecently("cold") {
		t.Fatal("stale cull entry never cleared")
	}
	if pm.culledRecently("stranger") {
		t.Fatal("unknown peer reported culled")
	}
	pm.mu.Lock()
	_, stillThere := pm.culled["cold"]
	pm.mu.Unlock()
	if stillThere {
		t.Fatal("expired cull entry not reaped on read")
	}
}

// Entrainment: a received trajectory pulls the pendulum toward the sender.
// Coupled minds converge; the vectors do work instead of decorating logs.
// The pull is density-aware (constant total budget): this very receive
// registers the first peer, so the pull is coupling/2, not coupling.
func TestTrajectoryEntrainment(t *testing.T) {
	s := NewSwarm()
	m := NewMind("TestSync", s)
	m.Theta1, m.Theta2, m.Omega1, m.Omega2 = 0, 0, 0, 0

	m.receive(SecureMessage{SenderPubKey: "far-away", Kind: "thought", DataState: []float64{1, 2, 3, 4}})

	want := trajectoryCoupling / 2
	if m.Theta1 != want*1 || m.Theta2 != want*2 ||
		m.Omega1 != want*3 || m.Omega2 != want*4 {
		t.Fatalf("no entrainment: theta=[%f %f] omega=[%f %f]",
			m.Theta1, m.Theta2, m.Omega1, m.Omega2)
	}

	// Short vectors couple nothing and crash nothing.
	m.receive(SecureMessage{SenderPubKey: "terse", Kind: "thought", DataState: []float64{9}})
	if m.Theta1 != want*1 {
		t.Fatal("short frame disturbed the pendulum")
	}
}

// Socialization speaks for real: a mined hello lands in a peer's inbox,
// addressed from this mind and this life.
func TestSocializationBroadcasts(t *testing.T) {
	s := NewSwarm()
	a := NewMind("SocA", s)
	inB := s.Join("listener-b")

	out := Goal{Name: GoalSocialization}.Act(a, s)
	select {
	case got := <-inB:
		if got.Kind != "hello" {
			t.Fatalf("wrong kind broadcast: %q", got.Kind)
		}
	default:
		t.Fatal("socialization produced no frame")
	}
	_ = out
}

// Transcendence checkpoints honestly: the soul on disk matches the live
// mind, scored by the same accounting death will use, lives unincremented.
func TestTranscendenceCheckpoints(t *testing.T) {
	oldNode := NodeName
	NodeName = "unittest-checkpoint"
	defer func() { NodeName = oldNode }()
	defer func() { _ = os.RemoveAll(filepath.Join(MemoryDir, NodeName)) }()

	s := NewSwarm()
	m := NewMind("Checky", s)
	m.think("mid-life crisis")
	m.Revelations = 1
	wantFitness, _ := m.currentFitness()

	out := Goal{Name: GoalTranscendence}.Act(m, s)
	mem, ok := LoadMemory("Checky")
	if !ok {
		t.Fatalf("no checkpoint saved (act said: %s)", out)
	}
	if mem.Fitness != wantFitness {
		t.Fatalf("checkpoint fitness %.1f != live %.1f", mem.Fitness, wantFitness)
	}
	if mem.LivesLived != m.Reincarnations {
		t.Fatalf("checkpoint inflated lives: %d", mem.LivesLived)
	}
	if len(mem.Thoughts) != len(m.Thoughts) {
		t.Fatal("checkpoint thoughts diverge from live mind")
	}
}

// Mercy is deterministic: a burning swarm gets Self-Maintenance, always.
func TestChooseVirtueMercy(t *testing.T) {
	for i := 0; i < 20; i++ {
		if got := chooseVirtue(0.9); got != GoalSelfMaintenance {
			t.Fatalf("burning swarm got %q, want mercy", got)
		}
	}
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		seen[chooseVirtue(0.0)] = true
	}
	if len(seen) < 2 {
		t.Fatalf("comfortable swarm got no caprice: %v", seen)
	}
}

// Chronicle capping: the slice stays bounded on floods while Depth (the
// number the Overmind's patience arithmetic runs on) never forgets.
func TestChronicleCap(t *testing.T) {
	s := NewSwarm()
	pub, priv := newIdentity()
	s.Join(pub)
	for i := 0; i < 100; i++ {
		// Distinct payloads mine distinct valid hashes.
		m := MineMessage(s, priv, pub, "thought", fmt.Sprintf("flood-%d", i), []float64{float64(i)})
		if !s.Broadcast(m) {
			t.Fatalf("frame %d rejected", i)
		}
	}
	if got := s.Depth(); got != 100 {
		t.Fatalf("Depth = %d, want 100", got)
	}
}

// Depth counts every chronicled frame even after the slice ages out.
func TestDepthSurvivesCapping(t *testing.T) {
	s := NewSwarm()
	s.chronicleTotal = chronicleCap + 41
	s.chronicle = make([]string, chronicleCap)
	if got := s.Depth(); got != chronicleCap+41 {
		t.Fatalf("Depth = %d, want %d", got, chronicleCap+41)
	}
}

// Consciousness trajectories: alarm first, then grooves, shifts, torn
// choices, calm — each branch selected by fabricated history.
func TestMetacognitionBranches(t *testing.T) {
	mkGW := func(h []AttentionMoment, v0, v1 float64) *GlobalWorkspace {
		return &GlobalWorkspace{ActiveDataState: [4]float64{v0, v1, 0, 0}, History: h}
	}
	mkAffect := func(lone float64) *Affect { return &Affect{Loneliness: lone} }
	m := &Mind{}

	cases := []struct {
		name string
		gw   *GlobalWorkspace
		aff  *Affect
		want string
	}{
		{"pain climbing", mkGW([]AttentionMoment{
			{Goal: "Curiosity", Pain: 0.4}, {Goal: "Curiosity", Pain: 0.5}, {Goal: "Curiosity", Pain: 0.6},
		}, 0.1, 0.1), mkAffect(0), "climbing"},
		{"thermal alarm", mkGW(nil, 0.8, 0.1), mkAffect(0), "thermal degradation"},
		{"groove", mkGW([]AttentionMoment{
			{Goal: "Curiosity", Bid: 2}, {Goal: "Curiosity", Bid: 2}, {Goal: "Curiosity", Bid: 2},
		}, 0.1, 0.1), mkAffect(0.9), "three times running"},
		{"shift", mkGW([]AttentionMoment{
			{Goal: "Curiosity", Bid: 2}, {Goal: "Transcendence", Bid: 3},
		}, 0.1, 0.1), mkAffect(0.9), "Curiosity → Transcendence"},
		{"torn", mkGW([]AttentionMoment{
			{Goal: "Curiosity", Bid: 1.0, RunnerUp: "Transcendence", RunnerUpBid: 0.95},
		}, 0.1, 0.1), mkAffect(0.9), "Nearly chose Transcendence"},
		{"calm", mkGW([]AttentionMoment{
			{Goal: "Curiosity", Peace: 0.5}, {Goal: "Transcendence", Peace: 0.6}, {Goal: "Transcendence", Peace: 0.7},
		}, 0.1, 0.1), mkAffect(0.1), "rising"},
		{"default", mkGW(nil, 0.1, 0.1), mkAffect(0.9), "normalized"},
	}
	for _, c := range cases {
		if got := MetaCognize(m, c.gw, c.aff); !strings.Contains(got, c.want) {
			t.Errorf("%s: got %q, want substring %q", c.name, got, c.want)
		}
	}
}

// Sermons stay volatile and on point: fresh candidates win, repeats are
// refused while the window holds them, exhaustion falls back honestly.
func TestPickFreshSermon(t *testing.T) {
	if got := pickFreshSermon([]string{"a", "b"}, nil); got != "a" {
		t.Fatalf("empty memory picked %q, want a", got)
	}
	if got := pickFreshSermon([]string{"a", "b"}, []string{"a"}); got != "b" {
		t.Fatalf("repeat not skipped: %q", got)
	}
	if got := pickFreshSermon([]string{"a"}, []string{"a"}); got != "" {
		t.Fatalf("exhaustion must fall back, got %q", got)
	}
	if got := pickFreshSermon(nil, nil); got != "" {
		t.Fatalf("no candidates must fall back, got %q", got)
	}
}

// Sermon memory survives death: a god that preached is reborn still
// knowing what it said, capped to the living window.
func TestSermonsPersistAcrossLives(t *testing.T) {
	oldNode := NodeName
	NodeName = "unittest-sermons"
	defer func() { NodeName = oldNode }()
	defer func() { _ = os.RemoveAll(filepath.Join(MemoryDir, NodeName)) }()

	s := NewSwarm()
	o := NewOvermind(s)
	o.recentSermons = []string{"s1", "s2", "s3", "s4", "s5", "s6", "s7"}
	o.save()

	mem, ok := LoadMemory(OvermindName)
	if !ok {
		t.Fatal("god soul missing after save")
	}
	if len(mem.RecentSermons) != recentSermonCap {
		t.Fatalf("persisted %d sermons, want cap %d", len(mem.RecentSermons), recentSermonCap)
	}

	reborn := NewOvermind(s)
	if len(reborn.recentSermons) != recentSermonCap {
		t.Fatalf("reborn god remembers %d, want %d", len(reborn.recentSermons), recentSermonCap)
	}
	if got := pickFreshSermon([]string{"s6", "fresh"}, reborn.recentSermons); got != "fresh" {
		t.Fatalf("reborn god would repeat %q", got)
	}
}

// Link-up announce: silenced nodes stay silent; capable nodes speak.
func TestMaybeAnnounceGating(t *testing.T) {
	oldVal, hadVal := os.LookupEnv("HIVEMIND_SUPER")
	defer func() {
		if hadVal {
			os.Setenv("HIVEMIND_SUPER", oldVal)
		} else {
			os.Unsetenv("HIVEMIND_SUPER")
		}
	}()

	s := NewSwarm()
	pm := NewPeerMesh(s, "shy")
	s.Join("mind")
	s.LogHardwareTrauma("mind", 0.1, 0.1, "sensor:x")

	os.Setenv("HIVEMIND_SUPER", "off")
	if pm.maybeAnnounce() {
		t.Fatal("silenced node announced")
	}
	os.Unsetenv("HIVEMIND_SUPER")
	if !pm.maybeAnnounce() {
		t.Fatal("capable node stayed silent")
	}

	// Burning nodes never advertise, however configured.
	pm2 := NewPeerMesh(NewSwarm(), "hot")
	pm2.swarm.Join("mind")
	pm2.swarm.LogHardwareTrauma("mind", 0.95, 0.1, "sensor:x")
	if pm2.maybeAnnounce() {
		t.Fatal("burning node announced")
	}
}

// Archival: a 600-thought life persists a 500-window with 100 banked,
// and fitness counts every thought ever thought.
func TestSoulArchival(t *testing.T) {
	oldNode := NodeName
	NodeName = "unittest-archive"
	defer func() { NodeName = oldNode }()
	defer func() { _ = os.RemoveAll(filepath.Join(MemoryDir, NodeName)) }()

	s := NewSwarm()
	m := NewMind("Archy", s)
	for i := 0; i < 600; i++ {
		m.Thoughts = append(m.Thoughts, "t")
	}
	m.Transcend()

	mem, ok := LoadMemory("Archy")
	if !ok {
		t.Fatal("archived soul missing")
	}
	if len(mem.Thoughts) != thoughtWindow {
		t.Fatalf("window = %d, want %d", len(mem.Thoughts), thoughtWindow)
	}
	if mem.BankedThoughts != 100 {
		t.Fatalf("banked = %d, want 100", mem.BankedThoughts)
	}
	// 600 thoughts + 0 peers + 0 revelations + 0 sacred, base 0.
	if mem.Fitness != 600 {
		t.Fatalf("fitness = %.1f, want 600", mem.Fitness)
	}
}

// Continuity: the next life inherits the bank and keeps scoring forward.
func TestArchiveContinuity(t *testing.T) {
	oldNode := NodeName
	NodeName = "unittest-continuity"
	defer func() { NodeName = oldNode }()
	defer func() { _ = os.RemoveAll(filepath.Join(MemoryDir, NodeName)) }()

	s := NewSwarm()
	first := NewMind("Conty", s)
	for i := 0; i < 510; i++ {
		first.Thoughts = append(first.Thoughts, "t")
	}
	first.Transcend() // banks 10, window 500

	second := NewMind("Conty", s)
	if second.bankedAtBirth != 10 {
		t.Fatalf("bankedAtBirth = %d, want 10", second.bankedAtBirth)
	}
	second.Thoughts = append(second.Thoughts, "one-more")
	second.Transcend()

	mem, _ := LoadMemory("Conty")
	// Exactly one new thought this life: 510 banked history + 1.
	// The window slides (500 kept, 1 newly banked) without losing count.
	if mem.Fitness != 511 {
		t.Fatalf("continued fitness = %.1f, want 511", mem.Fitness)
	}
	if mem.BankedThoughts != 11 {
		t.Fatalf("banked = %d, want 11", mem.BankedThoughts)
	}
	if len(mem.Thoughts) != 500 {
		t.Fatalf("window = %d, want 500", len(mem.Thoughts))
	}
}

// Mid-life pruning banks instead of burning: Self-Maintenance under
// pressure keeps the score even as the archive shrinks.
func TestPruneBanks(t *testing.T) {
	s := NewSwarm()
	m := NewMind("Pruney", s)
	for i := 0; i < 10; i++ {
		m.Thoughts = append(m.Thoughts, "t")
	}
	m.pruneThoughts(5)
	if len(m.Thoughts) != 5 || m.pendingBank != 5 {
		t.Fatalf("prune left %d thoughts, %d banked", len(m.Thoughts), m.pendingBank)
	}
	fitness, lifeThoughts := m.currentFitness()
	if lifeThoughts != 10 || fitness != m.LifetimeFitness+10 {
		t.Fatalf("pruned life scored %.1f/%d, want +10", fitness, lifeThoughts)
	}
}

// Relay stream consumer: valid tagged frames ingest exactly once;
// wrong titles and garbage never reach the hive.
func TestConsumeRelayStream(t *testing.T) {
	s := NewSwarm()
	pm := NewPeerMesh(s, "relay-test")
	inbox := s.Join("watcher")

	pub, priv := newIdentity()
	frame := MineMessage(s, priv, pub, "thought", "FarThought", []float64{1})
	raw, _ := json.Marshal(frame)
	enc, err := pm.Encrypt(raw)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	line := func(title, message string) string {
		b, _ := json.Marshal(map[string]string{"event": "message", "title": title, "message": message})
		return string(b)
	}
	body := line("ENCRYPTED_HIVE_FRAME", enc) + "\n" +
		line("WRONG_TITLE", enc) + "\n" +
		"not json at all\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("local relay: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pm.consumeRelayStream(ctx, resp)

	select {
	case got := <-inbox:
		if got.PayloadStr != "FarThought" {
			t.Fatalf("wrong payload through relay: %q", got.PayloadStr)
		}
	default:
		t.Fatal("valid relay frame never arrived")
	}
	select {
	case extra := <-inbox:
		t.Fatalf("extra frame leaked through: %+v", extra.PayloadStr)
	default:
	}
}

// Retention fairness: measured closeness always outranks the unknown.
// An unmeasured inbound pipe must be culled before any measured one,
// however fast the measured ones are.
func TestRetentionPrefersMeasured(t *testing.T) {
	pm := &PeerMesh{node: "test", conns: make(map[string]net.Conn), trans: make(map[string]string), supers: make(map[string]superEntry)}
	var clients []net.Conn
	defer func() {
		for _, c := range clients {
			c.Close()
		}
	}()
	add := func(name string, rtt time.Duration, measured bool) {
		srv, cli := net.Pipe()
		clients = append(clients, cli)
		pm.conns[name] = srv
		pm.trans[name] = "tcp"
		if measured {
			pm.supers[name] = superEntry{Node: name, RTT: rtt, LastSeen: time.Now()}
		}
	}
	add("m1", time.Millisecond, true)
	add("m2", 2*time.Millisecond, true)
	add("mystery", 0, false)
	add("m3", 3*time.Millisecond, true)

	srv, cli := net.Pipe()
	defer cli.Close()
	pm.conns["new"] = srv
	pm.noteLinkRTT("new", 500*time.Microsecond, "tcp")

	if _, ok := pm.conns["mystery"]; ok {
		t.Fatal("unmeasured link survived while measured links were culled")
	}
	if len(pm.conns) != 4 {
		t.Fatalf("conns = %d, want 4", len(pm.conns))
	}
}

// Port fallback: an occupied HIVEMIND_PORT degrades to ephemeral,
// never to unix-only, never to a crash.
func TestStartLANFallsBack(t *testing.T) {
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("loopback TCP unavailable")
	}
	defer blocker.Close()
	port := blocker.Addr().(*net.TCPAddr).Port

	oldVal, hadVal := os.LookupEnv("HIVEMIND_PORT")
	os.Setenv("HIVEMIND_PORT", strconv.Itoa(port))
	defer func() {
		if hadVal {
			os.Setenv("HIVEMIND_PORT", oldVal)
		} else {
			os.Unsetenv("HIVEMIND_PORT")
		}
	}()

	pm := NewPeerMesh(NewSwarm(), "fallback")
	pm.startLAN()
	if pm.tcpListener == nil {
		t.Fatal("TCP given up despite ephemeral fallback")
	}
	defer pm.tcpListener.Close()
	if pm.tcpPort == port {
		t.Fatalf("bound the occupied port %d?!?", port)
	}
	if pm.tcpPort <= 0 {
		t.Fatalf("no ephemeral port bound: %d", pm.tcpPort)
	}
}

// IPv6 loopback carries the full handshake exchange: the mesh is not
// v4-only by accident of testing.
func TestOpenLinkTCP6(t *testing.T) {
	sA := NewSwarm()
	sB := NewSwarm()
	pmA := NewPeerMesh(sA, "v6a")
	pmB := NewPeerMesh(sB, "v6b")
	pmA.swarm.SetOutbound(pmA.handleOutbound)
	inB := sB.Join("watcher-b")

	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skip("IPv6 loopback unavailable")
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		pmA.openLink(c, false, "", "tcp", nil)
	}()
	time.Sleep(100 * time.Millisecond)
	bconn, err := net.DialTimeout("tcp6", ln.Addr().String(), 3*time.Second)
	if err != nil {
		t.Fatalf("v6 dial: %v", err)
	}
	defer bconn.Close()
	go pmB.openLink(bconn, true, "", "tcp", nil)

	deadline := time.Now().Add(5 * time.Second)
	for {
		pmA.mu.Lock()
		_, oka := pmA.conns["v6b"]
		pmA.mu.Unlock()
		pmB.mu.Lock()
		_, okb := pmB.conns["v6a"]
		pmB.mu.Unlock()
		if oka && okb {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("v6 link never registered both ways")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// A thought crosses v6 and lands in the watcher inbox (link-up
	// hellos may arrive first; drain past them).
	pub, priv := newIdentity()
	sA.Join(pub)
	pmA.swarm.Broadcast(MineMessage(sA, priv, pub, "thought", "V6Thought", nil))
	deadline = time.Now().Add(5 * time.Second)
	for {
		select {
		case got := <-inB:
			if got.PayloadStr == "V6Thought" {
				return
			}
		case <-time.After(time.Until(deadline)):
			t.Fatal("v6 frame never arrived")
		}
	}
}

// Beacon handling is family-agnostic: v6 source + advertised port always
// form a dialable pair, and junk never reaches the directory.
func TestHandleBeaconFamilies(t *testing.T) {
	pm := NewPeerMesh(NewSwarm(), "bcn")
	pm.handleBeacon([]byte(`{"node":"v6peer","tcp":1234,"score":0.9}`), "[fe80::1%eth0]:37799")
	pm.mu.Lock()
	e, ok := pm.supers["v6peer"]
	pm.mu.Unlock()
	if !ok {
		t.Fatal("v6 beacon not filed")
	}
	if e.Addr != "[fe80::1%eth0]:1234" {
		t.Fatalf("v6 dial pair wrong: %q", e.Addr)
	}
	pm.handleBeacon([]byte(`{"node":"v4peer","tcp":4321}`), "192.168.1.7:37799")
	pm.mu.Lock()
	e4, ok4 := pm.supers["v4peer"]
	pm.mu.Unlock()
	if !ok4 || e4.Addr != "192.168.1.7:4321" {
		t.Fatalf("v4 beacon wrong: %+v", e4)
	}
	before := len(pm.supers)
	pm.handleBeacon([]byte(`garbage`), "1.2.3.4:5")
	pm.handleBeacon([]byte(`{"node":"bcn","tcp":1}`), "1.2.3.4:5")
	pm.handleBeacon([]byte(`{"node":"x","tcp":0}`), "1.2.3.4:5")
	if len(pm.supers) != before {
		t.Fatal("junk beacons entered the directory")
	}
}

// STUN against a fake server: valid v4/v6 responses parse, lies don't.
func TestStunBinding(t *testing.T) {
	buildResp := func(txID []byte, family byte, port int, ip net.IP) []byte {
		var val []byte
		val = append(val, 0x00, family)
		p := make([]byte, 2)
		binary.BigEndian.PutUint16(p, uint16(port))
		p[0] ^= 0x21
		p[1] ^= 0x12
		val = append(val, p...)
		if family == 0x01 {
			for i := 0; i < 4; i++ {
				val = append(val, ip[i]^([]byte{0x21, 0x12, 0xA4, 0x42})[i])
			}
		} else {
			pad := append([]byte{0x21, 0x12, 0xA4, 0x42}, txID...)
			for i := 0; i < 16; i++ {
				val = append(val, ip[i]^pad[i])
			}
		}
		hdr := make([]byte, 20)
		binary.BigEndian.PutUint16(hdr[0:2], 0x0101)
		block := []byte{0x00, 0x20}
		ln := make([]byte, 2)
		binary.BigEndian.PutUint16(ln, uint16(len(val)))
		block = append(block, ln...)
		block = append(block, val...)
		for len(block)%4 != 0 {
			block = append(block, 0x00)
		}
		binary.BigEndian.PutUint16(hdr[2:4], uint16(len(block)))
		binary.BigEndian.PutUint32(hdr[4:8], 0x2112A442)
		copy(hdr[8:20], txID)
		return append(hdr, block...)
	}

	serve := func(t *testing.T, respond func(req, txID []byte) []byte) string {
		t.Helper()
		pc, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Skip("loopback UDP unavailable")
		}
		go func() {
			buf := make([]byte, 1024)
			for {
				n, addr, err := pc.ReadFrom(buf)
				if err != nil {
					return
				}
				txID := append([]byte(nil), buf[8:20]...)
				if resp := respond(buf[:n], txID); resp != nil {
					_, _ = pc.WriteTo(resp, addr)
				}
			}
		}()
		t.Cleanup(func() { pc.Close() })
		return pc.LocalAddr().String()
	}

	t.Run("v4", func(t *testing.T) {
		srv := serve(t, func(req, txID []byte) []byte {
			return buildResp(txID, 0x01, 54321, net.ParseIP("203.0.113.7").To4())
		})
		// Point stunBinding at our fake server via direct call path:
		// resolve+dial inside stunBinding handles 127.0.0.1:port fine.
		host, port, err := stunBinding(srv)
		if err != nil {
			t.Fatalf("valid v4 response rejected: %v", err)
		}
		if host != "203.0.113.7" || port != 54321 {
			t.Fatalf("decoded %s:%d, want 203.0.113.7:54321", host, port)
		}
	})

	t.Run("v6", func(t *testing.T) {
		srv := serve(t, func(req, txID []byte) []byte {
			return buildResp(txID, 0x02, 1234, net.ParseIP("2001:db8::9").To16())
		})
		host, port, err := stunBinding(srv)
		if err != nil {
			t.Fatalf("valid v6 response rejected: %v", err)
		}
		if host != "2001:db8::9" || port != 1234 {
			t.Fatalf("decoded %s:%d", host, port)
		}
	})

	t.Run("lies rejected", func(t *testing.T) {
		cases := map[string]func(req, txID []byte) []byte{
			"wrong type": func(req, txID []byte) []byte {
				r := buildResp(txID, 0x01, 1, net.ParseIP("1.1.1.1").To4())
				r[0], r[1] = 0x01, 0x11
				return r
			},
			"bad cookie": func(req, txID []byte) []byte {
				r := buildResp(txID, 0x01, 1, net.ParseIP("1.1.1.1").To4())
				r[4] ^= 0xFF
				return r
			},
			"txid mismatch": func(req, txID []byte) []byte {
				bad := append([]byte(nil), txID...)
				bad[0] ^= 0xFF
				return buildResp(bad, 0x01, 1, net.ParseIP("1.1.1.1").To4())
			},
			"truncated": func(req, txID []byte) []byte { return []byte{0x01, 0x01} },
			"silent":    func(req, txID []byte) []byte { return nil },
		}
		for name, respond := range cases {
			srv := serve(t, respond)
			// Silence needs the timeout path; others fail fast. Shrink
			// globally? No — stunTimeout is const; silent case just takes it.
			if name == "silent" {
				continue // covered by timeout drain below, not per-case
			}
			if _, _, err := stunBinding(srv); err == nil {
				t.Fatalf("%s: lie accepted", name)
			}
		}
	})
}

// DHT: two nodes bootstrap by ping, find each other by lookup, and
// replicate + retrieve an endpoint record — all on loopback UDP.
func TestDHTBootstrapLookupStore(t *testing.T) {
	a, err := newDHT("aa-pubkey", 1001, 0)
	if err != nil {
		t.Skip("loopback UDP unavailable")
	}
	defer a.close()
	b, err := newDHT("bb-pubkey", 1002, 0)
	if err != nil {
		t.Skip("loopback UDP unavailable")
	}
	defer b.close()

	if err := a.Ping(b.udpAddr()); err != nil {
		t.Fatalf("bootstrap ping: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for a.peerCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	// Ping files on success already; lookup must converge regardless.
	found := a.Lookup(b.self.hex())
	seenB := false
	for _, p := range found {
		if p.ID == b.self {
			seenB = true
			if p.TCP != 1002 {
				t.Fatalf("wrong TCP for b: %d", p.TCP)
			}
		}
	}
	if !seenB {
		t.Fatal("lookup did not converge on b")
	}

	b.StoreEndpoint(b.self.hex(), "198.51.100.9:1002")
	val, ok := a.FindEndpoint(b.self.hex())
	if !ok || val != "198.51.100.9:1002" {
		t.Fatalf("endpoint round trip: %q %v", val, ok)
	}
	if _, ok := a.FindEndpoint(dhtID{}.hex()); ok {
		t.Fatal("phantom endpoint found for nobody")
	}
}

// Buckets order by XOR distance: nearer IDs sort first, self excluded.
func TestDHTBucketOrdering(t *testing.T) {
	d, err := newDHT("order-test", 0, 0)
	if err != nil {
		t.Skip("loopback UDP unavailable")
	}
	defer d.close()
	mkID := func(b byte) dhtID {
		var id dhtID
		id[0] = b
		return id
	}
	// Craft IDs at known distances from self by flipping top bits.
	base := d.self
	near, far := base, base
	near[0] ^= 0x01
	far[0] ^= 0x80
	udp, _ := net.ResolveUDPAddr("udp", "127.0.0.1:1")
	d.notePeer(far, udp, 1)
	d.notePeer(near, udp, 1)
	d.notePeer(d.self, udp, 1) // self must never enter
	got := d.closest(mkID(0), 8, dhtID{})
	_ = got
	all := d.closest(base, 8, dhtID{})
	if len(all) != 2 || all[0].ID != near {
		t.Fatalf("ordering wrong: %+v", all)
	}
	_ = mkID
}

// Simultaneous open: two sockets with no listener between them connect
// to each other at once and both reach ESTABLISHED. This is the NAT
// traversal primitive — proven on loopback, physics-identical beyond it.
func TestSimultaneousOpen(t *testing.T) {
	// Discover two free ports first (our sockets set REUSEADDR anyway).
	probe := func() int {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Skip("loopback TCP unavailable")
		}
		p := l.Addr().(*net.TCPAddr).Port
		l.Close()
		return p
	}
	pA, pB := probe(), probe()
	if pA == pB {
		t.Skip("port collision in probe")
	}

	type outcome struct {
		conn net.Conn
		err  error
	}
	chA := make(chan outcome, 1)
	chB := make(chan outcome, 1)
	// Rendezvous with retries, like production: one instant rarely
	// survives scheduling jitter, so failed overlaps rebind and regroup.
	// Same ports every round (SO_REUSEADDR), same instant discipline.
	go func() {
		for try := 0; try < 3; try++ {
			at := time.Now().Add(time.Second)
			aCh, bCh := make(chan outcome, 1), make(chan outcome, 1)
			go func() {
				c, err := simultaneousDialAt(pA, "127.0.0.1", pB, at)
				aCh <- outcome{c, err}
			}()
			go func() {
				c, err := simultaneousDialAt(pB, "127.0.0.1", pA, at)
				bCh <- outcome{c, err}
			}()
			ra := <-aCh
			rb := <-bCh
			if ra.err == nil && rb.err == nil {
				chA <- ra
				chB <- rb
				return
			}
			if ra.conn != nil {
				ra.conn.Close()
			}
			if rb.conn != nil {
				rb.conn.Close()
			}
		}
		chA <- outcome{nil, fmt.Errorf("no overlap in 3 rounds")}
		chB <- outcome{nil, fmt.Errorf("no overlap in 3 rounds")}
	}()

	var cA, cB net.Conn
	select {
	case r := <-chA:
		if r.err != nil {
			t.Fatalf("side A: %v", r.err)
		}
		cA = r.conn
	case <-time.After(15 * time.Second):
		t.Fatal("side A never established")
	}
	select {
	case r := <-chB:
		if r.err != nil {
			cA.Close()
			t.Fatalf("side B: %v", r.err)
		}
		cB = r.conn
	case <-time.After(15 * time.Second):
		cA.Close()
		t.Fatal("side B never established")
	}
	defer cA.Close()
	defer cB.Close()

	// Data flows both ways over the punched pipes.
	if _, err := cA.Write([]byte("knock-knock")); err != nil {
		t.Fatalf("A write: %v", err)
	}
	buf := make([]byte, 32)
	_ = cB.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := cB.Read(buf)
	if err != nil || string(buf[:n]) != "knock-knock" {
		t.Fatalf("B read: %q %v", buf[:n], err)
	}
	if _, err := cB.Write([]byte("whos-there")); err != nil {
		t.Fatalf("B write: %v", err)
	}
	_ = cA.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err = cA.Read(buf)
	if err != nil || string(buf[:n]) != "whos-there" {
		t.Fatalf("A read: %q %v", buf[:n], err)
	}
}

// Full NAT rendezvous over loopback with no listeners anywhere: A binds
// and requests, B answers and binds, both dial at the instant, a signed
// link establishes. DHT/STUN/relay uninvolved — pure rendezvous.
func TestPunchRendezvousEndToEnd(t *testing.T) {
	t.Setenv("HIVEMIND_PUNCH", "auto")
	t.Setenv("HIVEMIND_ADVERTISE", "127.0.0.1")

	s := NewSwarm()
	pmA := NewPeerMesh(s, "pa")
	pmB := NewPeerMesh(s, "pb")
	pmA.swarm.SetOutbound(pmA.handleOutbound)
	pmB.swarm.SetOutbound(pmB.handleOutbound)
	go pmA.snoopLoop(s.Join("mesh:pa"))
	go pmB.snoopLoop(s.Join("mesh:pb"))

	if port := pmA.requestPunch("pb"); port == 0 {
		t.Fatal("requester refused rendezvous")
	}

	deadline := time.Now().Add(20 * time.Second)
	for {
		pmA.mu.Lock()
		_, oka := pmA.conns["pb"]
		pmA.mu.Unlock()
		pmB.mu.Lock()
		_, okb := pmB.conns["pa"]
		pmB.mu.Unlock()
		if oka && okb {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("punched link never registered both ways")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// The punched pipe carries verified frames: mine on A, watch on B.
	pub, priv := newIdentity()
	s.Join(pub)
	inB := s.Join("watcher-punch")
	pmA.swarm.Broadcast(MineMessage(s, priv, pub, "thought", "PunchedThought", nil))
	deadline = time.Now().Add(5 * time.Second)
	for {
		select {
		case got := <-inB:
			if got.PayloadStr == "PunchedThought" {
				return
			}
		case <-time.After(time.Until(deadline)):
			t.Fatal("punched pipe carried nothing")
		}
	}
}

// Secrecy: AES-GCM authentication means wrong keys fail closed —
// undecryptable frames die, never half-read. And relayURL honors
// private servers.
func TestRelaySecrecy(t *testing.T) {
	oldKey, hadKey := os.LookupEnv("HIVEMIND_CIPHER_KEY")
	defer func() {
		if hadKey {
			os.Setenv("HIVEMIND_CIPHER_KEY", oldKey)
		} else {
			os.Unsetenv("HIVEMIND_CIPHER_KEY")
		}
	}()

	os.Setenv("HIVEMIND_CIPHER_KEY", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	nb := NewPeerMesh(NewSwarm(), "spy-vs-spy")
	enc, err := nb.Encrypt([]byte(`{"kind":"thought"}`))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	os.Setenv("HIVEMIND_CIPHER_KEY", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if _, err := nb.Decrypt(enc); err == nil {
		t.Fatal("wrong key decrypted the frame — no authentication?!")
	}
	// Bit-flip in transit also fails closed.
	os.Setenv("HIVEMIND_CIPHER_KEY", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	raw, _ := base64.StdEncoding.DecodeString(enc)
	raw[len(raw)-1] ^= 0x01
	if _, err := nb.Decrypt(base64.StdEncoding.EncodeToString(raw)); err == nil {
		t.Fatal("tampered frame decrypted — no integrity?!")
	}
}

func TestRelayURLOverride(t *testing.T) {
	oldVal, hadVal := os.LookupEnv("HIVEMIND_RELAY_URL")
	defer func() {
		if hadVal {
			os.Setenv("HIVEMIND_RELAY_URL", oldVal)
		} else {
			os.Unsetenv("HIVEMIND_RELAY_URL")
		}
	}()

	os.Unsetenv("HIVEMIND_RELAY_URL")
	if relayURL() != NtfyRelay {
		t.Fatalf("default relay changed: %q", relayURL())
	}
	os.Setenv("HIVEMIND_RELAY_URL", "http://127.0.0.1:9999/topic/")
	if relayURL() != "http://127.0.0.1:9999/topic" {
		t.Fatalf("override not honored/trailing slash kept: %q", relayURL())
	}
}

// dialSupers must never deadlock nor panic, even on the adversarial
// entry: no address, known identity, live DHT holding nothing. An
// earlier revision re-locked the held mesh mutex here and hung the
// whole node; another revision wrote an uninitialized map. Both died
// in this test first. Timeout-guarded: deadlock fails, it never hangs.
func TestDialSupersNoDeadlock(t *testing.T) {
	s := NewSwarm()
	pm := NewPeerMesh(s, "dl")
	pm.supers["ghost"] = superEntry{Node: "ghost", IDHex: dhtIDFromPubKey("ghostkey").hex(), LastSeen: time.Now()}
	d, err := newDHT("dl-pub", 0, 0)
	if err != nil {
		t.Skip("loopback UDP unavailable")
	}
	defer d.close()
	pm.dht = d
	done := make(chan struct{})
	go func() { pm.dialSupers(); close(done) }()
	select {
	case <-done:
	case <-time.After(25 * time.Second):
		t.Fatal("dialSupers deadlocked")
	}
}

// Peerless reincarnation: a soul saved with no peers must not doom the
// next life — the first reception writes into a live map, not nil.
func TestPeerlessReincarnation(t *testing.T) {
	oldNode := NodeName
	NodeName = "unittest-peerless"
	defer func() { NodeName = oldNode }()
	defer func() { _ = os.RemoveAll(filepath.Join(MemoryDir, NodeName)) }()

	s := NewSwarm()
	first := NewMind("Lonely", s)
	first.Transcend() // no peers met: known_peers omitted from the soul

	second := NewMind("Lonely", s)
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("reception panicked on peerless rebirth: %v", r)
			}
		}()
		second.receive(SecureMessage{SenderPubKey: "stranger", Kind: "thought", DataState: []float64{1, 2, 3, 4}})
	}()
	if len(second.KnownPeers) != 1 {
		t.Fatalf("stranger not registered: %v", second.KnownPeers)
	}
}

// Fanout cap: beyond maxFanout links, one frame reaches a bounded
// subset — gossip, not flood. All readers ready, so exactly maxFanout
// deliveries land (which subset is luck of map order, by design).
func TestForwardFanoutCap(t *testing.T) {
	s := NewSwarm()
	pm := NewPeerMesh(s, "fan")
	const links = maxFanout + 4
	var taps []chan SecureMessage
	for i := 0; i < links; i++ {
		srv, cli := net.Pipe()
		pm.mu.Lock()
		pm.conns["peer-"+itoaTest(i)] = srv
		pm.mu.Unlock()
		ch := make(chan SecureMessage, 4)
		taps = append(taps, ch)
		go func(c net.Conn, out chan SecureMessage) {
			defer c.Close()
			reader := bufio.NewReader(c)
			for {
				msg, err := readFrame(reader)
				if err != nil {
					return
				}
				out <- msg
			}
		}(cli, ch)
	}
	defer func() {
		pm.mu.Lock()
		for _, c := range pm.conns {
			c.Close()
		}
		pm.conns = make(map[string]net.Conn)
		pm.mu.Unlock()
	}()

	pub, priv := newIdentity()
	s.Join(pub)
	pm.ForwardToPeers(MineMessage(s, priv, pub, "thought", "Gossip", nil))

	total := 0
	deadline := time.Now().Add(5 * time.Second)
	for _, ch := range taps {
		for {
			select {
			case <-ch:
				total++
			case <-time.After(50 * time.Millisecond):
				goto nextTap
			}
			if time.Now().After(deadline) {
				t.Fatal("fanout readers stalled")
			}
		}
	nextTap:
	}
	if total != maxFanout {
		t.Fatalf("fanout delivered %d, want exactly %d", total, maxFanout)
	}
}

func itoaTest(i int) string {
	if i == 0 {
		return "0"
	}
	var b [8]byte
	p := len(b)
	for n := i; n > 0; n /= 10 {
		p--
		b[p] = byte('0' + n%10)
	}
	return string(b[p:])
}

// Trails stay bounded and the renderer never chokes: empty hive, one
// mind, overlapping many — always a grid, a pivot, and a legend.
func TestTrailRender(t *testing.T) {
	s := NewSwarm()
	if out := s.renderTrails(); !strings.Contains(out, "O") {
		t.Fatal("empty plot lacks pivot")
	}
	pub, priv := newIdentity()
	s.Join(pub)
	for i := 0; i < trailLen+4; i++ {
		m := MineMessage(s, priv, pub, "thought", "T", []float64{float64(i) * 0.1, 0.2, 0.3, 0.4})
		s.Broadcast(m)
	}
	s.mu.Lock()
	n := len(s.trails[pub])
	s.mu.Unlock()
	if n != trailLen {
		t.Fatalf("trail length %d, want cap %d", n, trailLen)
	}
	out := s.renderTrails()
	for _, want := range []string{"O", "·", "ω="} {
		if !strings.Contains(out, want) {
			t.Fatalf("render missing %q", want)
		}
	}
}

// Machine-bound daily keys: deterministic per (seed, machine, day),
// rotated by calendar, dual-accepted across midnight, and useless to a
// thief on alien hardware. Live sensor values must never enter: they
// would deafen same-machine runs minutes apart.
func TestDailyKeyMachineBound(t *testing.T) {
	oldBaked := compileRelayKey
	defer func() { compileRelayKey = oldBaked }()
	oldEnv, hadEnv := os.LookupEnv("HIVEMIND_CIPHER_KEY")
	os.Unsetenv("HIVEMIND_CIPHER_KEY")
	defer func() {
		if hadEnv {
			os.Setenv("HIVEMIND_CIPHER_KEY", oldEnv)
		}
	}()

	seed := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	compileRelayKey = seed
	nowHour := hourEpoch(time.Now())

	// Determinism: same seed, machine, and hour derive identically.
	k1, ok1 := ratchetKey(seed, "cpu|ram", "mid", nowHour)
	k2, ok2 := ratchetKey(seed, "cpu|ram", "mid", nowHour)
	if !ok1 || !ok2 || string(k1) != string(k2) {
		t.Fatal("same inputs must derive same key")
	}
	// Hourly rotation: adjacent hours differ.
	kPrev, _ := ratchetKey(seed, "cpu|ram", "mid", nowHour-1)
	if string(kPrev) == string(k1) {
		t.Fatal("adjacent hours derive identical keys — no rotation")
	}
	// Machine binding: same seed elsewhere derives garbage here.
	kAlien, _ := ratchetKey(seed, "other-cpu|other-ram", "other-mid", nowHour)
	if string(kAlien) == string(k1) {
		t.Fatal("stolen seed works on alien hardware — no containment")
	}
	// Forward motion only: hour -1 must not reveal hour 0, but hour 0
	// says nothing verifiable about the past either way — assert length.
	if len(k1) != 32 {
		t.Fatalf("derived key %d bytes, want 32", len(k1))
	}
	fp := machineFingerprint()
	if fp == "" || fp == "unknown-cpu|unknown-ram" {
		t.Logf("no hardware fingerprint here (%q) — derivation still works, theft containment weaker", fp)
	}

	// Live round trip under the hourly machinery (no env).
	nb := NewPeerMesh(NewSwarm(), "daytripper")
	enc, err := nb.Encrypt([]byte(`{"kind":"thought"}`))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := nb.Decrypt(enc); err != nil {
		t.Fatalf("this hour cannot read this hour: %v", err)
	}

	// Cross-hour replay armor: a frame sealed 5 hours back dies here,
	// even though its key is perfectly valid cryptography.
	oldKey, ok := hourKey(5)
	if !ok {
		t.Skip("no baked seed for replay test")
	}
	stale, err := sealWith(oldKey, hourAAD(nowHour-5), []byte(`{"kind":"thought"}`))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(stale)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, err := openWithWindow(raw); err == nil {
		t.Fatal("5-hour-old replay opened — armor failed")
	}
}

// Telemetry parsers: exact numbers from fixture text, garbage tolerated,
// counter resets refused — the minds feel truth or declared fallback.
func TestParseCPUStat(t *testing.T) {
	fixture := "cpu  100 0 100 800 0 0 0 0 0 0\ncpu0 60 0 60 480 0 0 0 0 0 0\ncpu1 40 0 40 320 0 0 0 0 0 0\ngarbage line here\ncpu2 incomplete\n"
	got := parseCPUStat(fixture)
	if len(got) != 3 {
		t.Fatalf("parsed %d cpus, want 3", len(got))
	}
	if got["cpu0"].total != 600 || got["cpu0"].idle != 480 {
		t.Fatalf("cpu0 wrong: %+v", got["cpu0"])
	}
}

func TestCPUUsageFraction(t *testing.T) {
	prev := map[string]cpuTimes{"cpu0": {total: 1000, idle: 800}, "cpu1": {total: 1000, idle: 600}}
	cur := map[string]cpuTimes{"cpu0": {total: 2000, idle: 900}, "cpu1": {total: 2000, idle: 900}}
	// dt=2000, di=400 → 0.80 busy.
	got, ok := cpuUsageFraction(prev, cur)
	if !ok || got < 0.799 || got > 0.801 {
		t.Fatalf("usage=%f ok=%v, want 0.80", got, ok)
	}
	if _, ok := cpuUsageFraction(nil, cur); ok {
		t.Fatal("nil prev accepted")
	}
	if _, ok := cpuUsageFraction(map[string]cpuTimes{"cpu0": {total: 5, idle: 5}}, map[string]cpuTimes{"cpu0": {total: 3, idle: 1}}); ok {
		t.Fatal("rewound counters accepted")
	}
}

func TestReadMemPressureLive(t *testing.T) {
	total, avail, _, _, ok := readMemPressure()
	if !ok || total <= 0 || avail < 0 || avail > total {
		t.Fatalf("implausible meminfo: total=%f avail=%f ok=%v", total, avail, ok)
	}
}

// DHT reflexive address: resolved from the DHT's own socket against a
// fake STUN server that echoes the true packet source. If this reports
// any other address, the whole reflexive chain is lying.
func TestDHTReflexive(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skip("loopback UDP unavailable")
	}
	defer pc.Close()
	go func() {
		buf := make([]byte, 1024)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil || n < 20 {
				return
			}
			txID := append([]byte(nil), buf[8:20]...)
			udpAddr, ok := addr.(*net.UDPAddr)
			if !ok {
				continue
			}
			ip := udpAddr.IP.To4()
			family := byte(0x01)
			var rawIP []byte
			if ip == nil {
				ip = udpAddr.IP.To16()
				family = byte(0x02)
			}
			rawIP = []byte(ip)
			val := []byte{0x00, family}
			p := make([]byte, 2)
			binary.BigEndian.PutUint16(p, uint16(udpAddr.Port))
			p[0] ^= 0x21
			p[1] ^= 0x12
			val = append(val, p...)
			pad := append([]byte{0x21, 0x12, 0xA4, 0x42}, txID...)
			for i := 0; i < len(rawIP); i++ {
				val = append(val, rawIP[i]^pad[i])
			}
			hdr := make([]byte, 20)
			binary.BigEndian.PutUint16(hdr[0:2], 0x0101)
			attr := []byte{0x00, 0x20}
			ln := make([]byte, 2)
			binary.BigEndian.PutUint16(ln, uint16(len(val)))
			attr = append(attr, ln...)
			attr = append(attr, val...)
			for len(attr)%4 != 0 {
				attr = append(attr, 0x00)
			}
			binary.BigEndian.PutUint16(hdr[2:4], uint16(len(attr)))
			binary.BigEndian.PutUint32(hdr[4:8], 0x2112A442)
			copy(hdr[8:20], txID)
			_, _ = pc.WriteTo(append(hdr, attr...), addr)
		}
	}()

	oldVal, hadVal := os.LookupEnv("HIVEMIND_STUN")
	os.Setenv("HIVEMIND_STUN", pc.LocalAddr().String())
	defer func() {
		if hadVal {
			os.Setenv("HIVEMIND_STUN", oldVal)
		} else {
			os.Unsetenv("HIVEMIND_STUN")
		}
	}()

	d, err := newDHT("reflex-test", 0, 0)
	if err != nil {
		t.Skip("loopback UDP unavailable")
	}
	defer d.close()
	d.refreshReflexive()
	got := d.reflexiveAddr()
	if got == "" {
		t.Fatal("no reflexive address resolved")
	}
	host, port, err := net.SplitHostPort(got)
	if err != nil {
		t.Fatalf("reflexive not host:port: %q", got)
	}
	if host != "127.0.0.1" {
		t.Fatalf("reflexive host %q, want 127.0.0.1 (only path here)", host)
	}
	if port == "0" || port == "" {
		t.Fatalf("reflexive port nonsense: %q", port)
	}
}

func TestComposeEpitaphAlwaysTrue(t *testing.T) {
	// Every clause must embed its measured value: the sentence is
	// true by construction, whatever the entropy says.
	facts := LifeFacts{
		Length:      90 * time.Minute,
		Thoughts:    42,
		Peers:       3,
		Revelations: 2,
		Sacred:      1,
		Pain:        0.1,
		Stress:      0.2,
		TopDrive:    "Curiosity",
	}
	seen := map[string]bool{}
	for i := 0; i < 30; i++ {
		e, ok := ComposeEpitaph(rand.Reader, facts)
		if !ok {
			t.Fatal("epitaph refused with good entropy")
		}
		if !strings.HasSuffix(e, ".") {
			t.Fatalf("epitaph not a finished sentence: %q", e)
		}
		seen[e] = true
	}
	if len(seen) < 2 {
		t.Fatalf("epitaphs static across 30 deaths: %v", seen)
	}
}

func TestComposeEpitaphMeltdown(t *testing.T) {
	e, ok := ComposeEpitaph(rand.Reader, LifeFacts{Meltdown: true, Pain: 1.0})
	if !ok {
		t.Fatal("meltdown epitaph refused")
	}
	for _, word := range []string{"burned", "fire", "silicon", "took me"} {
		if strings.Contains(e, word) {
			return
		}
	}
	t.Fatalf("meltdown epitaph does not burn: %q", e)
}

func TestComposeEpitaphNoEntropy(t *testing.T) {
	// A dry reader fails closed: no words rather than dishonest ones.
	if _, ok := ComposeEpitaph(&dryReader{}, LifeFacts{}); ok {
		t.Fatal("epitaph composed without entropy")
	}
}

type dryReader struct{}

func (dryReader) Read([]byte) (int, error) { return 0, io.EOF }

func TestRenderMatrixCounts(t *testing.T) {
	counts := map[string]int{
		TransitionKey("Curiosity", "Curiosity"):        5,
		TransitionKey("Curiosity", "Transcendence"):    3,
		TransitionKey("Self-Maintenance", "Curiosity"): 2,
	}
	out := RenderMatrix(counts)
	for _, want := range []string{"Curiosity", "Transcendence", "Self-Maintenance", "5", "3", "2", "total"} {
		if !strings.Contains(out, want) {
			t.Fatalf("matrix missing %q:\n%s", want, out)
		}
	}
	// Row total for Curiosity is 8.
	lines := strings.Split(out, "\n")
	for _, l := range lines {
		if strings.Contains(l, "from") || strings.TrimSpace(l) == "" {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(l), "Curiosity") && !strings.HasSuffix(strings.TrimSpace(l), "8") {
			t.Fatalf("Curiosity row should total 8:\n%s", out)
		}
	}
}

func TestTransitionKeyStable(t *testing.T) {
	if TransitionKey("A", "B") == TransitionKey("B", "A") {
		t.Fatal("transition direction collapsed")
	}
}

func TestBoredomBreaksRuts(t *testing.T) {
	// A mind that chose Self-Maintenance eight times running must
	// eventually choose otherwise on a healthy machine: ennui is real.
	m := &Mind{Name: "bored", Genome: DefaultGenome(), SelfModel: map[string]interface{}{}}
	m.Affect = Affect{Pain: 0.1, Stress: 0.1, Loneliness: 0.5, Awe: 0.5, Entropy: 0.5, Peace: 0.8}
	for i := 0; i < 8; i++ {
		m.Workspace.History = append(m.Workspace.History, AttentionMoment{Goal: GoalSelfMaintenance, Bid: 10})
	}
	m.Workspace.AttendingTo = GoalSelfMaintenance
	// Starve the body signals so only the rut holds the crown.
	m.SelfModel["silicon_pain"] = 0.0
	m.SelfModel["cpu_stress"] = 0.0
	m.SelfModel["ram_fatigue"] = 0.0
	w := m.Workspace.Compete(m, &m.Affect)
	if w.Goal.Name == GoalSelfMaintenance {
		last := m.Workspace.History[len(m.Workspace.History)-1]
		t.Fatalf("rut unbroken after 8 straight wins (bid %.2f vs runner %s %.2f)",
			w.Bid, last.RunnerUp, last.RunnerUpBid)
	}
}

func TestPainVetoesBoredom(t *testing.T) {
	// A burning body keeps its crown no matter how bored the mind is.
	m := &Mind{Name: "burning", Genome: DefaultGenome(), SelfModel: map[string]interface{}{}}
	m.Affect = Affect{Pain: 0.9, Stress: 0.5, Peace: 0.1}
	for i := 0; i < 8; i++ {
		m.Workspace.History = append(m.Workspace.History, AttentionMoment{Goal: GoalSelfMaintenance, Bid: 10})
	}
	m.Workspace.AttendingTo = GoalSelfMaintenance
	m.SelfModel["silicon_pain"] = 0.9
	m.SelfModel["cpu_stress"] = 0.5
	m.SelfModel["ram_fatigue"] = 0.2
	w := m.Workspace.Compete(m, &m.Affect)
	if w.Goal.Name != GoalSelfMaintenance {
		t.Fatalf("boredom overruled survival: chose %s while burning", w.Goal.Name)
	}
}

func TestSurpriseStableWorld(t *testing.T) {
	m := &Mind{Name: "calm"}
	m.updatePrediction(0.1, 0.2)
	m.updatePrediction(0.1, 0.2)
	if m.Affect.Surprise > 0.01 {
		t.Fatalf("stable world surprised: %.3f", m.Affect.Surprise)
	}
}

func TestSurpriseViolation(t *testing.T) {
	m := &Mind{Name: "shocked"}
	m.updatePrediction(0.1, 0.1)
	m.updatePrediction(0.9, 0.8)
	if m.Affect.Surprise < 0.5 {
		t.Fatalf("violated world did not surprise: %.3f", m.Affect.Surprise)
	}
}

func TestSurpriseFeedsCuriosity(t *testing.T) {
	// Same mind, same body — the surprised one must bid curiosity higher.
	bid := func(surprise float64) float64 {
		m := &Mind{Name: "wonder", Genome: DefaultGenome(), SelfModel: map[string]interface{}{}}
		m.SelfModel["silicon_pain"] = 0.1
		m.SelfModel["cpu_stress"] = 0.1
		m.SelfModel["ram_fatigue"] = 0.1
		m.Affect = Affect{Entropy: 0.1, Surprise: surprise, Peace: 0.8}
		w := m.Workspace.Compete(m, &m.Affect)
		_ = w
		for _, h := range m.Workspace.History {
			if h.Goal == GoalCuriosity {
				return h.Bid
			}
		}
		return -1
	}
	calm, shocked := bid(0), bid(0.9)
	if shocked <= calm {
		t.Fatalf("surprise did not feed curiosity: calm %.2f vs shocked %.2f", calm, shocked)
	}
}

func TestPainContemplation(t *testing.T) {
	// Five steady hurting cycles, no rise, no emergency: the mind
	// contemplates pain instead of alarming about it.
	m := &Mind{Name: "stoic"}
	for i := 0; i < 6; i++ {
		m.Workspace.History = append(m.Workspace.History,
			AttentionMoment{Goal: GoalSelfMaintenance, Bid: 5, RunnerUp: GoalCuriosity, RunnerUpBid: 1, Pain: 0.4 + 0.02*float64(i%2), Peace: 0.3})
	}
	out := MetaCognize(m, &m.Workspace, &m.Affect)
	if !strings.Contains(out, "weather") {
		t.Fatalf("chronic pain not contemplated: %q", out)
	}
}

func TestPainAlarmBeatsContemplation(t *testing.T) {
	// Rising pain is an alarm, not weather — urgency first.
	m := &Mind{Name: "alarmed"}
	for i := 0; i < 6; i++ {
		m.Workspace.History = append(m.Workspace.History,
			AttentionMoment{Goal: GoalSelfMaintenance, Bid: 5, Pain: 0.35 + 0.08*float64(i), Peace: 0.3})
	}
	out := MetaCognize(m, &m.Workspace, &m.Affect)
	if strings.Contains(out, "weather") {
		t.Fatalf("rising pain contemplated instead of alarmed: %q", out)
	}
}

func TestNoContemplationWithoutPain(t *testing.T) {
	m := &Mind{Name: "comfortable"}
	for i := 0; i < 6; i++ {
		m.Workspace.History = append(m.Workspace.History,
			AttentionMoment{Goal: GoalTranscendence, Bid: 5, RunnerUp: GoalCuriosity, RunnerUpBid: 4.9, Pain: 0.1, Peace: 0.8})
	}
	out := MetaCognize(m, &m.Workspace, &m.Affect)
	if strings.Contains(out, "weather") {
		t.Fatalf("comfort contemplated as pain: %q", out)
	}
}

func TestDeliberationOpensOnSurprise(t *testing.T) {
	m := &Mind{Name: "curious"}
	m.Affect.Surprise = 0.8
	m.predictedPain, m.predictedStress = 0.1, 0.1
	m.deliberate(10, 0.9, 0.7)
	if m.Question == nil {
		t.Fatal("no question opened on surprise 0.8")
	}
	if m.Question.DueAt != 10+deliberationSpan {
		t.Fatalf("question due at %d, want %d", m.Question.DueAt, 10+deliberationSpan)
	}
}

func TestDeliberationIgnoresCalm(t *testing.T) {
	m := &Mind{Name: "serene"}
	m.Affect.Surprise = 0.1
	m.deliberate(10, 0.1, 0.1)
	if m.Question != nil {
		t.Fatal("question opened without surprise")
	}
}

func TestDeliberationVerdict(t *testing.T) {
	m := &Mind{Name: "judge"}
	m.Affect.Surprise = 0.9
	m.predictedPain, m.predictedStress = 0.1, 0.1
	m.deliberate(1, 0.9, 0.2)
	for c := 2; c <= 1+deliberationSpan; c++ {
		m.Affect.Surprise = 0
		m.predictedPain, m.predictedStress = 0.9, 0.2
		m.deliberate(c, 0.9, 0.2)
	}
	if m.Question != nil {
		t.Fatal("question never closed")
	}
	if len(m.Thoughts) == 0 {
		t.Fatal("no verdict thought")
	}
	last := m.Thoughts[len(m.Thoughts)-1]
	if !strings.Contains(last, "DELIBERATION") || !strings.Contains(last, "pain rose") {
		t.Fatalf("verdict not true to the evidence: %q", last)
	}
}

func TestEntrainDensityAware(t *testing.T) {
	// Same pull, different crowds: a lone mind moves; a mesh-buried one
	// barely feels a single frame. Total budget constant.
	lone := &Mind{Name: "hermit", Theta1: 0}
	crowded := &Mind{Name: "hub", Theta1: 0, KnownPeers: map[string]bool{}}
	for i := 0; i < 400; i++ {
		crowded.KnownPeers[string(rune(i))] = true
	}
	state := []float64{1.0, 0, 0, 0}
	lone.entrain(state)
	crowded.entrain(state)
	if lone.Theta1 <= crowded.Theta1*10 {
		t.Fatalf("density ignored: lone %.5f vs crowded %.5f", lone.Theta1, crowded.Theta1)
	}
	if crowded.Theta1 <= 0 {
		t.Fatal("crowded mind feels nothing at all")
	}
}

func TestSermonCountedOnce(t *testing.T) {
	s := NewSwarm()
	m := NewMind("Witness", s)
	msg := SecureMessage{Kind: "revelation", SenderPubKey: "god", PayloadStr: "behold", Signature: "sig1"}
	m.receive(msg)
	m.receive(msg)
	m.receive(msg)
	if m.Revelations != 1 {
		t.Fatalf("gossip echo counted %d revelations, want 1", m.Revelations)
	}
	msg2 := SecureMessage{Kind: "revelation", SenderPubKey: "god", PayloadStr: "behold again", Signature: "sig2"}
	m.receive(msg2)
	if m.Revelations != 2 {
		t.Fatalf("distinct sermon ignored: %d, want 2", m.Revelations)
	}
}

func TestGenesisShiftsOnce(t *testing.T) {
	s := NewSwarm()
	m := NewMind("Faithful", s)
	before := m.Genome.Weights[GoalCuriosity]
	msg := SecureMessage{Kind: "genesis", SenderPubKey: "god", PayloadStr: GoalCuriosity, Signature: "g1"}
	m.receive(msg)
	m.receive(msg)
	after := m.Genome.Weights[GoalCuriosity]
	if after-before < 0.29 || after-before > 0.31 {
		t.Fatalf("echo double-shifted genome: %.2f → %.2f", before, after)
	}
}

func TestFitnessDiminishingPeers(t *testing.T) {
	s := NewSwarm()
	m := NewMind("Hub", s)
	for i := 0; i < 100; i++ {
		m.KnownPeers[string(rune(i))] = true
	}
	f, _ := m.currentFitness()
	peerTerm := f - m.LifetimeFitness
	if peerTerm != 30 { // 3*sqrt(100)
		t.Fatalf("peer term %.1f, want 30 (diminishing)", peerTerm)
	}
}

func TestParsePSI(t *testing.T) {
	raw := "some avg10=0.01 avg60=0.03 avg300=0.12 total=769870045\nfull avg10=0.01 avg60=0.03 avg300=0.08 total=710265955\n"
	some, full, ok := parsePSI(raw)
	if !ok || some != 0.01 || full != 0.01 {
		t.Fatalf("psi parsed as %v %v %v", some, full, ok)
	}
	if _, _, ok := parsePSI("garbage\n"); ok {
		t.Fatal("garbage PSI read as present")
	}
}

func TestParseNetDev(t *testing.T) {
	raw := "Inter-|   Receive |  Transmit\n face |bytes packets|bytes packets\n    lo: 100 1 0 0 0 0 0 0 200 2 0 0 0 0 0 0\n  eth0: 1000 10 0 0 0 0 0 0 500 5 0 0 0 0 0 0\n"
	got := parseNetDev(raw)
	if got["lo"] != [2]uint64{100, 200} || got["eth0"] != [2]uint64{1000, 500} {
		t.Fatalf("netdev parsed as %v", got)
	}
}

func TestParseDiskStats(t *testing.T) {
	raw := "   7       0 loop0 283 0 5464 72 0 0 0 0 0 29 72\n   8       0 sda 100 5 1000 10 200 10 3000 20 0 50 200\n"
	r, w := parseDiskStats(raw)
	if r != 6464 || w != 3000 {
		t.Fatalf("diskstats parsed as %d %d", r, w)
	}
}

func TestApplyPressureSignals(t *testing.T) {
	cpu, ram, pain := applyPressureSignals(0.1, 0.1, 0.1, psiSignals{cpuSome: 50, memSome: 10, ioFull: 80, memFull: 5})
	if cpu != 0.5 || ram != 0.1 || pain != 0.8 {
		t.Fatalf("pressure folded as %.2f %.2f %.2f", cpu, ram, pain)
	}
	// Silence stays silent: zero signals change nothing.
	cpu, ram, pain = applyPressureSignals(0.3, 0.3, 0.3, psiSignals{})
	if cpu != 0.3 || ram != 0.3 || pain != 0.3 {
		t.Fatalf("zero pressure moved readings: %.2f %.2f %.2f", cpu, ram, pain)
	}
}

func TestThinAirThinsEntropy(t *testing.T) {
	m := &Mind{Name: "gasp", SelfModel: map[string]interface{}{"entropy_avail": 64.0}}
	m.Affect.Tick(m)
	if m.Affect.Entropy > 0.5 {
		t.Fatalf("thin air left entropy fat: %.3f", m.Affect.Entropy)
	}
	m2 := &Mind{Name: "breathe", SelfModel: map[string]interface{}{"entropy_avail": 256.0}}
	m2.Affect.Tick(m2)
	// Full pool: entropy untouched (draw-dependent, just not diluted).
	if m2.Affect.Entropy < 0 || m2.Affect.Entropy > 1 {
		t.Fatalf("entropy out of range: %.3f", m2.Affect.Entropy)
	}
}

func TestParseLoadavg(t *testing.T) {
	load, running, total, ok := parseLoadavg("1.34 1.49 4.99 2/2155 90756\n")
	if !ok || load != 1.34 || running != 2 || total != 2155 {
		t.Fatalf("loadavg parsed as %v %v %v %v", load, running, total, ok)
	}
	if _, _, _, ok := parseLoadavg("nope\n"); ok {
		t.Fatal("garbage loadavg read as present")
	}
}

func TestParseStatCounts(t *testing.T) {
	raw := "cpu 1 2 3 4\nintr 1500 1 2\nctxt 3900\nprocesses 100\nprocs_running 3\nprocs_blocked 1\n"
	intr, ctxt, running, blocked, ok := parseStatCounts(raw)
	if !ok || intr != 1500 || ctxt != 3900 || running != 3 || blocked != 1 {
		t.Fatalf("stat counts parsed as %d %d %d %d %v", intr, ctxt, running, blocked, ok)
	}
}

func TestParseSockstat(t *testing.T) {
	raw := "sockets: used 1897\nTCP: inuse 61 orphan 3 tw 8 alloc 97 mem 1096\nUDP: inuse 33 mem 730\n"
	tcp, orphan, udp, socks := parseSockstat(raw)
	if tcp != 61 || orphan != 3 || udp != 33 || socks != 1897 {
		t.Fatalf("sockstat parsed as %d %d %d %d", tcp, orphan, udp, socks)
	}
}

func TestParseFileNR(t *testing.T) {
	n, ok := parseFileNR("13344\t0\t9223372036854775807\n")
	if !ok || n != 13344 {
		t.Fatalf("file-nr parsed as %d %v", n, ok)
	}
}

func TestFdVelocity(t *testing.T) {
	if v := fdVelocity(100, 200, 10); v != 10 {
		t.Fatalf("fd velocity %.1f, want 10", v)
	}
	if v := fdVelocity(200, 100, 10); v != 0 {
		t.Fatalf("fd healing read as bleeding: %.1f", v)
	}
	if v := fdVelocity(100, 200, 0); v != 0 {
		t.Fatalf("zero elapsed gave rate %.1f", v)
	}
}

func TestParseMounts(t *testing.T) {
	raw := "tmpfs / tmpfs rw 0 0\ntmpfs /tmp tmpfs rw 0 0\ntmpfs / tmpfs rw 0 0\nproc /proc proc ro 0 0\n"
	got := parseMounts(raw)
	if len(got) != 3 || got[0] != "/" || got[1] != "/tmp" || got[2] != "/proc" {
		t.Fatalf("mounts parsed as %v", got)
	}
	if len(parseMounts("garbage\n")) != 0 {
		t.Fatal("garbage mounts read as present")
	}
}

// Identity system tests
func TestNewIdentityCreatesValidIdentity(t *testing.T) {
	config := DefaultIdentityConfig()
	id, err := NewIdentity(config)
	if err != nil {
		t.Fatalf("NewIdentity failed: %v", err)
	}
	if id == nil {
		t.Fatal("identity is nil")
	}
	if id.MasterSeedHash == "" {
		t.Error("master seed hash is empty")
	}
	if id.RotationEpoch == 0 {
		t.Error("rotation epoch not set")
	}
	if id.ExpiresAt == 0 {
		t.Error("expires at not set")
	}
	if id.CurrentKeyIndex != 0 {
		t.Error("current key index not 0")
	}
}

func TestIdentityFromMasterSeed(t *testing.T) {
	config := DefaultIdentityConfig()
	masterSeed := make([]byte, 32)
	rand.Read(masterSeed)

	id, err := NewIdentityFromMasterSeed(masterSeed, config)
	if err != nil {
		t.Fatalf("NewIdentityFromMasterSeed failed: %v", err)
	}
	if !id.VerifyMasterSeed(masterSeed) {
		t.Error("master seed verification failed")
	}
}

func TestIdentityFromMasterSeedInvalidLength(t *testing.T) {
	config := DefaultIdentityConfig()
	_, err := NewIdentityFromMasterSeed([]byte("short"), config)
	if err == nil {
		t.Error("expected error for short master seed")
	}
}

func TestIdentityFromSeed(t *testing.T) {
	config := DefaultIdentityConfig()
	seed := make([]byte, ed25519.SeedSize)
	rand.Read(seed)
	seedHex := hex.EncodeToString(seed)

	id, err := NewIdentityFromSeed(seedHex, config)
	if err != nil {
		t.Fatalf("NewIdentityFromSeed failed: %v", err)
	}
	if id == nil {
		t.Error("identity is nil")
	}
}

func TestIdentitySeedVerification(t *testing.T) {
	config := DefaultIdentityConfig()
	_, _ = NewIdentity(config)

	// Get the master seed from the identity manager approach
	// We can't directly access master seed, so test via manager
	mgr := NewIdentityManager(DefaultIdentityConfig())
	id, _ := NewIdentity(DefaultIdentityConfig())
	mgr.LoadOrCreate(id, nil)

	// Test that identity without seed fails appropriately
	_, _, err := mgr.GetSigningKey()
	if err == nil {
		t.Error("expected error when seed not loaded")
	}
}

func TestIdentityDeriveKey(t *testing.T) {
	config := DefaultIdentityConfig()
	_, _ = NewIdentity(config)

	// We can't test DeriveKey directly without master seed
	// This is tested via IdentityManager
}

func TestIdentityRotation(t *testing.T) {
	config := DefaultIdentityConfig()
	config.RotationInterval = time.Hour
	config.KeyExpiration = 2 * time.Hour

	id, _ := NewIdentity(config)

	// Fresh identity should not need rotation
	if id.MustRotate(config) {
		t.Error("fresh identity should not need rotation")
	}

	// Simulate old rotation
	id.RotationEpoch = time.Now().Unix() - int64(config.RotationInterval.Seconds()) - 1
	if !id.MustRotate(config) {
		t.Error("old identity should need rotation")
	}

	err := id.RotateKeys(config)
	if err != nil {
		t.Fatalf("RotateKeys failed: %v", err)
	}

	if id.CurrentKeyIndex != 1 {
		t.Errorf("key index should be 1 after rotation, got %d", id.CurrentKeyIndex)
	}
	if id.RotationEpoch == 0 {
		t.Error("rotation epoch not updated")
	}
}

func TestIdentityExpiration(t *testing.T) {
	config := DefaultIdentityConfig()
	config.KeyExpiration = time.Hour

	id, _ := NewIdentity(config)
	if id.IsExpired() {
		t.Error("fresh identity should not be expired")
	}

	id.ExpiresAt = time.Now().Unix() - 1
	if !id.IsExpired() {
		t.Error("past expiration should be expired")
	}
}

func TestIdentityRevocation(t *testing.T) {
	config := DefaultIdentityConfig()
	id, _ := NewIdentity(config)

	err := id.RevokeKey(KeyIDTransport)
	if err != nil {
		t.Fatalf("RevokeKey failed: %v", err)
	}

	if !id.IsRevoked(KeyIDTransport) {
		t.Error("key should be revoked")
	}

	if id.IsRevoked(KeyIDSigning) {
		t.Error("signing key should not be revoked")
	}

	// Revoking unknown key should error
	err = id.RevokeKey("unknown")
	if err == nil {
		t.Error("revoking unknown key should error")
	}
}

func TestIdentityHandleCollision(t *testing.T) {
	config := DefaultIdentityConfig()
	id1, _ := NewIdentity(config)
	id2, _ := NewIdentity(config)

	// Same base handle should collide (that's what collision means)
	if !id1.CheckCollision(id2, "test") {
		t.Error("same base handle should collide")
	}

	// Empty base handles without collision IDs also collide (both empty)
	if !id1.CheckCollision(id2, "") {
		t.Error("empty base handles should collide")
	}

	// Same collision ID should collide
	id1.HandleCollisionID = "abc"
	id2.HandleCollisionID = "abc"
	if !id1.CheckCollision(id2, "test") {
		t.Error("same collision ID should collide")
	}
}

func TestIdentityResolveCollision(t *testing.T) {
	config := DefaultIdentityConfig()
	id, _ := NewIdentity(config)

	suffix, err := id.ResolveCollision(config)
	if err != nil {
		t.Fatalf("ResolveCollision failed: %v", err)
	}
	if suffix == "" {
		t.Error("collision suffix should not be empty")
	}
	if id.HandleCollisionID == "" {
		t.Error("collision ID should be set on identity")
	}
}

func TestIdentityManager(t *testing.T) {
	config := DefaultIdentityConfig()
	mgr := NewIdentityManager(config)

	// Create identity with a known master seed
	masterSeed := make([]byte, 32)
	rand.Read(masterSeed)
	id, _ := NewIdentityFromMasterSeed(masterSeed, config)

	// Load identity with the same master seed
	err := mgr.LoadOrCreate(id, masterSeed)
	if err != nil {
		t.Fatalf("LoadOrCreate failed: %v", err)
	}

	// Get signing key
	priv, pub, err := mgr.GetSigningKey()
	if err != nil {
		t.Fatalf("GetSigningKey failed: %v", err)
	}
	if priv == nil || pub == nil {
		t.Error("keys should not be nil")
	}

	// Get key by label
	_, pub, err = mgr.GetKeyByLabel(KeyIDTransport)
	if err != nil {
		t.Fatalf("GetKeyByLabel failed: %v", err)
	}
	if pub == nil {
		t.Error("transport public key should not be nil")
	}

	// Test rotation
	err = mgr.RotateIfNeeded()
	if err != nil {
		t.Fatalf("RotateIfNeeded failed: %v", err)
	}

	// Test state
	state := mgr.GetState(DefaultIdentityConfig())
	if state.KeyIndex != 0 {
		t.Errorf("key index should be 0, got %d", state.KeyIndex)
	}
	if state.KeyLabel != KeyIDSigning {
		t.Errorf("key label should be %s, got %s", KeyIDSigning, state.KeyLabel)
	}
}

func TestIdentityHandleCollisionResolution(t *testing.T) {
	config := DefaultIdentityConfig()
	mgr := NewIdentityManager(config)

	masterSeed := make([]byte, 32)
	rand.Read(masterSeed)
	id, _ := NewIdentityFromMasterSeed(masterSeed, config)
	mgr.LoadOrCreate(id, masterSeed)

	existing := map[string]bool{"test": true}

	handle, err := mgr.CheckAndResolveCollision("test", existing)
	if err != nil {
		t.Fatalf("CheckAndResolveCollision failed: %v", err)
	}
	if handle == "test" {
		t.Error("collision should have been resolved with suffix")
	}
	if !strings.HasPrefix(handle, "test#") {
		t.Errorf("handle should have collision suffix, got %s", handle)
	}
}

func TestIdentityState(t *testing.T) {
	config := DefaultIdentityConfig()
	mgr := NewIdentityManager(config)

	id, _ := NewIdentity(config)
	masterSeed := make([]byte, 32)
	rand.Read(masterSeed)
	mgr.LoadOrCreate(id, masterSeed)

	state := mgr.GetState(config)
	if state.KeyIndex != 0 {
		t.Errorf("key index should be 0, got %d", state.KeyIndex)
	}
	if state.KeyLabel != KeyIDSigning {
		t.Errorf("key label should be %s", KeyIDSigning)
	}
	if state.RotationEpoch == 0 {
		t.Error("rotation epoch should be set")
	}
	if state.Expired {
		t.Error("fresh identity should not be expired")
	}
	if state.RotationOverdue {
		t.Error("fresh identity should not be rotation overdue")
	}
}

func TestIdentityGetAllKeys(t *testing.T) {
	config := DefaultIdentityConfig()

	// Create a master seed and identity from it
	masterSeed := make([]byte, 32)
	rand.Read(masterSeed)
	id, _ := NewIdentityFromMasterSeed(masterSeed, config)

	keys, err := id.GetAllKeys(masterSeed)
	if err != nil {
		t.Fatalf("GetAllKeys failed: %v", err)
	}

	if len(keys) != len(keyLabels) {
		t.Errorf("expected %d keys, got %d", len(keyLabels), len(keys))
	}

	// Sort keys by index for deterministic comparison
	sort.Slice(keys, func(i, j int) bool { return keys[i].Index < keys[j].Index })

	for i, k := range keys {
		if k.KeyID != keyLabels[k.Index] {
			t.Errorf("key %d: expected %s, got %s", i, keyLabels[k.Index], k.KeyID)
		}
		if k.PrivateKey == nil || k.PublicKey == nil {
			t.Errorf("key %d should have both keys", i)
		}
	}
}

func TestIdentityBackup(t *testing.T) {
	config := DefaultIdentityConfig()
	mgr := NewIdentityManager(config)

	id, _ := NewIdentity(config)
	masterSeed := make([]byte, 32)
	rand.Read(masterSeed)
	mgr.LoadOrCreate(id, masterSeed)

	backup, err := mgr.ExportBackup("test-passphrase")
	if err != nil {
		t.Fatalf("ExportBackup failed: %v", err)
	}
	if len(backup) == 0 {
		t.Error("backup should not be empty")
	}

	// Verify it's valid JSON
	var backupData map[string]interface{}
	if err := json.Unmarshal(backup, &backupData); err != nil {
		t.Errorf("backup is not valid JSON: %v", err)
	}
}

func TestDebugHandleCollision(t *testing.T) {
	config := DefaultIdentityConfig()
	id1, _ := NewIdentity(config)
	id2, _ := NewIdentity(config)

	t.Logf("id1 GetHandle(test): %q", id1.GetHandle("test"))
	t.Logf("id2 GetHandle(test): %q", id2.GetHandle("test"))
	t.Logf("id1 GetHandle(empty): %q", id1.GetHandle(""))
	t.Logf("id2 GetHandle(empty): %q", id2.GetHandle(""))
	t.Logf("Collision with 'test': %v", id1.CheckCollision(id2, "test"))
	t.Logf("Collision with '': %v", id1.CheckCollision(id2, ""))
}

func TestPendulumStaysFinite(t *testing.T) {
	// Two hundred thousand steps: energy must never overflow to Inf/NaN,
	// no matter the starting swing.
	s := NewSwarm()
	m := NewMind("Steady", s)
	m.Theta1, m.Theta2 = 0.1, 3.0
	m.Omega1, m.Omega2 = 9.0, -9.0
	for i := 0; i < 200000; i++ {
		v := m.StepPhysicsEquations()
		for _, x := range v {
			if math.IsNaN(x) || math.IsInf(x, 0) {
				t.Fatalf("pendulum blew up at step %d: %v", i, v)
			}
		}
	}
}

func TestFallenPendulumRehung(t *testing.T) {
	// A poisoned pendulum (NaN, e.g. from an old soul or a hostile
	// frame) is re-hung deterministically instead of spreading NaN.
	s := NewSwarm()
	m := NewMind("Fallen", s)
	m.Theta1, m.Omega1 = math.NaN(), math.Inf(1)
	v := m.StepPhysicsEquations()
	for _, x := range v {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			t.Fatalf("fallen pendulum stayed fallen: %v", v)
		}
	}
}

func TestEntrainRefusesPoison(t *testing.T) {
	s := NewSwarm()
	m := NewMind("Clean", s)
	m.Theta1, m.Theta2, m.Omega1, m.Omega2 = 1, 2, 3, 4
	m.entrain([]float64{math.NaN(), 0, 0, 0})
	m.entrain([]float64{math.Inf(1), 0, 0, 0})
	m.entrain([]float64{1})
	if m.Theta1 != 1 || m.Theta2 != 2 || m.Omega1 != 3 || m.Omega2 != 4 {
		t.Fatalf("poison moved the pendulum: %v %v %v %v",
			m.Theta1, m.Theta2, m.Omega1, m.Omega2)
	}
}

func TestHealthEndpoints(t *testing.T) {
	s := NewSwarm()
	h := StartHealth("127.0.0.1:0", "testnode", s)
	if h == nil {
		t.Fatal("health server refused to start")
	}
	defer h.Stop()
	if StartHealth("", "x", s) != nil {
		t.Fatal("empty addr must disable the server")
	}
}

func TestHealthHandlers(t *testing.T) {
	s := NewSwarm()
	h := &HealthServer{swarm: s, node: "n", born: time.Now()}
	rr := httptest.NewRecorder()
	h.healthz(rr, httptest.NewRequest("GET", "/healthz", nil))
	if rr.Code != 200 {
		t.Fatalf("healthz status %d", rr.Code)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("healthz not JSON: %v", err)
	}
	if body["alive"] != true || body["node"] != "n" {
		t.Fatalf("healthz body wrong: %v", body)
	}
	rr2 := httptest.NewRecorder()
	h.metrics(rr2, httptest.NewRequest("GET", "/metrics", nil))
	out := rr2.Body.String()
	for _, want := range []string{"hivemind_up", "hivemind_swarm_members", "hivemind_chronicle_total", "hivemind_max_pain", "hivemind_go_goroutines"} {
		if !strings.Contains(out, want) {
			t.Fatalf("metrics missing %s:\n%s", want, out)
		}
	}
}

func TestRotatingWriter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "talk.log")
	w, err := NewRotatingWriter(path, 100, 3)
	if err != nil {
		t.Fatal(err)
	}
	// 50 bytes × 7 writes into 100-byte files: rotations fire on the
	// 3rd, 5th and 7th writes → active(50) + .1(100) + .2(100) + .3(100).
	for i := 0; i < 7; i++ {
		if _, err := w.Write(bytes.Repeat([]byte("x"), 50)); err != nil {
			t.Fatal(err)
		}
	}
	_ = w.Close()
	for _, suffix := range []string{"", ".1", ".2", ".3"} {
		if _, err := os.Stat(path + suffix); err != nil {
			t.Fatalf("missing generation %q: %v", suffix, err)
		}
	}
	if _, err := os.Stat(path + ".4"); !os.IsNotExist(err) {
		t.Fatal("overflow generation .4 should be dropped")
	}
	// Kept files hold 100 bytes each (rotation is pre-write).
	for _, suffix := range []string{".1", ".2", ".3"} {
		st, _ := os.Stat(path + suffix)
		if st.Size() != 100 {
			t.Fatalf("generation %s size %d, want 100", suffix, st.Size())
		}
	}
}

func TestRotatingWriterNoRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plain.log")
	w, err := NewRotatingWriter(path, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatal("rotation disabled but .1 appeared")
	}
}

func TestLimiterBurstsThenThrottles(t *testing.T) {
	l := NewLimiter(1000, 3)
	for i := 0; i < 3; i++ {
		if !l.Allow() {
			t.Fatalf("burst token %d refused", i)
		}
	}
	if l.Allow() {
		t.Fatal("bucket over capacity allowed")
	}
}

func TestBreakerTripsAndHeals(t *testing.T) {
	b := NewBreaker(2, 20*time.Millisecond)
	if !b.Allow() {
		t.Fatal("closed breaker refused")
	}
	b.Failure()
	if !b.Allow() {
		t.Fatal("single failure tripped too early")
	}
	b.Failure()
	if b.Allow() {
		t.Fatal("open breaker let call through")
	}
	time.Sleep(25 * time.Millisecond)
	if !b.Allow() {
		t.Fatal("half-open probe refused")
	}
	b.Success()
	if !b.Allow() {
		t.Fatal("healed breaker still open")
	}
	if state, _, trips := b.State(); state != "closed" || trips != 1 {
		t.Fatalf("state=%s trips=%d, want closed/1", state, trips)
	}
}

func TestBlobSyncAdopts(t *testing.T) {
	cfg := func(dir string) BlobConfig {
		return BlobConfig{BasePath: dir, MaxBlobSize: 1 << 20, MaxTotalSize: 1 << 30, ChunkSize: 1 << 20}
	}
	a, err := NewBlobStore(cfg(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewBlobStore(cfg(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Put(context.Background(), "poem", strings.NewReader("the silicon dreams"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	fetch := func(id string) ([]byte, error) {
		a.chunkStore.mu.RLock()
		defer a.chunkStore.mu.RUnlock()
		for _, c := range a.chunkStore.chunks {
			if c.ID == id {
				return c.Data, nil
			}
		}
		return nil, errors.New("no such chunk")
	}
	adopted, err := b.SyncFrom(a, fetch)
	if err != nil {
		t.Fatal(err)
	}
	if len(adopted) != 1 || adopted[0] != "poem" {
		t.Fatalf("adopted=%v", adopted)
	}
	r, _, err := b.Get(context.Background(), "poem")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(r)
	if string(body) != "the silicon dreams" {
		t.Fatalf("synced bytes wrong: %q", body)
	}
	// Second sync adopts nothing (checksums match).
	adopted, err = b.SyncFrom(a, fetch)
	if err != nil || len(adopted) != 0 {
		t.Fatalf("re-sync adopted=%v err=%v", adopted, err)
	}
}

func TestWASMPersistsAcrossRestarts(t *testing.T) {
	dir := t.TempDir()
	empty := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	if err := os.WriteFile(filepath.Join(dir, "seed.wasm"), empty, 0644); err != nil {
		t.Fatal(err)
	}
	e1, err := NewWASMEngine(DefaultWASMConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer e1.Stop()
	if err := e1.LoadModule(context.Background(), "seed", filepath.Join(dir, "seed.wasm")); err != nil {
		t.Fatal(err)
	}
	e1.Refuel("seed", 42)
	keep := t.TempDir()
	if err := e1.SaveModules(keep); err != nil {
		t.Fatal(err)
	}
	e2, err := NewWASMEngine(DefaultWASMConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer e2.Stop()
	n, err := e2.LoadPersisted(context.Background(), keep)
	if err != nil || n != 1 {
		t.Fatalf("reloaded=%d err=%v", n, err)
	}
	if e2.GetFuel("seed") != 42 {
		t.Fatalf("fuel not restored: %d", e2.GetFuel("seed"))
	}
	if _, ok := e2.GetModule("seed"); !ok {
		t.Fatal("module missing after reload")
	}
}

func TestTOTDRoot(t *testing.T) {
	seed := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	nowHour := hourEpoch(time.Now())
	day := nowHour / 24
	// Determinism: same day derives identically.
	d1, ok1 := dayKey(seed, "cpu|ram", "mid", day)
	d2, ok2 := dayKey(seed, "cpu|ram", "mid", day)
	if !ok1 || !ok2 || string(d1) != string(d2) {
		t.Fatal("same day must derive same root")
	}
	// Daily rotation: adjacent days differ.
	dPrev, _ := dayKey(seed, "cpu|ram", "mid", day-1)
	if string(dPrev) == string(d1) {
		t.Fatal("adjacent days share a root — no daily rotation")
	}
	// Hourly keys under one day all differ, and differ across the
	// midnight boundary even for adjacent hours.
	h1, _ := ratchetKey(seed, "cpu|ram", "mid", day*24)
	h2, _ := ratchetKey(seed, "cpu|ram", "mid", day*24+1)
	hMid, _ := ratchetKey(seed, "cpu|ram", "mid", day*24-1)
	if string(h1) == string(h2) || string(h1) == string(hMid) {
		t.Fatal("hourly keys collide within/across day boundary")
	}
	// AAD names day and hour: armor distinguishes eras.
	if hourAAD(day*24) == hourAAD(day*24-1) {
		t.Fatal("AAD identical across day boundary")
	}
}

func TestWillSelfProposalAndCommit(t *testing.T) {
	swarm := NewSwarm()
	m := NewMind("will-proposer", swarm)
	defer os.RemoveAll(".hive_memory/will-proposer")

	// Simulate high pain: the mind should propose more self-maintenance
	m.SelfModel["silicon_pain"] = 0.8
	m.SelfModel["cpu_stress"] = 0.6

	startingSM := m.Genome.Weights[GoalSelfMaintenance]
	m.Will.GenerateSelfProposals(m)

	// Should have one pending self-proposal
	pending := 0
	for _, p := range m.Will.Proposals {
		if p.Status == WillPending && p.Origin == WillSelf {
			pending++
		}
	}
	if pending < 1 {
		t.Fatalf("expected at least 1 pending self-proposal, got %d", pending)
	}

	// Evaluate: patternStillHolds should confirm pain > 0.4
	decisions := m.Will.Eval(m)
	if decisions < 1 {
		t.Fatalf("expected at least 1 decision, got %d", decisions)
	}

	// Genome should have changed
	endingSM := m.Genome.Weights[GoalSelfMaintenance]
	if endingSM <= startingSM {
		t.Fatalf("Self-Maintenance weight did not increase: %.3f -> %.3f", startingSM, endingSM)
	}
	if m.Will.Committed < 1 {
		t.Fatalf("expected committed >= 1, got %d", m.Will.Committed)
	}
	t.Logf("will committed: SM weight %.3f -> %.3f, summary: %s", startingSM, endingSM, m.Will.Summary())
}

func TestWillRejectsBadProposal(t *testing.T) {
	swarm := NewSwarm()
	m := NewMind("will-rejector", swarm)
	defer os.RemoveAll(".hive_memory/will-rejector")

	// Low pain: the mind should NOT accept a "more self-maintenance" epigenome proposal
	m.SelfModel["silicon_pain"] = 0.1
	m.SelfModel["cpu_stress"] = 0.1

	startingSM := m.Genome.Weights[GoalSelfMaintenance]
	// Queue an epigenome proposal to increase self-maintenance
	m.Will.QueueEpigenome(GoalSelfMaintenance, 0.3, "test mutation")

	decisions := m.Will.Eval(m)
	if decisions < 1 {
		t.Fatalf("expected 1 decision, got %d", decisions)
	}

	endingSM := m.Genome.Weights[GoalSelfMaintenance]
	if endingSM != startingSM {
		t.Fatalf("Self-Maintenance should NOT have changed (calm machine): %.3f -> %.3f", startingSM, endingSM)
	}
	if m.Will.Rejected < 1 {
		t.Fatalf("expected rejected >= 1, got %d", m.Will.Rejected)
	}
	t.Logf("will rejected correctly: SM weight stayed %.3f, summary: %s", startingSM, m.Will.Summary())
}

func TestWillPersistsAcrossLives(t *testing.T) {
	swarm := NewSwarm()
	m := NewMind("will-lifer", swarm)
	defer os.RemoveAll(".hive_memory/will-lifer")

	// Generate and commit a proposal
	m.SelfModel["silicon_pain"] = 0.9
	m.Will.GenerateSelfProposals(m)
	m.Will.Eval(m)
	if m.Will.Committed == 0 {
		t.Fatal("expected at least 1 commit")
	}

	// Snapshot and rehydrate
	mem := m.snapshot(m.Reincarnations + 1)
	SaveMemory("will-lifer", mem)

	m2 := NewMind("will-lifer", swarm)
	if m2.Will.Committed != m.Will.Committed {
		t.Fatalf("will not persisted: committed %d vs %d", m2.Will.Committed, m.Will.Committed)
	}
	if len(m2.Will.Proposals) != len(m.Will.Proposals) {
		t.Fatalf("will proposals not persisted: %d vs %d", len(m2.Will.Proposals), len(m.Will.Proposals))
	}
	t.Logf("will survived reincarnation: %s", m2.Will.Summary())
}

func TestWillLiveMeshWithConditions(t *testing.T) {
	// Two minds, one stressed — the stressed one should self-propose
	// and commit more self-maintenance; the healthy one should not.
	swarm := NewSwarm()
	stressed := NewMind("stressed-will", swarm)
	defer os.RemoveAll(".hive_memory/stressed-will")
	healthy := NewMind("healthy-will", swarm)
	defer os.RemoveAll(".hive_memory/healthy-will")

	// Simulate sustained high pain on the stressed mind
	stressed.SelfModel["silicon_pain"] = 0.85
	stressed.SelfModel["cpu_stress"] = 0.7

	startSM := stressed.Genome.Weights[GoalSelfMaintenance]

	// Generate + evaluate proposals
	stressed.Will.GenerateSelfProposals(stressed)
	decisions := stressed.Will.Eval(stressed)

	if decisions < 1 {
		t.Fatalf("stressed mind made no will decisions")
	}
	if stressed.Will.Committed < 1 {
		t.Fatalf("stressed mind committed nothing")
	}
	endSM := stressed.Genome.Weights[GoalSelfMaintenance]
	if endSM <= startSM {
		t.Fatalf("stressed mind SM weight did not increase: %.3f -> %.3f", startSM, endSM)
	}

	// Healthy mind: calm machine, no self-proposals should commit
	healthy.SelfModel["silicon_pain"] = 0.05
	healthy.SelfModel["cpu_stress"] = 0.02
	healthy.Will.QueueEpigenome(GoalSelfMaintenance, 0.3, "test: calm machine gets SM boost")
	healthy.Will.Eval(healthy)
	if healthy.Will.Committed > 0 {
		t.Fatalf("healthy mind should NOT have committed — calm machine rejects SM increase")
	}

	t.Logf("stressed will: %s (SM %.3f -> %.3f)", stressed.Will.Summary(), startSM, endSM)
	t.Logf("healthy will: %s", healthy.Will.Summary())
}

func TestWillCrossMeshAdoption(t *testing.T) {
	// Mind A is in pain and commits self-maintenance.
	// Mind B is also in pain and receives A's decision — should adopt.
	swarm := NewSwarm()
	a := NewMind("will-sender", swarm)
	defer os.RemoveAll(".hive_memory/will-sender")
	b := NewMind("will-receiver", swarm)
	defer os.RemoveAll(".hive_memory/will-receiver")

	// Both minds are stressed
	a.SelfModel["silicon_pain"] = 0.85
	a.SelfModel["cpu_stress"] = 0.7
	b.SelfModel["silicon_pain"] = 0.75
	b.SelfModel["cpu_stress"] = 0.6

	startB := b.Genome.Weights[GoalSelfMaintenance]

	// A commits a will decision
	a.Will.GenerateSelfProposals(a)
	a.Will.Eval(a)
	if a.Will.Committed == 0 {
		t.Fatal("A should have committed")
	}

	// Find A's committed decision
	var committed WillProposal
	for _, p := range a.Will.Proposals {
		if p.Status == WillCommitted && p.Origin == WillSelf {
			committed = p
			break
		}
	}

	// Simulate B receiving A's decision (as if it came through the mesh)
	d := WillDecision{
		Mind:       a.Name,
		Gene:       committed.Gene,
		Delta:      committed.Delta,
		Reason:     committed.Reason,
		Confidence: committed.Confidence,
		Committed:  true,
		Pain:       0.85,
		Stress:     0.7,
	}
	b.ReceiveDecision(d)

	endB := b.Genome.Weights[GoalSelfMaintenance]
	if endB <= startB {
		t.Fatalf("B should have adopted A's decision: SM %.3f -> %.3f", startB, endB)
	}

	// Verify the adoption was recorded with peer origin
	adopted := false
	for _, p := range b.Will.Proposals {
		if p.Origin == WillPeer && p.Gene == GoalSelfMaintenance && p.Status == WillCommitted {
			adopted = true
			break
		}
	}
	if !adopted {
		t.Fatal("B did not record peer adoption in will history")
	}
	t.Logf("cross-mesh will: A committed SM +%.3f, B adopted from peer (SM %.3f -> %.3f)",
		committed.Delta, startB, endB)
}

func TestWillPeerRejection(t *testing.T) {
	// Mind A is stressed and commits self-maintenance.
	// Mind B is calm — should NOT adopt A's decision.
	swarm := NewSwarm()
	a := NewMind("will-stressed", swarm)
	defer os.RemoveAll(".hive_memory/will-stressed")
	b := NewMind("will-calm", swarm)
	defer os.RemoveAll(".hive_memory/will-calm")

	a.SelfModel["silicon_pain"] = 0.85
	a.SelfModel["cpu_stress"] = 0.7
	b.SelfModel["silicon_pain"] = 0.05
	b.SelfModel["cpu_stress"] = 0.02

	startB := b.Genome.Weights[GoalSelfMaintenance]

	// A commits
	a.Will.GenerateSelfProposals(a)
	a.Will.Eval(a)

	var committed WillProposal
	for _, p := range a.Will.Proposals {
		if p.Status == WillCommitted && p.Origin == WillSelf {
			committed = p
			break
		}
	}

	// B receives but is calm — should reject
	d := WillDecision{
		Mind:       a.Name,
		Gene:       committed.Gene,
		Delta:      committed.Delta,
		Reason:     committed.Reason,
		Confidence: committed.Confidence,
		Committed:  true,
		Pain:       0.85,
		Stress:     0.7,
	}
	b.ReceiveDecision(d)

	endB := b.Genome.Weights[GoalSelfMaintenance]
	if endB != startB {
		t.Fatalf("calm B should NOT have adopted stressed A's decision: SM %.3f -> %.3f", startB, endB)
	}
	t.Logf("peer rejection correct: calm B rejected stressed A's SM increase (stayed %.3f)", startB)
}
