package hivemind

import (
	"context"
	"fmt"
	"testing"
	"time"

	"gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind/language"
)

// A peer thought is witnessed once per node (not once per mind), and the
// hear/reply namespaces do not collide.
func TestNodeDialogWitness(t *testing.T) {
	if !nodeDialog.witness("hear|abc") {
		t.Fatal("first hear should be new")
	}
	if nodeDialog.witness("hear|abc") {
		t.Fatal("same hear registered twice")
	}
	if stale := nodeDialog.witness("reply|abc"); nodeDialog.witness("hear|abc") || nodeDialog.witness("reply|abc") {
		t.Fatalf("namespaces must not re-register existing keys (stale=%v)", stale)
	}
}

func TestNodeDialogEvictsOldest(t *testing.T) {
	d := &nodeDialogSet{seen: make(map[string]bool)}
	for i := 0; i < 600; i++ {
		d.witness(fmt.Sprintf("hear|frame-%03d", i))
	}
	if !d.witness("hear|frame-000") {
		t.Fatal("oldest entry should have been evicted by the cap (re-witness true)")
	}
	if d.witness("hear|frame-599") {
		t.Fatal("newer entry must still be remembered")
	}
}

// A stranger's broadcast thought registers a peer, counts as heard, and —
// with no language model installed — never spawns a reply frame onto the
// swarm. Dialogue must never fabricate traffic it cannot actually voice.
func TestPeerThoughtWithoutBridgeNeverReplies(t *testing.T) {
	s := NewSwarm()
	m := NewMind("Listen", s)
	in := s.Join("observer")
	if len(m.KnownPeers) != 0 {
		t.Fatal("brand-new mind must have no peers")
	}

	m.receive(SecureMessage{SenderPubKey: "stranger-outside-the-hive", Kind: "thought", PayloadStr: "We are burning."})

	if m.peerThoughtsHeard != 1 {
		t.Fatalf("peerThoughtsHeard = %d, want 1", m.peerThoughtsHeard)
	}
	if len(m.KnownPeers) != 1 {
		t.Fatalf("stranger not registered as peer: %v", m.KnownPeers)
	}
	select {
	case got := <-in:
		t.Fatalf("no bridge, yet a frame appeared on the swarm: %s (%s)", got.Kind, got.PayloadStr)
	default:
	}
}

// The reply throttle belongs to the mind that answers: a second thought
// arriving before the window reopens cannot mint another reply, even in a
// different listen node. This bounds gossip at one answer per minute per
// voice.
func TestReplyThrottleHolds(t *testing.T) {
	s := NewSwarm()
	cmd := &replyStub{out: "I am here."}
	m := NewMind("Chatty", s)
	m.LanguageBridge = language.NewLanguageBridge(cmd, 0)
	m.Genome.Weights[GoalSocialization] = 1.5 // social enough to answer
	m.lastRepliedAt = time.Now()
	before := m.Replies

	m.answerPeer(&SecureMessage{SenderPubKey: "stranger-one", Kind: "thought", PayloadStr: "Hello"}, "stranger")
	if m.Replies != before {
		t.Fatalf("reply fired inside throttle window: Replies %d -> %d", before, m.Replies)
	}
}

type replyStub struct {
	out string
}

func (r *replyStub) Generate(ctx context.Context, prompt string) (string, error) { return r.out, nil }
func (r *replyStub) Name() string                                                { return "replyStub" }
func (r *replyStub) Close() error                                                { return nil }
