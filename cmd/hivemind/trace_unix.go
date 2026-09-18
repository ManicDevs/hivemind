//go:build unix

package main

import (
	"os"
	"syscall"
)

// traceSignals are the live-backtrace signals. Unix only; Windows gets
// none (no SIGQUIT/SIGUSR1 there) and the trace loop stays parked.
func traceSignals() []os.Signal {
	return []os.Signal{syscall.SIGQUIT, syscall.SIGUSR1}
}
