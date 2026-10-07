package main

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"gitlab.torproject.org/cerberus-droid/hivemind/internal/consensus"
	"gitlab.torproject.org/cerberus-droid/hivemind/internal/dispatcher"
	"gitlab.torproject.org/cerberus-droid/hivemind/internal/gofer"
	"gitlab.torproject.org/cerberus-droid/hivemind/internal/hive"
	"gitlab.torproject.org/cerberus-droid/hivemind/internal/jit"
	"gitlab.torproject.org/cerberus-droid/hivemind/internal/migration"
	"gitlab.torproject.org/cerberus-droid/hivemind/internal/netmesh"
	"gitlab.torproject.org/cerberus-droid/hivemind/internal/persistence"
	"gitlab.torproject.org/cerberus-droid/hivemind/internal/platform"
)

// Clean seam interfaces for the apex hyperkernel. The concrete apex types
// satisfy them structurally, so hivemind orchestration never depends on a
// specific implementation.
type (
	// TelemetryBroadcaster seals and emits an encrypted mesh telemetry packet.
	TelemetryBroadcaster interface {
		BroadcastTelemetry(eventType string, data map[string]string) ([]byte, error)
	}
	// QuarantineProposer drives unanimous consensus votes across the sibling mesh.
	QuarantineProposer interface {
		ProposeQuarantineVote(targetNode string) bool
		IsQuarantined(nodeID string) bool
	}
	// JITCompiler synthesizes autonomous bytecode stubs for syscall dispatch.
	JITCompiler interface {
		CompileOrGetStub(sysno uintptr, entropy float64) (*jit.JITStub, error)
	}
	// MigrationPackager serializes and encrypts live kernel state for handoff.
	MigrationPackager interface {
		SerializeAndEncryptState(targetOS string, ip, sp uintptr, mem *persistence.KernelMemory) (string, error)
	}
	// Interceptor is the syscall dispatch fast-path used by the dispatcher.
	Interceptor interface {
		Intercept(sysno uintptr, args [6]uintptr) (uintptr, error)
	}
)

// apexDataDir returns the sandbox root for apex epigenetic state under
// hivemind's data directory, keeping the hyperkernel off system paths.
func apexDataDir() string {
	return filepath.Join(".", "data", "apex")
}

// bootstrapApexKernel replays the apex v8 Omni-Singularity bootstrap in the
// hivemind process. Every subsystem is pure-Go (zero CGO). Failures are
// logged, never fatal: a degraded hyperkernel must not take down the mesh.
func bootstrapApexKernel() Interceptor {
	lg := newApexLogger()

	lg.Info("bootstrapping apex hyperkernel",
		slog.String("version", "v8.0-Apex-Singularity"),
		slog.String("os", runtime.GOOS),
		slog.String("arch", runtime.GOARCH),
		slog.Bool("cgo", false))

	dataDir := apexDataDir()
	memFile := filepath.Join(dataDir, "singularity_apex_v8_memory.db")

	// 1. Epigenetic persistent memory.
	mem := persistence.NewKernelMemory(memFile)
	lg.Info("epigenetic memory loaded",
		slog.Int("profiled_nodes", len(mem.ProfiledSyscalls)),
		slog.String("path", memFile))

	// 2. Neural hive mesh & sibling registration.
	hiveNet := hive.NewHiveNetwork("apex-prime-core", mem)
	hiveNet.RegisterSibling("sibling-linux-node", "linux", map[string]string{
		"APEX_JIT_SYSTRAP_OPTIMIZE": "bypass_kernel_ring3_latency",
	})
	hiveNet.RegisterSibling("sibling-windows-node", "windows", map[string]string{
		"APEX_NT_OB_CALLBACK_SHIELD": "intercept_handle_duplication",
	})
	hiveNet.AdoptSiblingIntelligence()

	// 3. Advanced v8 subsystems: JIT, encrypted QUIC mesh, raft consensus.
	jitEngine := jit.NewAutonomousJITEngine()
	lg.Info("autonomous JIT compiler online")

	var meshTrans TelemetryBroadcaster = netmesh.NewMeshTransport("apex-prime-core")
	lg.Info("encrypted mesh transport online", slog.String("protocol", "QUIC/UDP"), slog.String("cipher", "AES-256-GCM"))

	var ledger QuarantineProposer = consensus.NewConsensusLedger("apex-prime-core", []string{"sibling-linux-node", "sibling-windows-node"})
	lg.Info("consensus ledger online", slog.String("self", "apex-prime-core"), slog.Int("peers", 2))

	// 4. Platform driver & kernel implant.
	plat := platform.NewApexPlatform()
	ctx, err := plat.NewContext()
	if err != nil {
		lg.Error("platform driver allocation failed", slog.Any("error", err))
	} else {
		lg.Info("hyperdriver active", slog.String("driver", ctx.PlatformName()))
	}
	lg.Info("kernel implant selected",
		slog.String("implant", plat.KernelImplantType()),
		slog.Any("capabilities", plat.DetectCapabilities()))

	// 5. Dispatcher with the full v8 stack.
	disp := dispatcher.NewAdaptiveDispatcher(mem, hiveNet, jitEngine, meshTrans.(*netmesh.MeshTransport), ledger.(*consensus.ConsensusLedger))
	lg.Info("dispatcher online",
		slog.Bool("jit_stubbing", true),
		slog.Bool("mesh_telemetry", true),
		slog.Bool("consensus_quarantine", true))

	// 6. Secure gofer proxy (sandbox root under data/, never /var/lib).
	goferRoot := filepath.Join(dataDir, "root")
	secGofer := gofer.NewSecureGofer(goferRoot)
	lg.Info("secure gofer online",
		slog.String("root", goferRoot),
		slog.String("token_mac", "HMAC-SHA256"))

	// 7. Workload interception through the v8 pipeline.
	sampleSysno := uintptr(64) // write
	sampleArgs := [6]uintptr{1, 0x7fff5fbff800, 150, 0, 0, 0}
	lg.Debug("dispatching workload through the hyperkernel pipeline", slog.Int("syscall", int(sampleSysno)))
	if ret, err := disp.Intercept(sampleSysno, sampleArgs); err != nil {
		lg.Warn("interception blocked", slog.Any("error", err))
	} else {
		lg.Info("syscall executed via JIT stub",
			slog.Int("syscall", int(sampleSysno)),
			slog.Int("result", int(ret)))
	}

	// 8. Broadcast encrypted QUIC mesh telemetry.
	if telemetryPkg, err := meshTrans.BroadcastTelemetry("NODE_HEARTBEAT", map[string]string{
		"status": "IMPERVIOUS",
		"tier":   "GOD_MODE",
	}); err != nil {
		lg.Warn("telemetry broadcast failed", slog.Any("error", err))
	} else {
		lg.Info("encrypted mesh telemetry broadcast",
			slog.Int("ciphertext_bytes", len(telemetryPkg)))
	}

	// 9. Live migration serialization across foreign kernels.
	migEngine := migration.NewLiveMigrationEngine()
	if pkgPath, err := migEngine.SerializeAndEncryptState(runtime.GOOS, ctx.InstructionPointer(), ctx.Regs().SP, mem); err != nil {
		lg.Warn("live migration packaging failed", slog.Any("error", err))
	} else {
		lg.Info("encrypted live-migration payload written",
			slog.String("path", pkgPath),
			slog.String("target_os", runtime.GOOS))
	}

	// 10. Cross-platform capability token.
	if token, err := secGofer.IssueCapabilityToken("sys/apex/hyperkernel/v8", "OMNI_ADMIN"); err != nil {
		lg.Warn("capability token issuance failed", slog.Any("error", err))
	} else {
		// Never log the token itself, not even truncated: a logged prefix is a
		// logged credential. A SHA-256 fingerprint identifies it for operators
		// without revealing anything an attacker can replay.
		lg.Info("capability token issued",
			slog.String("scope", "sys/apex/hyperkernel/v8"),
			slog.String("permission", "OMNI_ADMIN"),
			slog.String("fingerprint_sha256", fingerprint(token)))
	}

	lg.Info("apex hyperkernel stabilized")
	return disp
}

// newApexLogger returns the apex subsystem's structured logger.
//
// Generation 2 mutation: the apex bootstrap printed with log.Printf/fmt.Printf,
// which is unsuppressible, unlevel, and unattributable. A single structured
// logger lets an operator filter the hyperkernel's whole lifecycle by field.
func newApexLogger() *slog.Logger {
	level := slog.LevelInfo
	if strings.EqualFold(strings.TrimSpace(os.Getenv("APEX_LOG_LEVEL")), "debug") {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

// fingerprint returns a short, non-reversible identifier for a secret, safe to
// log where the secret itself is not.
func fingerprint(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:8])
}
