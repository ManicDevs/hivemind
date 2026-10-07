package platform

import (
	"context"
	"io"
	"sync"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// host: the backend that runs the core on a real machine today
// ─────────────────────────────────────────────────────────────────────────────

// Host is the real-machine backend.
//
// It is what makes the kernel core verifiable before any bootloader exists: the
// core is identical here and on bare metal, so running it here exercises the same
// module registry, the same boot sequence, and the same allocator accounting that
// will later run under QEMU.
type Host struct {
	console Console
	clock   Clock
	heap    *GoAllocator
}

// NewHost returns a Host writing to w, with heap limited to limit live bytes
// (0 for unbounded).
//
// w is a parameter rather than hardcoded stdout so a test can capture the real
// backend's output. The host backend performs real allocations and reads the real
// monotonic clock; it is not a mock.
func NewHost(w io.Writer, limit int) *Host {
	return &Host{
		console: NewWriterConsole(w),
		clock:   &hostClock{started: time.Now()},
		heap:    NewGoAllocator(limit),
	}
}

func (h *Host) Name() string                    { return "host" }
func (h *Host) Console() Console                { return h.console }
func (h *Host) Clock() Clock                    { return h.clock }
func (h *Host) Heap() Allocator                 { return h.heap }
func (h *Host) Aware() int                      { return h.heap.Limit }
func (h *Host) Reset(ctx context.Context) error { return nil }

// hostClock reads the runtime monotonic clock.
type hostClock struct{ started time.Time }

func (c *hostClock) SinceBoot() time.Duration { return time.Since(c.started) }

// WriterConsole adapts an io.Writer to Console.
//
// Writes are serialised because a booted kernel writes from several goroutines,
// and an interleaved line is worse than a slow one.
type WriterConsole struct {
	mu sync.Mutex
	w  io.Writer
}

// NewWriterConsole wraps w.
func NewWriterConsole(w io.Writer) *WriterConsole {
	return &WriterConsole{w: w}
}

func (c *WriterConsole) Name() string { return "stdout" }

func (c *WriterConsole) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.w.Write(p)
}

func (c *WriterConsole) WriteString(s string) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// One write call rather than Write([]byte(s)), so a serial backend sees a
	// single transaction and a test writer sees one event.
	return io.WriteString(c.w, s)
}

// Flush is a no-op: an io.Writer owns its own buffering.
func (c *WriterConsole) Flush() error { return nil }

var _ Platform = (*Host)(nil)
