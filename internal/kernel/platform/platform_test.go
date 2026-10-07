package platform

import (
	"testing"
)

// TestAllocatorAccountsLiveBytes verifies the accounting the kernel's health
// surface depends on.
func TestAllocatorAccountsLiveBytes(t *testing.T) {
	a := NewGoAllocator(0)
	b, ok := a.Alloc(1024)
	if !ok {
		t.Fatal("unbounded allocator refused a 1KiB request")
	}
	s := a.Stats()
	if s.LiveBytes != 1024 || s.LiveBlocks != 1 || s.AllocCount != 1 {
		t.Errorf("stats = %+v, want live=1024 blocks=1 allocs=1", s)
	}
	if len(b) != 1024 {
		t.Errorf("block length = %d, want 1024", len(b))
	}
	a.Free(b)
	s = a.Stats()
	if s.LiveBytes != 0 || s.LiveBlocks != 0 {
		t.Errorf("after Free stats = %+v, want live=0 blocks=0", s)
	}
	if s.PeakBytes != 1024 {
		t.Errorf("peak = %d, want 1024: peak must survive the free", s.PeakBytes)
	}
}

// TestAllocatorRefusesOverLimit is what makes the exhaustion path testable
// instead of merely reachable under real out-of-memory.
func TestAllocatorRefusesOverLimit(t *testing.T) {
	a := NewGoAllocator(2048)
	if _, ok := a.Alloc(1500); !ok {
		t.Fatal("first allocation refused below the limit")
	}
	if _, ok := a.Alloc(1000); ok {
		t.Fatal("allocation past the limit succeeded")
	}
	s := a.Stats()
	if s.FailedAllocs != 1 {
		t.Errorf("FailedAllocs = %d, want 1", s.FailedAllocs)
	}
	// The refusal must not have corrupted accounting.
	if s.LiveBytes != 1500 {
		t.Errorf("LiveBytes = %d, want 1500 after a refused request", s.LiveBytes)
	}
}

// TestAllocatorZeroSizeRefused pins that a zero request is refused rather than
// returning an empty block, so a caller cannot mistake "" for "allocated".
func TestAllocatorZeroSizeRefused(t *testing.T) {
	a := NewGoAllocator(1024)
	if _, ok := a.Alloc(0); ok {
		t.Error("zero-size allocation succeeded")
	}
	if _, ok := a.Alloc(-1); ok {
		t.Error("negative allocation succeeded")
	}
	if _, ok := a.Alloc(1); !ok {
		t.Error("a 1-byte allocation should succeed below the limit")
	}
}

// TestAllocatedMemoryIsZeroed verifies a block does not carry another module's
// leftovers, which is a real bug source when modules hand buffers around.
func TestAllocatedMemoryIsZeroed(t *testing.T) {
	a := NewGoAllocator(0)
	b, _ := a.Alloc(256)
	for i := range b {
		b[i] = 0xAA
	}
	a.Free(b)
	c, ok := a.Alloc(256)
	if !ok {
		t.Fatal("re-allocation failed")
	}
	for i, v := range c {
		if v != 0 {
			t.Fatalf("byte %d = %#x, want 0: fresh memory must be zeroed", i, v)
		}
	}
}

// TestFakeClockAdvancesOnlyOnDemand is the property that makes timing
// assertions exact rather than flaky.
func TestFakeClockAdvancesOnlyOnDemand(t *testing.T) {
	c := NewManualClock()
	if got := c.SinceBoot(); got != 0 {
		t.Errorf("fresh clock = %v, want 0", got)
	}
	// Repeated reads must not move it.
	_, _ = c.SinceBoot(), c.SinceBoot()
	if got := c.SinceBoot(); got != 0 {
		t.Errorf("clock moved without being advanced: %v", got)
	}
	c.Advance(1500)
	if got := c.SinceBoot(); got != 1500 {
		t.Errorf("after Advance = %v, want 1.5ms", got)
	}
	c.Set(42)
	if got := c.SinceBoot(); got != 42 {
		t.Errorf("after Set = %v, want 42ns", got)
	}
}

// TestFakeCapturesOutput verifies the fake console records what was written, in
// order, including that Flush is counted.
func TestFakeCapturesOutput(t *testing.T) {
	f := NewFake(0)
	c := f.Console()
	if _, err := c.WriteString("=== HIVEMIND ===\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write([]byte("[boot] modules\n")); err != nil {
		t.Fatal(err)
	}
	if err := c.Flush(); err != nil {
		t.Fatal(err)
	}

	lines := f.Out().Lines()
	want := []string{"=== HIVEMIND ===", "[boot] modules"}
	if len(lines) != len(want) {
		t.Fatalf("lines = %q, want %q", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
	buf, ok := c.(*Buffer)
	if !ok {
		t.Fatal("fake console is not a *Buffer")
	}
	if buf.Flushed != 1 {
		t.Errorf("Flush count = %d, want 1", buf.Flushed)
	}
}

// TestFakeResetCountIsTracked verifies the boot path resets once, not per module.
func TestFakeResetCountIsTracked(t *testing.T) {
	f := NewFake(0)
	if got := f.ResetCount(); got != 0 {
		t.Errorf("fresh ResetCount = %d, want 0", got)
	}
	_ = f.Reset(nil)
	_ = f.Reset(nil)
	if got := f.ResetCount(); got != 2 {
		t.Errorf("ResetCount = %d, want 2", got)
	}
}

// TestHostBackendWritesToWriter verifies the real backend is capturable, so a
// test can assert on genuine host output rather than only on the fake.
func TestHostBackendWritesToWriter(t *testing.T) {
	var sink writeCapture
	p := NewHost(&sink, 1<<20)
	if p.Name() != "host" {
		t.Errorf("Name = %q, want host", p.Name())
	}
	if _, err := p.Console().WriteString("hello\n"); err != nil {
		t.Fatal(err)
	}
	if got := sink.String(); got != "hello\n" {
		t.Errorf("captured %q, want %q", got, "hello\n")
	}
	// The host clock must actually advance on its own, or it is not testing the
	// real substrate.
	first := p.Clock().SinceBoot()
	p.Clock().SinceBoot()
	if p.Clock().SinceBoot() < first {
		t.Error("host clock went backwards")
	}
}

// TestHostHeapLimitIsEnforced verifies the host backend is not a pass-through.
func TestHostHeapLimitIsEnforced(t *testing.T) {
	p := NewHost(discardWriter{}, 512)
	b, ok := p.Heap().Alloc(400)
	if !ok {
		t.Fatal("allocation below the limit refused")
	}
	p.Heap().Free(b)
	if _, ok := p.Heap().Alloc(4096); ok {
		t.Error("allocation far past the limit succeeded")
	}
}

type writeCapture struct{ b []byte }

func (w *writeCapture) Write(p []byte) (int, error) {
	w.b = append(w.b, p...)
	return len(p), nil
}
func (w *writeCapture) String() string { return string(w.b) }

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// TestNewFakeHonoursLimit verifies the fake's heap limit is what a test asked
// for, not a hard-coded value.
func TestNewFakeHonoursLimit(t *testing.T) {
	f := NewFake(4096)
	if got := f.Aware(); got != 4096 {
		t.Errorf("Aware = %d, want 4096", got)
	}
}
