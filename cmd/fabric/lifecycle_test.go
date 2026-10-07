package main

import (
	"context"
	"runtime"
	"testing"
	"time"
)

// waitForGoroutines polls until the runtime settles at or below want, or the
// deadline expires. Comparing counts with a poll (rather than asserting
// immediately) tolerates the runtime's own transient goroutines — GC workers,
// timer goroutines — while still failing on a genuine leak, which never goes
// away on its own.
func waitForGoroutines(t *testing.T, want int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		got := runtime.NumGoroutine()
		if got <= want {
			return
		}
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			n := runtime.Stack(buf, true)
			t.Fatalf("goroutines leaked: got %d, want <= %d\n%s", got, want, buf[:n])
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestLifecycleDrainsWithoutLeaks is the fitness proof for Generation 1.
//
// The mutation replaced nine bare `go` statements with one errgroup
// supervisor. The failure mode that matters is not a wrong answer — it is a
// node that cannot finish shutting down, or a loop that outlives the process
// and holds a socket open. So this test starts a real node on the loopback
// matrix, makes it dial and accept real peers, then tears it down and proves
// the group actually drained and the goroutine count returned to baseline.
func TestLifecycleDrainsWithoutLeaks(t *testing.T) {
	// Keep the node quiet: the loop under test is the lifecycle, not traffic.
	t.Setenv("FABRIC_TRAFFIC", "0")
	t.Setenv("FABRIC_METRICS", "")

	settle := runtime.NumGoroutine()

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
	f.ctx, f.cancel = context.WithCancel(context.Background())
	f.tel = newTelemetry(256)
	f.start()

	if err := f.listen(); err != nil {
		t.Fatalf("listen: %v", err)
	}
	f.spawn(f.acceptLoop)
	f.spawn(f.keyRotator)
	f.spawn(f.trafficLoop)

	// A peer that is guaranteed to exist but is not listening: getOrCreatePeer
	// must record the failure and return without wedging the node.
	if _, err := f.getOrCreatePeer(f.ctx, NodeID{2, tierMaster}); err == nil {
		t.Fatal("expected dial failure against an unbound matrix address")
	}

	// Let the supervisor's loops actually start.
	time.Sleep(150 * time.Millisecond)

	f.shutdown()

	// shutdown drains the group with a bounded grace period. After it returns,
	// nothing the node started may still be running. The bound is exact, not
	// "close to baseline": a tolerance of even +2 lets a single leaked loop
	// pass, which is precisely the regression this test exists to catch.
	waitForGoroutines(t, settle, 5*time.Second)
}

// TestAcceptLoopExitsOnCancel pins the leak that motivated splitting listen
// into bind + acceptLoop: the accept path must return when its context is
// cancelled, not spin on a closed listener.
func TestAcceptLoopExitsOnCancel(t *testing.T) {
	seed, err := loadRootSeed()
	if err != nil {
		t.Fatalf("loadRootSeed: %v", err)
	}
	id, err := buildPKI(NodeID{1, tierCtrl}, seed)
	if err != nil {
		t.Fatalf("buildPKI: %v", err)
	}
	f, err := newFabric(id)
	if err != nil {
		t.Fatalf("newFabric: %v", err)
	}
	f.ctx, f.cancel = context.WithCancel(context.Background())
	f.tel = newTelemetry(256)
	f.start()
	if err := f.listen(); err != nil {
		t.Fatalf("listen: %v", err)
	}

	done := make(chan error, 1)
	f.spawn(func(ctx context.Context) error {
		err := f.acceptLoop(ctx)
		done <- err
		return err
	})

	time.Sleep(50 * time.Millisecond)
	_ = f.ln.Close() // closing the listener must wake Accept
	f.cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("acceptLoop returned %v, want clean nil exit on cancel", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("acceptLoop did not exit after listener close + cancel")
	}
}

// TestNextBackoffIsBounded pins the anti-hot-spin mutation on the accept path:
// the delay must grow and then clamp, never run away.
func TestNextBackoffIsBounded(t *testing.T) {
	d := nextBackoff(0)
	if d != 10*time.Millisecond {
		t.Fatalf("first backoff = %v, want 10ms", d)
	}
	prev := d
	for i := 0; i < 20; i++ {
		d = nextBackoff(d)
		if d < prev {
			t.Fatalf("backoff shrank: %v → %v", prev, d)
		}
		if d > 2*time.Second {
			t.Fatalf("backoff %v exceeded the 2s ceiling", d)
		}
		prev = d
	}
	if d != 2*time.Second {
		t.Fatalf("clamped backoff = %v, want 2s", d)
	}
}
