//go:build release && linux && (amd64 || arm64)

package hivemind

// rawSyscall issues a raw syscall without cgo. Implemented in
// antire_asm_linux_amd64.s / antire_asm_linux_arm64.s; returns
// (r1, r2, errno) following Go's syscall convention.
func rawSyscall(trap, a1, a2, a3 uintptr) (r1, r2 uintptr, err Errno)

// isPtraced returns true when the current process is already being traced.
// ptrace(PTRACE_TRACEME, 0, 0, 0) fails with EPERM when a tracer is already
// attached, so a non-zero errno here is the debugger signal itself.
func isPtraced() bool {
	_, _, errno := rawSyscall(SYS_PTRACE, PTRACE_TRACEME, 0, 0)
	return errno == EPERM
}
