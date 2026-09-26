//go:build linux && arm64 && release
// +build linux,arm64,release

// Raw ptrace(PTRACE_TRACEME) syscall for ARM64.
// Returns (r1, r2, errno).

#include "textflag.h"

TEXT ·rawSyscall(SB), NOSPLIT, $0-40
    MOVD trap+0(FP), X8
    MOVD a1+8(FP), X0
    MOVD a2+16(FP), X1
    MOVD a2+24(FP), X2
    SVC 0
    MOVD X0, r1+32(FP)
    MOVD X1, r2+40(FP)
    // Check for error: negative return in X0 indicates -errno
    TBZ X0, #63, success
    NEG X0, X0
    MOVD X0, err+48(FP)
    RET
success:
    MOVD XZR, err+48(FP)
    RET
