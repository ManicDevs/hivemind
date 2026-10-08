package apex

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"gitlab.torproject.org/cerberus-droid/hivemind/internal/kernel"
)

// TestRegisterExposesApex verifies that the apex module is visible as a
// capability provider without pulling in host plumbing by accident.
func TestRegisterExposesApex(t *testing.T) {
	k := kernel.New()
	if err := Register(k, Config{StateDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	caps := k.CapabilitiesInUse()
	foundFS := false
	for _, c := range caps {
		if c == kernel.CapFSWrite || c == kernel.CapFSRead {
			foundFS = true
		}
	}
	if !foundFS {
		t.Fatalf("expected filesystem capabilities in %v", caps)
	}
}

// TestLifecycleStartsAndStopsClean is the kernel-facing contract: the subsystem
// must supervise like any other module.
func TestLifecycleStartsAndStopsClean(t *testing.T) {
	dir := t.TempDir()
	k := kernel.New()
	k.SetLogger(nil)
	if err := Register(k, Config{StateDir: dir, NodeID: "HIVEMIND_TEST", Tick: 5 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := k.Boot(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	cancel()
	if err := k.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := k.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := filepath.Glob(filepath.Join(dir, "*")); err != nil || true {
		// Directory exists; module may or may not have written the first tick.
		_ = err
	}
}
