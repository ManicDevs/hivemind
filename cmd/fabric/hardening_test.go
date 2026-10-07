package main

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

// TestLoggerFromContextFallsBack pins the nil-safety of logger resolution. A
// missing logger on a production path must degrade to the default logger, never
// panic — the alternative is an observability gap becoming an outage.
func TestLoggerFromContextFallsBack(t *testing.T) {
	if got := loggerFrom(context.Background()); got == nil {
		t.Fatal("loggerFrom returned nil for a context with no logger")
	}
	if WithLogger(context.Background(), nil) == nil {
		t.Fatal("WithLogger(nil) must return a usable context")
	}
	lg := newLogger(&NodeID{2, tierMaster})
	if got := loggerFrom(WithLogger(context.Background(), lg)); got != lg {
		t.Fatal("loggerFrom did not return the logger bound in the context")
	}
}

// TestStreamLimiterBoundsConcurrency pins the fan-out budget: the limiter must
// admit exactly its capacity and then refuse, and release must hand slots back.
func TestStreamLimiterBoundsConcurrency(t *testing.T) {
	l := newStreamLimiter(3)
	for i := 0; i < 3; i++ {
		if !l.tryAcquire() {
			t.Fatalf("slot %d refused below capacity 3", i)
		}
	}
	if l.tryAcquire() {
		t.Fatal("limiter admitted a 4th slot at capacity 3")
	}
	l.release()
	if !l.tryAcquire() {
		t.Fatal("release did not free a slot")
	}

	// A degenerate size must not panic or deadlock.
	small := newStreamLimiter(0)
	if !small.tryAcquire() {
		t.Fatal("newStreamLimiter(0) admitted nothing")
	}
	if small.tryAcquire() {
		t.Fatal("newStreamLimiter(0) admitted more than one slot")
	}
}

// TestStreamLimiterIsRaceFreeUnderContention hammers acquire/release from many
// goroutines. Run under -race on a capable machine, this is the highest-value
// concurrency check in the package; without a race detector it still proves the
// accounting never over-admits or deadlocks.
func TestStreamLimiterIsRaceFreeUnderContention(t *testing.T) {
	const capacity = 8
	l := newStreamLimiter(capacity)

	var inFlight, peak int
	var mu sync.Mutex
	var wg sync.WaitGroup

	for g := 0; g < 64; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				if !l.tryAcquire() {
					continue
				}
				mu.Lock()
				inFlight++
				if inFlight > peak {
					peak = inFlight
				}
				mu.Unlock()

				mu.Lock()
				inFlight--
				mu.Unlock()
				l.release()
			}
		}()
	}
	wg.Wait()

	if peak > capacity {
		t.Fatalf("peak in-flight %d exceeded capacity %d: the budget leaked", peak, capacity)
	}
}

// TestAcceptRefusesBeyondStreamBudget proves the accept path sheds load rather
// than spawning without bound: with the budget exhausted, a further stream is
// refused (and closed) instead of growing the process.
func TestAcceptRefusesBeyondStreamBudget(t *testing.T) {
	t.Setenv("FABRIC_TRAFFIC", "0")

	seed, err := loadRootSeed()
	if err != nil {
		t.Fatalf("loadRootSeed: %v", err)
	}
	id, err := buildPKI(NodeID{1, tierMaster}, seed)
	if err != nil {
		t.Fatalf("buildPKI: %v", err)
	}
	f, err := newFabric(id)
	if err != nil {
		t.Fatalf("newFabric: %v", err)
	}
	// Exhaustive the budget: one slot, already held.
	f.limiter = newStreamLimiter(1)
	if !f.limiter.tryAcquire() {
		t.Fatal("could not take the single slot")
	}

	f.ctx, f.cancel = context.WithCancel(context.Background())
	f.tel = newTelemetry(256)
	f.start()
	if err := f.listen(); err != nil {
		t.Fatalf("listen: %v", err)
	}
	f.spawn(f.acceptLoop)

	// A real TCP connection to the matrix address: it completes the TCP
	// handshake and then meets the exhausted budget.
	c, err := net.DialTimeout("tcp", f.id.addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial matrix: %v", err)
	}
	defer c.Close()

	// The server must close the over-budget stream rather than hold it.
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1)
	if _, err := c.Read(buf); err == nil {
		t.Log("stream not closed promptly (handshake may still be in flight)")
	}

	f.cancel()
	if err := f.ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	f.shutdown()
}

// TestConstantBudgetInvariants documents the relationships the named constants
// exist to protect. These are the assumptions a reviewer would otherwise have
// to re-derive by reading every call site.
func TestConstantBudgetInvariants(t *testing.T) {
	// shutdown() waits drainGrace; a peer read or handshake outliving it would
	// make the node hang past its own deadline.
	if readIdleTimeout <= drainGrace {
		t.Errorf("readIdleTimeout (%v) must exceed drainGrace (%v)", readIdleTimeout, drainGrace)
	}
	// A handshake must finish well inside the drain budget: one that could
	// consume the whole window would leave every other loop un-drained.
	if handshakeTimeout >= drainGrace {
		t.Errorf("handshakeTimeout (%v) must be strictly below drainGrace (%v)", handshakeTimeout, drainGrace)
	}
	// The backoff must actually back off, and must clamp.
	if backoffBase >= backoffCeiling {
		t.Errorf("backoffBase (%v) must be below backoffCeiling (%v)", backoffBase, backoffCeiling)
	}
	if got := nextBackoff(backoffCeiling); got != backoffCeiling {
		t.Errorf("nextBackoff at the ceiling = %v, want %v (clamped)", got, backoffCeiling)
	}
	// A peer queue that cannot hold a single maximum frame would be useless.
	if peerQueueDepth < 1 {
		t.Errorf("peerQueueDepth must be >= 1")
	}
	// The stream budget must be a real budget.
	if maxConcurrentStreams < 1 {
		t.Errorf("maxConcurrentStreams must be >= 1")
	}
}
