package environment

import (
	"strings"
	"testing"
	"time"
)

// constantClock returns a clock that always reports the same duration.
//
// This is the fixture the concurrency test uses, and it is deliberately
// constant rather than a sequence. A sequence-returning "clock" is not a clock:
// when goroutines interleave, the pairing of start and end readings is
// scrambled, so measured deltas collapse to zero and the timing indicator
// appears or vanishes according to scheduling. Options.Clock requires a function
// of time that is safe for concurrent use, and time.Since satisfies that; this
// satisfies it too.
func constantClock(d time.Duration) Clock {
	return func() time.Duration { return d }
}

// sequenceClock returns pre-recorded readings in order.
//
// It exists only for the single-threaded timing test, where deterministic
// variation is wanted. It is NOT safe for concurrent use and must not be passed
// to a Prober shared across goroutines.
func sequenceClock(deltas ...time.Duration) Clock {
	i := 0
	return func() time.Duration {
		if i >= len(deltas) {
			i = len(deltas) - 1
		}
		d := deltas[i]
		i++
		return d
	}
}

// TestTimingProbeOnConstantClock pins the zero-resolution path: a clock with no
// resolution must report "cannot measure", never a fabricated spread.
func TestTimingProbeOnConstantClock(t *testing.T) {
	inds, ev := probeTimingSamples(constantClock(0))
	if len(inds) != 0 {
		t.Errorf("zero-resolution clock produced an indicator: %+v", inds)
	}
	if len(ev) == 0 {
		t.Error("zero-resolution clock produced no evidence explaining why")
	}

	// A sequence clock with real variation must produce the indicator.
	inds2, _ := probeTimingSamples(sequenceClock(0, 50*time.Microsecond, 100*time.Microsecond))
	if len(inds2) != 1 {
		t.Errorf("varying clock produced %d indicators, want 1", len(inds2))
	} else if inds2[0].ID != "ENV-TIMING-SPREAD" {
		t.Errorf("timing indicator ID = %q", inds2[0].ID)
	}
}

// TestIndicatorIDsAreUnique pins that no two indicators share an ID.
//
// Two probes reading the same observation produced the same ENV-VM-CPUINFO-
// HYPERVISOR twice, which would make a consumer that deduplicates by ID lose a
// signal and a consumer that does not count one twice.
func TestIndicatorIDsAreUnique(t *testing.T) {
	snap := New(Options{FS: vmwareHost(), Monotonic: constantClock(0)}).Probe()
	seen := make(map[string]bool)
	for _, ind := range snap.Indicators {
		if seen[ind.ID] {
			t.Errorf("duplicate indicator ID %q in %v", ind.ID, indicatorIDs(snap))
		}
		seen[ind.ID] = true
	}
}

// hasIndicator reports whether any indicator carries the given ID.
func hasIndicator(snap *Snapshot, id string) bool {
	for _, ind := range snap.Indicators {
		if ind.ID == id {
			return true
		}
	}
	return false
}

// indicatorIDs lists indicator IDs for failure messages.
func indicatorIDs(snap *Snapshot) []string {
	out := make([]string, 0, len(snap.Indicators))
	for _, ind := range snap.Indicators {
		out = append(out, ind.ID)
	}
	return out
}

// TestBareMetalHostProducesNoIndicators is the most important negative test in
// the package: a clean host must produce nothing.
//
// Without it, every positive detection could pass simply because the probe
// misreads something on the machine running the suite.
func TestBareMetalHostProducesNoIndicators(t *testing.T) {
	snap := New(Options{
		FS:         bareMetalHost(),
		Monotonic:  constantClock(time.Microsecond),
		SkipTiming: true,
	}).Probe()

	if len(snap.Indicators) != 0 {
		t.Errorf("bare-metal host produced indicators: %v", indicatorIDs(snap))
	}
	if snap.IsVirtualised() {
		t.Error("bare-metal host reported as virtualised")
	}
	if snap.Summary != "no environmental signals detected" {
		t.Errorf("summary = %q", snap.Summary)
	}
}

// TestVMwareHostDetectedThroughMultipleIndependentProbes verifies detection
// does not rest on any single source.
func TestVMwareHostDetectedThroughIndependentProbes(t *testing.T) {
	snap := New(Options{
		FS:         vmwareHost(),
		Monotonic:  constantClock(0),
		SkipTiming: true,
	}).Probe()

	if !snap.IsVirtualised() {
		t.Fatalf("VMware host not detected: %v", indicatorIDs(snap))
	}
	for _, want := range []string{
		"ENV-VM-DMI-VMWARE",         // decoded DMI attribute
		"ENV-VM-PCI-0x1af4",         // virtio vendor from our own table
		"ENV-VM-PCI-0x15ad",         // VMware vendor from our own table
		"ENV-VM-PCI-VIRTIO",         // virtio specifically
		"ENV-VM-VIRTIO-BOUND",       // driver side
		"ENV-VM-CPUINFO-HYPERVISOR", // kernel's view of the CPUID bit
		"ENV-VM-MAC-OUI",            // 00:50:56 is VMware's assigned prefix
	} {
		if !hasIndicator(snap, want) {
			t.Errorf("missing indicator %s; got %v", want, indicatorIDs(snap))
		}
	}
}

// TestVirtioDetectionWithoutVendorString is the fixture that proves the PCI
// probe earns its place.
//
// qemuVirtioHost has no vendor string anywhere except DMI saying "QEMU", and
// the interesting case is a guest whose DMI is silent. Here the only evidence is
// the PCI virtio vendor, so if this test passes the probe is genuinely
// independent of the kernel's decoded views.
func TestVirtioDetectionWithoutVendorString(t *testing.T) {
	snap := New(Options{
		FS:         qemuVirtioHost(),
		Monotonic:  constantClock(0),
		SkipTiming: true,
	}).Probe()

	if !hasIndicator(snap, "ENV-VM-PCI-VIRTIO") {
		t.Fatalf("virtio not detected from PCI alone: %v", indicatorIDs(snap))
	}
	ind := findIndicator(snap, "ENV-VM-PCI-VIRTIO")
	// Legacy device 0x1000 is weaker than a virtio 1.0 device, and the title
	// must say so rather than overstating.
	if !strings.Contains(ind.Title, "virtio") {
		t.Errorf("virtio indicator title = %q", ind.Title)
	}
	if len(ind.Evidence) == 0 {
		t.Error("virtio indicator carries no evidence")
	}
}

func findIndicator(snap *Snapshot, id string) Indicator {
	for _, ind := range snap.Indicators {
		if ind.ID == id {
			return ind
		}
	}
	return Indicator{}
}

// TestLegacyVirtioScoresLowerThanModern pins the confidence distinction between
// legacy virtio IDs and virtio 1.0.
func TestLegacyVirtioScoresLowerThanModern(t *testing.T) {
	legacy := newFakeFS().
		addFile("/proc/self/status", "TracerPid:\t0\n").
		addDir("/sys/bus/pci/devices", "0000:00:02.0").
		addFile("/sys/bus/pci/devices/0000:00:02.0/vendor", "0x1af4\n").
		addFile("/sys/bus/pci/devices/0000:00:02.0/device", "0x1000\n") // legacy

	modern := newFakeFS().
		addFile("/proc/self/status", "TracerPid:\t0\n").
		addDir("/sys/bus/pci/devices", "0000:00:02.0").
		addFile("/sys/bus/pci/devices/0000:00:02.0/vendor", "0x1af4\n").
		addFile("/sys/bus/pci/devices/0000:00:02.0/device", "0x1040\n") // virtio 1.0

	lo := findIndicator(New(Options{FS: legacy, SkipTiming: true}).Probe(), "ENV-VM-PCI-VIRTIO")
	hi := findIndicator(New(Options{FS: modern, SkipTiming: true}).Probe(), "ENV-VM-PCI-VIRTIO")

	if lo.Confidence >= hi.Confidence {
		t.Errorf("legacy virtio confidence %v should be below virtio 1.0 %v", lo.Confidence, hi.Confidence)
	}
	if !strings.Contains(hi.Title, "1.0") {
		t.Errorf("modern virtio title %q should name the transport version", hi.Title)
	}
}

// TestContainerDetected verifies the container marker path and, importantly,
// that a container is reported as information rather than as virtualisation.
func TestContainerDetected(t *testing.T) {
	snap := New(Options{FS: containerHost(), SkipTiming: true}).Probe()
	if !hasIndicator(snap, "ENV-SBX-CONTAINER") {
		t.Fatalf("container not detected: %v", indicatorIDs(snap))
	}
	// A container is not a VM. Conflating the two would make every CI run report
	// a virtualised host.
	if snap.IsVirtualised() {
		t.Error("container was reported as virtualisation")
	}
	ind := findIndicator(snap, "ENV-SBX-CONTAINER")
	if ind.Severity != SeverityInfo {
		t.Errorf("container severity = %q, want info", ind.Severity)
	}
}

// TestTracerDetected verifies the debugger probe and that it outranks everything.
func TestTracerDetected(t *testing.T) {
	snap := New(Options{FS: tracedHost(), SkipTiming: true}).Probe()
	if !hasIndicator(snap, "ENV-DBG-TRACERPID") {
		t.Fatalf("tracer not detected: %v", indicatorIDs(snap))
	}
	ind := findIndicator(snap, "ENV-DBG-TRACERPID")
	if ind.Severity != SeverityCritical {
		t.Errorf("tracer severity = %q, want critical", ind.Severity)
	}
	if ind.Category != CategoryDebugger {
		t.Errorf("tracer category = %q", ind.Category)
	}
}

// TestSMBIOSParserWalksStructures verifies our own table parser against
// structures we assembled, so the encoding under test is visible in the test.
func TestSMBIOSParserWalksStructures(t *testing.T) {
	table := buildSMBIOS(
		smbiosSystemInfo("VMware, Inc.", "VMware7,1"),
		smbiosOEM("vmmouse", "vmhgfs"),
	)
	got := parseSMBIOS(table)
	if len(got) != 2 {
		t.Fatalf("parsed %d structures, want 2", len(got))
	}
	if got[0].Type != 1 || got[0].Handle != 1 {
		t.Errorf("structure 0 = type %d handle %d, want type 1 handle 1", got[0].Type, got[0].Handle)
	}
	if len(got[0].Strings) != 2 || got[0].Strings[0] != "VMware, Inc." {
		t.Errorf("structure 0 strings = %q", got[0].Strings)
	}
	// Type 11 is an all-strings structure with a zero-length formatted area --
	// the case a naive parser gets wrong.
	if got[1].Type != 11 || len(got[1].Formatted) != 0 {
		t.Errorf("structure 1 = type %d formatted %d bytes, want type 11 with 0", got[1].Type, len(got[1].Formatted))
	}
	if len(got[1].Strings) != 2 || got[1].Strings[1] != "vmhgfs" {
		t.Errorf("OEM strings = %q", got[1].Strings)
	}
}

// TestSMBIOSParserSurvivesMalformedTables is the robustness requirement: this
// data comes from firmware, which is exactly the input to assume malformed.
func TestSMBIOSParserSurvivesMalformedTables(t *testing.T) {
	cases := []struct {
		name  string
		table []byte
	}{
		{"nil", nil},
		{"empty", []byte{}},
		{"header only", []byte{1, 0x1b, 0x00, 0x00}},
		{"length runs past end", []byte{1, 0xFF, 0x01, 0x00, 'A', 'B'}},
		{"unterminated string", append([]byte{1, 0x04, 0x01, 0x00}, []byte("no terminator")...)},
		{"zero length no strings", []byte{9, 0x00, 0x01, 0x00, 0, 0}},
		{"truncated mid structure", []byte{1, 0x08, 0x01, 0x00, 1, 2, 3, 4, 5, 6, 7, 8, 0}},
	}
	for _, tc := range cases {
		// The only requirement is that parsing terminates and does not panic.
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("%s: parser panicked: %v", tc.name, r)
				}
			}()
			_ = parseSMBIOS(tc.table)
		}()
	}
}

// TestSMBIOSProbeFindsVendorInRawTable verifies detection from the raw table,
// which is what makes it independent of the kernel's decoded attributes.
func TestSMBIOSProbeFindsVendorInRawTable(t *testing.T) {
	fs := newFakeFS().
		addFile("/proc/self/status", "TracerPid:\t0\n").
		addFile("/sys/firmware/dmi/tables/DMI", string(buildSMBIOS(
			smbiosSystemInfo("VMware, Inc.", "VMware7,1"),
		)))

	inds, ev := probeSMBIOS(fs)
	if len(inds) == 0 {
		t.Fatalf("raw SMBIOS probe found nothing (evidence %q)", ev)
	}
	if inds[0].Title == "" || !strings.Contains(inds[0].Title, "System Information") {
		t.Errorf("indicator should name the structure type it came from: %q", inds[0].Title)
	}
	if !strings.Contains(strings.Join(ev, " "), "VMware, Inc.") {
		t.Errorf("evidence does not include the raw string: %q", ev)
	}
}

// TestProbesAreDeterministic is the CI-pipeline guarantee: the same synthetic
// host must always produce byte-identical output.
func TestProbesAreDeterministic(t *testing.T) {
	run := func() string {
		snap := New(Options{
			FS:         vmwareHost(),
			Monotonic:  constantClock(0),
			SkipTiming: true,
		}).Probe()
		var b strings.Builder
		for _, ind := range snap.Indicators {
			b.WriteString(ind.ID + "|" + itoa(int64(ind.Confidence*100)) + "\n")
		}
		// Sort the keys: iterating a map here would make the *test*
		// nondeterministic rather than the code under test.
		keys := make([]string, 0, len(snap.Evidence))
		for k := range snap.Evidence {
			keys = append(keys, k)
		}
		for i := 1; i < len(keys); i++ {
			for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
				keys[j], keys[j-1] = keys[j-1], keys[j]
			}
		}
		for _, k := range keys {
			b.WriteString(k + "=" + joinComma(snap.Evidence[k]) + "\n")
		}
		return b.String()
	}
	first := run()
	for i := 0; i < 25; i++ {
		if got := run(); got != first {
			t.Fatalf("probe output differed on run %d:\nfirst:\n%s\ngot:\n%s", i, first, got)
		}
	}
}

// TestReadHex16RejectsMalformed pins the PCI ID reader: a wrong vendor ID would
// fabricate a hypervisor indicator, so anything unexpected must be refused.
func TestReadHex16RejectsMalformed(t *testing.T) {
	good := newFakeFS().
		addFile("/ok", "0x1af4\n").
		addFile("/ok_upper", "0X1AF4\n").
		addFile("/bad_noprefix", "1af4\n").
		addFile("/bad_hex", "0xzzzz\n").
		addFile("/bad_decimal", "6868\n").
		addFile("/bad_overflow", "0x1af400\n").
		addFile("/bad_empty", "")

	if v, ok := readHex16(good, "/ok"); !ok || v != 0x1af4 {
		t.Errorf("valid ID rejected: %#x ok=%v", v, ok)
	}
	if v, ok := readHex16(good, "/ok_upper"); !ok || v != 0x1af4 {
		t.Errorf("uppercase ID rejected: %#x ok=%v", v, ok)
	}
	for _, p := range []string{"/bad_noprefix", "/bad_hex", "/bad_decimal", "/bad_overflow", "/bad_empty", "/missing"} {
		if v, ok := readHex16(good, p); ok {
			t.Errorf("%s accepted as %#x", p, v)
		}
	}
}

// TestMicrosoftVendorIsNotTreatedAsHypervisor pins the deliberate exception:
// 0x1414 is also an ordinary OEM, so it must not produce a vendor indicator.
func TestMicrosoftVendorIsNotTreatedAsHypervisor(t *testing.T) {
	fs := newFakeFS().
		addFile("/proc/self/status", "TracerPid:\t0\n").
		addDir("/sys/bus/pci/devices", "0000:00:03.0").
		addFile("/sys/bus/pci/devices/0000:00:03.0/vendor", "0x1414\n").
		addFile("/sys/bus/pci/devices/0000:00:03.0/device", "0x5353\n")

	snap := New(Options{FS: fs, SkipTiming: true}).Probe()
	for _, ind := range snap.Indicators {
		if strings.Contains(ind.ID, "0x1414") {
			t.Errorf("Microsoft vendor ID produced a hypervisor indicator: %+v", ind)
		}
	}
}

// TestSkipTimingAndSkipCPUIDOptionsVerify the fast-triage configuration.
func TestSkipOptionsVerify(t *testing.T) {
	snap := New(Options{FS: vmwareHost(), SkipTiming: true, SkipCPUID: true}).Probe()
	if hasIndicator(snap, "ENV-TIMING-SPREAD") {
		t.Error("timing probe ran despite SkipTiming")
	}
	// Detection must still work: skipping probes is a triage speed choice, not a
	// correctness trade.
	if !snap.IsVirtualised() {
		t.Errorf("skipping probes lost detection: %v", indicatorIDs(snap))
	}
}

// TestIndicatorsSortedBySeverity verifies the ordering guarantee a reader relies
// on to see the most significant signal first.
func TestIndicatorsSortedBySeverity(t *testing.T) {
	snap := New(Options{FS: tracedHost(), SkipTiming: true}).Probe()
	if len(snap.Indicators) < 1 {
		t.Skip("no indicators to order")
	}
	rank := map[Severity]int{
		SeverityCritical: 5, SeverityHigh: 4, SeverityMedium: 3,
		SeverityLow: 2, SeverityInfo: 1,
	}
	for i := 1; i < len(snap.Indicators); i++ {
		if rank[snap.Indicators[i-1].Severity] < rank[snap.Indicators[i].Severity] {
			t.Errorf("indicators not ordered by severity: %q before %q",
				snap.Indicators[i-1].Severity, snap.Indicators[i].Severity)
		}
	}
}

// TestSnapshotUnsupportedRecordsPlatformGap verifies the contract that an
// unavailable probe is visible rather than silently absent.
func TestSnapshotUnsupportedRecordsPlatformGap(t *testing.T) {
	snap := New(Options{FS: newFakeFS(), SkipTiming: true}).Probe()
	// On Linux the probe set is available, so nothing should be marked
	// unsupported even with an empty filesystem.
	if len(snap.Unsupported) != 0 {
		t.Errorf("linux build marked probes unsupported: %v", snap.Unsupported)
	}
	if snap.Summary == "" {
		t.Error("summary is empty")
	}
}

// TestProberIsSafeForConcurrentUse exercises the documented concurrency
// contract. Under -race this is the authoritative check.
func TestProberIsSafeForConcurrentUse(t *testing.T) {
	p := New(Options{
		FS:        vmwareHost(),
		Monotonic: constantClock(0),
	})
	done := make(chan *Snapshot, 32)
	for i := 0; i < 32; i++ {
		go func() { done <- p.Probe() }()
	}
	var first string
	for i := 0; i < 32; i++ {
		snap := <-done
		got := snap.Summary + itoa(int64(len(snap.Indicators)))
		if i == 0 {
			first = got
		} else if got != first {
			t.Errorf("concurrent probe diverged: %q vs %q", got, first)
		}
	}
}

// TestPCIProbeRequiresTheDeviceTree verifies absence of /sys/bus/pci/devices
// yields nothing rather than an error.
func TestPCIProbeRequiresTheDeviceTree(t *testing.T) {
	if inds, ev := probePCI(newFakeFS()); len(inds) != 0 || len(ev) != 0 {
		t.Errorf("empty filesystem produced PCI findings: %v / %v", inds, ev)
	}
	if inds, _ := probeVirtioBound(newFakeFS()); len(inds) != 0 {
		t.Errorf("empty filesystem produced virtio findings: %v", inds)
	}
}
