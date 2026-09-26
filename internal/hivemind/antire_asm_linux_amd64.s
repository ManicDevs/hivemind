//go:build linux && amd64 && release
// +build linux,amd64,release

// Raw ptrace(PTRACE_TRACEME) syscall for anti-debug detection.
// Returns (r1, r2, errno) per Go's syscall convention.
// This avoids CGO and works in pure Go binaries.

#include "textflag.h"

TEXT ·rawSyscall(SB), NOSPLIT, $0-40
    MOVQ trap+0(FP), AX
    MOVQ a1+8(FP), DI
    MOVQ a2+16(FP), SI
    MOVQ a3+24(FP), DX
    MOVQ $0, R10
    MOVQ $0, R8
    MOVQ $0, R9
    SYSCALL
    MOVQ AX, r1+32(FP)
    MOVQ DX, r2+40(FP)
    // On error, CF=1 and AX contains -errno
    JC error
    MOVQ $0, err+48(FP)
    RET
error:
    NEGQ AX
    MOVQ AX, err+48(FP)
    RET
