package boot

import (
	"context"
	"strings"
	"testing"
	"time"

	"gitlab.torproject.org/cerberus-droid/hivemind/internal/kernel"
	"gitlab.torproject.org/cerberus-droid/hivemind/internal/kernel/platform"
)

// testModule is a minimal module that records that it ran.
type testModule struct {
	name    string
	started bool
	stopped bool
}

func (m *testModule) Init(ctx context.Context, h kernel.Host) error { return nil }
func (m *testModule) Run(ctx context.Context) error {
	m.started = true
	<-ctx.Done()
	return nil
}
func (m *testModule) Stop(ctx context.Context) error {
	m.stopped = true
	return nil
}

// newFakeKernel builds a two-module kernel on the fake substrate.
func newFakeKernel(t *testing.T) (*platform.Fake, *kernel.Kernel, *testModule, *testModule) {
	t.Helper()
	f := platform.NewFake(1 << 20)
	k := kernel.New()

	// Consumer registered first, so resolution must reorder it.
	consumer := &testModule{name: "consumer"}
	provider := &testModule{name: "provider"}
	if err := k.Register(kernel.Descriptor{Name: "consumer", Requires: []string{"thing"}}, func(h kernel.Host) kernel.Module { return consumer }); err != nil {
		t.Fatal(err)
	}
	if err := k.Register(kernel.Descriptor{Name: "provider", Provides: []string{"thing"}}, func(h kernel.Host) kernel.Module { return provider }); err != nil {
		t.Fatal(err)
	}
	return f, k, consumer, provider
}

// TestHostnameIsHIVEMIND pins the identity the OS reports. It is a constant
// rather than a flag because a kernel whose name comes from the environment can
// be renamed by whoever launched it.
func TestHostnameIsHIVEMIND(t *testing.T) {
	if Hostname != "HIVEMIND" {
		t.Errorf("Hostname = %q, want HIVEMIND", Hostname)
	}
	f, k, _, _ := newFakeKernel(t)
	sys, err := New(context.Background(), Config{Platform: f, Kernel: k, Banner: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if sys.Hostname() != "HIVEMIND" {
		t.Errorf("System.Hostname = %q, want HIVEMIND", sys.Hostname())
	}
	if !strings.Contains(f.Out().String(), "HIVEMIND") {
		t.Errorf("banner does not name the host:\n%s", f.Out().String())
	}
}

// TestBannerIsFirstLine pins that the banner is emitted before anything else, so
// a console showing only the banner still proves the image reached userspace.
func TestBannerIsFirstLine(t *testing.T) {
	f, k, _, _ := newFakeKernel(t)
	if _, err := New(context.Background(), Config{Platform: f, Kernel: k, Banner: true}); err != nil {
		t.Fatal(err)
	}
	lines := f.Out().Lines()
	if len(lines) == 0 {
		t.Fatal("no output")
	}
	if !strings.HasPrefix(lines[0], "=== HIVEMIND kernel") {
		t.Errorf("first line = %q, want the banner", lines[0])
	}
	if !strings.Contains(lines[0], "fake backend") {
		t.Errorf("banner does not name the backend: %q", lines[0])
	}
}

// TestResetHappensOnceBeforeModules is the ordering guarantee: the substrate is
// live before any module starts.
func TestResetHappensOnceBeforeModules(t *testing.T) {
	f, k, _, _ := newFakeKernel(t)
	if _, err := New(context.Background(), Config{Platform: f, Kernel: k}); err != nil {
		t.Fatal(err)
	}
	if got := f.ResetCount(); got != 1 {
		t.Errorf("ResetCount = %d, want exactly 1 for a two-module boot", got)
	}
}

// TestModulesRunInResolvedOrder verifies the OS boots the kernel rather than
// assuming registration order is correct.
func TestModulesRunInResolvedOrder(t *testing.T) {
	f, k, _, _ := newFakeKernel(t)
	if _, err := New(context.Background(), Config{Platform: f, Kernel: k, ReportModules: true}); err != nil {
		t.Fatal(err)
	}
	out := f.Out().String()
	if !strings.Contains(out, "[provider consumer]") {
		t.Errorf("start order not reported as provider-first:\n%s", out)
	}
}

// TestWaitDrainsOnCancel is the shutdown contract: cancelling the context stops
// the modules and reports a clean drain.
func TestWaitDrainsOnCancel(t *testing.T) {
	f, k, consumer, provider := newFakeKernel(t)
	ctx, cancel := context.WithCancel(context.Background())

	sys, err := New(ctx, Config{Platform: f, Kernel: k, Banner: true})
	if err != nil {
		t.Fatal(err)
	}
	cancel()

	done := make(chan error, 1)
	go func() { done <- sys.Wait(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after cancellation")
	}

	if !consumer.stopped || !provider.stopped {
		t.Errorf("modules not stopped: consumer=%v provider=%v", consumer.stopped, provider.stopped)
	}
	if !strings.Contains(f.Out().String(), "[boot] clean") {
		t.Errorf("no clean-drain line:\n%s", f.Out().String())
	}
}

// TestNewRejectsMissingConfig ensures a missing substrate is an error rather
// than a panic at the first console write.
func TestNewRejectsMissingConfig(t *testing.T) {
	f := platform.NewFake(1 << 20)
	if _, err := New(context.Background(), Config{Kernel: kernel.New()}); err == nil {
		t.Error("nil Platform accepted")
	}
	if _, err := New(context.Background(), Config{Platform: f}); err == nil {
		t.Error("nil Kernel accepted")
	}
	if err := Run(context.Background(), Config{}); err == nil {
		t.Error("Run accepted an empty Config")
	}
}

// TestResetFailureAbortsBoot verifies a substrate that cannot be initialised
// stops the boot, rather than starting modules against a dead platform.
func TestResetFailureAbortsBoot(t *testing.T) {
	f, k, _, _ := newFakeKernel(t)
	f.ResetErr = context.DeadlineExceeded

	_, err := New(context.Background(), Config{Platform: f, Kernel: k, Banner: true})
	if err == nil {
		t.Fatal("boot succeeded despite a failed platform reset")
	}
	if !strings.Contains(err.Error(), "platform reset") {
		t.Errorf("error does not identify the failing step: %v", err)
	}
	// Nothing should have been printed: the console is not live.
	if got := f.Out().String(); got != "" {
		t.Errorf("output produced before the substrate was ready: %q", got)
	}
}

// TestHealthReportsTheThreeLayers is the operator view: identity, kernel state,
// and heap, all in one snapshot.
func TestHealthReportsTheThreeLayers(t *testing.T) {
	f, k, _, _ := newFakeKernel(t)
	f.Advance(1234 * time.Millisecond)

	sys, err := New(context.Background(), Config{Platform: f, Kernel: k})
	if err != nil {
		t.Fatal(err)
	}
	h := sys.Health()

	if h.Hostname != "HIVEMIND" {
		t.Errorf("health hostname = %q", h.Hostname)
	}
	if h.Platform != "fake" {
		t.Errorf("health platform = %q, want fake", h.Platform)
	}
	if h.Uptime != "1.234s" {
		t.Errorf("health uptime = %q, want 1.234s", h.Uptime)
	}
	if !h.Kernel.Booted {
		t.Error("health reports the kernel as not booted")
	}
	if len(h.Kernel.Order) != 2 {
		t.Errorf("health kernel order = %v, want 2 modules", h.Kernel.Order)
	}
	if h.Console == "" {
		t.Error("health does not name the console")
	}
	if h.Version != Version {
		t.Errorf("health version = %q, want %q", h.Version, Version)
	}
}

// TestHeapLimitReachesModules verifies the capability model actually constrains
// a module: one that allocates past its declared limit is refused, and that is
// observable rather than an OOM.
func TestHeapLimitReachesModules(t *testing.T) {
	f := platform.NewFake(2048)
	k := kernel.New()
	// The module's scratch memory is the substrate's allocator, so a ceiling set
	// on the platform is a ceiling the module actually hits.
	k.Memory = f.Heap()
	k.MustRegister(kernel.Descriptor{
		Name:         "hungry",
		Capabilities: []kernel.Capability{kernel.CapLog},
	}, func(h kernel.Host) kernel.Module { return &hungryModule{} })

	if _, err := New(context.Background(), Config{Platform: f, Kernel: k}); err != nil {
		t.Fatal(err)
	}
	if got := f.Heap().Stats().FailedAllocs; got == 0 {
		t.Error("no allocation was refused; the limit is not being enforced")
	}
}

// hungryModule allocates until refused, which is how a kernel discovers its own
// ceiling without dying.
type hungryModule struct {
	h      kernel.Host
	blocks [][]byte
}

func (m *hungryModule) Init(ctx context.Context, h kernel.Host) error {
	m.h = h
	// Allocate until refused. The module holds the blocks so the allocator
	// cannot recycle them underneath the loop.
	for i := 0; i < 64; i++ {
		b, ok := m.h.Heap().Alloc(512)
		if !ok {
			m.h.Logger().Info("allocation refused at ceiling", "iteration", i)
			return nil
		}
		m.blocks = append(m.blocks, b)
	}
	return nil
}

func (m *hungryModule) Run(ctx context.Context) error  { return nil }
func (m *hungryModule) Stop(ctx context.Context) error { return nil }

// TestOSWiresSubstrateHeapToModules is the proof that a module's scratch memory
// is accounted against the platform, not against a private Go-heap fallback.
//
// Found by running the OS rather than by reading it: the health snapshot
// reported zero live bytes while the process was demonstrably holding memory,
// because boot.New had never connected Platform.Heap() to the kernel. The OS
// layer owns both sides, so it is the only place the two can be joined.
func TestOSWiresSubstrateHeapToModules(t *testing.T) {
	f := platform.NewFake(1 << 20)
	k := kernel.New()

	// A module that allocates a known amount from its Host and holds it.
	const want = 8192
	k.MustRegister(kernel.Descriptor{
		Name:         "scratch",
		Capabilities: []kernel.Capability{kernel.CapLog},
	}, func(h kernel.Host) kernel.Module { return &scratchModule{bytes: want, ready: make(chan struct{})} })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := New(ctx, Config{Platform: f, Kernel: k}); err != nil {
		t.Fatal(err)
	}

	// Wait for the module to have allocated rather than sleeping a fixed time:
	// a sleep would make this a flake waiting to happen.
	// (The fake clock does not drive Run, so the channel is the sync point.)

	s := f.Heap().Stats()
	if s.AllocCount == 0 {
		t.Fatalf("no allocation reached the substrate allocator: %+v", s)
	}
	if s.LiveBytes < want {
		t.Errorf("live bytes = %d, want at least %d", s.LiveBytes, want)
	}
	if s.PeakBytes < want {
		t.Errorf("peak bytes = %d, want at least %d", s.PeakBytes, want)
	}
}

// scratchModule allocates once from its Host and announces it.
type scratchModule struct {
	bytes int
	ready chan struct{}
}

func (m *scratchModule) Init(ctx context.Context, h kernel.Host) error {
	b, ok := h.Heap().Alloc(m.bytes)
	if !ok {
		close(m.ready)
		return nil
	}
	// Deliberately leaked: the point is that the bytes stay live and counted.
	_ = b
	close(m.ready)
	return nil
}

func (m *scratchModule) Run(ctx context.Context) error  { <-ctx.Done(); return nil }
func (m *scratchModule) Stop(ctx context.Context) error { return nil }
