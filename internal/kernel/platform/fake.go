package platform

import (
	"context"
	"strings"
	"sync"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// fake: the backend tests run against
// ─────────────────────────────────────────────────────────────────────────────

// Fake is a deterministic in-memory platform.
//
// Everything a test might need to control is controllable: output is captured,
// time only moves when the test moves it, and allocation failure is induced by
// lowering the heap limit. That is what makes the kernel core testable without a
// host, a drifting clock, or a device that can wedge.
type Fake struct {
	console *Buffer
	clock   *ManualClock
	heap    *GoAllocator

	// ResetErr, when non-nil, is returned by Reset, so the boot-failure path is
	// testable.
	ResetErr error

	mu         sync.Mutex
	resetCount int
}

// NewFake returns a Fake whose heap is capped at limit live bytes.
func NewFake(limit int) *Fake {
	return &Fake{
		console: NewBuffer(),
		clock:   NewManualClock(),
		heap:    NewGoAllocator(limit),
	}
}

func (f *Fake) Name() string     { return "fake" }
func (f *Fake) Console() Console { return f.console }
func (f *Fake) Clock() Clock     { return f.clock }
func (f *Fake) Heap() Allocator  { return f.heap }
func (f *Fake) Aware() int       { return f.heap.Limit }

func (f *Fake) Reset(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resetCount++
	return f.ResetErr
}

// ResetCount reports how many times Reset ran, so a test can assert the boot
// path called it exactly once rather than per-module.
func (f *Fake) ResetCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resetCount
}

// Out returns the captured console, so a test can assert on output directly.
func (f *Fake) Out() *Buffer { return f.console }

// Advance moves the fake clock forward.
func (f *Fake) Advance(d time.Duration) { f.clock.Advance(d) }

// Buffer is a Console backed by a strings.Builder.
type Buffer struct {
	mu  sync.Mutex
	buf strings.Builder
	// Flushed counts Flush calls, so a test can assert a buffered console was
	// flushed rather than relying on process exit to do it.
	Flushed int
}

func NewBuffer() *Buffer { return &Buffer{} }

func (b *Buffer) Name() string { return "buffer" }

func (b *Buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *Buffer) WriteString(s string) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.WriteString(s)
}

func (b *Buffer) Flush() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Flushed++
	return nil
}

func (b *Buffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Lines returns captured output as trimmed lines, which is what a test usually
// asserts against rather than one long string.
func (b *Buffer) Lines() []string {
	s := b.String()
	if s == "" {
		return nil
	}
	raw := strings.Split(strings.TrimRight(s, "\n"), "\n")
	out := make([]string, 0, len(raw))
	for _, l := range raw {
		out = append(out, strings.TrimRight(l, "\r"))
	}
	return out
}

// Reset clears captured output.
func (b *Buffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

// ManualClock is a Clock that advances only when a test advances it.
//
// Every timing assertion in the core becomes exact: "the module polled every
// 100ms" is a deterministic fact rather than a threshold that might flake on a
// loaded CI runner.
type ManualClock struct {
	mu  sync.Mutex
	now time.Duration
}

func NewManualClock() *ManualClock { return &ManualClock{} }

func (c *ManualClock) SinceBoot() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *ManualClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now += d
}

func (c *ManualClock) Set(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = d
}

var (
	_ Platform = (*Fake)(nil)
	_ Console  = (*Buffer)(nil)
	_ Clock    = (*ManualClock)(nil)
)
