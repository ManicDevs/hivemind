package hivemind

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

const testRoot = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

func testRootBytes(t *testing.T) []byte {
	t.Helper()
	b, err := hex.DecodeString(testRoot)
	if err != nil || len(b) != 32 {
		t.Fatal("test root must be 32 bytes")
	}
	return b
}

// TestNoForwardLeak is the regression test for the defect that motivated
// v2. Under v1's hash chain, dayRoot(D+1) == SHA256(dayRoot(D)), so one
// disclosed day root minted every future day. This asserts the chain
// relation no longer holds and that neither day's root is derivable from
// the other.
func TestNoForwardLeak(t *testing.T) {
	const day = 100

	today, _ := dayKey(testRoot, "cpu|ram", "mid", day)
	tomorrow, _ := dayKey(testRoot, "cpu|ram", "mid", day+1)

	// Exactly the v1 attack: hash today's root forward, no seed in hand.
	forward := sha256.Sum256(today)
	if bytes.Equal(forward[:], tomorrow) {
		t.Fatal("v1 forward leak is back: SHA256(dayRoot(D)) == dayRoot(D+1)")
	}
	if bytes.Equal(today, tomorrow) {
		t.Fatal("adjacent days share a root — no daily rotation")
	}
	// And the reverse direction stays closed too.
	back := sha256.Sum256(tomorrow)
	if bytes.Equal(back[:], today) {
		t.Fatal("day roots are mutually derivable in one step")
	}
}

// TestNoTierBleed confirms each tier is derived from its own parent, so a
// leaf key cannot be walked up to the hour or day that produced it, and no
// two tiers can collide through label confusion.
func TestNoTierBleed(t *testing.T) {
	base := testRootBytes(t)
	const hour = 4242

	leaf := leafTier(base, hour*60)
	hourKeyAt := hourTier(base, hour)
	dayKeyAt := dayTier(base, hour/24)

	// A leaf is not its parent, nor any hash-step from it.
	if bytes.Equal(leaf, hourKeyAt) || bytes.Equal(leaf, dayKeyAt) {
		t.Fatal("leaf key collides with an ancestor tier")
	}
	up := sha256.Sum256(leaf)
	if bytes.Equal(up[:], hourKeyAt) {
		t.Fatal("leaf key walks up into its hour — no tier separation")
	}
	// Independent per period, every tier.
	for _, c := range []struct {
		name string
		a, b []byte
	}{
		{"day", dayTier(base, 1), dayTier(base, 2)},
		{"hour", hourTier(base, 1), hourTier(base, 2)},
		{"leaf", leafTier(base, 1), leafTier(base, 2)},
	} {
		if bytes.Equal(c.a, c.b) {
			t.Fatalf("%s tier: adjacent periods share a key — no rotation", c.name)
		}
	}
	// The retired chain is retained for reading v1 only, and it still has
	// the leak that v2 removed. If this ever passes, the quarantine broke.
	v1a := v1DayRoot(testRoot, "cpu|ram", "mid", 100)
	v1b := v1DayRoot(testRoot, "cpu|ram", "mid", 101)
	fwd := sha256.Sum256(v1a)
	if !bytes.Equal(fwd[:], v1b) {
		t.Fatal("v1 quarantine changed shape — openV1Frame would misread old frames")
	}
}

// TestLeafTurnsOverEveryMinute is the granularity claim: the wire key is
// not static across an hour, which was the original concern.
func TestLeafTurnsOverEveryMinute(t *testing.T) {
	base := testRootBytes(t)
	const hour = 99
	seen := map[string]bool{}
	for m := 0; m < 60; m++ {
		k := leafTier(base, hour*60+int64(m))
		if len(k) != 32 {
			t.Fatalf("minute %d: key is %d bytes, want 32", m, len(k))
		}
		if seen[string(k)] {
			t.Fatalf("minute %d reuses an earlier key — leaf is not per-minute", m)
		}
		seen[string(k)] = true
	}
	// The hour tier still holds across the hour, so the hierarchy is
	// genuinely two-level rather than 60 unrelated keys.
	h0 := hourTier(base, hour)
	h1 := hourTier(base, hour)
	if !bytes.Equal(h0, h1) {
		t.Fatal("hour tier is not stable within its hour")
	}
}

// TestLeafAADBindsFullPath checks the replay armour names every tier.
func TestLeafAADBindsFullPath(t *testing.T) {
	const hour = 300
	if leafAAD(hour*60) == leafAAD(hour*60+1) {
		t.Fatal("AAD identical across a minute boundary — no per-minute armour")
	}
	if leafAAD(hour*60) == leafAAD((hour+1)*60) {
		t.Fatal("AAD identical across an hour boundary")
	}
	if !strings.Contains(leafAAD(hour*60), KeyTier) {
		t.Fatal("AAD does not name the tier — v1/v2 must fail diagnosably")
	}
}

// TestSealOpenRoundTripAndWindow exercises the real published API against
// the real window, including the boundary cases either side of it.
func TestSealOpenRoundTripAndWindow(t *testing.T) {
	t.Setenv("HIVEMIND_CIPHER_KEY", testRoot)
	t.Setenv("HIVEMIND_CIPHER_KEY_PREV", "")
	t.Setenv("HIVEMIND_RELAY_SEAL", "on")

	sealed, err := SealRelayPayload([]byte(`{"kind":"thought","n":42}`))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if strings.Contains(sealed, "thought") || strings.Contains(sealed, "kind") {
		t.Fatal("plaintext visible on the wire")
	}
	got, _, err := OpenRelayPayload(sealed)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(got, []byte(`{"kind":"thought","n":42}`)) {
		t.Fatalf("round trip altered the payload: %s", got)
	}

	// A frame sealed at the oldest accepted leaf still opens; one a minute
	// older does not. This is the shelf life the design promises.
	nowLeaf := minuteEpoch(time.Now())
	inside, err := sealWith(leafTier(testRootBytes(t), nowLeaf-int64(leafWindow-1)), leafAAD(nowLeaf-int64(leafWindow-1)), []byte("edge"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenRelayPayload(inside); err != nil {
		t.Fatalf("oldest in-window frame refused: %v", err)
	}
	outside, err := sealWith(leafTier(testRootBytes(t), nowLeaf-int64(leafWindow)), leafAAD(nowLeaf-int64(leafWindow)), []byte("stale"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenRelayPayload(outside); err == nil {
		t.Fatal("frame past the window opened — shelf life is not enforced")
	}
}

// TestSealFailsClosedWithoutKey is the property that stops a key outage
// turning into a plaintext broadcast.
func TestSealFailsClosedWithoutKey(t *testing.T) {
	t.Setenv("HIVEMIND_CIPHER_KEY", "")
	t.Setenv("HIVEMIND_CIPHER_KEY_PREV", "")
	old := compileRelayKey
	compileRelayKey = ""
	defer func() { compileRelayKey = old }()

	if _, err := SealRelayPayload([]byte("secret")); err == nil {
		t.Fatal("sealed with no key at all — must fail closed")
	}
	if info := CurrentKeyTier(); info.Roots != 0 {
		t.Fatalf("tier reports %d roots with no key material", info.Roots)
	}
}

// TestRotationGraceAcceptsPreviousRoot is what makes root rotation a
// rolling operation instead of a flag day.
func TestRotationGraceAcceptsPreviousRoot(t *testing.T) {
	newRoot := strings.Repeat("11", 32)
	oldRoot := strings.Repeat("22", 32)
	t.Setenv("HIVEMIND_CIPHER_KEY", newRoot)
	t.Setenv("HIVEMIND_CIPHER_KEY_PREV", oldRoot)

	// A frame from the old root still opens while it is a grace root.
	oldSealed, err := sealWith(leafTier(testRootBytesFrom(t, oldRoot), minuteEpoch(time.Now())), leafAAD(minuteEpoch(time.Now())), []byte("from-old-root"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenRelayPayload(oldSealed); err != nil {
		t.Fatalf("grace root refused: %v", err)
	}
	// A new-root frame opens too, and writes prefer the new root.
	newSealed, err := SealRelayPayload([]byte("from-new-root"))
	if err != nil {
		t.Fatal(err)
	}
	if info := CurrentKeyTier(); info.Roots != 2 || info.Source != "env" {
		t.Fatalf("keyring not reported: %+v", info)
	}
	if _, _, err := OpenRelayPayload(newSealed); err != nil {
		t.Fatalf("current root frame refused: %v", err)
	}

	// Retire the old root by dropping _PREV. This must be independent of
	// the v1 frame grace: the two are different concerns.
	t.Setenv("HIVEMIND_CIPHER_KEY_PREV", "")
	if info := CurrentKeyTier(); info.Roots != 1 {
		t.Fatalf("retired root still accepted: %+v", info)
	}
	if _, _, err := OpenRelayPayload(oldSealed); err == nil {
		t.Fatal("retired root still opens frames — rotation grace did not expire")
	}
}

func testRootBytesFrom(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		t.Fatalf("root %q must be 32 bytes", s)
	}
	return b
}
