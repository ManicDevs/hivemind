//go:build windows

// Process-group control for the child process fleet (Windows).
//
// Windows has no process groups in the POSIX sense, and os/exec has no
// equivalent of Setpgid, so the world cannot signal a continent's worth of
// nodes as a unit. Each child is instead tracked individually and torn down
// one at a time.
//
// Consequence worth knowing before deploying: on Windows a world shutdown
// walks the process list and kills each node, whereas on Unix one signal
// reaches the whole group. Shutdown is therefore slower and less atomic on
// Windows, and a child that has already spawned grandchildren of its own is
// not guaranteed to take them down with it. Every node is a single static
// executable with no subprocesses of its own, so this is acceptable here.

package main

import (
	"os"
	"os/exec"
)

// detachProcessGroup is a no-op: Windows has no Setpgid.
func detachProcessGroup(cmd *exec.Cmd) {}

// terminateProcessGroup kills the single process with the given pid.
//
// On Unix the negative pid reaches a whole group; here there is no group, so
// the pid refers to the one process. force is accepted to keep the signature
// identical across platforms: Windows' os.Process.Kill is already forceful,
// so there is no polite-then-forceful escalation to offer.
func terminateProcessGroup(pid int, force bool) {
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}
