//go:build !release
// +build !release

package hivemind

// rawSyscall fallback for non-release builds or unsupported platforms.
// Always returns "not ptraced" to avoid false positives in dev.
func rawSyscall(trap, a1, a2, a3 uintptr) (uintptr, uintptr, Errno) {
	return 0, 0, 0
}

type Errno uintptr

const (
	SYS_PTRACE     = 101
	PTRACE_TRACEME = 0
	EPERM          = 1
)

// AnnounceKeyPosture is a no-op in non-release builds.
func AnnounceKeyPosture() {}
