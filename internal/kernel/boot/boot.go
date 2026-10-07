// Package boot is the OS layer: the third of the three.
//
//	core   — internal/kernel/platform: Console, Clock, Allocator
//	kernel — internal/kernel: module registry, capabilities, lifecycle
//	OS     — this package: boots a platform, runs kernel modules, reports health
//
// # What "OS" means here
//
// This is a unikernel in the Firecracker/gVisor sense: a single image that owns
// its own lifecycle and mediates everything beneath it. It is not a Unix
// derivative, it runs no external programs, and it provides no syscall surface to
// anything else.
//
// # Why it runs on a host first
//
// A kernel that can only be tested by rebooting under a hypervisor is a kernel
// that is tested rarely. Because the substrate is an interface, the same boot
// sequence that a booted kernel runs also runs on a normal process where it can
// be asserted exactly — banner bytes, module order, health shape, allocator
// accounting.
//
// The hypervisor path changes exactly two things: which Platform implementation
// is supplied, and where the entry point is. Nothing in the boot sequence itself
// knows which it is running on, which is the entire point of the seam.
package boot

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gitlab.torproject.org/cerberus-droid/hivemind/internal/kernel"
	"gitlab.torproject.org/cerberus-droid/hivemind/internal/kernel/platform"
)

// Hostname is the node identity this OS reports.
//
// A booted node has no DHCP and no name service, so the identity is compiled in.
// It is a constant rather than a flag because a kernel whose name comes from the
// environment can be renamed by whoever launched it, which is exactly the
// property a kernel should not have.
const Hostname = "HIVEMIND"

// Version is the OS build identifier.
const Version = "0.1.0-light"

// Banner is the line a booting OS emits first.
//
// It goes out before anything else so that a serial console which shows
// nothing but this line still proves the image reached userspace, which is the
// first thing to check when a boot fails.
func Banner(p platform.Platform) string {
	return fmt.Sprintf("=== %s kernel %s (%s backend) ===", Hostname, Version, p.Name())
}

// Config is the boot configuration.
type Config struct {
	// Platform is the substrate. Required.
	Platform platform.Platform
	// Kernel holds the registered modules. Required.
	Kernel *kernel.Kernel
	// Banner enables the boot banner.
	Banner bool
	// ReportModules prints each module's start order once booted.
	ReportModules bool
	// DrainTimeout bounds shutdown. Zero uses the kernel's own bound.
	DrainTimeout time.Duration
}

// System is a booted OS.
type System struct {
	platform platform.Platform
	kernel   *kernel.Kernel
	hostname string
	started  time.Time
}

// ErrNotConfigured is returned when a Config lacks a substrate or kernel.
var ErrNotConfigured = errors.New("boot: Platform and Kernel are required")

// Run boots the OS and blocks until ctx is cancelled, then drains.
//
// The sequence is deliberately fixed and observable:
// reset the platform, print the banner, boot the kernel, report, wait for
// shutdown. Each step is testable independently because each is one call.
func Run(ctx context.Context, cfg Config) error {
	if cfg.Platform == nil || cfg.Kernel == nil {
		return ErrNotConfigured
	}
	sys, err := New(ctx, cfg)
	if err != nil {
		return err
	}
	return sys.Wait(ctx)
}

// New boots the OS and returns without blocking.
func New(ctx context.Context, cfg Config) (*System, error) {
	if cfg.Platform == nil || cfg.Kernel == nil {
		return nil, ErrNotConfigured
	}
	p, k := cfg.Platform, cfg.Kernel

	// Reset before anything else: a console must be live before the banner, and
	// a kernel must not start against uninitialised substrate.
	if err := p.Reset(ctx); err != nil {
		return nil, fmt.Errorf("boot: platform reset: %w", err)
	}

	sys := &System{platform: p, kernel: k, hostname: Hostname, started: time.Now()}

	if cfg.Banner {
		if _, err := p.Console().WriteString(Banner(p) + "\n"); err != nil {
			return nil, fmt.Errorf("boot: banner: %w", err)
		}
	}

	if err := k.Boot(ctx); err != nil {
		return nil, fmt.Errorf("boot: kernel: %w", err)
	}

	if cfg.ReportModules {
		sys.reportModules()
	}
	if cfg.DrainTimeout > 0 {
		k.SetDrainTimeout(cfg.DrainTimeout)
	}
	return sys, nil
}

// reportModules prints the resolved start order and the host capabilities the
// process will use.
//
// This is the operator's first question when a node misbehaves, so it is printed
// at boot rather than requiring a debugging session: which modules loaded, in
// what order, and what they are permitted to touch.
func (s *System) reportModules() {
	order, err := s.kernel.Resolve()
	if err != nil {
		fmt.Fprintf(s.platform.Console(), "[boot] resolve failed: %v\n", err)
		return
	}
	fmt.Fprintf(s.platform.Console(), "[boot] modules (%d): %v\n", len(order), order)
	caps := s.kernel.CapabilitiesInUse()
	if len(caps) == 0 {
		fmt.Fprintf(s.platform.Console(), "[boot] host capabilities: none\n")
		return
	}
	fmt.Fprintf(s.platform.Console(), "[boot] host capabilities: %v\n", caps)
}

// Hostname returns the OS identity.
func (s *System) Hostname() string { return s.hostname }

// Uptime is the monotonic time since boot.
func (s *System) Uptime() time.Duration { return s.platform.Clock().SinceBoot() }

// Health is the OS health snapshot.
type Health struct {
	Hostname string              `json:"hostname"`
	Version  string              `json:"version"`
	Platform string              `json:"platform"`
	Uptime   string              `json:"uptime"`
	Kernel   kernel.Health       `json:"kernel"`
	Heap     platform.AllocStats `json:"heap"`
	Console  string              `json:"console"`
}

// Health returns a snapshot suitable for a /healthz response.
func (s *System) Health() Health {
	return Health{
		Hostname: s.hostname,
		Version:  Version,
		Platform: s.platform.Name(),
		Uptime:   s.platform.Clock().SinceBoot().Round(time.Millisecond).String(),
		Kernel:   s.kernel.Health(),
		Heap:     s.platform.Heap().Stats(),
		Console:  s.platform.Console().Name(),
	}
}

// Wait blocks until ctx is cancelled, then shuts the kernel down and reports.
//
// The drain is bounded by the kernel so a wedged module cannot keep the process
// alive, and the outcome is reported rather than swallowed: a node that could not
// shut down cleanly is a fault an operator should see.
func (s *System) Wait(ctx context.Context) error {
	<-ctx.Done()
	if _, err := s.platform.Console().WriteString("[boot] draining\n"); err != nil {
		return err
	}
	if err := s.kernel.Shutdown(context.Background()); err != nil {
		fmt.Fprintf(s.platform.Console(), "[boot] shutdown: %v\n", err)
		return err
	}
	_, _ = s.platform.Console().WriteString("[boot] clean\n")
	return s.platform.Console().Flush()
}
