//go:build release && !(linux && (amd64 || arm64))

package hivemind

import (
	"os"
	"strings"
)

// isPtraced returns true when a tracer is attached to this process. On
// platforms without the assembly syscall stub we fall back to reading
// /proc/self/status and checking TracerPid. Note this is advisory: /proc is
// not available on every platform, and when it is, a hostile debugger can
// suppress the report. The assembly-backed version is preferred.
func isPtraced() bool {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "TracerPid:") {
			fields := strings.Fields(line)
			return len(fields) >= 2 && fields[1] != "0"
		}
	}
	return false
}
