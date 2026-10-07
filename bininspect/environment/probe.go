package environment

import (
	"sort"
	"time"
)

// Snapshot is the complete environmental observation.
//
// Unsupported lists the probes that could not run on this platform, which is
// deliberately part of the output: a verdict with three probes skipped is not
// the same claim as one where all six ran.
type Snapshot struct {
	// ProbedAt is when the snapshot was taken.
	ProbedAt time.Time `json:"probed_at"`
	// Arch is the Go architecture string, e.g. "amd64".
	Arch string `json:"arch"`
	// Indicators are the detected signals.
	Indicators []Indicator `json:"indicators,omitempty"`
	// Evidence groups the raw observations behind them, keyed by probe.
	Evidence map[string][]string `json:"evidence,omitempty"`
	// Unsupported names the probes that returned ErrUnsupported.
	Unsupported []string `json:"unsupported,omitempty"`
	// Summary is a one-line verdict.
	Summary string `json:"summary"`
}

// IsVirtualised reports whether the snapshot found a virtualisation signal.
//
// It is a convenience, not a verdict: a single low-confidence indicator will
// make this true. Callers wanting a judgement should read the indicators.
func (s *Snapshot) IsVirtualised() bool {
	for _, ind := range s.Indicators {
		if ind.Category == CategoryVirtualisation {
			return true
		}
	}
	return false
}

// ForCategory returns the indicators in one category.
func (s *Snapshot) ForCategory(c Category) []Indicator {
	var out []Indicator
	for _, ind := range s.Indicators {
		if ind.Category == c {
			out = append(out, ind)
		}
	}
	return out
}

// Prober inspects the host.
//
// A Prober is safe for concurrent use: its dependencies are read-only after
// construction and every probe writes only to local state.
type Prober struct {
	fs    FileReader
	mono  Clock
	opts  Options
	nowFn func() time.Time
}

// New returns a Prober configured by opts.
//
// A nil FS selects the real filesystem and a nil clock selects the runtime
// monotonic clock. Tests inject both, which is what makes the probe suite
// deterministic in CI: the same synthetic host always yields the same snapshot,
// regardless of whether the runner is a VM.
func New(opts Options) *Prober {
	p := &Prober{
		fs:    opts.FS,
		mono:  opts.Monotonic,
		opts:  opts,
		nowFn: time.Now,
	}
	if p.fs == nil {
		p.fs = osFS{}
	}
	if p.mono == nil {
		p.mono = realMonotonic
	}
	return p
}

// Probe inspects the host and returns a Snapshot.
//
// Individual probe failures never abort the run: each is recorded in Evidence, and
// a probe that cannot run on this platform is listed in Unsupported. A partial
// snapshot is far more useful than none, provided the gaps are visible.
func (p *Prober) Probe() *Snapshot {
	snap := &Snapshot{
		ProbedAt: p.nowFn(),
		Arch:     goArch,
		Evidence: make(map[string][]string),
	}

	add := func(name string, inds []Indicator, ev []string) {
		if len(ev) > 0 {
			snap.Evidence[name] = ev
		}
		snap.Indicators = append(snap.Indicators, inds...)
	}

	// Pure-Go portable probes. These run everywhere and carry most of the value
	// on Linux.
	if inds, ev, _ := probeDMI(p.fs); len(inds) > 0 {
		add("dmi", inds, ev)
	}
	if inds, ev := probeACPI(p.fs); len(inds) > 0 {
		add("acpi", inds, ev)
	}
	if inds, ev := probeCgroup(p.fs); len(inds) > 0 {
		add("cgroup", inds, ev)
	}
	if inds, ev := probeMACAddresses(p.fs); len(inds) > 0 {
		add("mac", inds, ev)
	}
	if inds, ev := probePCI(p.fs); len(inds) > 0 {
		add("pci", inds, ev)
	}
	if inds, ev := probeVirtioBound(p.fs); len(inds) > 0 {
		add("virtio", inds, ev)
	}
	if inds, ev := probeSMBIOS(p.fs); len(inds) > 0 {
		add("smbios", inds, ev)
	}
	// The pure-Go replacement for reading the CPUID hypervisor bit: the kernel
	// publishes that bit as a cpuinfo flag, so no instruction is executed.
	if hypervisor, ev := probeHypervisorFlag(p.fs); hypervisor {
		add("hypervisor-flag", []Indicator{{
			ID:         "ENV-VM-CPUINFO-HYPERVISOR",
			Category:   CategoryVirtualisation,
			Severity:   SeverityMedium,
			Confidence: 0.70,
			Title:      "Kernel reports a hypervisor CPU flag",
			Evidence:   ev,
		}}, ev)
	}
	if inds, ev := probeEnvironmentSignals(p.fs); len(inds) > 0 {
		add("instrumentation", inds, ev)
	}
	if inds, ev := probeTracerFiles(p.fs); len(inds) > 0 {
		add("tracer", inds, ev)
	}

	// Platform gate. Off Linux the filesystem probes above find nothing, so the
	// snapshot records that the probe set was unavailable rather than reporting
	// a clean host.
	if !platformSupported() {
		snap.Unsupported = append(snap.Unsupported, "filesystem-probes")
	}

	if !p.opts.SkipTiming {
		if inds, ev := probeTimingSamples(p.mono); len(inds) > 0 || len(ev) > 0 {
			add("timing", inds, ev)
		}
	}

	snap.summarise()
	return snap
}

// summarise computes the one-line verdict and orders the indicators.
//
// Indicators are sorted by severity then confidence so a reader sees the most
// significant signal first without having to scan the whole list.
func (s *Snapshot) summarise() {
	rank := map[Severity]int{
		SeverityCritical: 5, SeverityHigh: 4, SeverityMedium: 3,
		SeverityLow: 2, SeverityInfo: 1,
	}
	sort.SliceStable(s.Indicators, func(i, j int) bool {
		if rank[s.Indicators[i].Severity] != rank[s.Indicators[j].Severity] {
			return rank[s.Indicators[i].Severity] > rank[s.Indicators[j].Severity]
		}
		return s.Indicators[i].Confidence > s.Indicators[j].Confidence
	})

	// Aggregate confidence is deliberately saturating rather than additive: two
	// weak hints should not combine into a certainty.
	total := 0.0
	for _, ind := range s.Indicators {
		total += ind.Confidence
	}
	aggregate := 1 - (1 - minf(total, 0.99)) // never reaches 1.0

	verdict := "no virtualisation signal detected"
	switch {
	case len(s.Indicators) == 0:
		verdict = "no environmental signals detected"
	case aggregate >= 0.85:
		verdict = "host is very likely virtualised or sandboxed"
	case aggregate >= 0.55:
		verdict = "host shows virtualisation or sandbox signals"
	case aggregate >= 0.25:
		verdict = "host shows weak environmental signals"
	default:
		verdict = "host shows only incidental signals"
	}
	if len(s.Unsupported) > 0 {
		verdict += " (" + itoa(int64(len(s.Unsupported))) + " probe(s) unsupported)"
	}
	s.Summary = verdict
}

// minf is a local min for float64, avoiding a math import for one call.
func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// goArch is set by the build-tagged files so the snapshot records which
// architecture's probe set actually ran.
var goArch string
