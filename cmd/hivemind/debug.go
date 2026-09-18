package main

import (
	"os"
	"strconv"
	"strings"
)

// Shared policy + parser across platforms; only the enforcement differs
// (debug_linux.go vs debug_other.go).

func debugPolicy() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("HIVEMIND_HARDEN"))) {
	case "exit":
		return "exit"
	case "off", "0", "false", "no":
		return "off"
	default:
		return "warn"
	}
}

// tracerPid parses /proc/self/status text for an attached tracer.
// Pure over text: unit-testable without being ptraced.
func tracerPid(statusText string) int {
	for _, line := range strings.Split(statusText, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == "TracerPid:" {
			if pid, err := strconv.Atoi(f[1]); err == nil {
				return pid
			}
		}
	}
	return 0
}
