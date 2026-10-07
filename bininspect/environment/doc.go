// Package environment probes the machine the analysis runs on for virtualisation,
// sandboxing, and analysis-instrumentation footprints.
//
// It answers a different question from [github.com/hivemind/bininspect]. That
// package asks "does this binary contain anti-analysis capability?" by reading
// a file. This one asks "is this machine a VM, a sandbox, or under a debugger
// right now?" by inspecting the host. Both are legitimate triage signals; they
// are separate products, and this one is deliberately a separate module so
// importing it is an explicit decision rather than a transitive side effect.
//
// # Thread safety and determinism
//
// A Prober is safe for concurrent use. Its dependencies are injectable — the
// filesystem reader, the clock, and the CPUID and timestamp instructions — so
// the whole probe suite can be exercised in CI against a synthetic host with no
// reliance on the machine running the tests. That is the property that makes
// these tests deterministic rather than dependent on whether the build agent
// happens to be a VM.
//
// # Portability
//
// Most probes here are pure Go and run everywhere: DMI and ACPI tables, cgroup
// membership, the CPU hypervisor flag, network-interface MAC prefixes, and
// monotonic timing.
//
// The two genuinely x86-only probes — the raw CPUID instruction and the raw
// timestamp counter — live behind build tags with a portable fallback that
// reports ErrUnsupported on other architectures, so an ARM64 build compiles and
// runs, simply without those two readings.
//
// # Confidence, not proof
//
// A DMI string saying "VMware, Inc." is strong evidence. A CPUID hypervisor bit
// is strong evidence. Neither is proof: a machine can be a VM without
// advertising it, and a bare-metal host can carry a leftover vendor string from
// an image. Every Indicator carries a Confidence and its evidence, and the
// aggregate verdict is explicitly probabilistic.
package environment

import (
	"errors"
	"time"
)

// ErrUnsupported is returned by probes that have no implementation for the
// current architecture or platform.
//
// It is distinct from a negative result. "Unsupported" means the question could
// not be asked on this machine; it must never be folded into "checked and
// found nothing", because that would silently weaken a verdict on ARM64.
var ErrUnsupported = errors.New("environment: probe unsupported on this platform")

// Category groups an Indicator by what it detects.
type Category string

const (
	// CategoryVirtualisation covers hypervisors: VMware, VirtualBox, KVM/QEMU,
	// Hyper-V, Xen, Parallels.
	CategoryVirtualisation Category = "virtualisation"
	// CategorySandbox covers analysis sandboxes: Cuckoo, Joe, Sandboxie,
	// commercial instrumented sandboxes.
	CategorySandbox Category = "sandbox"
	// CategoryDebugger covers debugger attachment.
	CategoryDebugger Category = "debugger"
	// CategoryInstrumentation covers tracers and injected libraries.
	CategoryInstrumentation Category = "instrumentation"
	// CategoryTiming covers timing anomalies, which are weak signals on their own.
	CategoryTiming Category = "timing"
)

// Severity ranks a single Indicator.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityLow      Severity = "low"
	SeverityMedium   Severity = "medium"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)

// Indicator is one detected environmental signal.
type Indicator struct {
	// ID is a stable machine-readable identifier, e.g. "ENV-VM-DMI-VMWARE".
	ID string `json:"id"`
	// Category and Severity classify the finding.
	Category Category `json:"category"`
	Severity Severity `json:"severity"`
	// Confidence in [0,1]. A strong signal is 0.9; a heuristic hint is 0.3.
	Confidence float64 `json:"confidence"`
	// Title is a one-line summary.
	Title string `json:"title"`
	// Evidence is the raw observation that produced the indicator: a DMI string,
	// a MAC prefix, a CPU flag. Never the conclusion alone.
	Evidence []string `json:"evidence,omitempty"`
}

// FileReader is the filesystem dependency, injected so probes can be tested
// against a synthetic host.
type FileReader interface {
	ReadFile(path string) ([]byte, error)
	ReadDir(path string) ([]string, error)
	Exists(path string) bool
}

// Clock supplies the monotonic reading used by timing probes.
//
// A Clock injected through Options must be safe for concurrent use. A Prober is
// safe to share, but it cannot make an unsafe function safe: if several goroutines
// share one Prober and its Clock mutates internal state, their readings differ and
// the resulting snapshots diverge. time.Since satisfies this trivially.
//
// This was found by TestProberIsSafeForConcurrentUse, where a test fixture that
// advanced a sequence index produced 8 indicators in one goroutine and 9 in
// another. The defect was in the fixture, not the Prober -- but the contract was
// undocumented, which is what made it a trap.
type Clock func() time.Duration

// Options configures a Prober.
type Options struct {
	// FS reads host files. Nil selects the real filesystem. Tests inject a fake,
	// which is what makes these probes deterministic in CI.
	FS FileReader
	// Monotonic is the portable clock for timing probes. Nil selects the runtime
	// monotonic clock.
	Monotonic Clock
	// SkipCPUID disables the x86 CPUID probes entirely. Useful on hosts where
	// they are undesirable, and in CI where the value is fixed anyway.
	SkipCPUID bool
	// SkipTiming disables timing probes. Timings are the noisiest signal and the
	// most sensitive to CI load, so a shared runner may reasonably exclude them.
	SkipTiming bool
	// IncludeHostnames records hostname and username observations. Off by default
	// because host identity is rarely needed for a VM verdict and is the finding
	// most likely to leak into a shared log.
	IncludeHostnames bool
}
