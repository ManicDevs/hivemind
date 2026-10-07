package main

import (
	"context"
	"log/slog"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Operational constants
//
// Generation 2 mutation: these were bare literals scattered across the node
// (3*time.Second, 5*time.Second, 30*time.Second, 64, 8192, 1<<20, math.Inf).
// Magic numbers are unreviewable — a reviewer cannot tell a dial timeout from
// a handshake timeout from a read deadline, so a change to one silently
// changes a guarantee the other was protecting. Every duration and buffer size
// the node's liveness depends on is named here, with the guarantee it carries.
//
// The liveness budget is the point: shutdown() must complete in drainGrace, so
// readIdleTimeout must exceed it or a peer read can outlive the drain.
// ─────────────────────────────────────────────────────────────────────────────

const (
	// dialTimeout bounds establishing the TCP stream to a candidate peer.
	dialTimeout = 3 * time.Second

	// handshakeTimeout bounds the mTLS handshake on an outbound peer. TLS 1.3
	// on loopback completes in single-digit milliseconds; this is the ceiling
	// for a wedged or absent peer.
	//
	// It is deliberately well under drainGrace: a half-open handshake that
	// outlives the drain budget would spend the whole grace period on one peer
	// and then exit with every other loop still running. The ordering is
	// asserted by TestConstantBudgetInvariants so the relationship cannot rot.
	handshakeTimeout = 2 * time.Second

	// readIdleTimeout bounds a single blocking read. A timeout is NOT an error:
	// readLoop treats it as "no traffic yet" and continues multiplexing, which
	// is what keeps the periodic read-deadline set below cheap.
	readIdleTimeout = 30 * time.Second

	// drainGrace is how long shutdown waits for the supervisor to finish before
	// exiting with loops still running.
	drainGrace = 5 * time.Second

	// keyCheckInterval is how often the rotator looks for an hour boundary. It
	// is deliberately far smaller than the 1h rotation period: this bounds the
	// detection error, not the rotation.
	keyCheckInterval = time.Minute

	// metricsInterval is the FABRIC_METRICS dump cadence.
	metricsInterval = 10 * time.Second

	// trafficInterval is the self-injected DATA cadence. Rate is deliberately
	// modest — this keeps Loss/backprop exercised on real RTTs without flooding.
	trafficInterval = 2 * time.Second

	// backoffCeiling caps the accept-path retry delay, and backoffBase is its
	// first step. A refused accept backs off exponentially up to the ceiling so
	// a dead peer cannot cost the routing math its CPU.
	backoffBase    = 10 * time.Millisecond
	backoffCeiling = 2 * time.Second

	// loopbackHandshakeTimeout bounds the in-process mTLS proof in self-test.
	loopbackHandshakeTimeout = 3 * time.Second

	// loopbackDialTimeout bounds the client side of that same proof.
	loopbackDialTimeout = 2 * time.Second
)

const (
	// peerQueueDepth is the per-peer send queue. 64 sealed frames is enough
	// burst to absorb a traffic burst; past that, sendOn refuses rather than
	// growing without bound, because an unbounded queue under a slow peer is a
	// memory leak that looks like healthy throughput.
	peerQueueDepth = 64

	// readBufferSize is the per-peer read buffer. Frames are bounded by
	// maxFrame; this is sized for a typical small cell and never truncates a
	// frame because readLoop copies whatever arrived this cycle.
	readBufferSize = 8192

	// telemetryRingCapacity is the node's rolling RTT window. Bounded on
	// purpose: percentiles are over the recent window, not the whole lifetime.
	telemetryRingCapacity = 4096
)

// ─────────────────────────────────────────────────────────────────────────────
// Typed context key
//
// Generation 2 mutation: stdlib log prints an unstructured prefix, so a caller
// could not carry state into a line, and any package could read anyone else's
// key by using the same string. A private unexported key type makes the
// namespace collision-proof, and passing the logger in the context is what
// lets a peer's read loop attribute its own lines without a struct field that
// would be nil in every test that builds a bare peer.
// ─────────────────────────────────────────────────────────────────────────────

// ctxKey is unexported, so no other package can collide with these values.
type ctxKey int

const (
	// loggerKey carries the node's *slog.Logger through a call chain.
	loggerKey ctxKey = iota
)

// WithLogger returns a copy of ctx carrying lg.
func WithLogger(ctx context.Context, lg *slog.Logger) context.Context {
	if lg == nil {
		return ctx
	}
	return context.WithValue(ctx, loggerKey, lg)
}

// loggerFrom resolves the context's logger, falling back to the default.
// A missing logger must never be a nil-pointer panic on a production path.
func loggerFrom(ctx context.Context) *slog.Logger {
	if lg, ok := ctx.Value(loggerKey).(*slog.Logger); ok && lg != nil {
		return lg
	}
	return slog.Default()
}

// ─────────────────────────────────────────────────────────────────────────────
// Bounded concurrency
//
// Generation 2 mutation: every inbound connection spawned a goroutine
// immediately, so acceptLoop's fan-out was unbounded — a node that accepted
// faster than it could hand off memory grew without limit. A semaphore makes
// the fan-out an explicit budget: the accept path now refuses (and drops) work
// it has no room for, which is a visible, loggable event rather than a slow
// OOM.
//
// The token is never leaked: the buffer is sized to exceed the simultaneous
// stream ceiling, so a release never blocks, and it is released in a defer
// even on the error paths.
// ─────────────────────────────────────────────────────────────────────────────

// maxConcurrentStreams bounds simultaneously served mTLS streams per node.
// It is a capacity budget, not a queue: the 4th tier exists so the edge tier
// can serve peers even while controllers and masters are busy.
const maxConcurrentStreams = 256

// streamLimiter is a counting semaphore sized to never block in practice. If
// the ceiling is reached the limiter refuses the stream instead of queueing,
// which is the behaviour we want under load: shed fast, stay honest.
type streamLimiter struct {
	sem chan struct{}
}

func newStreamLimiter(n int) *streamLimiter {
	if n < 1 {
		n = 1
	}
	return &streamLimiter{sem: make(chan struct{}, n)}
}

// tryAcquire takes a slot without blocking, reporting whether one was free.
func (l *streamLimiter) tryAcquire() bool {
	select {
	case l.sem <- struct{}{}:
		return true
	default:
		return false
	}
}

// release returns a slot.
func (l *streamLimiter) release() { <-l.sem }
