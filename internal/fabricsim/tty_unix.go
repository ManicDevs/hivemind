//go:build !windows

package fabricsim

import (
	"io"
	"os"
	"syscall"
	"unsafe"
)

// isTerminal reports whether w is an interactive terminal.
//
// The check is a raw TCGETS ioctl rather than golang.org/x/term: the project
// forbids heavyweight external dependencies, and this is the only terminal
// capability the simulator needs. It is confined to this file so that the
// renderer stays portable; see tty_windows.go for the fallback.
//
// Where the ioctl is unavailable the answer is simply "not a terminal", and
// the renderer falls back to full tables. That is always correct, only noisier
// in a log.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	var termios [64]byte
	const tcgets = 0x5401 // TCGETS on Linux
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, f.Fd(), tcgets,
		uintptr(unsafe.Pointer(&termios[0])), 0, 0, 0)
	return errno == 0
}
