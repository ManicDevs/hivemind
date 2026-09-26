package hivemind

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// The v2 relay-bus key hierarchy.
//
//	v2 fleet root        shared 32-byte secret, rotatable (never a wire key)
//	   |  HMAC(root, "hivemind/v2/day|D")
//	 day root(D)         one independent root per UTC day
//	   |  HMAC(day,   "hivemind/v2/hour|H")
//	 hour key(H)
//	   |  HMAC(hour,  "hivemind/v2/leaf|L")     L = UTC minute
//	 leaf key(L)         the AES-256 key; turns over every minute
//
// Every period is derived from its parent with HMAC over that period's
// label. Nothing is chained, which is the whole point:
//
//	v1 chained days:  dayRoot(D+1) = SHA256(dayRoot(D))
//	                 an attacker holding one day root could walk the chain
//	                 forward forever. Every future day, and every future
//	                 hour key under it, fell with that one disclosure.
//
// With independent derivation, holding leaf(L) reveals nothing about
// leaf(L+1) -- that needs the hour key; holding hour(H) reveals nothing
// about the next day -- that needs the day root; holding day(D) reveals
// nothing about day(D+1) -- that needs the root. Compromise is contained
// to the period actually leaked, in both directions, and the disclosure
// radius is a minute rather than an hour.
const (
	// KeyTier names the derivation so a v1 and a v2 peer fail against each
	// other diagnosably rather than as generic garbage.
	KeyTier = "v2"

	// leafWindow accepts the live minute plus the two before it: enough
	// that clock skew or a slow scheduler cannot partition the bus, short
	// enough that a captured frame has a shelf life measured in minutes.
	leafWindow = 3
)

// minuteEpoch counts whole UTC minutes since the anchor, so a leaf key is
// a pure function of (root, minute) with no accumulated state.
func minuteEpoch(t time.Time) int64 {
	return int64(t.UTC().Sub(keyEpochAnchor).Minutes())
}

// tierKey is the only primitive in the hierarchy: HMAC-SHA256 over the
// parent keyed with this tier's label and period. Independent per period
// by construction -- this is what removes the forward-leak.
func tierKey(parent []byte, label string, period int64) []byte {
	mac := hmac.New(sha256.New, parent)
	mac.Write([]byte(label))
	mac.Write([]byte(strconv.FormatInt(period, 10)))
	return mac.Sum(nil)
}

// dayTier is the per-day root. Independent of every other day.
func dayTier(root []byte, day int64) []byte {
	return tierKey(root, "hivemind/v2/day|", day)
}

// hourTier is the per-hour key, under its day's root.
func hourTier(root []byte, hour int64) []byte {
	return tierKey(dayTier(root, hour/24), "hivemind/v2/hour|", hour)
}

// leafTier is the per-minute key, under its hour. This is the wire key.
func leafTier(root []byte, leaf int64) []byte {
	return tierKey(hourTier(root, leaf/60), "hivemind/v2/leaf|", leaf)
}

// hostKey is this machine's identity subkey, derived from the same root
// but bound to /etc/machine-id. It is deliberately NOT a bus key: the bus
// key must be derivable by every host in the fleet, this must not. Keeping
// them separate is what lets a shared bus and per-host identity coexist;
// v1 conflated them and could not span hosts without abandoning the
// machine binding that was supposed to contain a stolen box.
func hostKey(root []byte) []byte {
	mac := hmac.New(sha256.New, root)
	mac.Write([]byte("hivemind/v2/host|" + machineSecret()))
	return mac.Sum(nil)
}

// HostKeyHex exposes the per-host identity subkey for diagnostics. It
// proves hosts differ where the bus key is deliberately shared.
func HostKeyHex() string {
	for _, r := range fleetRoots() {
		return hex.EncodeToString(hostKey(r.key))
	}
	return ""
}

// leafAAD welds a frame to its full position in the hierarchy: day, hour
// and minute. Replaying a captured frame into any other minute fails
// authentication even under a genuinely valid key.
func leafAAD(leaf int64) string {
	return fmt.Sprintf("hivemind-relay-%s|d%d|h%d|l%d", KeyTier, leaf/1440, leaf/60, leaf)
}

// fleetRoot is one accepted root plus where it came from, so telemetry
// can say which key a node is actually speaking.
type fleetRoot struct {
	key    []byte
	source string
}

// parseRoot accepts a root in either form an operator is likely to have:
// 32 raw bytes, or the 64 hex chars of a .relaykey file. Accepting both is
// not leniency for its own sake — .relaykey is written as hex, so copying
// it straight into HIVEMIND_CIPHER_KEY is the obvious thing to do, and
// under a strict 32-byte reading that silently yields no root at all.
func parseRoot(s string) ([]byte, bool) {
	s = strings.TrimSpace(s)
	switch {
	case len(s) == 32:
		return []byte(s), true
	case len(s) == 64:
		if b, err := hex.DecodeString(s); err == nil {
			return b, true
		}
	}
	return nil, false
}

// fleetRoots returns the accepted roots in preference order.
//
//	Fleet mode  HIVEMIND_CIPHER_KEY, raw and unbound, so every host in the
//	            fleet derives the same bus. Rotation grace: the previous
//	            root stays accepted via HIVEMIND_CIPHER_KEY_PREV.
//	Solo mode   the build-time .relaykey, bound to this host's hardware
//	            and machine-id, so a copied seed alone opens nothing.
//
// The keyring is what makes root rotation a rolling operation: add the
// new root as primary while the old one lingers as previous, then retire
// it. No flag day.
//
// HIVEMIND_CIPHER_KEY_PREV is independent of the v1 frame grace below.
// They are different concerns and conflating them would mean a fleet
// could not rotate its root during an upgrade, or that retiring a root
// would quietly re-enable v1 reading.
func fleetRoots() []fleetRoot {
	var roots []fleetRoot
	if k, ok := parseRoot(os.Getenv("HIVEMIND_CIPHER_KEY")); ok {
		roots = append(roots, fleetRoot{k, "env"})
	}
	if k, ok := parseRoot(os.Getenv("HIVEMIND_CIPHER_KEY_PREV")); ok {
		roots = append(roots, fleetRoot{k, "env-prev"})
	}
	if len(roots) > 0 {
		return roots
	}
	if b, ok := ratchetBase(compileRelayKey, machineFingerprint(), machineSecret()); ok {
		roots = append(roots, fleetRoot{b, "baked"})
	}
	return roots
}

// Namespace is the bridge's topic namespace on a shared public broker.
//
// These brokers are public infrastructure, and other hivemind fleets
// publish there too. Subscribing to a bare "hive/#" means we ingest every
// one of them -- someone else's private frames land in our process, we
// spend a decryption attempt per frame, and the rejected counter fills
// with traffic that was never an attack against us, which destroys its
// value as a security signal. It also means our own topic names collide
// with theirs.
//
// Deriving the namespace from the root fixes all of it without
// configuration: one fleet shares one namespace across its hosts, and
// every other fleet derives a different one and cannot address us.
func Namespace() string {
	for _, r := range fleetRoots() {
		sum := sha256.Sum256(tierKey(r.key, "hivemind/v2/namespace|", 0))
		return hex.EncodeToString(sum[:4])
	}
	return ""
}

// v2LeafKey is the wire key for a leaf `back` minutes in the past.
func v2LeafKey(back int) ([]byte, string, bool) {
	leaf := minuteEpoch(time.Now()) - int64(back)
	if leaf < 0 {
		return nil, "", false
	}
	for _, r := range fleetRoots() {
		return leafTier(r.key, leaf), r.source, true
	}
	return nil, "", false
}

// OpenRelayPayload opens a sealed bus frame.
//
// It is v2-only, and that is deliberate rather than an omission. There is no
// v1 fallback and no environment variable that reintroduces one: a bus that
// can be talked into accepting pre-v2 frames can be fed forged ones, because
// v1's day tier is a forward hash chain and a leaked day root mints every
// future hour key under it. See DecryptLegacyFrame for the offline path,
// which is the only way v1 is still readable, and cannot inject anything.
//
// The second return is the key schedule that opened the frame, for telemetry.
//
// SealRelayPayload encrypts one relay wire payload under the current leaf
// key. Fails closed: with no root there is no key, and a caller that
// cannot tell "sealed" from "best effort" will eventually publish
// something it should not.
func SealRelayPayload(plaintext []byte) (string, error) {
	key, source, ok := v2LeafKey(0)
	if !ok {
		return "", fmt.Errorf("no relay key: refusing to send %d bytes in the clear", len(plaintext))
	}
	sealed, err := sealWith(key, leafAAD(minuteEpoch(time.Now())), plaintext)
	if err != nil {
		return "", err
	}
	sealSource.Store(source)
	return sealed, nil
}

// sealSource remembers which root the last published frame used, for
// /healthz. Only ever a label, never key material.
var sealSource atomic.Value

// OpenRelayPayload opens a sealed frame. It tries, in order:
//
//  1. the live leaf and the leafWindow-1 before it, under every accepted
//     root -- current root first, then any rotation-grace previous root;
//  2. during the v1 grace period only, the v1 hour-key frames an
//     un-upgraded peer still writes.
//
// Anything else is reported undecryptable so the caller drops it unread
// rather than parsing a stranger's bytes. There is deliberately no static
// fallback: a key published in this source is a universal write key.
func OpenRelayPayload(cryptoText string) ([]byte, string, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(cryptoText)
	if err != nil {
		return nil, "", fmt.Errorf("not a sealed frame: %w", err)
	}
	if len(ciphertext) < 12 {
		return nil, "", fmt.Errorf("sealed frame too short")
	}

	nowLeaf := minuteEpoch(time.Now())
	for back := 0; back < leafWindow; back++ {
		leaf := nowLeaf - int64(back)
		for _, r := range fleetRoots() {
			if plain, err := gcmOpen(ciphertext, leafTier(r.key, leaf), leafAAD(leaf)); err == nil {
				return plain, "v2", nil
			}
		}
	}
	return nil, "", fmt.Errorf("sealed frame undecryptable under this fleet's %s keys", KeyTier)
}

// gcmOpen is one authentication attempt. It reports failure rather than
// distinguishing wrong-key from tampered: that difference is an oracle.
func gcmOpen(ciphertext, key []byte, aad string) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < aesgcm.NonceSize() {
		return nil, fmt.Errorf("sealed frame too short")
	}
	nonce, body := ciphertext[:aesgcm.NonceSize()], ciphertext[aesgcm.NonceSize():]
	return aesgcm.Open(nil, nonce, body, []byte(aad))
}

// DecryptLegacyFrame opens a pre-v2 frame offline.
//
// This is the *only* remaining way to read v1, and it is deliberately not
// reachable from the bus. An operator with historical v1 data -- an old
// capture, a log from before the upgrade -- can still recover it, which is
// the part of v1 worth keeping. What they cannot do is put v1 back on the
// wire: this function takes bytes and returns bytes, it accepts no
// connection, ingests no message, and routes nothing. There is deliberately
// no SealLegacyFrame to go with it, so no code path can reintroduce v1
// traffic into a live bus.
//
// That asymmetry is the point, and it is why the v1 -> v2 migration is a
// cutover rather than a rolling window. Reading old data offline is free of
// risk. Speaking v1 is not: the day tier was a forward hash chain,
// dayRoot(D+1) = SHA256(dayRoot(D)), so one disclosed day root mints every
// future hour key under it. A bus that accepts v1 can therefore be fed
// forged frames, permanently, and cannot tell them from real ones. The
// relay is a backup channel with a live local path, so tolerating a brief
// partition during a coordinated upgrade is much cheaper than keeping that
// write path open -- see the migration runbook in the README.
//
// The chain walk below is the defect v2 exists to remove. Do not reuse it
// for anything that mints keys.
func DecryptLegacyFrame(sealed string) ([]byte, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sealed))
	if err != nil {
		return nil, fmt.Errorf("not a sealed frame: %w", err)
	}
	if len(ciphertext) < 12 {
		return nil, fmt.Errorf("sealed frame too short")
	}
	nowHour := hourEpoch(time.Now())
	// v1 fleet mode used the raw operator key as the wire key.
	if k, ok := parseRoot(os.Getenv("HIVEMIND_CIPHER_KEY")); ok {
		for back := int64(0); back < relaySealWindow; back++ {
			if plain, err := gcmOpen(ciphertext, k, hourAAD(nowHour-back)); err == nil {
				return plain, nil
			}
		}
	}
	// v1 solo mode used the chained hourly ratchet.
	for back := int64(0); back < relaySealWindow; back++ {
		if key, ok := legacyRatchetKey(int(back)); ok {
			if plain, err := gcmOpen(ciphertext, key, hourAAD(nowHour-back)); err == nil {
				return plain, nil
			}
		}
	}
	return nil, fmt.Errorf("no v1 key opens this frame; is HIVEMIND_CIPHER_KEY the key this fleet used under v1?")
}

// legacyRatchetKey is the retired derivation, used only by
// DecryptLegacyFrame. Its chain walk is the defect v2 removes; do not reuse
// it for anything that mints new keys.
func legacyRatchetKey(backHours int) ([]byte, bool) {
	hour := hourEpoch(time.Now()) - int64(backHours)
	if hour < 0 {
		return nil, false
	}
	h := v1DayRoot(compileRelayKey, machineFingerprint(), machineSecret(), hour/24)
	if h == nil {
		return nil, false
	}
	mac := hmac.New(sha256.New, h)
	mac.Write([]byte(fmt.Sprintf("hivemind-hour|%d", hour)))
	return mac.Sum(nil), true
}

// RootRotationActive reports whether a previous root is being accepted, so
// the relay knows to listen on that root's namespace as well. Without this
// the previous root is dead weight: a rotated fleet changes namespace, and
// HIVEMIND_CIPHER_KEY_PREV can decrypt a frame perfectly well that the topic
// filter never delivers.
func RootRotationActive() bool {
	_, ok := parseRoot(os.Getenv("HIVEMIND_CIPHER_KEY_PREV"))
	return ok
}

// Namespaces lists every topic namespace this node must listen on: its own,
// plus the namespace of a root it is still willing to accept. Publishing
// always uses the first entry, so a rotation moves traffic forward while
// the old namespace drains.
func Namespaces() []string {
	roots := fleetRoots()
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		sum := sha256.Sum256(tierKey(r.key, "hivemind/v2/namespace|", 0))
		out = append(out, hex.EncodeToString(sum[:4]))
	}
	return out
}

// v1RatchetKey is the retired derivation, kept only to read v1 frames.
// Its chain walk is the defect v2 removes; do not reuse it for anything
// that mints new keys.
func v1RatchetKey(backHours int) ([]byte, bool) {
	hour := hourEpoch(time.Now()) - int64(backHours)
	if hour < 0 {
		return nil, false
	}
	h := v1DayRoot(compileRelayKey, machineFingerprint(), machineSecret(), hour/24)
	if h == nil {
		return nil, false
	}
	mac := hmac.New(sha256.New, h)
	mac.Write([]byte(fmt.Sprintf("hivemind-hour|%d", hour)))
	return mac.Sum(nil), true
}

// v1DayRoot is the forward hash chain v1 shipped: SHA256 applied `day`
// times. Its forward leak is why v2 derives each day independently.
func v1DayRoot(seedHex, fingerprint, machineID string, day int64) []byte {
	h, ok := ratchetBase(seedHex, fingerprint, machineID)
	if !ok || day < 0 {
		return nil
	}
	for i := int64(0); i < day; i++ {
		sum := sha256.Sum256(h)
		h = sum[:]
	}
	return h
}

// KeyTierInfo is the /healthz view of which key a node is speaking.
// Labels only; no key material ever leaves this process.
type KeyTierInfo struct {
	Tier      string `json:"tier"`
	Namespace string `json:"namespace"`
	Roots     int    `json:"roots"`
	Source    string `json:"source"`
	Leaf      int64  `json:"leaf"`
	Window    int    `json:"window_minutes"`
	HostBound bool   `json:"host_bound"`
}

// CurrentKeyTier reports the live tier for telemetry and dashboards.
func CurrentKeyTier() KeyTierInfo {
	roots := fleetRoots()
	info := KeyTierInfo{
		Tier:      KeyTier,
		Namespace: Namespace(),
		Roots:     len(roots),
		Window:    leafWindow,
		Leaf:      minuteEpoch(time.Now()),
		HostBound: len(roots) == 1 && roots[0].source == "baked",
	}
	if s, _ := sealSource.Load().(string); s != "" {
		info.Source = s
	} else if len(roots) > 0 {
		info.Source = roots[0].source
	}
	return info
}

// CurrentLeafKeyHex exposes the live minute key so rotation is observable
// rather than assumed. Short by design -- this is a diagnostic, not a
// place to ship key material around.
func CurrentLeafKeyHex() string {
	if k, _, ok := v2LeafKey(0); ok {
		return hex.EncodeToString(k[:16])
	}
	return ""
}
