//go:build windows

package fabricsim

import "io"

// isTerminal reports whether w is an interactive terminal.
//
// The Windows build has no ioctl to ask, so this reports false and the
// renderer emits full tables. That is the correct answer, not a degraded one:
// tables are the right output for a redirected stream, which is what a Windows
// run almost always is.
func isTerminal(w io.Writer) bool { return false }
