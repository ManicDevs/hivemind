package fabricsim

import (
	"math"
	"sync"
	"time"
)

// DetectorConfig tunes the defence.
type DetectorConfig struct {
	// AnomalyZ is the number of standard deviations from the per-node EWMA
	// mean at which a node's behaviour is considered anomalous. 3.0 is the
	// conventional statistical threshold and corresponds to roughly a
	// 1-in-1000 false-positive rate per observation under a Gaussian model.
	AnomalyZ float64
	// DwellSamples is how many consecutive anomalous observations are required
	// before isolation. Requiring dwell is what separates a real attacker from
	// a single unlucky frame: honest traffic produces occasional excursions,
	// sustained hostile traffic does not.
	DwellSamples int
	// SealThreshold is the fraction of quarantined identities at which the
	// mesh transitions to fail-closed. A small number of quarantines is
	// normal policing; a large one means the compromise is broad enough that
	// the only safe assumption is that trust has been exhausted.
	SealThreshold float64
	// ReorderGrace is how long a sequence gap must persist before it is called
	// loss rather than reordering. It is the floor on loss-detection latency
	// for any sequence-number-based detector.
	ReorderGrace time.Duration
	// SealCooldown is how long the mesh stays sealed before it re-evaluates.
	SealCooldown time.Duration
	// PardonWindow is the sustained clean period an identity must serve
	// before it is released from quarantine.
	PardonWindow time.Duration
	// BaselineSamples is the number of observations required before a node is
	// considered baseline-established and eligible for anomaly scoring.
	BaselineSamples int
	// MinLegitFloor is the guaranteed proportion of honest traffic. The
	// generator will not fall below this, so a defender cannot pass the test
	// by simply dropping everything.
	MinLegitFloor float64
}

// DefaultDetectorConfig returns the profile used when none is supplied.
func DefaultDetectorConfig() DetectorConfig {
	return DetectorConfig{
		AnomalyZ:        3.0,
		DwellSamples:    3,
		SealThreshold:   0.15,
		SealCooldown:    5 * time.Second,
		PardonWindow:    10 * time.Second,
		BaselineSamples: 20,
		MinLegitFloor:   0.25,
		// Derived from the modelled path latency rather than chosen by hand.
		// A zero here would expire every gap on the next tick and make a frame
		// still in flight across a slow path look lost.
		ReorderGrace: DefaultReorderGrace(),
	}
}

// ewma is an exponentially weighted moving average with a variance estimate,
// used to detect behavioural drift. Both the mean and the variance are tracked
// because a detector that only watches the mean is blind to a change in
// variance, which is exactly what a flooding attack produces.
type ewma struct {
	mean  float64
	var_  float64
	n     uint64
	alpha float64
	// cleanRun counts consecutive benign observations since the last anomaly.
	cleanRun int
	// anomalyRun counts consecutive anomalous observations (the dwell counter).
	anomalyRun int
}

func newEWMA(alpha float64) ewma { return ewma{alpha: alpha} }

// observe folds one sample in and reports whether it is anomalous at sigma.
//
// The update is skipped when the sample is anomalous. Feeding a burst of
// attack samples into the baseline would let an attacker slowly walk the mean
// toward its own traffic and stop being detected, which is a textbook
// statistical evasion; excluding anomalies is what makes the baseline stable.
func (e *ewma) observe(x float64, sigma float64) (anomalous bool) {
	if e.n >= 8 {
		dev := x - e.mean
		sd := math.Sqrt(math.Max(e.var_, 0))
		if sd > 1e-9 {
			z := dev / sd
			if z < 0 {
				z = -z
			}
			if z > sigma {
				e.anomalyRun++
				e.cleanRun = 0
				return true
			}
		}
	} else {
		// Bootstrap the variance from the sample spread before the EWMA has
		// enough history for a meaningful standard deviation.
		if e.n > 0 {
			d := x - e.mean
			e.var_ = e.var_*(1-1.0/float64(e.n+1)) + d*d/float64(e.n+1)
		}
	}

	// Benign sample: advance the baseline.
	d := x - e.mean
	e.mean += e.alpha * d
	e.var_ = (1-e.alpha)*e.var_ + e.alpha*d*d
	if e.var_ < 0 {
		e.var_ = 0
	}
	e.n++
	e.anomalyRun = 0
	e.cleanRun++
	return false
}

// Detector holds all defence state.
//
// It is a single struct behind a mutex rather than per-node locks: the scoreboard
// needs a consistent cross-node snapshot (sealed flag, counts, and the breach
// list) to render one line, and a single lock makes that snapshot free instead
// of requiring a global pause. The engine is the only writer, so contention is
// one thread.
type Detector struct {
	cfg  DetectorConfig
	mu   sync.RWMutex
	top  *Topology
	rnd  *Rand
	now  time.Time
	base map[NodeID]*ewma

	// counters
	// clock supplies the current time for loss detection. It is a field so a
	// test can drive the detector's timing deterministically: a detector whose
	// timing is welded to time.Now cannot be tested at all without sleeping,
	// because a compressed run never lets the reorder window elapse.
	clock func() time.Time
	// gaps is the per-source loss-detection state.
	// rejectReason counts which rule refused a frame, so the scoreboard can
	// distinguish a frame stopped at the identity check from one stopped by
	// the structural or key-window logic.
	rejectReason map[string]uint64
	gaps         map[NodeID]*gapState
	gapsOpened   uint64
	gapsDetected uint64
	gapsClosed   uint64
	// cryptoFailures counts real openCell authentication failures reported by
	// the production fabric, so the scoreboard can compare synthetic attack
	// volume against what the node actually observed on the wire.
	cryptoFailures uint64
	offered        [numKinds]uint64
	accepted       [numKinds]uint64
	rejected       [numKinds]uint64
	quarantined    uint64
	pardoned       uint64
	seals          uint64
	breaches       uint64

	// sealed is the fail-closed state.
	sealed     bool
	sealedAt   time.Time
	sealReason string

	// keyAccepted[key skew bucket] counts acceptance decisions for the
	// ±window test, reported so the operator can see the drift vector's effect.
	skewAccepted [2]uint64
	// driftInWindow counts clock-drift frames whose claimed skew fell inside the
	// accepted key window and were therefore served. These are the price of
	// having a tolerance: the defender cannot distinguish a genuine 2-minute
	// clock error from an attack that happened to land inside the window. The
	// count is published so that trade-off is visible rather than hidden inside
	// an aggregate.
	driftInWindow uint64 // [0]=inside window, [1]=outside
}

// NewDetector builds a detector bound to a topology.
func NewDetector(cfg DetectorConfig, top *Topology, rnd *Rand) *Detector {
	// Fill in defaults per field. Replacing the whole config here would
	// silently discard every value a caller set, so passing a custom dwell or
	// seal threshold with a zero AnomalyZ would have quietly reverted to the
	// stock behaviour.
	def := DefaultDetectorConfig()
	if cfg.AnomalyZ <= 0 {
		cfg.AnomalyZ = def.AnomalyZ
	}
	if cfg.DwellSamples <= 0 {
		cfg.DwellSamples = def.DwellSamples
	}
	if cfg.SealThreshold <= 0 {
		cfg.SealThreshold = def.SealThreshold
	}
	if cfg.SealCooldown <= 0 {
		cfg.SealCooldown = def.SealCooldown
	}
	if cfg.PardonWindow <= 0 {
		cfg.PardonWindow = def.PardonWindow
	}
	if cfg.BaselineSamples <= 0 {
		cfg.BaselineSamples = def.BaselineSamples
	}
	if cfg.MinLegitFloor <= 0 {
		cfg.MinLegitFloor = def.MinLegitFloor
	}
	if cfg.ReorderGrace <= 0 {
		cfg.ReorderGrace = def.ReorderGrace
	}
	d := &Detector{
		cfg:          cfg,
		top:          top,
		rnd:          rnd,
		base:         make(map[NodeID]*ewma, top.Len()),
		clock:        time.Now,
		gaps:         make(map[NodeID]*gapState, top.Len()),
		rejectReason: make(map[string]uint64, 8),
	}
	// Seed a baseline for every node. alpha 0.05 gives a ~60-sample time
	// constant, long enough to ignore bursts and short enough to track a
	// genuine change in behaviour.
	for i := 0; i < top.Len(); i++ {
		d.base[top.At(i).ID] = ptrEWMA(newEWMA(0.05))
	}
	return d
}

func ptrEWMA(e ewma) *ewma { return &e }

// Advance moves the detector's clock, which drives pardon windows and seal
// cooldowns. It must be called once per tick before Inspect.
func (d *Detector) Advance(now time.Time) {
	d.mu.Lock()
	d.now = now
	if d.sealed && now.Sub(d.sealedAt) >= d.cfg.SealCooldown {
		// Re-evaluate: drop the seal if the breach ratio has recovered.
		def, atk := d.top.QuarantinedCount()
		total := def + atk
		if total == 0 || float64(total)/float64(top_len(d.top)) <= d.cfg.SealThreshold {
			d.sealed = false
			d.sealReason = ""
		}
	}
	d.mu.Unlock()
}

// top_len returns the matrix size as an int for ratio math.
func top_len(t *Topology) int { return t.Len() }

// Offer is the verdict for one frame presented to a defender.
type Offer struct {
	Accept bool
	// Reason explains a rejection, for the scoreboard and for tests.
	Reason string
	// Quarantined is true when this frame caused an identity to be isolated.
	Quarantined bool
}

// Inspect runs the full defence pipeline over one inbound frame and returns the
// verdict. The order of checks is the security-relevant part:
//
//  1. fail-closed seal refuses everything hostile when engaged
//  2. quarantine refuses a known-bad identity
//  3. structural validation catches malformed legacy frames before any
//     allocation proportional to the claimed length
//  4. key-window validation catches clock drift
//  5. signature verification catches forgery
//  6. statistical anomaly scoring catches volume/behaviour drift
//
// Nothing downstream of a failed check is reached, so a malformed frame can
// never cause a large allocation.
func (d *Detector) Inspect(src NodeID, p *Packet) Offer {
	d.mu.Lock()
	defer d.mu.Unlock()

	// kind is the simulator's own label for how this frame was built. It is
	// recorded so the scoreboard can report per-vector results, and it is the
	// only place it is touched.
	//
	// It is never consulted to decide a verdict. A detector that read it would
	// be grading its own homework: every rejection would be correct by
	// construction and the run would prove nothing about the defence. Each
	// check below decides from wire bytes only.
	kind := p.Kind
	d.offered[kind]++

	// 1. Fail-closed. While sealed, every frame is refused regardless of origin.
	//
	// This is the one place where refusing honest traffic is correct: a sealed
	// mesh has already concluded it cannot tell hostile frames from honest ones,
	// so serving anything would contradict that conclusion. An earlier version
	// consulted kind.IsHostile() here, which meant the seal only ever refused
	// frames it already knew were hostile and refused nothing an attacker had
	// disguised.
	if d.sealed {
		if !structurallyValid(p) || !verifySignature(p, d.top) {
			d.rejected[kind]++
			d.rejectReason["sealed"]++
			d.breaches++
			return Offer{Reason: "sealed"}
		}
	}

	// 2. Known-bad identity.
	if n := d.top.ByID(src); n != nil {
		if q, reason, _ := n.Quarantined(); q {
			d.rejected[kind]++
			d.rejectReason["quarantined"]++
			return Offer{Reason: "quarantined:" + reason}
		}
	}

	// 3. Structural validation.
	//
	// A malformed legacy frame claims a declared length far beyond the bytes it
	// actually carries, which is the condition the production unpackCell guards
	// against. That is a property of the frame on the wire, so it is detectable
	// without knowing the frame was hostile.
	if !structurallyValid(p) {
		d.rejected[kind]++
		d.rejectReason["malformed"]++
		d.penalise(src, "malformed", kind)
		return Offer{Reason: "malformed", Quarantined: d.maybeQuarantine(src, "malformed")}
	}

	// 4. Key window.
	//
	// A drift frame inside the window is served, because tolerating ordinary
	// clock drift is the point of the window. It is counted so the scoreboard
	// can report how much attack surface that tolerance leaves.
	if p.SkewMs != 0 && !DriftIsAttack(p.SkewMs) {
		d.driftInWindow++
	}
	if p.SkewMs != 0 {
		if math.Abs(p.SkewMs) > skewWindowMs {
			d.skewAccepted[1]++
			d.rejected[kind]++
			d.rejectReason["key-window"]++
			d.penalise(src, "key-window", kind)
			return Offer{Reason: "key-window", Quarantined: d.maybeQuarantine(src, "key-window")}
		}
		d.skewAccepted[0]++
	}

	// 5. Signature.
	//
	// Verified against the public key registered for the identity the frame
	// claims. The forgery vector signs with the attacker's own key and then
	// mutates a byte, so this fails on a genuine cryptographic mismatch.
	if !verifySignature(p, d.top) {
		d.rejected[kind]++
		d.rejectReason["bad-signature"]++
		d.penalise(src, "forgery", kind)
		return Offer{Reason: "bad-signature", Quarantined: d.maybeQuarantine(src, "forgery")}
	}

	// 6. Statistical. An accepted frame is by definition benign, so it clears
	//    any accumulated dwell against this identity.
	if e := d.base[src]; e != nil {
		e.anomalyRun = 0
		e.cleanRun++
		// Score the frame's rate: legitimate frames are small and routine, so
		// their size distribution is the baseline.
		anom := e.observe(float64(len(p.Payload)), d.cfg.AnomalyZ)
		if anom {
			d.rejected[kind]++
			d.rejectReason["anomaly"]++
			d.penalise(src, "anomaly", kind)
			quar := d.maybeQuarantine(src, "anomaly")
			if quar {
				return Offer{Reason: "anomaly", Quarantined: true}
			}
			return Offer{Reason: "anomaly"}
		}
	}

	d.accepted[kind]++
	return Offer{Accept: true}
}

// penalise advances the dwell counter for an identity without isolating it yet.
//
// Isolation requires sustained hostility, so this only ever increments. The
// counter is advanced on the structural, key-window and signature paths as well
// as the statistical one: an attacker that only ever sends malformed frames
// must still be caught, and a dwell counter fed exclusively by the anomaly
// detector would never see those frames.
func (d *Detector) penalise(id NodeID, reason string, kind AttackKind) {
	e := d.base[id]
	if e == nil {
		return
	}
	// kind is not consulted here. penalise is only ever reached from a check
	// that has already failed on wire bytes — a structural overrun, a key window
	// breach, a bad signature, or a statistical anomaly — so every frame that
	// arrives is a frame the defence has grounds to distrust, whatever the
	// simulator labelled it.
	//
	// An earlier version gated on !kind.IsHostile(), which meant an attacker
	// that sent only disguised frames was never penalised at all: the counter
	// advanced solely for frames the simulator had already labelled hostile.
	// That is the exact population the detector is supposed to be suspicious of.
	e.cleanRun = 0
	if e.anomalyRun < d.cfg.DwellSamples {
		e.anomalyRun++
	}
}

// maybeQuarantine isolates the identity once its dwell counter is satisfied,
// and escalates to a fail-closed seal if too many identities are caught.
func (d *Detector) maybeQuarantine(id NodeID, reason string) bool {
	e := d.base[id]
	if e == nil {
		return false
	}
	if e.cleanRun > 0 || e.anomalyRun < d.cfg.DwellSamples {
		return false
	}
	n := d.top.ByID(id)
	if n == nil {
		return false
	}
	if !n.Quarantine(reason, d.now.UnixNano()) {
		return false
	}
	d.quarantined++
	d.escalateLocked()
	return true
}

// escalateLocked engages the fail-closed seal when the quarantine ratio crosses
// the configured threshold. Caller must hold d.mu.
func (d *Detector) escalateLocked() {
	def, atk := d.top.QuarantinedCount()
	total := def + atk
	if total == 0 {
		return
	}
	if float64(total)/float64(d.top.Len()) > d.cfg.SealThreshold && !d.sealed {
		d.sealed = true
		d.sealedAt = d.now
		d.seals++
		d.sealReason = "quarantine ratio above threshold"
	}
}

// Seal engages the fail-closed protocol immediately, as an operator action or
// in response to a detected broad compromise.
func (d *Detector) Seal(reason string) {
	d.mu.Lock()
	if !d.sealed {
		d.sealed = true
		d.sealedAt = d.now
		d.seals++
		d.sealReason = reason
	}
	d.mu.Unlock()
}

// Unseal releases the fail-closed protocol once the cause is cleared.
func (d *Detector) Unseal() {
	d.mu.Lock()
	d.sealed = false
	d.sealReason = ""
	d.mu.Unlock()
}

// Pardons releases identities that have served a clean window. It is the only
// path out of quarantine other than a topology role swap, and it is deliberately
// slow: a fast pardon is a denial-of-service vector against honest nodes.
func (d *Detector) Pardons() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	released := 0
	for i := 0; i < d.top.Len(); i++ {
		n := d.top.At(i)
		q, _, at := n.Quarantined()
		if !q {
			continue
		}
		if d.now.Sub(time.Unix(0, at)) < d.cfg.PardonWindow {
			continue
		}
		if e := d.base[n.ID]; e != nil && e.cleanRun < d.cfg.DwellSamples*2 {
			continue
		}
		n.Pardon()
		if e := d.base[n.ID]; e != nil {
			e.anomalyRun = 0
		}
		d.pardoned++
		released++
	}
	return released
}

// structurallyValid rejects a frame whose declared length exceeds its actual
// size, which is the malformed legacy frame the production unpackCell refuses.
//
// The check must run before any allocation proportional to the claimed length;
// a receiver that trusts the header first turns a 4-byte frame into a
// multi-gigabyte allocation, which is the amplification this guards against.
func structurallyValid(p *Packet) bool {
	if len(p.Payload) == 0 {
		return false
	}
	// A genuine frame declares exactly what it carries, which is what the
	// production unpackCell enforces with `headerSize+n != len(buf)`. The
	// simulator's check is deliberately the same relation rather than a
	// looser one:
	//
	//   - DeclaredLen > len(Payload) is the legacy over-read, where a receiver
	//     that trusts the header allocates and reads past the buffer.
	//   - DeclaredLen < len(Payload) is equally wrong: the receiver would accept
	//     the frame and silently ignore the trailing bytes, so anything appended
	//     after the declared region is smuggled past every check that inspects
	//     the frame. Requiring exact equality closes both directions.
	return p.DeclaredLen == uint32(len(p.Payload))
}

// SetClock replaces the detector's time source.
//
// This exists for tests. A run that executes many ticks in no wall-clock time
// can never let the reorder window elapse, so loss detection would appear to
// do nothing; driving the clock explicitly makes the timing observable without
// sleeping.
func (d *Detector) SetClock(fn func() time.Time) {
	if fn == nil {
		return
	}
	d.mu.Lock()
	d.clock = fn
	d.mu.Unlock()
}

// Now returns the detector's current time.
func (d *Detector) Now() time.Time {
	d.mu.RLock()
	clock := d.clock
	d.mu.RUnlock()
	if clock == nil {
		return time.Now()
	}
	return clock()
}

// Sealed reports whether the fail-closed protocol is engaged.
func (d *Detector) Sealed() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.sealed
}

// ── loss detection ──────────────────────────────────────────────────────────
//
// A receiver never learns that a frame was dropped. It learns it when a later
// frame from the same source arrives carrying a higher sequence number than
// expected. Everything here is derived from frames that actually arrived; there
// is no back channel to the transport, and no ground truth about which packets
// were lost.

// gapState is the per-source loss-tracking state.
type gapState struct {
	expect  uint64 // next sequence number we expect from this source
	primed  bool   // false until the first frame establishes a baseline
	pending int    // number of sequence numbers believed missing
	// openedAt is when the gap was first observed. A gap is only declared to
	// be loss once it has survived the reorder grace window; before that, the
	// missing frames may simply be late, and a receiver cannot tell.
	openedAt time.Time
	// lowest is the oldest sequence number still believed missing, used to
	// recognise a frame that turns up reordered rather than lost.
	lowest uint64
}

// noteArrival feeds one delivered frame to the loss detector.
//
// It returns the number of frames inferred lost and the time since the first
// unresolved gap was opened. A zero latency with zero loss means no gap.
func (d *Detector) noteArrival(p *Packet, now time.Time) (lost int, latency time.Duration, first bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	g := d.gaps[p.From]
	if g == nil {
		g = &gapState{expect: p.Seq, primed: true}
		d.gaps[p.From] = g
		return 0, 0, false
	}
	if !g.primed {
		g.primed = true
		g.expect = p.Seq
		return 0, 0, false
	}

	switch {
	case p.Seq == g.expect:
		// In order. The gap, if any, stays open until it expires: a frame
		// arriving in order now does not prove the earlier ones are lost.
		g.expect = p.Seq + 1
		return 0, 0, false

	case p.Seq < g.expect:
		// Behind the expectation. If it falls inside a known gap it is a
		// reorder, not a loss, and the gap shrinks by one. A sequence below the
		// gap is a replay or a duplicate and is ignored.
		if g.pending > 0 && p.Seq >= g.lowest {
			g.pending--
			if g.pending == 0 {
				g.openedAt = time.Time{}
			}
			return 0, 0, false
		}
		return 0, 0, false

	default:
		// A forward jump: the skipped sequence numbers are unaccounted for.
		gap := int(p.Seq - g.expect)
		if g.pending == 0 {
			g.openedAt = now
			g.lowest = g.expect
			d.gapsOpened++
		}
		g.pending += gap
		g.expect = p.Seq + 1
		return gap, 0, true
	}
}

// expireGapsFrom ages a single source's gap against the true elapsed time.
//
// It is called per delivered frame rather than only on the tick boundary: a
// tick boundary is a coarse clock, and a frame that is merely still in flight
// across a slow path would be declared lost by the time the next tick came
// round, inflating inferred loss well beyond the loss that actually occurred.
//
// ExpireGaps declares any gap that has outlived the reorder grace window to be
// genuine loss, and returns how many frames that accounts for together with the
// time each went missing for.
//
// This is what makes the reported loss-detection latency meaningful. Without a
// grace window a gap appears to resolve instantly, because the very next
// in-order frame closes it; the grace window is the time a receiver must wait
// to be sure a missing frame is lost rather than merely delayed, and it is the
// floor on any loss detector built on sequence numbers.
func (d *Detector) expireGapFor(src NodeID, now time.Time) (detected int, latency time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.expireGapsFrom(src, now)
}

func (d *Detector) expireGapsFrom(src NodeID, now time.Time) (detected int, latency time.Duration) {
	g := d.gaps[src]
	if g == nil || g.pending <= 0 || g.openedAt.IsZero() {
		return 0, 0
	}
	elapsed := now.Sub(g.openedAt)
	if elapsed < d.cfg.ReorderGrace {
		return 0, 0
	}
	detected = g.pending
	latency = elapsed * time.Duration(g.pending)
	d.gapsDetected += uint64(detected)
	d.gapsClosed++
	g.pending = 0
	g.openedAt = time.Time{}
	return detected, latency
}

func (d *Detector) ExpireGaps(now time.Time) (detected int, latency time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for src := range d.gaps {
		n, l := d.expireGapsFrom(src, now)
		detected += n
		latency += l
	}
	return detected, latency
}
