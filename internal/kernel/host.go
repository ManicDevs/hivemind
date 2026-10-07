package kernel

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Memory is a scratch allocator handed to modules that need one.
//
// It is declared here rather than imported from the platform package so the
// kernel stays independent of its substrate. Go interfaces are structural, so a
// platform.Allocator satisfies this without an adapter.
type Memory interface {
	Alloc(n int) ([]byte, bool)
	Free(b []byte)
}

// Host is the capability-scoped view of the machine handed to a module.
//
// This is the seam that answers "make our own": a module never imports os,
// never touches /proc, and never shells out. It asks the Host, and the Host
// answers only what the module declared.
//
// A Host that is asked for something undeclared returns an error rather than the
// value. Silence would let a missing declaration go unnoticed until production.
type Host interface {
	// Name is the module's own name, for logging.
	Name() string
	// Logger returns a logger already tagged with the module name.
	Logger() *slog.Logger
	// Clock returns the monotonic duration since boot. Requires CapClock.
	Clock() (time.Duration, error)
	// ReadFile reads a path under the module's declared roots. Requires
	// CapFSRead or CapProc or CapSys.
	ReadFile(path string) ([]byte, error)
	// WriteFile writes a path under the module's declared roots. Requires
	// CapFSWrite.
	WriteFile(path string, data []byte, perm os.FileMode) error
	// ReadDir lists a directory under the module's declared roots.
	ReadDir(path string) ([]string, error)
	// Stat reports whether a path under the module's roots exists.
	Stat(path string) (os.FileInfo, error)
	// Getenv reads an environment variable. Requires CapEnv.
	Getenv(key string) (string, error)
	// Heap returns the module's scratch allocator. Always available: scratch
	// memory is not a host privilege, it is the module's own working set.
	Heap() Memory
}

// host is the concrete Host. One per module, holding that module's grants.
type host struct {
	name   string
	module string
	caps   map[Capability]bool
	roots  []string
	log    *slog.Logger

	// now and readEnv are injected so a test Host never touches the real clock
	// or the real process environment.
	now     func() time.Duration
	readEnv func(string) string

	// fs is the underlying reader, injected for the same reason.
	fs hostFS
	// mem is the scratch allocator handed to the module.
	mem Memory
}

// hostFS is the file access a Host performs, isolated so tests can substitute it.
type hostFS interface {
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte, perm os.FileMode) error
	ReadDir(path string) ([]string, error)
	Stat(path string) (os.FileInfo, error)
}

// osHostFS is the real filesystem.
type osHostFS struct{}

func (osHostFS) ReadFile(p string) ([]byte, error)                 { return os.ReadFile(p) }
func (osHostFS) WriteFile(p string, d []byte, m os.FileMode) error { return os.WriteFile(p, d, m) }
func (osHostFS) Stat(p string) (os.FileInfo, error)                { return os.Stat(p) }

// ReadDir returns names rather than os.DirEntry values, so a substituted
// filesystem in a test does not have to reproduce an interface carrying
// filesystem-bound methods.
func (osHostFS) ReadDir(p string) ([]string, error) {
	entries, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out, nil
}

var _ Host = (*host)(nil)

func (h *host) Name() string         { return h.name }
func (h *host) Logger() *slog.Logger { return h.log }
func (h *host) Heap() Memory         { return h.mem }

func (h *host) Clock() (time.Duration, error) {
	if !h.caps[CapClock] {
		return 0, h.denied("clock")
	}
	return h.now(), nil
}

func (h *host) Getenv(key string) (string, error) {
	if !h.caps[CapEnv] {
		return "", h.denied("env")
	}
	return h.readEnv(key), nil
}

func (h *host) ReadFile(path string) ([]byte, error) {
	if !h.caps[CapFSRead] && !h.caps[CapProc] && !h.caps[CapSys] {
		return nil, h.denied("file read")
	}
	if err := h.checkRoot(path); err != nil {
		return nil, err
	}
	return h.fs.ReadFile(path)
}

func (h *host) WriteFile(path string, data []byte, perm os.FileMode) error {
	if !h.caps[CapFSWrite] {
		return h.denied("file write")
	}
	if err := h.checkRoot(path); err != nil {
		return err
	}
	return h.fs.WriteFile(path, data, perm)
}

func (h *host) ReadDir(path string) ([]string, error) {
	if !h.caps[CapFSRead] && !h.caps[CapProc] && !h.caps[CapSys] {
		return nil, h.denied("directory read")
	}
	if err := h.checkRoot(path); err != nil {
		return nil, err
	}
	entries, err := h.fs.ReadDir(path)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e)
	}
	return out, nil
}

func (h *host) Stat(path string) (os.FileInfo, error) {
	if !h.caps[CapFSRead] && !h.caps[CapProc] && !h.caps[CapSys] {
		return nil, h.denied("stat")
	}
	if err := h.checkRoot(path); err != nil {
		return nil, err
	}
	return h.fs.Stat(path)
}

// denied builds a uniform error naming the module, so a log line alone explains
// the refusal without needing to correlate with the registration table.
func (h *host) denied(what string) error {
	return fmt.Errorf("kernel: module %q attempted %s without declaring the capability", h.module, what)
}

// checkRoot enforces the declared roots.
//
// filepath.Clean is applied to both sides so that "..", duplicate separators and
// trailing slashes cannot be used to escape a grant. A prefix comparison alone
// would accept "/procfoo" for a "/proc" grant, and would accept "/proc/../etc"
// for anything at all.
func (h *host) checkRoot(path string) error {
	if len(h.roots) == 0 {
		return fmt.Errorf("kernel: module %q has no filesystem roots declared, so no path is readable", h.module)
	}
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return fmt.Errorf("kernel: module %q asked for relative path %q; absolute paths only", h.module, path)
	}
	for _, root := range h.roots {
		r := filepath.Clean(root)
		if clean == r {
			return nil
		}
		if strings.HasPrefix(clean, r+string(filepath.Separator)) {
			return nil
		}
	}
	return fmt.Errorf("kernel: module %q asked for %q which is outside its declared roots %v", h.module, clean, h.roots)
}

// hostFactory builds the Host for one module.
type hostFactory struct {
	base    *slog.Logger
	now     func() time.Duration
	readEnv func(string) string
	fs      hostFS
	// mem is the scratch allocator handed to every module.
	mem Memory
}

func (f *hostFactory) forModule(desc Descriptor) Host {
	caps := make(map[Capability]bool, len(desc.Capabilities))
	for _, c := range desc.Capabilities {
		caps[c] = true
	}
	// CapLog is implicit: a module that reached a Host can log through it. It is
	// still declared in AllCapabilities so the audit view stays honest.
	caps[CapLog] = true

	return &host{
		name:    desc.Name,
		module:  desc.Name,
		caps:    caps,
		roots:   desc.Roots,
		log:     f.base.With(slog.String("module", desc.Name)),
		now:     f.now,
		readEnv: f.readEnv,
		fs:      f.fs,
		mem:     f.mem,
	}
}

// startTime anchors the monotonic clock. Captured once so Clock reports a
// duration since boot rather than a wall-clock instant.
var startTime = time.Now()

func realClock() time.Duration { return time.Since(startTime) }

// defaultHostFactory builds the factory used when no substrate is injected.
//
// The scratch allocator defaults to the Go heap: a kernel with no arena yet
// still gives its modules working memory, so a module is never blocked on
// kernel plumbing that has not been written yet.
func defaultHostFactory(base *slog.Logger, mem Memory) *hostFactory {
	if base == nil {
		base = slog.Default()
	}
	if mem == nil {
		mem = &goMemory{}
	}
	return &hostFactory{
		base:    base,
		now:     realClock,
		readEnv: os.Getenv,
		fs:      osHostFS{},
		mem:     mem,
	}
}

// goMemory is the fallback scratch allocator over the Go heap.
type goMemory struct{ live, blocks int }

func (g *goMemory) Alloc(n int) ([]byte, bool) {
	if n <= 0 {
		return nil, false
	}
	b := make([]byte, n)
	g.live += n
	g.blocks++
	return b, true
}

func (g *goMemory) Free(b []byte) {
	if b == nil {
		return
	}
	g.live -= len(b)
	g.blocks--
}
