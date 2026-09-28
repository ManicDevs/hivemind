package fabricsim

import "math"

// AttackKind identifies which adversarial behaviour produced a packet. The
// defender keys its signature table and the scoreboard's per-vector tallies on
// this value, so every attack the spec asks for has a distinct identity.
type AttackKind int

const (
	// KindLegitimate is genuine mesh traffic, which the defence must never
	// drop. This is the control group: a defence that cannot pass honest
	// traffic is useless regardless of its detection rate.
	KindLegitimate AttackKind = iota
	// KindPacketDrop is a flood engineered to exhaust the queue and cause
	// involuntary loss of honest frames.
	KindPacketDrop
	// KindClockDrift pushes a sender's claimed key hour beyond the accepted
	// ±3 minute skew window, attempting to desynchronise leaf-key rotation.
	KindClockDrift
	// KindMalformedLegacy is a legacy/garbage frame that must fail
	// authentication before it is ever parsed.
	KindMalformed
	// KindForgery is a frame carrying a valid structure with a signature that
	// does not verify, i.e. an impersonation attempt against a known handle.
	KindForgery
	numKinds
)

func (k AttackKind) String() string {
	switch k {
	case KindLegitimate:
		return "legit"
	case KindPacketDrop:
		return "pkt-drop"
	case KindClockDrift:
		return "clock-drift"
	case KindMalformed:
		return "malformed"
	case KindForgery:
		return "forgery"
	default:
		return "unknown"
	}
}

// IsHostile reports whether the kind is one of the four attack vectors. Every
// non-hostile frame is legitimate traffic and must pass.
func (k AttackKind) IsHostile() bool {
	return k != KindLegitimate
}

// attackKindNames orders the scoreboard columns.
var attackKindNames = [numKinds]string{
	"legit", "pkt-drop", "clock-drift", "malformed", "forgery",
}

// Attack weights control how often each vector is exercised. The ratios matter
// more than the absolute values: a real attacker probes broadly rather than
// hammering one technique, and a defence tuned on a uniform mix is more robust
// than one tuned on a single vector.
type attackProfile struct {
	drop    int // relative weight
	drift   int
	malform int
	forge   int
}

// DefaultAttackProfile returns a balanced mix. Legitimate traffic is generated
// separately and at a higher absolute rate, because a simulation with no honest
// baseline cannot measure a false-positive rate.
func DefaultAttackProfile() attackProfile {
	return attackProfile{drop: 30, drift: 20, malform: 30, forge: 20}
}

// RefusableByFrame reports whether refusing an individual frame is the correct
// outcome for this vector.
//
// Two of the four vectors are not frame attacks at all, and scoring them as
// misses conflates unrelated things:
//
//   - KindPacketDrop is a flood. The attacker's own frame is well-formed and
//     correctly signed, because a flood is not a bad frame — it is a valid frame
//     sent in volume. Refusing it would be wrong. What must be caught is the
//     damage, which the transport's loss modelling and the sequence-gap
//     detector handle.
//   - KindClockDrift is only an attack when the claimed skew falls outside the
//     accepted key window. The attacker deliberately aims inside the window as
//     often as outside, and those frames must be served: the window exists so
//     ordinary clock drift is tolerated. Counting them as missed attacks would
//     punish the defence for having a tolerance at all.
//
// What remains — a structural overrun and a broken signature — are the cases
// where a single frame is self-evidently hostile and refusing it is exactly
// right. Those are the vectors a per-frame recall figure can honestly measure.
func (k AttackKind) RefusableByFrame() bool {
	return k == KindMalformed || k == KindForgery
}

// DriftIsAttack reports whether a drift frame's skew falls outside the window
// and must therefore be refused.
//
// A drift frame inside the window is legitimate traffic that happens to carry a
// skew value, so it is not counted against recall.
func DriftIsAttack(skewMs float64) bool {
	return math.Abs(skewMs) > skewWindowMs
}

// pickKind chooses an attack vector by weight.
func (p attackProfile) pickKind(rnd *Rand) AttackKind {
	total := p.drop + p.drift + p.malform + p.forge
	if total <= 0 {
		return KindPacketDrop
	}
	r := rnd.Intn(total)
	switch {
	case r < p.drop:
		return KindPacketDrop
	case r < p.drop+p.drift:
		return KindClockDrift
	case r < p.drop+p.drift+p.malform:
		return KindMalformed
	default:
		return KindForgery
	}
}

// skewWindowMs is the tolerated clock skew, in milliseconds, when a receiver
// decides which hour a cell was sealed under. Frames outside this window are
// refused outright. It is a float64 so it composes with the millisecond
// measurements it is compared against.
const skewWindowMs = 3.0 * 60 * 1000 // 3 minutes, per the namespace policy

// SkewWindowMs returns the accepted leaf-key skew window in milliseconds.
func SkewWindowMs() float64 { return skewWindowMs }

// driftTargets are the skew values the clock-drift attacker aims for. They
// bracket the acceptance boundary on purpose: a few land inside the window and
// must be tolerated, the rest overshoot and must be refused. An attacker that
// only ever overshoots would be trivially filtered and would not test the
// tolerance logic.
var driftTargets = []float64{
	-240e3, -200e3, -181e3, -179e3, -150e3, -30e3, -1e3, 1e3, 30e3,
	150e3, 179e3, 181e3, 200e3, 240e3, 600e3, 3.6e6,
}

// buildPayload produces a payload appropriate to the attack kind.
//
// The malformed builder intentionally emits a frame whose declared length
// exceeds the buffer, which is precisely the condition the production
// unpackCell guards against. Reproducing the real malformed shape, rather than
// random bytes, is what makes the defence test meaningful.
func buildPayload(kind AttackKind, rnd *Rand, n *Node, size int) []byte {
	if size <= 0 {
		size = 64
	}
	switch kind {
	case KindMalformed:
		// Overrun the declared length field: a receiver that trusts the
		// header before authenticating will over-read the cell.
		b := make([]byte, size)
		copy(b, []byte("LEGACY/0x1f-vuln-frame"))
		return b
	case KindForgery:
		// Structurally plausible, cryptographically wrong.
		b := make([]byte, size)
		copy(b, []byte("cell|auth=ed25519|sig="))
		for i := 17; i < len(b) && i < 17+16; i++ {
			b[i] = byte(rnd.Uint64())
		}
		return b
	default:
		b := make([]byte, size)
		copy(b, []byte(n.ID.String()))
		for i := 8; i < len(b); i += 7 {
			b[i] = byte(rnd.Uint64())
		}
		return b
	}
}
