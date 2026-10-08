// Package apex wires the inherited apex hyperkernel subsystems into the kernel
// module lifecycle.
package apex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gitlab.torproject.org/cerberus-droid/hivemind/internal/consensus"
	"gitlab.torproject.org/cerberus-droid/hivemind/internal/dispatcher"
	"gitlab.torproject.org/cerberus-droid/hivemind/internal/gofer"
	"gitlab.torproject.org/cerberus-droid/hivemind/internal/hive"
	"gitlab.torproject.org/cerberus-droid/hivemind/internal/jit"
	"gitlab.torproject.org/cerberus-droid/hivemind/internal/kernel"
	"gitlab.torproject.org/cerberus-droid/hivemind/internal/migration"
	"gitlab.torproject.org/cerberus-droid/hivemind/internal/netmesh"
	"gitlab.torproject.org/cerberus-droid/hivemind/internal/persistence"
)

// Config controls the apex ambient-mesh module.
type Config struct {
	// StateDir holds the persistent kernel memory file. Empty uses a
	// deterministic user-private location.
	StateDir string
	// NodeID is the identity reported to consensus and netmesh.
	NodeID string
	// Peers are the known ambient peers.
	Peers []string
	// Tick is the heartbeat interval. Zero uses a 5s interval.
	Tick time.Duration
}

// Module is a kernel.Module that owns the ambient apex substrate.
type Module struct {
	h   kernel.Host
	cfg Config

	memory  *persistence.KernelMemory
	hiveNet *hive.HiveNetwork
	mesh    *netmesh.MeshTransport
	ledger  *consensus.ConsensusLedger
	jit     *jit.AutonomousJITEngine
	disp    *dispatcher.AdaptiveDispatcher
	gofer   *gofer.SecureGofer
	mig     *migration.LiveMigrationEngine
}

// Name returns the module identity, useful outside a registration table.
func (m *Module) Name() string { return "apex" }

// New returns an Apex module with the supplied configuration.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

func (m *Module) Init(ctx context.Context, h kernel.Host) error {
	m.h = h
	stateDir := m.cfg.StateDir
	if stateDir == "" {
		stateDir = ".hivemind/apex"
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("apex: create state dir: %w", err)
	}
	m.memory = persistence.NewKernelMemory(filepath.Join(stateDir, "memory.json"))
	m.hiveNet = hive.NewHiveNetwork(m.nodeID(), m.memory)
	m.mesh = netmesh.NewMeshTransport(m.nodeID())
	m.ledger = consensus.NewConsensusLedger(m.nodeID(), m.cfg.Peers)
	m.jit = jit.NewAutonomousJITEngine()
	m.disp = dispatcher.NewAdaptiveDispatcher(m.memory, m.hiveNet, m.jit, m.mesh, m.ledger)
	m.gofer = gofer.NewSecureGofer(stateDir)
	m.mig = migration.NewLiveMigrationEngine()
	h.Logger().Info("apex ambient mesh online", "node_id", m.nodeID(), "peers", len(m.cfg.Peers), "state_dir", stateDir)
	return nil
}

func (m *Module) nodeID() string {
	if m.cfg.NodeID != "" {
		return m.cfg.NodeID
	}
	return "HIVEMIND"
}

func (m *Module) tickInterval() time.Duration {
	if m.cfg.Tick > 0 {
		return m.cfg.Tick
	}
	return 5 * time.Second
}

func (m *Module) Run(ctx context.Context) error {
	if m.disp == nil {
		return fmt.Errorf("apex: run before init")
	}
	interval := m.tickInterval()
	t := time.NewTicker(interval)
	defer t.Stop()
	seq := uintptr(0x40)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			seq++
			ret, err := m.disp.Intercept(seq, [6]uintptr{0, 0, 1, 1, 1, 1})
			if err != nil {
				m.h.Logger().Warn("apex interception blocked", "syscall", seq, "error", err)
				continue
			}
			m.h.Logger().Debug("apex dispatch", "syscall", seq, "result", ret, "rules", len(m.memory.GetJITRules()))
		}
	}
}

func (m *Module) Stop(ctx context.Context) error {
	if m.memory != nil {
		if err := m.memory.Save(); err != nil {
			return fmt.Errorf("apex: save kernel memory: %w", err)
		}
	}
	m.h.Logger().Info("apex ambient mesh offline")
	return nil
}

// Register wires the apex ambient substrate into the kernel.
//
// It must be registered after any module it may depend on; it publishes the
// "apex" capability for the boot report and demands write access only for its
// own state directory.
func Register(k *kernel.Kernel, cfg Config) error {
	stateDir := cfg.StateDir
	if stateDir == "" {
		stateDir = ".hivemind/apex"
	}
	return k.Register(kernel.Descriptor{
		Name:         "apex-mesh",
		Provides:     []string{"apex", "mesh-transport", "consensus"},
		Capabilities: []kernel.Capability{kernel.CapFSRead, kernel.CapFSWrite, kernel.CapProc, kernel.CapLog, kernel.CapClock},
		Roots:        []string{filepath.Clean(stateDir)},
	}, func(h kernel.Host) kernel.Module { return New(cfg) })
}
