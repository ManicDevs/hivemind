//go:build !windows

// Process-group control for the child process fleet.
//
// Each continent is a whole group of nodes. The world must be able to signal
// that group as a unit, so a Ctrl-C or a failed preflight tears down 56
// processes rather than orphaning the survivors. Only Unix exposes Setpgid and
// negative-PID signalling, so the Windows build gets the no-op equivalents in
// procs_windows.go.

package main

import (
	"os/exec"
	"syscall"
)

// detachProcessGroup puts the child into its own process group so a signal
// aimed at the world does not also reach every node directly.
func detachProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// terminateProcessGroup signals the whole process group led by pid. When force
// is false it asks politely with SIGTERM; when true it escalates to SIGKILL.
//
// Errors are deliberately ignored: teardown runs during error handling, and a
// process that has already exited is a success from the caller's point of view.
func terminateProcessGroup(pid int, force bool) {
	sig := syscall.SIGTERM
	if force {
		sig = syscall.SIGKILL
	}
	_ = syscall.Kill(-pid, sig)
}
