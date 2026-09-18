//go:build unix

package hivemind

import (
	"fmt"
	"net"
	"os"
	"syscall"
	"time"
)

// Unix raw-socket primitives for TCP simultaneous open. Separated from
// punch.go by build tag because Windows' syscall package speaks Handle,
// not int — see punch_windows.go for the fail-closed twins.

func bindPunchPort() (fd, port int, err error) {
	fd, err = syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		return 0, 0, fmt.Errorf("punch socket: %w", err)
	}
	_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Port: 0}); err != nil {
		syscall.Close(fd)
		return 0, 0, fmt.Errorf("punch bind: %w", err)
	}
	sa, err := syscall.Getsockname(fd)
	if err != nil {
		syscall.Close(fd)
		return 0, 0, fmt.Errorf("punch name: %w", err)
	}
	if inet, ok := sa.(*syscall.SockaddrInet4); ok {
		return fd, inet.Port, nil
	}
	syscall.Close(fd)
	return 0, 0, fmt.Errorf("punch: unexpected socket family")
}

// connectBoundFD connects a pre-bound socket, with a guillotine: close
// the fd on timeout, which aborts the connect from underneath it.
func connectBoundFD(fd int, remoteHost string, remotePort int) (net.Conn, error) {
	rip := net.ParseIP(remoteHost)
	if rip == nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("punch: bad remote host %q", remoteHost)
	}
	var raddr syscall.Sockaddr
	if v4 := rip.To4(); v4 != nil {
		var a [4]byte
		copy(a[:], v4)
		raddr = &syscall.SockaddrInet4{Port: remotePort, Addr: a}
	} else {
		syscall.Close(fd)
		return nil, fmt.Errorf("punch: IPv6 simultaneous open not implemented")
	}
	type result struct {
		conn net.Conn
		err  error
	}
	done := make(chan result, 1)
	go func() {
		if err := syscall.Connect(fd, raddr); err != nil {
			syscall.Close(fd)
			done <- result{nil, fmt.Errorf("punch connect: %w", err)}
			return
		}
		f := os.NewFile(uintptr(fd), "punch-conn")
		conn, err := net.FileConn(f)
		_ = f.Close()
		if err != nil {
			syscall.Close(fd)
			done <- result{nil, fmt.Errorf("punch wrap: %w", err)}
			return
		}
		done <- result{conn, nil}
	}()
	select {
	case r := <-done:
		return r.conn, r.err
	case <-time.After(punchTimeout):
		syscall.Close(fd)
		return nil, fmt.Errorf("punch rendezvous missed (timeout)")
	}
}

// bindPort creates a bound TCP socket on a specific local port.
func bindPort(localPort int) (fd, port int, err error) {
	fd, err = syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		return 0, 0, fmt.Errorf("punch socket: %w", err)
	}
	// Best effort; failure here must not doom the attempt below.
	_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Port: localPort}); err != nil {
		syscall.Close(fd)
		return 0, 0, fmt.Errorf("punch bind :%d: %w", localPort, err)
	}
	return fd, localPort, nil
}

// closePunchFD releases a punch socket. Unix: a real close.
func closePunchFD(fd int) {
	syscall.Close(fd)
}
