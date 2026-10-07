package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingModule is a test module that records its lifecycle calls.
type recordingModule struct {
	name     string
	h        Host
	initErr  error
	runErr   error
	runBlock bool

	mu       sync.Mutex
	calls    []string
	stopDone chan struct{}
}

func (m *recordingModule) record(s string) {
	m.mu.Lock()
	m.calls = append(m.calls, s)
	m.mu.Unlock()
}

func (m *recordingModule) CallOrder() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.calls))
	copy(out, m.calls)
	return out
}

func (m *recordingModule) Init(ctx context.Context, h Host) error {
	m.h = h
	if m.initErr != nil {
		return m.initErr
	}
	m.record("init")
	return nil
}

func (m *recordingModule) Run(ctx context.Context) error {
	m.record("run")
	if m.runErr != nil {
		return m.runErr
	}
	if m.runBlock {
		<-ctx.Done()
		m.record("run-exit")
	}
	return nil
}

func (m *recordingModule) Stop(ctx context.Context) error {
	m.record("stop")
	if m.stopDone != nil {
		close(m.stopDone)
		m.stopDone = nil
	}
	return nil
}

// noopModule returns immediately from Run.
//
// It still honours runErr: the first version ignored it, which meant a test
// asserting that a failing Run propagates through Wait could never fail -- the
// fixture silently defeated the test rather than the test passing for a good
// reason.
type noopModule struct{ recordingModule }

func (m *noopModule) Run(ctx context.Context) error {
	m.record("run")
	return m.runErr
}

// eventLog records lifecycle events across modules, so ordering *between*
// modules is observable. Per-module call slices cannot show that: Run executes in
// a goroutine, so a module's own "stop" and "run" records can interleave
// arbitrarily and any assertion about their relative order is a race.
type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) add(s string) {
	l.mu.Lock()
	l.events = append(l.events, s)
	l.mu.Unlock()
}

func (l *eventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

// loggedModule records into a shared log, tagged with its own name.
type loggedModule struct {
	name  string
	log   *eventLog
	inner recordingModule
}

func (m *loggedModule) Init(ctx context.Context, h Host) error {
	m.inner.record("init")
	m.log.add(m.name + ":init")
	return m.inner.initErr
}

func (m *loggedModule) Run(ctx context.Context) error {
	m.inner.record("run")
	m.log.add(m.name + ":run")
	return m.inner.runErr
}

func (m *loggedModule) Stop(ctx context.Context) error {
	m.inner.record("stop")
	m.log.add(m.name + ":stop")
	return nil
}

// fsFixtureModule is a module that declares filesystem roots and tries to escape.
type fsFixtureModule struct {
	recordingModule
	fs hostFS
	// reads records (path, err) for every ReadFile attempt.
	readLog []readAttempt
}

type readAttempt struct {
	path string
	err  error
}

func newTestKernel(t *testing.T) *Kernel {
	t.Helper()
	k := New()
	k.SetLogger(slog.New(slog.NewTextHandler(discard{}, &slog.HandlerOptions{Level: slog.LevelError})))
	return k
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// memFS is an in-memory filesystem for Host tests.
type memFS struct {
	mu    sync.Mutex
	files map[string][]byte
	dirs  map[string][]string
	wrote []string
}

func newMemFS() *memFS {
	return &memFS{files: map[string][]byte{}, dirs: map[string][]string{}}
}

func (m *memFS) addFile(path, content string) *memFS {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[path] = []byte(content)
	parts := strings.Split(strings.Trim(path, "/"), "/")
	cur := ""
	for i, p := range parts {
		if i > 0 {
			cur += "/"
		}
		cur += p
		if i == len(parts)-1 {
			break
		}
		parent := cur
		child := parts[i+1]
		found := false
		for _, e := range m.dirs[parent] {
			if e == child {
				found = true
			}
		}
		if !found {
			m.dirs[parent] = append(m.dirs[parent], child)
		}
	}
	return m
}

func (m *memFS) ReadFile(p string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.files[p]
	if !ok {
		return nil, os.ErrNotExist
	}
	return b, nil
}

func (m *memFS) WriteFile(p string, data []byte, perm os.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[p] = data
	m.wrote = append(m.wrote, p)
	return nil
}

func (m *memFS) ReadDir(p string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.dirs[p]
	if !ok {
		return nil, os.ErrNotExist
	}
	return e, nil
}

func (m *memFS) Stat(p string) (os.FileInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.files[p]; ok {
		return fakeInfo{name: filepath.Base(p), size: 1}, nil
	}
	return nil, os.ErrNotExist
}

func (m *memFS) writes() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.wrote...)
}

type fakeInfo struct {
	name string
	size int64
}

func (f fakeInfo) Name() string       { return f.name }
func (f fakeInfo) Size() int64        { return f.size }
func (f fakeInfo) Mode() os.FileMode  { return 0o644 }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return false }
func (f fakeInfo) Sys() any           { return nil }

// TestResolveOrdersByDependency is the core guarantee: a provider starts before
// its consumer, regardless of registration order.
func TestResolveOrdersByDependency(t *testing.T) {
	k := newTestKernel(t)

	// Register the consumer FIRST, so a naive resolver that follows
	// registration order would get this backwards.
	consumer := &noopModule{recordingModule{name: "consumer"}}
	provider := &noopModule{recordingModule{name: "provider"}}

	if err := k.Register(Descriptor{Name: "consumer", Requires: []string{"storage"}}, func(h Host) Module { return consumer }); err != nil {
		t.Fatal(err)
	}
	if err := k.Register(Descriptor{Name: "provider", Provides: []string{"storage"}}, func(h Host) Module { return provider }); err != nil {
		t.Fatal(err)
	}

	order, err := k.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(order) != 2 {
		t.Fatalf("order = %v, want 2 modules", order)
	}
	if order[0] != "provider" {
		t.Errorf("order = %v, want provider first", order)
	}

	ctx := context.Background()
	if err := k.Boot(ctx); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	if got := provider.CallOrder(); got[0] != "init" {
		t.Errorf("provider init = %v", got)
	}
	if got := consumer.CallOrder(); got[0] != "init" {
		t.Errorf("consumer init = %v", got)
	}
	_ = k.Shutdown(ctx)
}

// TestResolveIsDeterministic pins that boot order does not vary between runs.
// A kernel that boots in a different order each time has unreproducible bugs.
func TestResolveIsDeterministic(t *testing.T) {
	build := func() []string {
		k := newTestKernel(t)
		for i := 0; i < 8; i++ {
			name := fmt.Sprintf("m%d", i)
			if err := k.Register(Descriptor{Name: name}, nil); err != nil {
				t.Fatal(err)
			}
		}
		order, err := k.Resolve()
		if err != nil {
			t.Fatal(err)
		}
		return order
	}
	first := strings.Join(build(), ",")
	for i := 0; i < 25; i++ {
		if got := strings.Join(build(), ","); got != first {
			t.Fatalf("order differed on run %d:\n%s\n%s", i, first, got)
		}
	}
}

// TestResolveDetectsCycle must name the members, so the declaration can be fixed.
func TestResolveDetectsCycle(t *testing.T) {
	k := newTestKernel(t)
	_ = k.Register(Descriptor{Name: "a", Requires: []string{"b"}}, nil)
	_ = k.Register(Descriptor{Name: "b", Requires: []string{"a"}}, nil)

	_, err := k.Resolve()
	if err == nil {
		t.Fatal("cycle not detected")
	}
	if !strings.Contains(err.Error(), "a") || !strings.Contains(err.Error(), "b") {
		t.Errorf("cycle error does not name the members: %v", err)
	}
}

// TestResolveRejectsUnsatisfiedRequirement verifies boot fails loudly at
// declaration rather than at first use.
func TestResolveRejectsUnsatisfiedRequirement(t *testing.T) {
	k := newTestKernel(t)
	_ = k.Register(Descriptor{Name: "consumer", Requires: []string{"nothing.provides.this"}}, nil)
	_, err := k.Resolve()
	if err == nil {
		t.Fatal("unsatisfied requirement accepted")
	}
	if !strings.Contains(err.Error(), "consumer") || !strings.Contains(err.Error(), "nothing.provides.this") {
		t.Errorf("error lacks context: %v", err)
	}
}

// TestResolveRejectsDuplicateProvider guards against a capability resolving to
// whichever module the map happened to yield.
func TestResolveRejectsDuplicateProvider(t *testing.T) {
	k := newTestKernel(t)
	_ = k.Register(Descriptor{Name: "a", Provides: []string{"shared"}}, nil)
	_ = k.Register(Descriptor{Name: "b", Provides: []string{"shared"}}, nil)
	if _, err := k.Resolve(); err == nil {
		t.Fatal("duplicate provider accepted; the winner would depend on map order")
	}
}

// TestRegisterRejectsDuplicateName verifies a duplicate is refused at
// registration, where the mistake is, not at boot.
func TestRegisterRejectsDuplicateName(t *testing.T) {
	k := newTestKernel(t)
	d := Descriptor{Name: "x"}
	if err := k.Register(d, nil); err != nil {
		t.Fatal(err)
	}
	if err := k.Register(d, nil); err == nil {
		t.Fatal("duplicate module name accepted")
	}
}

// TestRegisterRejectsUnknownCapability catches a typo in a declaration.
func TestRegisterRejectsUnknownCapability(t *testing.T) {
	k := newTestKernel(t)
	err := k.Register(Descriptor{Name: "x", Capabilities: []Capability{"capability.that.does.not.exist"}}, nil)
	if err == nil {
		t.Fatal("unknown capability accepted")
	}
	if !strings.Contains(err.Error(), "x") {
		t.Errorf("error does not name the module: %v", err)
	}
}

// TestRegisterRejectsUnboundedFSGrant is the important one: a filesystem
// capability with no roots would be an unbounded grant, which defeats the point
// of declaring.
func TestRegisterRejectsUnboundedFSGrant(t *testing.T) {
	k := newTestKernel(t)
	if err := k.Register(Descriptor{Name: "x", Capabilities: []Capability{CapFSRead}}, nil); err == nil {
		t.Fatal("filesystem read with no roots accepted")
	}
	if err := k.Register(Descriptor{Name: "y", Capabilities: []Capability{CapFSRead}, Roots: []string{"/data"}}, nil); err != nil {
		t.Errorf("bounded grant rejected: %v", err)
	}
}

// TestRegisterAfterBootRejected verifies the kernel is single-shot for
// registration, which is what lets Register be lock-free during construction.
func TestRegisterAfterBootRejected(t *testing.T) {
	k := newTestKernel(t)
	_ = k.Register(Descriptor{Name: "a"}, func(h Host) Module { return &noopModule{} })
	if err := k.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := k.Register(Descriptor{Name: "b"}, nil); err == nil {
		t.Error("register after Boot accepted")
	}
	_ = k.Shutdown(context.Background())
}

// TestBootTwiceRejected pins the single-shot contract.
func TestBootTwiceRejected(t *testing.T) {
	k := newTestKernel(t)
	_ = k.Register(Descriptor{Name: "a"}, func(h Host) Module { return &noopModule{} })
	ctx := context.Background()
	if err := k.Boot(ctx); err != nil {
		t.Fatal(err)
	}
	if err := k.Boot(ctx); err == nil {
		t.Error("second Boot accepted")
	}
	_ = k.Shutdown(ctx)
}

// TestShutdownStopsInReverseOrder is the ordering guarantee on the way down: a
// module is stopped only after everything that might use it.
func TestShutdownStopsInReverseOrder(t *testing.T) {
	k := newTestKernel(t)
	lg := &eventLog{}
	consumer := &loggedModule{name: "consumer", log: lg}
	provider := &loggedModule{name: "provider", log: lg}

	_ = k.Register(Descriptor{Name: "consumer", Requires: []string{"storage"}}, func(h Host) Module { return consumer })
	_ = k.Register(Descriptor{Name: "provider", Provides: []string{"storage"}}, func(h Host) Module { return provider })

	ctx := context.Background()
	if err := k.Boot(ctx); err != nil {
		t.Fatal(err)
	}
	_ = k.Shutdown(ctx)

	events := lg.snapshot()

	// Init order: provider before consumer.
	pi, ci := indexOf(events, "provider:init"), indexOf(events, "consumer:init")
	if pi < 0 || ci < 0 {
		t.Fatalf("missing init events: %v", events)
	}
	if pi > ci {
		t.Errorf("provider init after consumer init: %v", events)
	}

	// Stop order: consumer before provider -- the exact reverse.
	cs, ps := indexOf(events, "consumer:stop"), indexOf(events, "provider:stop")
	if cs < 0 || ps < 0 {
		t.Fatalf("missing stop events: %v", events)
	}
	if cs > ps {
		t.Errorf("stop order is not the reverse of start order: %v", events)
	}
}

func indexOf(list []string, want string) int {
	for i, v := range list {
		if v == want {
			return i
		}
	}
	return -1
}

// TestFailedInitRollsBack verifies a partial boot leaves no module half-started.
func TestFailedInitRollsBack(t *testing.T) {
	k := newTestKernel(t)
	good := &noopModule{recordingModule{name: "good"}}
	bad := &noopModule{recordingModule: recordingModule{name: "bad", initErr: errors.New("boom")}}

	_ = k.Register(Descriptor{Name: "good"}, func(h Host) Module { return good })
	_ = k.Register(Descriptor{Name: "bad"}, func(h Host) Module { return bad })

	err := k.Boot(context.Background())
	if err == nil {
		t.Fatal("Boot succeeded despite a failing Init")
	}
	if !strings.Contains(err.Error(), "bad") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("error lacks module or cause: %v", err)
	}
	// The module that did initialise must have been stopped again.
	if got := good.CallOrder(); len(got) != 2 || got[1] != "stop" {
		t.Errorf("good module not rolled back: %v", got)
	}
}

// TestRunFailurePropagates verifies a failing Run surfaces through Wait.
func TestRunFailurePropagates(t *testing.T) {
	k := newTestKernel(t)
	boom := errors.New("module failed")
	_ = k.Register(Descriptor{Name: "bad"}, func(h Host) Module {
		return &noopModule{recordingModule{name: "bad", runErr: boom}}
	})
	ctx := context.Background()
	if err := k.Boot(ctx); err != nil {
		t.Fatal(err)
	}
	if err := k.Wait(); err == nil {
		t.Fatal("Wait returned nil despite a failing Run")
	}
	_ = k.Shutdown(ctx)
}

// TestWaitIsDrainBarrier: after Wait returns, no module is running.
func TestWaitIsDrainBarrier(t *testing.T) {
	k := newTestKernel(t)
	ctx, cancel := context.WithCancel(context.Background())
	var stopped bool
	// A real channel: sending on a nil one blocks forever, which reads as a
	// hung test rather than as the bug it is.
	m := &blockingModule{started: make(chan struct{}), onExit: func() { stopped = true }}
	_ = k.Register(Descriptor{Name: "m"}, func(h Host) Module { return m })

	if err := k.Boot(ctx); err != nil {
		t.Fatal(err)
	}
	// Wait for Run to actually be running before cancelling, so the assertion
	// below is about the drain barrier rather than about a race with startup.
	select {
	case <-m.started:
	case <-time.After(2 * time.Second):
		t.Fatal("Run never started")
	}
	cancel()
	if err := k.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !stopped {
		t.Error("Wait returned before Run exited")
	}
}

type blockingModule struct {
	started chan struct{}
	onExit  func()
}

func (m *blockingModule) Init(ctx context.Context, h Host) error { return nil }
func (m *blockingModule) Run(ctx context.Context) error {
	// Guarded because Run executes in a goroutine: a panic there aborts the
	// whole test binary with a stack that does not name the test. An unset
	// channel simply means no test wanted a start signal.
	if m.started != nil {
		close(m.started)
	}
	<-ctx.Done()
	if m.onExit != nil {
		m.onExit()
	}
	return nil
}
func (m *blockingModule) Stop(ctx context.Context) error { return nil }

// TestShutdownTimeoutIsReported verifies a wedged module cannot hold the process
// open indefinitely, and that the failure is visible rather than swallowed.
func TestShutdownTimeoutIsReported(t *testing.T) {
	k := newTestKernel(t)
	k.SetDrainTimeout(50 * time.Millisecond)
	_ = k.Register(Descriptor{Name: "wedged"}, func(h Host) Module { return &wedgedModule{} })

	ctx := context.Background()
	if err := k.Boot(ctx); err != nil {
		t.Fatal(err)
	}
	err := k.Shutdown(ctx)
	if err == nil {
		t.Fatal("Shutdown reported success despite a wedged module")
	}
	if !strings.Contains(err.Error(), "drain timed out") {
		t.Errorf("error does not explain the timeout: %v", err)
	}
}

// wedgedModule never returns from Run.
type wedgedModule struct{}

func (m *wedgedModule) Init(ctx context.Context, h Host) error { return nil }
func (m *wedgedModule) Run(ctx context.Context) error {
	select {} // never returns
}
func (m *wedgedModule) Stop(ctx context.Context) error { return nil }

// TestShutdownIdempotent pins that a second Shutdown is a no-op, so a signal
// handler and a deferred call do not fight.
func TestShutdownIdempotent(t *testing.T) {
	k := newTestKernel(t)
	_ = k.Register(Descriptor{Name: "a"}, func(h Host) Module { return &noopModule{} })
	ctx := context.Background()
	_ = k.Boot(ctx)
	if err := k.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := k.Shutdown(ctx); err != nil {
		t.Errorf("second Shutdown returned %v, want nil", err)
	}
}

// TestCapabilitiesInUseIsTheAuditView verifies the question the model exists to
// answer: what does this process touch the host for?
func TestCapabilitiesInUseIsTheAuditView(t *testing.T) {
	k := newTestKernel(t)
	_ = k.Register(Descriptor{Name: "reader", Capabilities: []Capability{CapFSRead}, Roots: []string{"/data"}}, nil)
	_ = k.Register(Descriptor{Name: "clock", Capabilities: []Capability{CapClock}}, nil)
	_ = k.Register(Descriptor{Name: "writer", Capabilities: []Capability{CapFSWrite}, Roots: []string{"/tmp"}}, nil)
	// A second reader of the same capability must not appear twice.
	_ = k.Register(Descriptor{Name: "reader2", Capabilities: []Capability{CapFSRead}, Roots: []string{"/etc"}}, nil)

	got := k.CapabilitiesInUse()
	if len(got) != 3 {
		t.Fatalf("CapabilitiesInUse = %v, want 3 deduplicated", got)
	}
	seen := make(map[Capability]bool)
	for _, c := range got {
		if seen[c] {
			t.Errorf("duplicate capability %q", c)
		}
		seen[c] = true
	}
	// Sorted, so the audit view is deterministic.
	for i := 1; i < len(got); i++ {
		if got[i-1] > got[i] {
			t.Errorf("capabilities not sorted: %v", got)
			break
		}
	}
}

// TestHealthReportsBootFailure verifies the boot error is visible in Health, so
// a node's /healthz can report it.
func TestHealthReportsBootFailure(t *testing.T) {
	k := newTestKernel(t)
	_ = k.Register(Descriptor{Name: "a", Requires: []string{"missing"}}, nil)
	_ = k.Boot(context.Background())

	h := k.Health()
	if h.BootError == "" {
		t.Error("Health.BootError empty despite a failed Boot")
	}
	if h.Booted {
		t.Error("Health.Booted true despite a failed Boot")
	}
}
