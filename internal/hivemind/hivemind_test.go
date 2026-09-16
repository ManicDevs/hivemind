package hivemind

import (
	"bufio"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
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
	for !pm.history["friend"] && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !pm.history["friend"] {
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

	// Baked key wins over static.
	baked := make([]byte, 32)
	for i := range baked {
		baked[i] = byte(i + 1)
	}
	compileRelayKey = hex.EncodeToString(baked)
	if got := string(cipherKey()); got != string(baked) {
		t.Fatal("baked compile-time key not selected")
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
		pmB.mu.Lock()
		e, ok := pmB.supers["aa"]
		pmB.mu.Unlock()
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
