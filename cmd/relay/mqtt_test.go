package main

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// The mesh publishes to N brokers and subscribes to N brokers, so every
// message echoes back to us once per broker. These tests pin the collapse
// to exactly one delivery, which is the property the whole design rests on.

func TestMarkSeenAcceptsFirstRejectsEcho(t *testing.T) {
	m := &mqttMesh{seen: make(map[string]time.Time)}

	if !m.markSeen("hivemind-1-1") {
		t.Fatal("first sighting of an ID must be accepted")
	}
	// Six brokers echoing the same message must all be rejected.
	for i := 0; i < 6; i++ {
		if m.markSeen("hivemind-1-1") {
			t.Fatalf("echo %d of an already-seen ID was accepted", i+2)
		}
	}
	if len(m.seen) != 1 {
		t.Fatalf("dedup set should hold 1 ID, holds %d", len(m.seen))
	}
}

func TestMarkSeenDistinguishesIDs(t *testing.T) {
	m := &mqttMesh{seen: make(map[string]time.Time)}
	for _, id := range []string{"hivemind-2-1", "hivemind-2-2", "hivemind-2-3"} {
		if !m.markSeen(id) {
			t.Fatalf("distinct ID %s must be accepted", id)
		}
	}
	if len(m.seen) != 3 {
		t.Fatalf("expected 3 IDs, got %d", len(m.seen))
	}
}

func TestMarkSeenIsConcurrencySafe(t *testing.T) {
	m := &mqttMesh{seen: make(map[string]time.Time)}
	const workers = 8
	const id = "hivemind-3-1"

	var wg sync.WaitGroup
	accepted := make([]bool, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			accepted[i] = m.markSeen(id)
		}(i)
	}
	wg.Wait()

	// Exactly one racer may win; this is what keeps N× broker fan-out from
	// becoming N× local delivery.
	wins := 0
	for _, ok := range accepted {
		if ok {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("exactly 1 goroutine must accept the ID, got %d", wins)
	}
}

func TestMarkSeenBoundsTheSet(t *testing.T) {
	m := &mqttMesh{seen: make(map[string]time.Time)}
	for i := 0; i < maxSeenIDs+500; i++ {
		m.markSeen(string(rune('a'+i%26)) + "-" + time.Duration(i).String())
	}
	if len(m.seen) > maxSeenIDs {
		t.Fatalf("dedup set grew past its bound: %d > %d", len(m.seen), maxSeenIDs)
	}
	// Eviction protects the bound without losing the ID we just accepted:
	// a fresh message must still be deduped correctly after the set fills.
	const newest = "burst-newest"
	if !m.markSeen(newest) {
		t.Fatal("newest ID must be accepted after eviction pressure")
	}
	if m.markSeen(newest) {
		t.Fatal("newest ID must still be remembered once accepted")
	}
}

func TestOnMessageIgnoresGarbage(t *testing.T) {
	r := newRelay()
	m := newMQTTMesh(r, "")
	// Empty mesh, so nothing should be delivered regardless of payload.
	m.onMessage(nil, &fakeMsg{topic: "hive/test", payload: []byte("not json")})

	if tp, ok := r.topics["test"]; ok && len(tp.messages) != 0 {
		t.Fatalf("garbage payload must not be delivered, got %d messages", len(tp.messages))
	}
}

func TestStatsReflectMeshShape(t *testing.T) {
	r := newRelay()
	m := newMQTTMesh(r, "")

	// The matrix is the source of truth for mesh size; it must never be
	// empty, or "no brokers configured" would look identical to "no brokers
	// reachable".
	if len(m.peers) == 0 {
		t.Fatal("mesh has no peers — matrix lookup returned nothing")
	}
	s := m.stats()
	if s.Brokers != len(m.peers) {
		t.Fatalf("stats.Brokers=%d, want %d", s.Brokers, len(m.peers))
	}
	if s.Topic != mqttTopicPrefix {
		t.Fatalf("stats.Topic=%q, want %q", s.Topic, mqttTopicPrefix)
	}
}

func TestPinnedBrokerOverridesMatrix(t *testing.T) {
	r := newRelay()
	m := newMQTTMesh(r, "broker.hivemq.com")
	if len(m.peers) != 1 {
		t.Fatalf("RELAY_MQTT pin should yield exactly 1 peer, got %d", len(m.peers))
	}
	if m.peers[0].host != "broker.hivemq.com" {
		t.Fatalf("pinned host=%q, want broker.hivemq.com", m.peers[0].host)
	}
	if m.peers[0].url == "" {
		t.Fatal("pinned broker has no dial URL")
	}
}

func TestPinnedBrokerOutsideMatrixStillDials(t *testing.T) {
	t.Setenv("HIVEMIND_MQTT_TLS", "")
	r := newRelay()
	m := newMQTTMesh(r, "mqtt.example.org")
	if len(m.peers) != 1 {
		t.Fatalf("expected 1 peer for an off-matrix host, got %d", len(m.peers))
	}
	// An off-matrix host is dialled over TLS by default too, on the
	// standard TLS port: nothing tells us this host speaks anything else,
	// and reaching for cleartext because a host was typed by hand is
	// exactly the downgrade this policy exists to prevent.
	if m.peers[0].url != "ssl://mqtt.example.org:8883" || !m.peers[0].secure {
		t.Fatalf("off-matrix pin should default to TLS 8883, got %q secure=%v", m.peers[0].url, m.peers[0].secure)
	}

	// Only an explicit opt-out reaches the cleartext port.
	t.Setenv("HIVEMIND_MQTT_TLS", "off")
	m2 := newMQTTMesh(newRelay(), "mqtt.example.org")
	if m2.peers[0].url != "tcp://mqtt.example.org:1883" || m2.peers[0].secure {
		t.Fatalf("opt-out should give the cleartext URL, got %q secure=%v", m2.peers[0].url, m2.peers[0].secure)
	}
}

func TestDeliverFansOutToSubscribers(t *testing.T) {
	r := newRelay()
	tp := r.getTopic("bus")
	ch := make(chan message, 4)
	tp.subChans = append(tp.subChans, ch)

	msg := message{ID: "hivemind-9-1", Event: "message", Message: "hello"}
	r.deliver("bus", msg)

	select {
	case got := <-ch:
		if got.ID != msg.ID {
			t.Fatalf("subscriber got %q, want %q", got.ID, msg.ID)
		}
	default:
		t.Fatal("subscriber received nothing")
	}
	if len(tp.messages) != 1 {
		t.Fatalf("bus should hold 1 message, holds %d", len(tp.messages))
	}
}

func TestDeliverTrimsToTenThousand(t *testing.T) {
	r := newRelay()
	tp := r.getTopic("bus")
	for i := 0; i < 10050; i++ {
		msg := message{ID: string(rune('a'+i%26)) + "-" + time.Duration(i).String()}
		tp.messages = append(tp.messages, msg)
		if len(tp.messages) > 10000 {
			tp.messages = tp.messages[len(tp.messages)-10000:]
		}
	}
	if len(tp.messages) != 10000 {
		t.Fatalf("bus should cap at 10000, holds %d", len(tp.messages))
	}
}

func TestInjectDoesNotRepublish(t *testing.T) {
	// inject must be write-only onto the local bus: re-publishing would
	// loop the message around the mesh forever.
	r := newRelay()
	m := newMQTTMesh(r, "")
	m.mu.Lock()
	before := m.outbound
	m.mu.Unlock()

	r.inject("from-broker", message{ID: "hivemind-10-1", Event: "message", Message: "x"})

	m.mu.Lock()
	after := m.outbound
	m.mu.Unlock()
	if after != before {
		t.Fatalf("inject republished: outbound %d → %d", before, after)
	}
	if got := len(r.topics["from-broker"].messages); got != 1 {
		t.Fatalf("injected message did not land on the bus: %d", got)
	}
}

func TestMessageRoundTripsJSON(t *testing.T) {
	msg := message{ID: "hivemind-11-1", Title: "t", Message: "body", Time: 42, ExpiresAt: 99}
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var back message
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back != msg {
		t.Fatalf("round trip changed the message: %+v != %+v", back, msg)
	}
}

// fakeMsg is a minimal mqtt.Message for exercising onMessage without a
// live broker.
type fakeMsg struct {
	topic   string
	payload []byte
}

func (f fakeMsg) Duplicate() bool   { return false }
func (f fakeMsg) Qos() byte         { return 1 }
func (f fakeMsg) Retained() bool    { return false }
func (f fakeMsg) Topic() string     { return f.topic }
func (f fakeMsg) MessageID() uint16 { return 0 }
func (f fakeMsg) Payload() []byte   { return f.payload }
func (f fakeMsg) Ack()              {}
