//go:build !linux

package main

// No ptrace interface here: anti-debug is a no-op off Linux.
// The policy parser stays shared so flags never surprise anyone.
func antiDebug() bool {
	if debugPolicy() == "exit" {
		// Nothing to detect with; exiting would brick honest runs.
		// Warn once via stderr in main if desired; here: allow.
	}
	return true
}
