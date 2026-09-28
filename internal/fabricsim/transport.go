package fabricsim

import (
	"sync"
	"sync/atomic"
	"time"
)

// DropCause explains why a frame never arrived. A receiver cannot tell these
// apart directly, which is precisely why a drop is detected rather than
// measured.
type DropCause uint8

const (
	// DropNone means the frame was delivered.
	DropNone DropCause = iota
	// DropCongestion is the in-flight bound being exceeded: a real fabric
	// dropping traffic because a link is saturated.
	DropCongestion
	// DropFlood is a frame lost because an adversary is flooding the path.
	DropFlood
	// DropBaseline is ordinary link loss.
	DropBaseline
)

func (c DropCause) String() string {
	switch c {
	case DropCongestion:
		return "congestion"
	case DropFlood:
		return "flood"
	case DropBaseline:
		return "baseline"
	}
	return "none"
}

// lostFrame is the delivery intent of a frame that never arrived. A receiver
// keeps the last one so that when the next frame shows a sequence gap it can
// say exactly when the loss happened.
type lostFrame struct {
	seq   uint64
	wasAt time.Time
}

// linkKey identifies one directed path.
type linkKey struct {
	from NodeID
	to   NodeID
}

// Packet is one simulated frame in flight. Payloads are byte slices that were
// allocated by the simulator and are never written anywhere: the transport
// copies them at most once, into a preallocated arena, and drops them.
//
// The struct is passed by pointer through channels, so a Frame in flight is a
// single heap object rather than a copy per hop.
type Packet struct {
	From     NodeID
	To       NodeID
	Kind     AttackKind
	Payload  []byte
	SealedAt int64 // hour index the sender claims the cell was sealed under
	SkewMs   float64
	SentAt   time.Time
	Hops     int
	// Seq is the per-source monotonic sequence number. A receiver never sees a
	// dropped frame at all, so loss can only be inferred from a gap in this
	// numbering. That inference is the whole detection mechanism, which is why
	// it is modelled explicitly rather than by counting misses.
	Seq uint64
	// IntendedAt is when this packet was scheduled to arrive. When the packet
	// is dropped the receiver still needs it to measure how long the loss went
	// unnoticed, so it travels with the in-flight record.
	IntendedAt time.Time
	// DropCause explains why a packet was lost, for the scoreboard.
	DropCause DropCause
	// DeclaredLen is the payload length the frame's header claims. It is the
	// field the production unpackCell trusts, so it is modelled explicitly
	// rather than sniffed out of the payload: a malformed legacy frame claims
	// far more than it carries.
	DeclaredLen uint32
	// Dropped is set by the transport when it discards the packet, so the
	// senders can account for loss without racing on the transport.
	Dropped atomic.Bool
	// Sig is the sender's ed25519 signature over the frame's wire-visible
	// fields and payload. The defence verifies it against the public key
	// registered for From, so forgery is detected cryptographically instead of
	// by reading the Kind label.
	Sig []byte
}

// TransportConfig bounds the in-memory fabric.
type TransportConfig struct {
	// MaxInFlight caps simultaneously queued packets per link. Without a
	// bound a 28-node flood would allocate without limit and could take the
	// host down, which defeats the purpose of a bounded simulation.
	MaxInFlight int
	// ArenaBytes is the size of the preallocated payload arena. The
	// simulator recycles payload buffers from here so a long run performs
	// zero steady-state allocations and never grows the heap without limit.
	ArenaBytes int
	// LinkDropBase is the baseline per-hop packet loss, which a real fabric
	// always has and which keeps the defence statistics honest.
	LinkDropBase float64
	// JitterFraction is the proportional RTT jitter applied per hop.
	JitterFraction float64
	// MaxHops bounds propagation loops.
	MaxHops int
	// Seed drives the transport's own loss and jitter draws. It is separate
	// from the engine seed so that changing detection logic cannot silently
	// change the loss pattern, which would make runs incomparable.
	Seed uint64
}

// DefaultTransportConfig returns the profile used when the caller does not
// supply one. The numbers are chosen for the measured 32 GiB control node: the
// whole simulator, including every buffer, stays in the low tens of MiB.
func DefaultTransportConfig() TransportConfig {
	return TransportConfig{
		MaxInFlight:    256,
		ArenaBytes:     8 << 20, // 8 MiB
		LinkDropBase:   0.004,   // 0.4% baseline loss
		JitterFraction: 0.18,    // ±18% RTT jitter
		MaxHops:        12,
		Seed:           0x5eed,
	}
}

// Transport is the in-memory packet fabric.
//
// It holds no sockets, no listeners and no file handles. Delivery latency is
// not slept through: a packet's due time is computed arithmetically and the
// engine drains a priority queue, so 56 nodes at 300 ms inter-continental RTT
// cost nothing in wall-clock time. Sleeping would make the simulation
// physically paced and useless for testing.
type Transport struct {
	cfg TransportConfig

	// due is the ordered set of in-flight packets keyed by due time. A binary
	// heap is required here: a slice scan is O(n) per delivery and the engine
	// drains thousands of packets per tick.
	due packetHeap

	mu       sync.Mutex
	cond     *sync.Cond
	closed   bool
	inflight int

	// counters are read by the scoreboard, written only by the engine.
	enqueued  atomic.Uint64
	delivered atomic.Uint64
	dropped   atomic.Uint64
	// dropsByCause attributes every lost frame, so the scoreboard can show
	// that the flood vector is actually destroying traffic rather than merely
	// being labelled as such.
	dropsByCause [4]atomic.Uint64
	// lost holds the delivery-intent record of frames that never arrived, so
	// the receiver-side gap detector can measure how long the loss went
	// unnoticed. It is keyed by source and bounded by lostCap.
	lost    map[NodeID]*lostFrame
	lostCap int
	// flood is the current flood intensity per directed path, raised by
	// attacking nodes and decayed by the engine each tick.
	flood map[linkKey]float64
	// rng drives loss and jitter. Every draw happens under t.mu, so the
	// transport's loss pattern is identical for a given seed regardless of
	// how many goroutines are delivering.
	rng     *Rand
	expired atomic.Uint64

	// arena recycles payload buffers.
	arenaMu sync.Mutex
	arena   [][]byte
	arenaIn int
}

// NewTransport builds a transport with a preallocated payload arena.
func NewTransport(cfg TransportConfig) *Transport {
	if cfg.MaxInFlight <= 0 {
		cfg.MaxInFlight = DefaultTransportConfig().MaxInFlight
	}
	if cfg.ArenaBytes <= 0 {
		cfg.ArenaBytes = DefaultTransportConfig().ArenaBytes
	}
	if cfg.MaxHops <= 0 {
		cfg.MaxHops = DefaultTransportConfig().MaxHops
	}
	t := &Transport{cfg: cfg}
	t.lost = make(map[NodeID]*lostFrame, MatrixSize)
	t.lostCap = 64
	t.flood = make(map[linkKey]float64, MatrixSize)
	t.rng = NewRand(cfg.Seed)
	t.cond = sync.NewCond(&t.mu)
	// Preallocate the arena as fixed-size slots so recycling cannot fragment.
	const slot = 4096
	slots := cfg.ArenaBytes / slot
	if slots < 1 {
		slots = 1
	}
	t.arena = make([][]byte, 0, slots)
	for i := 0; i < slots; i++ {
		t.arena = append(t.arena, make([]byte, slot))
	}
	return t
}

// acquire returns a payload buffer of exactly n bytes, recycled where possible.
func (t *Transport) acquire(n int) []byte {
	if n > 4096 {
		// Oversized payloads are not worth caching in a fixed-slot arena.
		return make([]byte, n)
	}
	t.arenaMu.Lock()
	if t.arenaIn < len(t.arena) {
		buf := t.arena[t.arenaIn]
		t.arenaIn++
		t.arenaMu.Unlock()
		return buf[:n]
	}
	t.arenaMu.Unlock()
	return make([]byte, n)
}

// recordDrop accounts for a frame that will never arrive.
//
// It also stashes the delivery intent against the source, which is what lets
// the receiver-side gap detector answer "how long did I not notice?" without
// any cooperation from the sender.
func (t *Transport) recordDrop(p *Packet, cause DropCause, now time.Time) {
	t.dropped.Add(1)
	if int(cause) < len(t.dropsByCause) {
		t.dropsByCause[cause].Add(1)
	}
	p.Dropped.Store(true)
	p.DropCause = cause

	t.mu.Lock()
	if lf, ok := t.lost[p.From]; !ok || p.Seq > lf.seq {
		t.lost[p.From] = &lostFrame{seq: p.Seq, wasAt: now}
	}
	t.mu.Unlock()
}

// SetFlood raises or clears the loss rate an adversary is inflicting on one
// directed path. The engine calls this every tick with the current flood
// intensity, so a stopped attack decays back to the baseline loss rate instead
// of leaving the link permanently broken.
func (t *Transport) SetFlood(from, to NodeID, intensity float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	k := linkKey{from, to}
	if intensity <= 0 {
		delete(t.flood, k)
		return
	}
	if intensity > 0.95 {
		intensity = 0.95
	}
	t.flood[k] = intensity
}

// FloodIntensity reports the current loss rate an adversary is inflicting on a
// path, for the scoreboard.
func (t *Transport) FloodIntensity(from, to NodeID) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.flood[linkKey{from, to}]
}

// DropsByCause reports the loss counters per cause.
func (t *Transport) DropsByCause() (congestion, flood, baseline uint64) {
	return t.dropsByCause[int(DropCongestion)].Load(),
		t.dropsByCause[int(DropFlood)].Load(),
		t.dropsByCause[int(DropBaseline)].Load()
}

// LostFrames returns how many source identities currently have an unresolved
// loss record awaiting a sequence gap.
func (t *Transport) LostFrames() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.lost)
}

// Enqueue schedules a packet for delivery after the modelled propagation
// delay. It returns false if the transport is closed or the in-flight bound is
// hit, which the caller must treat as a congestion drop, not an error.
func (t *Transport) Enqueue(p *Packet, now time.Time) bool {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return false
	}
	if t.inflight >= t.cfg.MaxInFlight {
		t.mu.Unlock()
		t.recordDrop(p, DropCongestion, now)
		return false
	}

	// Propagation delay for this link. BaseRTTMs is a round trip, so one way
	// is half of it; the additional relays add serialisation latency.
	delayMs := BaseRTTMs(p.From.Zone, p.To.Zone) / 2
	if p.Hops > 0 {
		delayMs += float64(p.Hops) * switchHopMs
	}

	// Real loss. Every real link loses frames, and an adversary flooding the
	// path loses many more. This is modelled as a loss probability rather than
	// a post-hoc counter, because a receiver must infer loss from a sequence
	// gap; a frame that was never dropped could not be detected.
	lossP := t.cfg.LinkDropBase + t.flood[linkKey{p.From, p.To}]
	if lossP > 0 && t.rng.Float64() < lossP {
		t.mu.Unlock()
		t.recordDrop(p, DropFlood, now)
		return false
	}
	if t.cfg.JitterFraction > 0 {
		// ±jitter, and never negative.
		delayMs *= 1 + t.rng.NormalZ()*t.cfg.JitterFraction
		if delayMs < 0 {
			delayMs = 0
		}
	}
	due := now.Add(time.Duration(delayMs * float64(time.Millisecond)))
	p.SentAt = now
	p.IntendedAt = due

	t.inflight++
	t.due = append(t.due, item{pkt: p, at: due})
	heapPush(&t.due)
	t.enqueued.Add(1)
	t.mu.Unlock()
	t.cond.Broadcast()
	return true
}

// DrainDue removes and returns every packet whose due time is at or before now,
// ordered by due time. Delivery order is therefore deterministic and
// causally consistent: a packet always arrives after everything sent before it
// on the same link.
func (t *Transport) DrainDue(now time.Time) []*Packet {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []*Packet
	for t.due.Len() > 0 {
		it := t.due[0]
		if it.at.After(now) {
			break
		}
		heapPop(&t.due)
		t.inflight--
		out = append(out, it.pkt)
		t.delivered.Add(1)
	}
	return out
}

// NextDue returns the due time of the earliest queued packet.
func (t *Transport) NextDue() (time.Time, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.due.Len() == 0 {
		return time.Time{}, false
	}
	return t.due[0].at, true
}

// InFlight returns the number of queued packets.
func (t *Transport) InFlight() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.inflight
}

// Stats snapshots the transport counters.
func (t *Transport) Stats() (enq, del, drop, exp uint64) {
	return t.enqueued.Load(), t.delivered.Load(), t.dropped.Load(), t.expired.Load()
}

// DropCount returns the current drop total.
func (t *Transport) DropCount() uint64 { return t.dropped.Load() }

// Close stops the transport and drops everything still queued.
func (t *Transport) Close() {
	t.mu.Lock()
	t.closed = true
	t.due = t.due[:0]
	t.inflight = 0
	t.mu.Unlock()
	t.cond.Broadcast()
}

// Closed reports whether Close has been called.
func (t *Transport) Closed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closed
}

// ── minimal binary heap over due times ──────────────────────────────────────
//
// container/heap would work, but the payload is a single pointer and the hot
// path is short enough that a hand-rolled sift avoids the interface dispatch
// and the closure allocation per Push. Kept deliberately small and total.

type item struct {
	at  time.Time
	pkt *Packet
}

type packetHeap []item

func (h packetHeap) Len() int { return len(h) }

// heapPush sifts the element the caller already appended to the end.
//
// It deliberately does not append anything itself: appending a zero-value item
// here would place an entry with a nil packet and a zero due time, which sorts
// to the root and is then handed back to the caller as a nil *Packet.
func heapPush(h *packetHeap) {
	a := *h
	s := len(a) - 1
	for s > 0 {
		parent := (s - 1) / 2
		if !a[parent].at.After(a[s].at) {
			break
		}
		a[parent], a[s] = a[s], a[parent]
		s = parent
	}
}

func heapPop(h *packetHeap) {
	a := *h
	n := len(a) - 1
	a[0] = a[n]
	a = a[:n]
	*h = a
	s := 0
	for {
		l := 2*s + 1
		if l >= len(a) {
			break
		}
		r := l + 1
		small := l
		if r < len(a) && a[r].at.Before(a[l].at) {
			small = r
		}
		if !a[s].at.After(a[small].at) {
			break
		}
		a[s], a[small] = a[small], a[s]
		s = small
	}
}
