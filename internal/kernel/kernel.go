// Package kernel is a userspace microkernel for hivemind.
//
// It replaces "construct everything in one function" with a module system: a
// registry, dependency-resolved startup, an enforced capability model, and a
// hardware-abstraction seam that modules use instead of reaching into the OS.
//
// # Scope: what "kernel" means here
//
// This is a userspace kernel in the unikernel/microkernel sense: a privileged
// layer inside one process that owns lifecycle and mediates access. It is not an
// OS kernel, it is not a hypervisor, and it loads no code from outside the
// binary. Everything it manages is compiled in.
//
// A Linux kernel module was explicitly rejected: it requires C and kernel
// headers, which would break the CGO_ENABLED=0 invariant documented in
// docs/DEPLOY.md and the security assessment.
//
// # Isolation: honest scope
//
// Modules run in-process and share a heap. The capability model is therefore
// *enforced discipline*, not a security boundary. A module that is granted a
// capability can observe everything the process can observe.
//
// What the capability model does buy, which is real:
//
//   - A module physically cannot read a path it did not declare, because the
//     Host hands it a scoped filesystem view. That is a structural guarantee,
//     not a convention, and it holds regardless of what the module's code does.
//   - The set of host capabilities in use is knowable by inspection: you can
//     read the declarations and know exactly what the process touches.
//   - A missing or over-broad declaration is a review-time, greppable fact.
//
// A module that needs a path outside its declaration must widen its declaration,
// which shows up in the diff. That is the mechanism working.
//
// True isolation requires separate processes and an RPC boundary. That is a
// different design with different costs, and it is not this one.
//
// # Lifecycle
//
// Resolution is a topological sort over Requires/Provides. Start runs in
// dependency order; Stop runs in exact reverse. A module whose dependencies
// cannot be satisfied fails at boot with a named error rather than at first use.
//
// Long-running modules implement Run, and the kernel supervises every one of
// them with a single errgroup: the first error cancels its siblings and Shutdown
// waits for the drain. This matches the errgroup lifecycle standard in AGENTS.md.
//
// # Thread safety
//
// A Kernel is safe for concurrent use once Register returns; Boot and Shutdown
// are single-shot and must not overlap. Register is expected during
// construction, before any goroutine observes the Kernel.
package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// Capability is a host permission a module may request.
//
// Capabilities are coarse by design. The point is not to enumerate every syscall
// but to make the host surface a module touches visible at its declaration site,
// where a reviewer will actually see it.
type Capability string

const (
	// CapClock reads the monotonic and wall clocks.
	CapClock Capability = "clock"
	// CapFSRead reads files under the module's declared roots.
	CapFSRead Capability = "fs:read"
	// CapFSWrite writes files under the module's declared roots.
	CapFSWrite Capability = "fs:write"
	// CapEnv reads environment variables.
	CapEnv Capability = "env"
	// CapProc reads process status files such as /proc/self/status.
	CapProc Capability = "proc"
	// CapSys reads kernel-exposed host tables such as /sys.
	CapSys Capability = "sys"
	// CapNet dials outbound network connections.
	CapNet Capability = "net"
	// CapLog emits records through the kernel logger.
	CapLog Capability = "log"
)

// AllCapabilities is the full set, used to validate declarations at boot.
var AllCapabilities = []Capability{
	CapClock, CapFSRead, CapFSWrite, CapEnv, CapProc, CapSys, CapNet, CapLog,
}

// Descriptor is a module's declaration: what it is, what it needs, and what it
// wants from the host.
//
// A module with no Module value registered is a declaration-only module: it
// publishes capabilities for others without running any goroutine. That is how
// a pure provider (a capability table, say) participates in ordering.
type Descriptor struct {
	// Name uniquely identifies the module. Required.
	Name string

	// Provides are the abstract capabilities this module publishes to others.
	// Other modules depend on these to be ordered.
	Provides []string

	// Requires are abstract capabilities this module needs. Boot fails if a
	// requirement is unsatisfied, naming the module and the capability.
	Requires []string

	// Capabilities are the host permissions requested. Boot fails if a module
	// requests a capability not in AllCapabilities, which catches typos at
	// declaration rather than at first use.
	Capabilities []Capability

	// Roots restricts filesystem access to these path prefixes. A module with
	// CapFSRead but no Roots receives a view that denies everything, because an
	// unbounded grant would make the declaration meaningless.
	Roots []string
}

// Validate checks a descriptor for internal consistency.
func (d Descriptor) Validate() error {
	if strings.TrimSpace(d.Name) == "" {
		return errors.New("kernel: descriptor has no name")
	}
	for _, c := range d.Capabilities {
		if !knownCapability(c) {
			return fmt.Errorf("kernel: module %q requests unknown capability %q", d.Name, c)
		}
	}
	if (hasCap(d.Capabilities, CapFSRead) || hasCap(d.Capabilities, CapFSWrite)) && len(d.Roots) == 0 {
		return fmt.Errorf("kernel: module %q requests filesystem access with no roots; an unbounded grant would make the declaration meaningless", d.Name)
	}
	for _, p := range d.Provides {
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("kernel: module %q provides an empty capability name", d.Name)
		}
	}
	return nil
}

func knownCapability(c Capability) bool {
	for _, k := range AllCapabilities {
		if k == c {
			return true
		}
	}
	return false
}

func hasCap(caps []Capability, want Capability) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}

// Module is a registered component.
//
// Init receives a capability-scoped Host. Run is optional: a module with no
// background work returns nil immediately, and Stop is then also expected to be
// light. Run blocks until its context is cancelled or it fails.
type Module interface {
	// Init prepares the module. It must not block on long-lived work; that
	// belongs in Run.
	Init(ctx context.Context, h Host) error
	// Run performs the module's long-lived work, returning nil on clean
	// shutdown. A non-nil error aborts the whole kernel and is reported.
	Run(ctx context.Context) error
	// Stop releases resources. It is called in reverse dependency order, after
	// every Run has returned.
	Stop(ctx context.Context) error
}

// Factory constructs a Module from its Host.
//
// A factory rather than a bare Module value keeps construction inside the
// kernel's control, which is what lets Init receive the scoped Host rather than
// having the module capture one at registration time.
type Factory func(h Host) Module

type registration struct {
	desc    Descriptor
	factory Factory
}

// moduleEntry pairs a module with its descriptor and Host, so Shutdown can stop
// modules in the same order they were started without re-deriving the mapping.
type moduleEntry struct {
	name string
	desc Descriptor
	h    Host
	m    Module
}

// Kernel owns the module set and its lifecycle.
type Kernel struct {
	mu    sync.RWMutex
	regs  map[string]registration
	order []string

	// bootAttempted guards re-entry. It is deliberately separate from started:
	// a failed Boot must not report the kernel as booted, but it must still
	// prevent a second attempt on a kernel whose state is now indeterminate.
	bootAttempted bool
	started       bool
	stopped       bool
	bootErr       error
	// entries is in *start* order; Shutdown walks it backwards.
	entries []moduleEntry
	// byName maps a module name to its entry, for Stop lookup.
	byName map[string]*moduleEntry

	// group supervises every module's Run; runCtx is the context it derives.
	group  *errgroup.Group
	runCtx context.Context

	log *slog.Logger

	// Memory is the scratch allocator handed to every module. The OS layer sets
	// it to the platform's allocator so a module's working set is accounted for
	// against the substrate's ceiling rather than against the Go heap where it
	// would be invisible. Nil means the Go-heap fallback.
	Memory Memory

	// drainTimeout bounds how long Shutdown waits for Run loops before
	// declaring them wedged. Without a bound a stuck module would hold the
	// process open forever.
	drainTimeout time.Duration
}

// New returns an empty Kernel.
func New() *Kernel {
	return &Kernel{
		regs:         make(map[string]registration),
		byName:       make(map[string]*moduleEntry),
		drainTimeout: 10 * time.Second,
		log:          slog.Default(),
	}
}

// SetLogger sets the base logger. Child loggers are tagged per module, so a line
// is attributable without any module having to add its own name.
func (k *Kernel) SetLogger(l *slog.Logger) {
	if l == nil {
		l = slog.Default()
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.log = l
}

// SetDrainTimeout bounds the shutdown drain. Zero disables the wait, which is
// only appropriate when every Run is known to return promptly.
func (k *Kernel) SetDrainTimeout(d time.Duration) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.drainTimeout = d
}

// Register adds a module.
//
// It fails on a duplicate name or an invalid descriptor, rather than deferring
// the error to Boot: a bad declaration is a programming mistake and should
// surface at the point it is made.
func (k *Kernel) Register(desc Descriptor, f Factory) error {
	if err := desc.Validate(); err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.started {
		return fmt.Errorf("kernel: cannot register %q after Boot", desc.Name)
	}
	if _, exists := k.regs[desc.Name]; exists {
		return fmt.Errorf("kernel: module %q already registered", desc.Name)
	}
	k.regs[desc.Name] = registration{desc: desc, factory: f}
	k.order = append(k.order, desc.Name)
	return nil
}

// MustRegister is Register for package initialisation, where a failure is a
// build-time mistake rather than a runtime condition.
func (k *Kernel) MustRegister(desc Descriptor, f Factory) {
	if err := k.Register(desc, f); err != nil {
		panic(err)
	}
}

// Modules returns the registered names in registration order.
func (k *Kernel) Modules() []string {
	k.mu.RLock()
	defer k.mu.RUnlock()
	out := make([]string, len(k.order))
	copy(out, k.order)
	return out
}

// CapabilitiesInUse returns every host capability any registered module
// requested, deduplicated and sorted.
//
// This is the audit view: it answers "what does this process touch the host
// for?" by inspection, which is the practical payoff of declaring capabilities
// at all.
func (k *Kernel) CapabilitiesInUse() []Capability {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.CapabilitiesInUseLocked()
}

// CapabilitiesInUseLocked is CapabilitiesInUse for callers already holding the
// lock.
func (k *Kernel) CapabilitiesInUseLocked() []Capability {
	seen := make(map[Capability]bool)
	for _, r := range k.regs {
		for _, c := range r.desc.Capabilities {
			seen[c] = true
		}
	}
	out := make([]Capability, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Resolve computes the start order: a topological sort of the module set by
// Requires/Provides.
//
// The order is deterministic. Ties are broken by registration order, not by map
// iteration, because a kernel that boots in a different order on each run is a
// kernel whose bugs are unreproducible.
func (k *Kernel) Resolve() ([]string, error) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.resolveLocked()
}

func (k *Kernel) resolveLocked() ([]string, error) {
	// provider maps a provided capability to the module that supplies it. Two
	// modules providing the same capability is ambiguous rather than benign:
	// whichever won would depend on iteration order.
	provider := make(map[string]string)
	for name, r := range k.regs {
		for _, p := range r.desc.Provides {
			if prev, dup := provider[p]; dup {
				return nil, fmt.Errorf("kernel: capability %q provided by both %q and %q", p, prev, name)
			}
			provider[p] = name
		}
	}

	// deps[module] is the set of modules that must start before it.
	deps := make(map[string]map[string]bool, len(k.regs))
	dependents := make(map[string][]string, len(k.regs))
	for _, name := range k.order {
		deps[name] = make(map[string]bool)
	}
	for _, name := range k.order {
		r := k.regs[name]
		for _, need := range r.desc.Requires {
			src, ok := provider[need]
			if !ok {
				return nil, fmt.Errorf("kernel: module %q requires %q which no registered module provides", name, need)
			}
			if src == name {
				return nil, fmt.Errorf("kernel: module %q requires %q which it provides itself", name, need)
			}
			deps[name][src] = true
			dependents[src] = append(dependents[src], name)
		}
	}

	// Kahn's algorithm, always taking the earliest-registered ready module.
	var out []string
	done := make(map[string]bool, len(k.regs))
	for len(out) < len(k.order) {
		picked := ""
		for _, name := range k.order {
			if done[name] {
				continue
			}
			ready := true
			for dep := range deps[name] {
				if !done[dep] {
					ready = false
					break
				}
			}
			if ready {
				picked = name
				break
			}
		}
		if picked == "" {
			// Nothing ready with modules remaining means a cycle. Name the
			// members so the declaration can be fixed.
			var stuck []string
			for _, name := range k.order {
				if !done[name] {
					stuck = append(stuck, name)
				}
			}
			return nil, fmt.Errorf("kernel: dependency cycle among %s", strings.Join(stuck, ", "))
		}
		done[picked] = true
		out = append(out, picked)
	}
	return out, nil
}
