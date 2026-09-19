package hivemind

// Guardrails: a token-bucket limiter and a circuit breaker, stdlib only.
// Small, generic, tested — wired where the mesh meets the world.

import (
	"sync"
	"time"
)

// Limiter is a token bucket: capacity burst, steady rate per second.
type Limiter struct {
	mu       sync.Mutex
	capacity float64
	tokens   float64
	rate     float64
	last     time.Time
}

// NewLimiter builds a bucket starting full.
func NewLimiter(ratePerSec, burst float64) *Limiter {
	return &Limiter{capacity: burst, tokens: burst, rate: ratePerSec, last: time.Now()}
}

// Allow reports whether one event may proceed now.
func (l *Limiter) Allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.tokens += now.Sub(l.last).Seconds() * l.rate
	if l.tokens > l.capacity {
		l.tokens = l.capacity
	}
	l.last = now
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}

// Breaker is a circuit breaker: closed (flow), open (failing fast),
// half-open (one probe). Opens after threshold consecutive failures,
// closes on probe success, re-opens on probe failure.
type Breaker struct {
	mu         sync.Mutex
	threshold  int
	timeout    time.Duration
	failures   int
	state      string // closed | open | half
	openedAt   time.Time
	successes  int
	totalCalls int
	totalTrips int
}

// NewBreaker builds a closed breaker: threshold consecutive failures
// trip it; timeout later one probe is allowed through.
func NewBreaker(threshold int, timeout time.Duration) *Breaker {
	if threshold < 1 {
		threshold = 1
	}
	return &Breaker{threshold: threshold, timeout: timeout, state: "closed"}
}

// Allow reports whether a call may proceed. Open breakers fail fast
// except one half-open probe per timeout.
func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case "closed":
		return true
	case "open":
		if time.Since(b.openedAt) >= b.timeout {
			b.state = "half"
			return true
		}
		return false
	default: // half: exactly one probe already let through per timeout
		return false
	}
}

// Success records a good call: closes half-open breakers, clears counts.
func (b *Breaker) Success() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.totalCalls++
	b.successes++
	b.failures = 0
	if b.state == "half" {
		b.state = "closed"
	}
}

// Failure records a bad call: trips at threshold.
func (b *Breaker) Failure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.totalCalls++
	b.failures++
	if b.state == "half" || b.failures >= b.threshold {
		if b.state != "open" {
			b.totalTrips++
		}
		b.state = "open"
		b.openedAt = time.Now()
	}
}

// State reports closed|open|half plus totals, for /metrics and tests.
func (b *Breaker) State() (string, int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state, b.successes, b.totalTrips
}
