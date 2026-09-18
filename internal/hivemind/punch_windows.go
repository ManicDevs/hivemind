//go:build windows

package hivemind

import (
	"fmt"
	"net"
)

// Windows has no simultaneous-open socket API in Go's syscall package:
// every punch primitive fails closed here, and the mesh falls back to
// relay. Documented, not silent.
func bindPunchPort() (fd, port int, err error) {
	return 0, 0, fmt.Errorf("punch: simultaneous open unsupported on windows")
}

func bindPort(localPort int) (fd, port int, err error) {
	return 0, 0, fmt.Errorf("punch: simultaneous open unsupported on windows")
}

func connectBoundFD(fd int, remoteHost string, remotePort int) (net.Conn, error) {
	return nil, fmt.Errorf("punch: simultaneous open unsupported on windows")
}

func closePunchFD(fd int) {}
