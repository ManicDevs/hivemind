//go:build linux

package main

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

// ── anti-debug: offer resistance, never theater ───────────────────────
// Against root or a determined reverser, nothing here holds — stated
// plainly so nobody mistakes friction for armor. What it does stop:
// casual gdb attaches, strace snooping, and core dumps of key material.
// Policy via HIVEMIND_HARDEN: warn (default), exit, or off.
// Stripped binaries (-s -w, see release-hardened) additionally deny
// symbol tables — at the honest cost of readable backtraces.

func currentTracerPid() int {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	return tracerPid(string(data))
}

// antiDebug applies the configured policy once at startup. Returns false
// when the process must not continue (exit policy + active tracer).
func antiDebug() bool {
	policy := debugPolicy()
	if policy == "off" {
		return true
	}
	// Refuse core dumps of key material first: cheap, unconditional
	// under any non-off policy.
	_, _, errno := syscall.Syscall(syscall.SYS_PRCTL, syscall.PR_SET_DUMPABLE, 0, 0)
	if errno != 0 {
		fmt.Fprintf(os.Stderr, "⚠️  [HARDEN] Could not disable core dumps (%v).\n", errno)
	}
	checkInjectorEnv(policy)
	if pid := currentTracerPid(); pid != 0 {
		fmt.Fprintf(os.Stderr, "⚠️  [HARDEN] Tracer attached (pid %d). Policy=%s.\n", pid, policy)
		if policy == "exit" {
			return false
		}
	}
	if selfTraceTest() {
		// Refusal is evidence, not proof: sandboxes that block ptrace
		// refuse too. So this warns always but exits never — only a
		// positive TracerPid reading carries the exit policy.
		fmt.Fprintf(os.Stderr, "⚠️  [HARDEN] PTRACE_TRACEME refused: traced, or ptrace-blocked sandbox. Policy=%s.\n", policy)
	}
	go debugWatchdog(policy)
	return true
}

// scanInjectorEnv lists set library-injection vectors. Pure over the
// environment: unit-testable, and the two detectors cross-check each
// other (a tracer that hides from /proc still trips TRACEME, and vice
// versa — agreement is the signal, either alone is a hint).
func scanInjectorEnv() []string {
	var found []string
	for _, key := range []string{"LD_PRELOAD", "LD_LIBRARY_PATH"} {
		if val := os.Getenv(key); val != "" {
			found = append(found, key+"="+val)
		}
	}
	return found
}

// checkInjectorEnv names the classic library-injection vectors. Their
// mere presence proves nothing (containers set these innocently), so
// this only ever warns — but a watched operator wants to know.
func checkInjectorEnv(policy string) {
	for _, hit := range scanInjectorEnv() {
		fmt.Fprintf(os.Stderr, "⚠️  [HARDEN] %s set: library injection vector present (policy=%s).\n", hit, policy)
	}
}

// selfTraceTest attempts PTRACE_TRACEME on ourselves: success means no
// tracer (and incidentally bars late attachers — only our parent may
// trace us now); failure means already traced. Atomic where
// /proc-parsing races: no TOCTOU between check and act.
func selfTraceTest() bool {
	_, _, errno := syscall.Syscall(syscall.SYS_PTRACE, uintptr(syscall.PTRACE_TRACEME), 0, 0)
	return errno != 0
}

// debugWatchdog re-checks for tracers for the life of the process: a
// tracer attaching a second after startup must not inherit invisibility.
func debugWatchdog(policy string) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if pid := currentTracerPid(); pid != 0 {
			fmt.Fprintf(os.Stderr, "⚠️  [HARDEN] Late tracer attached (pid %d). Policy=%s.\n", pid, policy)
			if policy == "exit" {
				fmt.Fprintln(os.Stderr, "🛡️  [HARDEN] Exiting rather than thinking watched.")
				os.Exit(4)
			}
			return // warned once; nagging every 30s helps nobody
		}
	}
}
