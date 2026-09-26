package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	hm "gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind"
)

// The MQTT bus is anonymous: anyone can publish to hive/#, and every broker
// operator and observer along the way can read what crosses it. These tests
// pin the two properties that make that survivable — the wire carries only
// ciphertext, and only a holder of the bus key can produce a frame we
// accept.

func sealedMesh(t *testing.T) *mqttMesh {
	t.Helper()
	t.Setenv("HIVEMIND_RELAY_SEAL", "")
	return &mqttMesh{seen: make(map[string]time.Time), sealing: relaySealEnabled()}
}

// The bus key is normally baked in at build time (ldflags, from .relaykey),
// so a plain `go test` has none. Tests set a known fleet key instead, which
// also keeps them independent of whichever machine they run on.
func busKey(t *testing.T) {
	t.Helper()
	t.Setenv("HIVEMIND_CIPHER_KEY", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
}

// A published payload must be opaque on the wire, and must survive the round
// trip intact.
func TestSealHidesPayloadFromTheWire(t *testing.T) {
	busKey(t)
	m := sealedMesh(t)
	if !m.sealing {
		t.Fatal("sealing must default on")
	}
	msg := message{ID: "m1", Event: "probe", Title: "t", Message: "plaintext-canary-9f3a"}
	plain, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := hm.SealRelayPayload(plain)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	for _, leak := range []string{"plaintext-canary-9f3a", `"id"`, "probe", "m1"} {
		if strings.Contains(sealed, leak) {
			t.Fatalf("sealed frame leaked %q", leak)
		}
	}
	opened, _, err := hm.OpenRelayPayload(sealed)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var back message
	if err := json.Unmarshal(opened, &back); err != nil {
		t.Fatal(err)
	}
	if back != msg {
		t.Fatalf("round-trip changed the message: got %+v want %+v", back, msg)
	}
}

// The admission policy. A frame that does not authenticate is refused, and
// the inputs tried here are exactly what an anonymous publisher would send.
func TestOpenRefusesEverythingUnauthenticated(t *testing.T) {
	busKey(t)
	good, err := hm.SealRelayPayload([]byte(`{"id":"ours","message":"hello"}`))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, _, err := hm.OpenRelayPayload(good); err != nil {
		t.Fatalf("our own frame must open: %v", err)
	}

	// Another swarm: identical code, different key material. This is the
	// case that matters — it is what every other hivemind relay on the
	// public brokers looks like from where we sit.
	t.Setenv("HIVEMIND_CIPHER_KEY", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb") //nolint:gosec // deliberately a different key
	if _, _, err := hm.OpenRelayPayload(good); err == nil {
		t.Fatal("a frame sealed under another swarm's key opened — the bus is not closed")
	}
	t.Setenv("HIVEMIND_CIPHER_KEY", "")

	for _, bad := range []string{
		"",
		"not base64 at all !!!",
		"aGVsbG8=",                       // valid base64, far too short to be a frame
		`{"id":"forged","message":"hi"}`, // raw plaintext injection attempt
		strings.Repeat("A", 4096),        // oversized garbage
		good[:len(good)-4] + "AAAA",      // tampered ciphertext, valid base64
	} {
		if _, _, err := hm.OpenRelayPayload(bad); err == nil {
			t.Fatalf("unauthenticated input opened: %.40q", bad)
		}
	}
}

// openWithWindow lets the peer mesh fall back to a static demo key, and that
// constant is published in this source. On an open bus that fallback would be
// a universal write key, so the relay path must have no equivalent. Forge
// precisely what that path would have produced and prove it is refused.
func TestNoStaticKeyFallbackOnTheWire(t *testing.T) {
	// A valid bus key is configured: the static forgery must still fail,
	// not merely fail because no key happened to be present.
	busKey(t)
	forged, err := sealLikeOpenWithWindow([]byte(`{"id":"forged-by-static-key"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := hm.OpenRelayPayload(forged); err == nil {
		t.Fatal("a frame sealed under the published static key opened — anyone could inject")
	}
}

// sealLikeOpenWithWindow reproduces the static fallback openWithWindow still
// permits: AES-256-GCM under the compiled-in demo constant with an empty AAD.
func sealLikeOpenWithWindow(plaintext []byte) (string, error) {
	block, err := aes.NewCipher([]byte("HIVE_MIND_32_BYTE_STATIC_KEY_PAD"))
	if err != nil {
		return "", err
	}
	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aesgcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(aesgcm.Seal(nonce, nonce, plaintext, nil)), nil
}

// Sealing is opt-out, and the opt-out is parsed strictly.
func TestSealOptOutParsing(t *testing.T) {
	for _, off := range []string{"off", "0", "false", "no", "OFF", " off "} {
		t.Setenv("HIVEMIND_RELAY_SEAL", off)
		if relaySealEnabled() {
			t.Errorf("HIVEMIND_RELAY_SEAL=%q must disable sealing", off)
		}
	}
	for _, on := range []string{"", "on", "1", "true", "yes", "TRUE"} {
		t.Setenv("HIVEMIND_RELAY_SEAL", on)
		if !relaySealEnabled() {
			t.Errorf("HIVEMIND_RELAY_SEAL=%q must keep sealing on", on)
		}
	}
}

// Rejection is counted apart from mesh traffic, so /healthz can prove the
// door is being enforced and a flood of unauthenticated frames cannot
// masquerade as legitimate inbound or make the mesh look bidirectional.
func TestRejectedIsCountedNotDelivered(t *testing.T) {
	m := sealedMesh(t)
	m.noteRejected()
	m.noteRejected()
	st := m.stats()
	if st.Rejected != 2 {
		t.Fatalf("rejected=%d, want 2", st.Rejected)
	}
	if st.Inbound != 0 || st.Outbound != 0 || st.Duplicates != 0 {
		t.Fatalf("rejected traffic leaked into mesh counters: %+v", st)
	}
	if st.Bidirectional {
		t.Fatal("rejected traffic must not make the mesh look bidirectional")
	}
	if !st.Sealed {
		t.Fatal("stats must publish the sealed posture")
	}
}

// The mesh tests above build a zero-value mqttMesh, which leaves sealing off
// and therefore exercises the plaintext path only. Production runs with
// sealing on, so the sealed path needs its own coverage: a frame from a peer
// swarm must be authenticated, delivered to the local bus exactly once, and
// still deduped across broker echoes — while the same message sent in the
// clear must never arrive.
func TestSealedPathDeliversPeerTrafficAndRefusesPlaintext(t *testing.T) {
	busKey(t)
	r := newRelay()
	m := newMQTTMesh(r, "")
	if !m.sealing {
		t.Fatal("newMQTTMesh must default to sealing on")
	}

	incoming := message{ID: "peer-1", Event: "probe", Title: "hi", Message: "from-a-peer"}
	plain, err := json.Marshal(incoming)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := hm.SealRelayPayload(plain)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	// Everything on a shared broker is addressed inside this fleet's
	// namespace, so that is the topic a real delivery carries.
	scoped := "hive/" + m.mqttNamespace() + "/secure"

	// The plaintext form of the same message, straight off a stranger.
	m.onMessage(nil, &fakeMsg{topic: scoped, payload: plain})
	if tp, ok := r.topics["secure"]; ok && len(tp.messages) != 0 {
		t.Fatal("an unsealed frame reached the local bus — the bus is open")
	}
	if m.stats().Rejected == 0 {
		t.Fatal("the unsealed frame was not counted as rejected")
	}

	// The genuine article, delivered once, then echoed by the other brokers.
	m.onMessage(nil, &fakeMsg{topic: scoped, payload: []byte(sealed)})
	tp, ok := r.topics["secure"]
	if !ok || len(tp.messages) != 1 {
		t.Fatalf("sealed frame did not deliver exactly once: %d messages", len(tp.messages))
	}
	if tp.messages[0].Message != incoming.Message {
		t.Fatalf("delivered body was altered: %q", tp.messages[0].Message)
	}
	for i := 0; i < 5; i++ {
		m.onMessage(nil, &fakeMsg{topic: scoped, payload: []byte(sealed)})
	}
	if got := len(tp.messages); got != 1 {
		t.Fatalf("broker echoes were not deduped: %d messages", got)
	}
	st := m.stats()
	if st.Inbound != 1 {
		t.Fatalf("inbound=%d, want 1", st.Inbound)
	}
	if st.Duplicates != 5 {
		t.Fatalf("duplicates=%d, want 5", st.Duplicates)
	}
	if !st.Bidirectional {
		t.Fatal("genuine peer traffic must make the mesh bidirectional")
	}
}

// TestForeignNamespaceIsIgnored proves the shared-broker boundary. These
// brokers are public infrastructure carrying other fleets; a frame
// addressed to another namespace must not reach our local bus, and must
// not land in the rejected counter either -- that counter is the only
// real intrusion signal, and other tenants' traffic must not be able to
// inflate it into meaninglessness.
func TestForeignNamespaceIsIgnored(t *testing.T) {
	busKey(t)
	r := newRelay()
	m := newMQTTMesh(r, "")

	sealed, err := hm.SealRelayPayload([]byte(`{"id":"spoof-1","message":"from a stranger"}`))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	ours := m.mqttNamespace()
	if ours == "" {
		t.Fatal("no namespace resolved")
	}

	// Right key, wrong address: another fleet's topic.
	m.onMessage(nil, &fakeMsg{topic: "hive/deadbeef/secure", payload: []byte(sealed)})
	if len(r.topics) != 0 {
		t.Fatalf("a foreign-namespace frame created topics: %v", r.topics)
	}
	if got := m.stats().Rejected; got != 0 {
		t.Fatalf("foreign namespace counted as %d rejections — counter is polluted", got)
	}

	// Our own namespace still works, so the guard is not just blocking all.
	m.onMessage(nil, &fakeMsg{topic: "hive/" + ours + "/secure", payload: []byte(sealed)})
	if len(r.topics["secure"].messages) != 1 {
		t.Fatal("our own namespaced frame was dropped")
	}
}

// TestPublishTopicIsUnderOurSubscribeFilter pins the property whose absence
// was invisible: the bridge published to the bare topic while subscribing
// to the namespaced one, so it never saw its own echoes and healthz showed
// a healthy-looking mesh carrying nothing. Both sides now go through
// mqttTopic, and this asserts they still agree.
func TestPublishTopicIsUnderOurSubscribeFilter(t *testing.T) {
	busKey(t)
	m := newMQTTMesh(newRelay(), "")
	ns := m.mqttNamespace()
	if ns == "" || ns == "no-key" {
		t.Fatalf("namespace not resolved: %q", ns)
	}
	filter := m.mqttSubscribeFilter()
	if filter != "hive/"+ns+"/#" {
		t.Fatalf("subscribe filter %q is not scoped to our namespace", filter)
	}
	for _, topic := range []string{"probe", "secure", "state", "g/t38/host"} {
		published := m.mqttTopic(topic)
		if !strings.HasPrefix(published, "hive/"+ns+"/") {
			t.Fatalf("%s published outside our namespace: %q", topic, published)
		}
		// The single-level wildcard filter must actually match it.
		if !mqttFilterMatches(filter, published) {
			t.Fatalf("published %q but subscribed %q — our own frame would never return", published, filter)
		}
	}
	// And the no-key path must not fall back onto the shared bare topic.
	t.Setenv("HIVEMIND_CIPHER_KEY", "")
	m2 := newMQTTMesh(newRelay(), "")
	if got := m2.mqttTopic("probe"); got == "hive/probe" {
		t.Fatal("with no root the bridge published onto the shared bare topic")
	}
}

// mqttFilterMatches applies the one wildcard level the bridge actually
// uses ("#"), so the test checks the real match rather than a string
// resemblance.
func mqttFilterMatches(filter, topic string) bool {
	if strings.HasSuffix(filter, "/#") {
		return strings.HasPrefix(topic, strings.TrimSuffix(filter, "#"))
	}
	return filter == topic
}
