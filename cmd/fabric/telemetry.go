package main

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// telemetry records what this node actually observed on the wire.
//
// It is deliberately part of the release build and contains no synthetic
// traffic whatsoever: every number here is a measurement taken from a real
// socket. The adversarial simulator is a development instrument, so nothing it
// produces can reach this code path, and a release deployment reports only
// facts about the mesh it is actually connected to.
//
// All methods are safe for concurrent use. Counters are atomic; the latency
// ring is guarded by mu.
type telemetry struct {
	// injected counts DATA cells this node put into the mesh; deliveredHere
	// counts DATA cells that arrived here as their declared destination.
	//
	// There is deliberately no ratio derived from these two. The protocol is
	// fire-and-forget: a DATA cell is delivered to its sink and nothing is sent
	// back to the origin. So a node that injects and is not itself a sink cannot
	// observe its own delivery, while a sink delivers cells injected by many
	// nodes. Dividing one count by the other produces a number that looks like a
	// loss rate but measures nothing — it read as 50% on a mesh where every
	// cross-continent cell had in fact arrived. Each count is real on its own
	// terms and is reported as such.
	//
	// A true end-to-end delivery rate would need an acknowledgement frame, which
	// the protocol does not have. Until it does, these are the honest figures.
	injected      atomic.Uint64 // DATA cells this node put into the mesh
	deliveredHere atomic.Uint64 // DATA cells that reached their destination here
	injectErrors  atomic.Uint64 // injections that failed to find a route
	authFailures  atomic.Uint64 // frames refused by openCell
	protoDrops    atomic.Uint64 // frames refused before authentication
	probesOK      atomic.Uint64 // successful liveness probes
	peerAdd       atomic.Uint64 // peers learned
	peerForget    atomic.Uint64 // peers dropped
	// rttSamples counts the real round trips behind the latency percentiles.
	// These come from live frame exchanges, so they describe link latency
	// rather than DATA end-to-end latency, and are labelled as such below.
	rttSamples atomic.Uint64

	mu       sync.RWMutex
	ring     []float64 // rolling RTT samples in milliseconds
	next     int
	ringFull bool
	maxRTT   float64
	minRTT   float64
	started  time.Time
}

// newTelemetry returns a telemetry collector with a bounded sample ring.
//
// The ring is bounded so that a node running for months cannot accumulate an
// unbounded history; a long-running node's percentiles are over the recent
// window, not over its entire lifetime.
//
// Note the deliberate asymmetry: the percentiles are windowed, but minRTT and
// maxRTT are lifetime extremes, because "the fastest this link has ever been"
// is worth knowing even once it has scrolled out of the window. The report
// labels each accordingly.
func newTelemetry(capacity int) *telemetry {
	if capacity < 64 {
		capacity = 64
	}
	return &telemetry{
		ring:    make([]float64, capacity),
		started: time.Now(),
		minRTT:  -1,
	}
}

// observeRTT records a real round trip measured by a peer.
//
// The sample is an actual timing of an actual frame exchange, which is why it
// is the only latency figure the release build publishes. It describes link
// latency from live control traffic, not DATA end-to-end latency, and the
// report says "link_rtt" so the distinction is not glossed over.
func (t *telemetry) observeRTT(rtt time.Duration) {
	ms := float64(rtt) / float64(time.Millisecond)
	if ms < 0 {
		return
	}
	t.rttSamples.Add(1)

	t.mu.Lock()
	t.ring[t.next] = ms
	t.next++
	if t.next >= len(t.ring) {
		t.next = 0
		t.ringFull = true
	}
	if ms > t.maxRTT {
		t.maxRTT = ms
	}
	if t.minRTT < 0 || ms < t.minRTT {
		t.minRTT = ms
	}
	t.mu.Unlock()
}

func (t *telemetry) recordInjected()    { t.injected.Add(1) }
func (t *telemetry) recordDelivered()   { t.deliveredHere.Add(1) }
func (t *telemetry) recordInjectError() { t.injectErrors.Add(1) }
func (t *telemetry) recordAuthFailure() { t.authFailures.Add(1) }
func (t *telemetry) recordProtoDrop()   { t.protoDrops.Add(1) }
func (t *telemetry) recordProbeOK()     { t.probesOK.Add(1) }
func (t *telemetry) recordPeerAdded()   { t.peerAdd.Add(1) }
func (t *telemetry) recordPeerForgot()  { t.peerForget.Add(1) }

// Uptime is how long this collector has been recording.
func (t *telemetry) Uptime() time.Duration { return time.Since(t.started) }

// percentiles returns the p50, p95, p99 and extremes of the recorded round
// trips, plus how many samples the figures are based on.
//
// A caller that has recorded nothing gets zeros and a sample count of zero, so
// it can distinguish "no data yet" from "the mesh is fast".
func (t *telemetry) percentiles() (p50, p95, p99, min, max float64, samples int) {
	t.mu.RLock()
	n := t.next
	if t.ringFull {
		n = len(t.ring)
	}
	out := make([]float64, n)
	copy(out, t.ring[:n])
	lo, hi := t.minRTT, t.maxRTT
	t.mu.RUnlock()

	if n == 0 {
		return 0, 0, 0, 0, 0, 0
	}
	sort.Float64s(out)
	at := func(q float64) float64 {
		idx := int(q * float64(n-1))
		return out[idx]
	}
	return at(0.50), at(0.95), at(0.99), lo, hi, n
}

// Report renders the observed figures as a single log line.
//
// Everything printed is a measurement from this node's real traffic. When
// nothing has been observed the line says so rather than showing zeros that
// could be mistaken for a fast, healthy link.
func (t *telemetry) Report() string {
	p50, p95, p99, lo, hi, n := t.percentiles()
	link := "no samples yet"
	if n > 0 {
		// The percentiles describe the retained window; the extremes are
		// lifetime running figures. Saying so here keeps a reader from
		// comparing a p99 that has scrolled on against a minimum from months
		// ago and concluding the link is erratic.
		link = fmt.Sprintf(
			"p50=%.2fms p95=%.2fms p99=%.2fms(window=%d) min=%.2fms max=%.2fms(lifetime)",
			p50, p95, p99, n, lo, hi)
	}
	// No delivery percentage is printed, and its absence is deliberate: see the
	// note on the injected/deliveredHere fields.
	return fmt.Sprintf(
		"[real] up=%s injected=%d delivered_here=%d "+
			"link_rtt %s(n=%d) inject_err=%d auth_fail=%d proto_drop=%d "+
			"probes=%d peers=+%d/-%d",
		t.Uptime().Round(time.Second),
		t.injected.Load(), t.deliveredHere.Load(),
		link, n,
		t.injectErrors.Load(), t.authFailures.Load(), t.protoDrops.Load(),
		t.probesOK.Load(), t.peerAdd.Load(), t.peerForget.Load())
}
