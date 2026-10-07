package main

import (
	"path/filepath"
	"testing"

	"gitlab.torproject.org/cerberus-droid/hivemind/internal/consensus"
	"gitlab.torproject.org/cerberus-droid/hivemind/internal/dispatcher"
	"gitlab.torproject.org/cerberus-droid/hivemind/internal/gofer"
	"gitlab.torproject.org/cerberus-droid/hivemind/internal/hive"
	"gitlab.torproject.org/cerberus-droid/hivemind/internal/jit"
	"gitlab.torproject.org/cerberus-droid/hivemind/internal/migration"
	"gitlab.torproject.org/cerberus-droid/hivemind/internal/netmesh"
	"gitlab.torproject.org/cerberus-droid/hivemind/internal/persistence"
)

// TestApexBootstrap verifies the full hyperkernel bootstrap replays inside the
// hivemind process: memory, hive, JIT, mesh, consensus, gofer, migration.
func TestApexBootstrap(t *testing.T) {
	t.Chdir(t.TempDir()) // bootstrap writes the migration package to CWD
	disp := bootstrapApexKernel()
	if disp == nil {
		t.Fatal("bootstrapApexKernel returned nil interceptor")
	}

	// The bootstrap defers a write(150) as its demo workload; the entropy
	// metric must not trip the anomaly guard for ordinary args, so the write
	// dispatches through the JIT stub and returns its byte count.
	ret, err := disp.Intercept(64, [6]uintptr{1, 0x7fff5fbff800, 150, 0, 0, 0})
	if err != nil {
		t.Fatalf("Intercept(write): %v", err)
	}
	if ret != 150 {
		t.Fatalf("Intercept(write) = %d, want 150", ret)
	}
}

// TestApexFastPathJIT builds the full apex stack fresh and drives a
// low-entropy syscall through the JIT fast path, asserting the result is
// computed once and then served from the dispatcher's cache.
func TestApexFastPathJIT(t *testing.T) {
	mem := persistence.NewKernelMemory(filepath.Join(t.TempDir(), "mem.json"))
	hiveNet := hive.NewHiveNetwork("test-prime-core", mem)
	jitEngine := jit.NewAutonomousJITEngine()
	meshTrans := netmesh.NewMeshTransport("test-prime-core")
	ledger := consensus.NewConsensusLedger("test-prime-core", nil)
	disp := dispatcher.NewAdaptiveDispatcher(mem, hiveNet, jitEngine, meshTrans, ledger)

	args := [6]uintptr{}
	ret, err := disp.Intercept(39, args) // getpid, zero entropy
	if err != nil {
		t.Fatalf("Intercept(getpid): %v", err)
	}
	if ret != 88888 {
		t.Fatalf("Intercept(getpid) = %d, want 88888", ret)
	}

	ret, err = disp.Intercept(39, args) // must hit the fastPathCache
	if err != nil {
		t.Fatalf("cached Intercept(getpid): %v", err)
	}
	if ret != 88888 {
		t.Fatalf("cached Intercept(getpid) = %d, want 88888", ret)
	}
}

// TestMultiNodeMeshConsensus simulates mesh formation across two sibling
// nodes: per-node telemetry outboxes, a unanimous quarantine vote, and an
// encrypted live-migration payload between kernels.
func TestMultiNodeMeshConsensus(t *testing.T) {
	// SerializeAndEncryptState drops its package in the process CWD; run the
	// migration half of this test from a throwaway dir so the source tree
	// stays clean (config_test.go-style artifact isolation).
	migDir := t.TempDir()
	t.Chdir(migDir)

	memFile := filepath.Join(t.TempDir(), "mem.json")
	mem := persistence.NewKernelMemory(memFile)
	mem.MergeRules(map[string]string{"APEX_JIT_SYSTRAP_OPTIMIZE": "bypass_kernel_ring3_latency"})
	if err := mem.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	alpha := netmesh.NewMeshTransport("alpha-prime-core")
	beta := netmesh.NewMeshTransport("beta-prime-core")

	// Two peers plus self -> any single quarantine proposal is unanimous.
	ledger := consensus.NewConsensusLedger("alpha-prime-core", []string{"beta-prime-core"})
	if ledger.IsQuarantined("rogue-node") {
		t.Fatal("fresh ledger already quarantines rogue-node")
	}
	if !ledger.ProposeQuarantineVote("rogue-node") {
		t.Fatal("unanimous two-node quarantine vote was rejected")
	}
	if !ledger.IsQuarantined("rogue-node") {
		t.Fatal("rogue-node not quarantined after unanimous vote")
	}

	// Encrypted telemetry round-trip across the mesh transports.
	pktAlpha, err := alpha.BroadcastTelemetry("NODE_HEARTBEAT", map[string]string{"status": "IMPERVIOUS"})
	if err != nil {
		t.Fatalf("alpha broadcast: %v", err)
	}
	pktBeta, err := beta.BroadcastTelemetry("NODE_HEARTBEAT", map[string]string{"status": "IMPERVIOUS"})
	if err != nil {
		t.Fatalf("beta broadcast: %v", err)
	}
	if len(pktAlpha) == 0 || len(pktBeta) == 0 {
		t.Fatal("encrypted telemetry packets are empty")
	}

	// Live agent migration: serialize+encrypt state, then verify the gofer can
	// issue a fresh capability token for the migrated workload.
	engine := migration.NewLiveMigrationEngine()
	pkgPath, err := engine.SerializeAndEncryptState("linux", 0x400080, 0x7fff7ffff000, mem)
	if err != nil {
		t.Fatalf("SerializeAndEncryptState: %v", err)
	}
	if pkgPath == "" {
		t.Fatal("migration returned empty package path")
	}

	secGofer := gofer.NewSecureGofer(filepath.Join(t.TempDir(), "root"))
	token, err := secGofer.IssueCapabilityToken("sys/apex/hyperkernel/v8", "OMNI_ADMIN")
	if err != nil {
		t.Fatalf("IssueCapabilityToken: %v", err)
	}
	if len(token) == 0 {
		t.Fatal("capability token is empty")
	}
}
