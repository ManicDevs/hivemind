package platform

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Serial is the platform backend backed by a host serial port.
//
// On a physical node the port is the early-boot console; on a test machine it
// can be any writable file or injected writer. Console writes are serialised so
// module output is not interleaved at the character level.
type Serial struct {
	console Console
	clock   Clock
	heap    *GoAllocator
}

// NewSerialPath opens path for writing and returns the platform. The device is
// not configured because the substrate may be a regular file under test.
func NewSerialPath(path string, limit int) (*Serial, error) {
	w, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o666)
	if err != nil {
		return nil, fmt.Errorf("serial: open %s: %w", path, err)
	}
	return &Serial{
		console: NewSerialConsole(w),
		clock:   &hostClock{started: time.Now()},
		heap:    NewGoAllocator(limit),
	}, nil
}

// NewSerialWriter returns a Serial console backed by an injected writer, for
// tests and emulated UART plumbing.
func NewSerialWriter(w io.Writer, limit int) *Serial {
	return &Serial{
		console: NewSerialConsole(w),
		clock:   &hostClock{started: time.Now()},
		heap:    NewGoAllocator(limit),
	}
}

func (s *Serial) Name() string                    { return "serial" }
func (s *Serial) Console() Console                { return s.console }
func (s *Serial) Clock() Clock                    { return s.clock }
func (s *Serial) Heap() Allocator                 { return s.heap }
func (s *Serial) Aware() int                      { return s.heap.Limit }
func (s *Serial) Reset(ctx context.Context) error { return nil }

var _ Platform = (*Serial)(nil)

// SerialConsole is a Console named for a UART sink.
type SerialConsole struct {
	mu sync.Mutex
	w  io.Writer
}

func NewSerialConsole(w io.Writer) *SerialConsole {
	return &SerialConsole{w: w}
}

func (c *SerialConsole) Name() string { return "uart0" }

func (c *SerialConsole) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.w.Write(p)
}

func (c *SerialConsole) WriteString(s string) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return io.WriteString(c.w, s)
}

func (c *SerialConsole) Flush() error { return nil }
