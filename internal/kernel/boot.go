package kernel

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sync/errgroup"
)

// Boot resolves, initialises, and starts every registered module.
//
// Order is a deterministic topological sort: dependencies first, ties broken by
// registration order. Init runs in that order, then every Run is supervised by a
// single errgroup, so the first module to fail cancels the rest.
//
// Boot does not block. It returns once every module has initialised and its Run
// goroutine has been launched; a failing Run surfaces through Wait.
//
// A module registered with a nil Factory is declaration-only: it publishes
// capabilities for ordering but owns no lifecycle.
func (k *Kernel) Boot(ctx context.Context) error {
	k.mu.Lock()
	if k.bootAttempted {
		k.mu.Unlock()
		return errors.New("kernel: already booted")
	}
	k.bootAttempted = true
	log := k.log
	factory := defaultHostFactory(log, k.Memory)
	k.mu.Unlock()

	order, err := k.Resolve()
	if err != nil {
		k.bootErr = err
		return err
	}

	entries := make([]moduleEntry, 0, len(order))
	for i := range order {
		name := order[i]
		k.mu.RLock()
		reg := k.regs[name]
		k.mu.RUnlock()

		entry := moduleEntry{name: name, desc: reg.desc, h: factory.forModule(reg.desc)}
		if reg.factory != nil {
			entry.m = reg.factory(entry.h)
		}
		if entry.m != nil {
			if err := entry.m.Init(ctx, entry.h); err != nil {
				// Unwind whatever already initialised, in reverse, so a partial
				// boot leaves no module half-started.
				k.stopEntries(ctx, entries)
				bootErr := fmt.Errorf("kernel: module %q failed to init: %w", name, err)
				k.bootErr = bootErr
				return bootErr
			}
		}
		entries = append(entries, entry)
	}

	k.mu.Lock()
	k.entries = entries
	for i := range entries {
		k.byName[entries[i].name] = &entries[i]
	}
	k.mu.Unlock()

	// One group supervises every Run. The first error cancels the shared context,
	// so a failing module takes its dependents down with it rather than leaving
	// them running against a kernel that is no longer coherent.
	g, gctx := errgroup.WithContext(ctx)
	k.mu.Lock()
	k.group = g
	k.runCtx = gctx
	k.mu.Unlock()

	for i := range entries {
		if entries[i].m == nil {
			continue
		}
		mod := entries[i].m
		g.Go(func() error { return mod.Run(gctx) })
	}

	// Only now is the kernel genuinely booted. Setting this earlier let a failed
	// Boot report Booted=true through Health(), which is precisely the sort of
	// false reassurance a health endpoint must never give.
	k.mu.Lock()
	k.started = true
	k.mu.Unlock()
	return nil
}

// stopEntries calls Stop in reverse order on the given entries.
func (k *Kernel) stopEntries(ctx context.Context, entries []moduleEntry) {
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].m == nil {
			continue
		}
		if err := entries[i].m.Stop(ctx); err != nil {
			k.log.Error("module stop failed during rollback",
				"module", entries[i].name, "error", err)
		}
	}
}

// Wait blocks until every module's Run has returned, returning the first error.
//
// This is the real drain barrier: when Wait returns, no module is running.
// Cancellation is reported as success, because cancellation is how the kernel is
// asked to stop rather than a failure.
func (k *Kernel) Wait() error {
	k.mu.RLock()
	g := k.group
	k.mu.RUnlock()
	if g == nil {
		return nil
	}
	err := g.Wait()
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// Shutdown stops every module in reverse dependency order and waits for the
// drain, bounded by the configured timeout.
//
// The bound matters: a wedged module must not be able to hold the process open
// indefinitely. Exceeding it is logged and reported rather than swallowed,
// because a kernel that could not drain cleanly is a fault worth seeing.
func (k *Kernel) Shutdown(ctx context.Context) error {
	k.mu.Lock()
	if !k.started || k.stopped {
		k.mu.Unlock()
		return nil
	}
	k.stopped = true
	entries := k.entries
	drain := k.drainTimeout
	k.mu.Unlock()

	k.stopEntries(ctx, entries)

	done := make(chan error, 1)
	go func() { done <- k.Wait() }()

	if drain <= 0 {
		return <-done
	}
	select {
	case err := <-done:
		return err
	case <-time.After(drain):
		k.log.Warn("drain timed out; exiting with modules still running",
			"timeout", drain)
		return fmt.Errorf("kernel: drain timed out after %v", drain)
	}
}

// Health describes the kernel's current state, for a /healthz surface.
type Health struct {
	Booted    bool         `json:"booted"`
	Stopped   bool         `json:"stopped"`
	Order     []string     `json:"order"`
	Modules   int          `json:"modules"`
	BootError string       `json:"boot_error,omitempty"`
	Caps      []Capability `json:"capabilities"`
}

// Health returns a snapshot of kernel state, so a node's health endpoint can
// report the module set without reaching into kernel internals.
func (k *Kernel) Health() Health {
	k.mu.RLock()
	defer k.mu.RUnlock()
	h := Health{
		Booted:  k.started,
		Stopped: k.stopped,
		Order:   append([]string(nil), k.order...),
		Modules: len(k.entries),
		Caps:    k.CapabilitiesInUseLocked(),
	}
	if k.bootErr != nil {
		h.BootError = k.bootErr.Error()
	}
	return h
}
