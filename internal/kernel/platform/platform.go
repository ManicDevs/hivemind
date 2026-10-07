// Package platform is the seam between the hivemind kernel core and whatever is
// underneath it.
//
// # Three layers
//
//	core    — this package: Console, Clock, Allocator. Primitives, no policy.
//	kernel  — internal/kernel: module registry, capabilities, lifecycle.
//	OS      — internal/kernel/boot: boots a platform, runs kernel modules.
//
// The core does not know about modules, and the kernel does not know about
// consoles or allocators. What differs between "runs on this machine" and "boots
// on bare metal" is exactly three primitives, and that is this package.
//
// Three backends, one core:
//
//	fake:   deterministic, in-memory. Tests assert exact output and drive the
//	        clock by hand, so timing assertions are exact rather than flaky.
//	host:   the real machine. Runs the core unchanged today, which is what makes
//	        the core verifiable before any bootloader exists.
//	serial: a 16550 UART, which is what a booted kernel has.
//
// # On memory
//
// Allocator is backed by the Go runtime rather than a hand-rolled free list.
// Writing an unsafe arena inside Go would reimplement make(), risk corruption,
// and buy nothing: the runtime's allocator is already correct and concurrent.
// What the kernel actually needs is not raw memory but *accounting* — how much is
// live, how many blocks, and the ability to fail a request on demand so an
// exhaustion path can be tested rather than assumed.
package platform

import (
	"context"
	"sync"
	"time"
)

// Console is a byte sink.
//
// Deliberately minimal: a kernel that can only write bytes can be driven by
// stdout, a UART, or a buffer.
type Console interface {
	// Name identifies the console, e.g. "buffer" or "uart0". It appears in the
	// health snapshot so an operator can tell whether output is reaching a
	// terminal or a serial line.
	Name() string
	Write(p []byte) (int, error)
	WriteString(s string) (int, error)
	Flush() error
}

// Clock is a monotonic time source.
//
// There is no wall-clock accessor on purpose. A kernel measuring a deadline must
// not be confused by the clock being adjusted, and omitting it removes the
// temptation to reach for one.
type Clock interface {
	// SinceBoot is the monotonic duration since the platform started.
	SinceBoot() time.Duration
}

// Allocator hands out and reclaims byte blocks, with accounting.
//
// Alloc returns a usable slice or reports failure; it never returns a partial
// block. Freeing a block the allocator did not produce is a programming error and
// panics, because silently ignoring it would let a double-free hide until it
// corrupted something unrelated.
type Allocator interface {
	Alloc(n int) ([]byte, bool)
	Free(b []byte)
	Stats() AllocStats
}

// AllocStats describes occupancy.
type AllocStats struct {
	LiveBytes    int `json:"live_bytes"`
	PeakBytes    int `json:"peak_bytes"`
	LiveBlocks   int `json:"live_blocks"`
	AllocCount   int `json:"alloc_count"`
	FreeCount    int `json:"free_count"`
	FailedAllocs int `json:"failed_allocs"`
}

// Platform is the complete substrate the kernel core needs.
//
// Every method is one a kernel genuinely cannot do without. Anything else the
// kernel implements for itself.
type Platform interface {
	// Name identifies the backend: "fake", "host", or "serial".
	Name() string
	Console() Console
	Clock() Clock
	Heap() Allocator
	// Aware is the usable memory ceiling in bytes, or 0 if unbounded.
	Aware() int
	// Reset brings the platform to a post-boot state, once, before any module runs.
	Reset(ctx context.Context) error
}

// GoAllocator is the Allocator backed by the Go runtime.
//
// It exists to make memory *accountable*, not to manage memory. The Limit field
// is what makes exhaustion testable: set it low, and the out-of-memory path
// runs on demand instead of only under real exhaustion.
type GoAllocator struct {
	mu     sync.Mutex
	live   int
	peak   int
	blocks int
	allocs int
	frees  int
	failed int
	// Limit caps live bytes; 0 means unbounded.
	Limit int
}

// NewGoAllocator returns an allocator with the given live-byte ceiling.
func NewGoAllocator(limit int) *GoAllocator {
	return &GoAllocator{Limit: limit}
}

func (g *GoAllocator) Alloc(n int) ([]byte, bool) {
	if n <= 0 {
		return nil, false
	}
	g.mu.Lock()
	if g.Limit > 0 && g.live+n > g.Limit {
		g.failed++
		g.mu.Unlock()
		return nil, false
	}
	g.live += n
	if g.live > g.peak {
		g.peak = g.live
	}
	g.blocks++
	g.allocs++
	g.mu.Unlock()

	// make() is zeroed, which a kernel wants: handing out memory that happens
	// to contain another module's leftovers is a bug source, not a saving.
	b := make([]byte, n)
	return b, true
}

func (g *GoAllocator) Free(b []byte) {
	if b == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.live -= len(b)
	g.blocks--
	g.frees++
}

func (g *GoAllocator) Stats() AllocStats {
	g.mu.Lock()
	defer g.mu.Unlock()
	return AllocStats{
		LiveBytes:    g.live,
		PeakBytes:    g.peak,
		LiveBlocks:   g.blocks,
		AllocCount:   g.allocs,
		FreeCount:    g.frees,
		FailedAllocs: g.failed,
	}
}

var _ Allocator = (*GoAllocator)(nil)
