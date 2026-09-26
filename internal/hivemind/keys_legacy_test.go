package hivemind

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// v1 is dead on the bus. These tests exist so that stays true: they assert
// the absence of a capability, which is the only kind of security test that
// can be run against code that is supposed to be unreachable.

const legacyTestRoot = "4b4b4b4b4b4b4b4b4b4b4b4b4b4b4b4b4b4b4b4b4b4b4b4b4b4b4b4b4b4b4b4b"

func legacyRootBytes(t *testing.T) []byte {
	t.Helper()
	b, err := hex.DecodeString(legacyTestRoot)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// legacyFrame builds a frame exactly as v1 would have sealed it, so the test
// is checking the real wire format rather than a convenient fiction.
func legacyFrame(t *testing.T, plain string) string {
	t.Helper()
	f, err := sealWith(legacyRootBytes(t), hourAAD(hourEpochNow()), []byte(plain))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// The bus must refuse a valid v1 frame. Not "refuse by default" -- refuse,
// with no switch, including when every v1 environment variable is set to
// every value that once enabled it.
func TestBusRefusesV1WithNoEscapeHatch(t *testing.T) {
	t.Setenv("HIVEMIND_CIPHER_KEY", legacyTestRoot)
	t.Setenv("HIVEMIND_CIPHER_KEY_PREV", "")
	frame := legacyFrame(t, `{"id":"legacy","marker":"v1"}`)

	// Every spelling of the switches that used to open the window.
	for _, v := range []string{"on", "1", "true", "yes", "ON", "True"} {
		t.Setenv("HIVEMIND_V1_GRACE", v)
		t.Setenv("HIVEMIND_V1_GRACE_UNTIL", "2099-01-01T00:00:00Z")
		t.Setenv("HIVEMIND_V1_FRAME_LIMIT", "1000000")
		if _, _, err := OpenRelayPayload(frame); err == nil {
			t.Fatalf("bus accepted a v1 frame with HIVEMIND_V1_GRACE=%q", v)
		}
	}
}

// A frame sealed under the fleet's own v1 key must not open on the bus,
// while the same frame still opens offline. That asymmetry is the whole
// design: recoverable old data, no way back onto the wire.
func TestV1ReadableOfflineButNotOnTheBus(t *testing.T) {
	t.Setenv("HIVEMIND_CIPHER_KEY", legacyTestRoot)
	plain := `{"id":"legacy","marker":"offline-recovery"}`
	frame := legacyFrame(t, plain)

	if _, _, err := OpenRelayPayload(frame); err == nil {
		t.Fatal("bus opened a v1 frame")
	}
	got, err := DecryptLegacyFrame(frame)
	if err != nil {
		t.Fatalf("offline recovery of a v1 frame failed: %v", err)
	}
	if string(got) != plain {
		t.Fatalf("offline recovery returned %q", got)
	}
}

// A v2 frame is not a v1 frame, and vice versa. If this ever stops being
// true the two hierarchies share a secret and the isolation argument fails.
func TestV1AndV2AreMutuallyUnreadable(t *testing.T) {
	t.Setenv("HIVEMIND_CIPHER_KEY", legacyTestRoot)
	v1 := legacyFrame(t, `{"id":"a"}`)
	v2, err := SealRelayPayload([]byte(`{"id":"b"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenRelayPayload(v1); err == nil {
		t.Fatal("bus opened a v1 frame")
	}
	if _, err := DecryptLegacyFrame(v2); err == nil {
		t.Fatal("the offline v1 reader opened a v2 frame")
	}
	// Both open as themselves.
	if _, _, err := OpenRelayPayload(v2); err != nil {
		t.Fatalf("bus cannot open its own v2 frame: %v", err)
	}
	if _, err := DecryptLegacyFrame(v1); err != nil {
		t.Fatalf("offline reader cannot open its own v1 frame: %v", err)
	}
}

// The namespace fix for root rotation is independent of the v1 cutover and
// must survive it: a rotated fleet changes namespace, so without listening
// on both, HIVEMIND_CIPHER_KEY_PREV could decrypt a frame the filter never
// delivered.
func TestNamespacesStillCoverRotatingRoot(t *testing.T) {
	oldNS := namespaceFor("1111111111111111111111111111111111111111111111111111111111111111")
	newNS := namespaceFor("2222222222222222222222222222222222222222222222222222222222222222")

	t.Setenv("HIVEMIND_CIPHER_KEY", strings.Repeat("22", 32))
	t.Setenv("HIVEMIND_CIPHER_KEY_PREV", "")
	if RootRotationActive() {
		t.Fatal("rotation reported with no previous root")
	}
	if got := Namespaces(); len(got) != 1 || got[0] != newNS {
		t.Fatalf("namespaces=%v, want [%s]", got, newNS)
	}

	t.Setenv("HIVEMIND_CIPHER_KEY_PREV", strings.Repeat("11", 32))
	if !RootRotationActive() {
		t.Fatal("rotation not reported with a previous root")
	}
	got := Namespaces()
	if len(got) != 2 || got[0] != newNS || got[1] != oldNS {
		t.Fatalf("namespaces=%v, want [%s %s] (new first: it is what we publish on)", got, newNS, oldNS)
	}
}

// A frame under the previous root must open, and must stop opening once
// that root is retired.
func TestPreviousRootOpensThenStops(t *testing.T) {
	const oldRoot = "1111111111111111111111111111111111111111111111111111111111111111"
	const newRoot = "2222222222222222222222222222222222222222222222222222222222222222"

	t.Setenv("HIVEMIND_CIPHER_KEY", oldRoot)
	oldSealed, err := SealRelayPayload([]byte(`{"id":"rot"}`))
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("HIVEMIND_CIPHER_KEY", newRoot)
	t.Setenv("HIVEMIND_CIPHER_KEY_PREV", oldRoot)
	got, _, err := OpenRelayPayload(oldSealed)
	if err != nil {
		t.Fatalf("frame under the previous root did not open: %v", err)
	}
	if !strings.Contains(string(got), "rot") {
		t.Fatalf("wrong plaintext: %s", got)
	}

	t.Setenv("HIVEMIND_CIPHER_KEY_PREV", "")
	if _, _, err := OpenRelayPayload(oldSealed); err == nil {
		t.Fatal("frame under a retired root still opened")
	}
	fresh, err := SealRelayPayload([]byte(`{"id":"new"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenRelayPayload(fresh); err != nil {
		t.Fatalf("new root cannot open its own frames: %v", err)
	}
}

func namespaceFor(rootHex string) string {
	k, _ := parseRoot(rootHex)
	sum := sha256.Sum256(tierKey(k, "hivemind/v2/namespace|", 0))
	return hex.EncodeToString(sum[:4])
}
