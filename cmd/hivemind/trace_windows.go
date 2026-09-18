//go:build windows

package main

import "os"

// traceSignals are the live-backtrace signals. Windows has neither
// SIGQUIT nor SIGUSR1, so the trace loop stays parked here.
func traceSignals() []os.Signal {
	return nil
}
