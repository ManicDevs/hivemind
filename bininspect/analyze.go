package bininspect

import (
	"bytes"
	"context"
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// Options configures an Analyzer.
//
// The zero value is valid and useful: it analyses fully with default thresholds.
// Analyzer copies the Options at construction, so the caller may reuse or mutate
// their Options afterwards without affecting a live Analyzer.
type Options struct {
	// MaxScanBytes caps how many bytes are read into memory for string and
	// marker scanning. Zero means defaultScanCap. Opcode scanning of code
	// sections is bounded separately by section size.
	MaxScanBytes int64
	// DisableByteScan skips opcode scanning of executable sections and marker
	// scanning entirely, leaving only header, entropy, and import analysis.
	// Useful for a fast triage pass.
	DisableByteScan bool
	// FullStringScan scans the entire image for markers, not just data
	// sections. Catches markers in regions that are neither clearly data nor
	// clearly code, at the cost of reading the whole file.
	FullStringScan bool
	// Logger, when non-nil, receives a debug line per analysed file. The
	// Analyzer never logs to a global by default.
	Logger Logger
}

// defaultScanCap bounds whole-image marker scanning at 64 MiB, which covers
// every realistic PE without allowing an enormous input to be pulled into
// memory wholesale.
const defaultScanCap = 64 << 20

// Logger is the minimal logging surface the Analyzer needs. It is satisfied by
// *log/slog.Logger, but is declared as an interface so the package does not
// force a logging dependency on its consumers.
type Logger interface {
	Debug(msg string, args ...any)
}

// internalState is the mutable per-analysis state. It is created fresh per call,
// which is what makes concurrent Analyze calls on one Analyzer safe.
type internalState struct {
	warnings []string
	start    time.Time
	opts     Options
	// ctx carries cancellation into the per-section and per-byte scan loops.
	// A large image with FullStringScan set can take long enough that a caller
	// needs a deadline, and a scan that ignores cancellation cannot be bounded.
	ctx context.Context
}

// cancelled reports whether the caller has given up. Scanning loops poll this
// at section granularity, which is frequent enough to bound a scan without
// costing measurable time.
func (s *internalState) cancelled() bool {
	return s.ctx != nil && s.ctx.Err() != nil
}

func (s *internalState) warnf(format string, args ...any) {
	// Cap warnings so a malformed file cannot produce an unbounded report.
	const maxWarnings = 64
	if len(s.warnings) >= maxWarnings {
		return
	}
	s.warnings = append(s.warnings, fmt.Sprintf(format, args...))
}

func (s *internalState) elapsed() time.Duration {
	return time.Since(s.start)
}

func (s *internalState) finish(stats *AnalysisStats) {
	stats.Duration = s.elapsed()
	stats.Warnings = s.warnings
}

// Analyzer inspects binaries for anti-analysis capability.
//
// An Analyzer is immutable after construction and safe for concurrent use by
// multiple goroutines. It holds no cache and no scratch state, so concurrent
// analyses neither contend nor interfere.
type Analyzer struct {
	opts Options
}

// New returns an Analyzer configured by opts.
//
// A nil Logger is fine: the Analyzer then emits no log output, so it does not
// write to the process-global logger behind the caller's back.
func New(opts Options) *Analyzer {
	if opts.MaxScanBytes <= 0 {
		opts.MaxScanBytes = defaultScanCap
	}
	return &Analyzer{opts: opts}
}

// Options returns a copy of the Analyzer's configuration.
func (a *Analyzer) Options() Options { return a.opts }

// AnalyzePath analyses a file on disk.
//
// The file is opened once and read through a single handle, so the bytes hashed
// are the same bytes parsed — there is no time-of-check/time-of-use window
// between identity and content.
func (a *Analyzer) AnalyzePath(ctx context.Context, path string) (*Report, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("bininspect: open %s: %w", path, err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("bininspect: stat %s: %w", path, err)
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("bininspect: %s is a directory", path)
	}

	name := fi.Name()
	modTime := fi.ModTime()
	return a.AnalyzeReaderAt(ctx, f, fi.Size(), name, modTime)
}

// AnalyzeBytes analyses an in-memory image. Convenient for engines that already
// hold the bytes.
func (a *Analyzer) AnalyzeBytes(ctx context.Context, b []byte, name string) (*Report, error) {
	return a.AnalyzeReaderAt(ctx, bytes.NewReader(b), int64(len(b)), name, time.Time{})
}

// AnalyzeReaderAt analyses an image through any io.ReaderAt.
//
// The format is detected by content, not by file extension, so a renamed or
// extension-less sample is still classified correctly.
//
// This is the single entry point both format-specific analyzers are dispatched
// from, which keeps format detection in exactly one place.
func (a *Analyzer) AnalyzeReaderAt(ctx context.Context, r io.ReaderAt, size int64, name string, modTime time.Time) (*Report, error) {
	if ctx == nil {
		return nil, errors.New("bininspect: nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errors.New("bininspect: nil reader")
	}
	if size <= 0 {
		return nil, errors.New("bininspect: empty input")
	}

	st := &internalState{start: time.Now(), opts: a.opts, ctx: ctx}

	magic, err := readMagic(r, size)
	if err != nil {
		return nil, fmt.Errorf("bininspect: read magic: %w", err)
	}

	var rep *Report
	switch {
	case isPEMagic(magic):
		rep, err = analyzePE(ctx, r, size, name, modTime, st)
		if err != nil {
			return nil, err
		}
	case isELFMagic(magic):
		rep, err = analyzeELF(ctx, r, size, name, modTime, st)
		if err != nil {
			return nil, err
		}
	default:
		st.warnf("unrecognised file format: magic %x", magic)
		// Still return identity so the sample is recorded even if unsupported.
		id, ierr := identify(name, io.NewSectionReader(r, 0, size), modTime)
		if ierr != nil {
			return nil, fmt.Errorf("bininspect: identity: %w", ierr)
		}
		rep = &Report{Format: "unknown", Identity: id, Stats: AnalysisStats{BytesHashed: size}}
	}
	// A format analyzer must never return a nil report with a nil error; guard
	// anyway so a future analyzer cannot turn that into a panic here.
	if rep == nil {
		return nil, errors.New("bininspect: analyzer produced no report")
	}

	st.finish(&rep.Stats)
	if rep.Stats.Warnings == nil {
		rep.Stats.Warnings = st.warnings
	}
	if rep.Identity.Name == "" {
		rep.Identity.Name = name
	}
	if a.opts.Logger != nil {
		a.opts.Logger.Debug("bininspect analysis complete",
			"file", name, "format", rep.Format, "findings", len(rep.Findings), "risk", rep.Risk.Score)
	}
	return rep, nil
}

// magicLen is the number of leading bytes needed to classify a file.
const magicLen = 4

func readMagic(r io.ReaderAt, size int64) ([]byte, error) {
	n := int64(magicLen)
	if size < n {
		n = size
	}
	buf := make([]byte, n)
	if _, err := r.ReadAt(buf, 0); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf, nil
}

// isPEMagic reports whether b begins with the "MZ" DOS signature that precedes
// the PE header.
func isPEMagic(b []byte) bool {
	return len(b) >= 2 && b[0] == 'M' && b[1] == 'Z'
}

// isELFMagic reports whether b begins with the ELF \x7fELF signature.
func isELFMagic(b []byte) bool {
	return len(b) >= 4 && b[0] == 0x7F && b[1] == 'E' && b[2] == 'L' && b[3] == 'F'
}

// readRange reads up to n bytes at off, returning a shorter slice at EOF.
//
// Used for marker scanning where a partial read is acceptable: markers found in
// the readable prefix are still valid evidence, and the caller records the
// truncation rather than silently claiming full coverage.
func readRange(r io.ReaderAt, off, n int64) []byte {
	if n <= 0 {
		return nil
	}
	// Guard against a pathological caller asking for the whole universe.
	const hardCap = 1 << 30
	if n > hardCap {
		n = hardCap
	}
	buf := make([]byte, n)
	read, err := r.ReadAt(buf, off)
	if read <= 0 {
		if err != nil && !errors.Is(err, io.EOF) {
			return nil
		}
		return nil
	}
	return buf[:read]
}

// min64 returns the smaller of two int64 values.
func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// AnalyzeFile is a convenience wrapper that analyses path with default options.
func AnalyzeFile(ctx context.Context, path string) (*Report, error) {
	return New(Options{}).AnalyzePath(ctx, path)
}

// AnalyzeBytes analyses an in-memory image with default options.
func AnalyzeBytes(ctx context.Context, b []byte, name string) (*Report, error) {
	return New(Options{}).AnalyzeBytes(ctx, b, name)
}

// ensure elf is referenced even when the ELF analyzer is compiled out in a
// future build; the import documents the format dependency in one place.
var _ = elf.ET_EXEC
