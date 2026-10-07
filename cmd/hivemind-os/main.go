// Command hivemind-os boots the hivemind OS on a real machine.
//
// This is the lightweight path: it runs the kernel core and module system as an
// ordinary process, so the boot sequence, module ordering, capability
// declarations, and heap accounting are all verifiable without a hypervisor.
//
// It is not a simulation. The core is the same code a booted kernel runs; only
// the substrate differs, and here that substrate is the host.
//
//	hivemind-os                 boot, run the built-in modules, wait for Ctrl-C
//	hivemind-os -health         print the health snapshot and exit
//	hivemind-os -platform fake  run against the deterministic fake substrate
//	hivemind-os -uptime 5s      exit automatically after a duration
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gitlab.torproject.org/cerberus-droid/hivemind/internal/kernel"
	"gitlab.torproject.org/cerberus-droid/hivemind/internal/kernel/boot"
	"gitlab.torproject.org/cerberus-droid/hivemind/internal/kernel/platform"
)

func main() {
	var (
		platformName = flag.String("platform", "host", "substrate: host | fake")
		healthOnly   = flag.Bool("health", false, "print health as JSON and exit")
		runFor       = flag.Duration("uptime", 0, "exit after this long (0 = until signalled)")
		heapLimit    = flag.Int("heap", 64<<20, "heap limit in bytes (0 = unbounded)")
	)
	flag.Parse()

	if err := run(*platformName, *healthOnly, *runFor, *heapLimit); err != nil {
		fmt.Fprintf(os.Stderr, "hivemind-os: %v\n", err)
		os.Exit(1)
	}
}

func run(platformName string, healthOnly bool, runFor time.Duration, heapLimit int) error {
	var p platform.Platform
	switch platformName {
	case "host":
		p = platform.NewHost(os.Stdout, heapLimit)
	case "fake":
		p = platform.NewFake(heapLimit)
	default:
		return fmt.Errorf("unknown platform %q: want host or fake", platformName)
	}

	k := kernel.New()
	registerModules(k)

	// Ctrl-C drains the OS rather than killing it, so the shutdown path runs
	// exactly as it would in a booted kernel.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if runFor > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, runFor)
		defer cancel()
	}

	sys, err := boot.New(ctx, boot.Config{
		Platform:      p,
		Kernel:        k,
		Banner:        !healthOnly,
		ReportModules: !healthOnly,
	})
	if err != nil {
		return err
	}

	if healthOnly {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(sys.Health())
	}

	return sys.Wait(ctx)
}

// registerModules wires the built-in set.
//
// The order here is deliberately not the start order: modules are registered
// consumer-before-provider so the kernel's topological resolution is doing real
// work rather than being handed a list that already happens to be correct.
func registerModules(k *kernel.Kernel) {
	// Consumer first — it requires "identity", which "platform" provides.
	k.MustRegister(kernel.Descriptor{
		Name:         "identity",
		Provides:     []string{"identity"},
		Capabilities: []kernel.Capability{kernel.CapClock},
	}, func(h kernel.Host) kernel.Module { return &identityModule{} })

	// Provider, registered after its consumer.
	k.MustRegister(kernel.Descriptor{
		Name:         "platform",
		Provides:     []string{"platform"},
		Requires:     []string{"identity"},
		Capabilities: []kernel.Capability{kernel.CapClock, kernel.CapLog},
	}, func(h kernel.Host) kernel.Module { return &platformModule{} })
}

// identityModule publishes the node identity. It is the root provider: nothing
// may run before the node knows what it is.
type identityModule struct {
	h kernel.Host
}

func (m *identityModule) Init(ctx context.Context, h kernel.Host) error {
	m.h = h
	h.Logger().Info("identity established", "hostname", boot.Hostname)
	return nil
}

func (m *identityModule) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func (m *identityModule) Stop(ctx context.Context) error {
	m.h.Logger().Info("identity released")
	return nil
}

// platformModule reports substrate health on a ticker, which exercises the
// kernel's supervision: it runs until the context is cancelled and returns nil.
type platformModule struct {
	h kernel.Host
}

func (m *platformModule) Init(ctx context.Context, h kernel.Host) error {
	m.h = h
	d, _ := h.Clock()
	h.Logger().Info("platform module online", "uptime", d.String())
	return nil
}

func (m *platformModule) Run(ctx context.Context) error {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			d, err := m.h.Clock()
			if err != nil {
				// A refused capability is a wiring bug, not a runtime condition,
				// so it is reported rather than retried forever.
				return err
			}
			m.h.Logger().Debug("heartbeat", "uptime", d.String())
		}
	}
}

func (m *platformModule) Stop(ctx context.Context) error {
	m.h.Logger().Info("platform module offline")
	return nil
}
