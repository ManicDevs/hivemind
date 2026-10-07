package environment

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// osFS is the real filesystem, used when Options.FS is nil.
type osFS struct{}

func (osFS) ReadFile(p string) ([]byte, error) { return os.ReadFile(p) }
func (osFS) ReadDir(p string) ([]string, error) {
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
func (osFS) Exists(p string) bool { _, err := os.Stat(p); return err == nil }

// realMonotonic is the runtime monotonic clock, which is immune to wall-clock
// adjustment and therefore the right basis for a timing probe.
func realMonotonic() time.Duration {
	// time.Since on a monotonic base. Wrapping time.Now().Sub(time.Now()) would
	// be equivalent but this reads more clearly at the call site.
	return time.Since(startTime)
}

// startTime is captured once at init; time.Since against it is monotonic.
var startTime = time.Now()

// dmiPaths are the Linux DMI attribute locations. They are the highest-value
// VM signal available without any instruction: a hypervisor names itself here.
var dmiPaths = map[string]string{
	"sys_vendor":        "/sys/class/dmi/id/sys_vendor",
	"product_name":      "/sys/class/dmi/id/product_name",
	"product_version":   "/sys/class/dmi/id/product_version",
	"bios_vendor":       "/sys/class/dmi/id/bios_vendor",
	"board_vendor":      "/sys/class/dmi/id/board_vendor",
	"chassis_asset_tag": "/sys/class/dmi/id/chassis_asset_tag",
}

// dmiKeys is dmiPaths in a fixed iteration order.
var dmiKeys = func() []string {
	out := make([]string, 0, len(dmiPaths))
	for k := range dmiPaths {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}()

// probeDMI reads the firmware DMI attributes and reports any hypervisor vendor.
//
// This is the strongest single signal on Linux: a bare-metal machine has no
// hypervisor to name itself, so a vendor string here is near-conclusive.
func probeDMI(fs FileReader) ([]Indicator, []string, error) {
	var raw []string
	// Iterate the paths in a fixed order, not map order. Go randomises map
	// iteration per run, so building evidence from the map directly made the
	// same host produce different ordering on every call -- which defeats
	// golden-file testing and makes two snapshots of an unchanged machine
	// compare unequal.
	for _, key := range dmiKeys {
		b, err := fs.ReadFile(dmiPaths[key])
		if err != nil {
			continue // attribute absent on this kernel or platform
		}
		v := strings.TrimSpace(string(b))
		if v == "" {
			continue
		}
		raw = append(raw, key+"="+v)
	}
	if len(raw) == 0 {
		return nil, nil, nil
	}

	joined := strings.ToLower(strings.Join(raw, " "))
	var out []Indicator
	for _, sig := range hypervisorSignatures {
		if strings.Contains(joined, sig.match) {
			conf := sig.confidence
			out = append(out, Indicator{
				ID:         sig.id,
				Category:   CategoryVirtualisation,
				Severity:   severityFor(conf),
				Confidence: conf,
				Title:      "Hypervisor identified in DMI: " + sig.name,
				Evidence:   raw,
			})
		}
	}
	return out, raw, nil
}

// hypervisorSignature is one DMI-based vendor match.
type hypervisorSignature struct {
	id         string
	name       string
	match      string // lowercase substring
	confidence float64
}

// hypervisorSignatures is deliberately specific. A generic term like "virtual"
// would fire on unrelated firmware strings; each entry below is a vendor or
// product string that appears on that hypervisor and essentially nowhere else.
var hypervisorSignatures = []hypervisorSignature{
	{"ENV-VM-DMI-VMWARE", "VMware", "vmware", 0.92},
	{"ENV-VM-DMI-VBOX", "VirtualBox", "innotek", 0.92},
	{"ENV-VM-DMI-VBOX2", "VirtualBox", "virtualbox", 0.90},
	{"ENV-VM-DMI-QEMU", "QEMU/KVM", "qemu", 0.90},
	{"ENV-VM-DMI-KVM", "KVM", "kvm", 0.80},
	{"ENV-VM-DMI-XEN", "Xen", "xen", 0.85},
	{"ENV-VM-DMI-HYPERV", "Hyper-V", "microsoft corporation", 0.55}, // ambiguous: also the OEM
	{"ENV-VM-DMI-PARALLELS", "Parallels", "parallels", 0.92},
	{"ENV-VM-DMI-BOCHS", "Bochs", "bochs", 0.88},
	{"ENV-VM-DMI-ANUBIS", "Anubis", "anubis", 0.85},
	{"ENV-VM-DMI-CUCKOO", "Cuckoo", "cuckoo", 0.90},
}

// probeCPUFlags is retained only as the evidence source for the hypervisor flag.
// The indicator itself is emitted by probeHypervisorFlag in probes_linux.go, so
// the /proc/cpuinfo hypervisor flag produces exactly one Indicator rather than
// one per probe that happens to read the same line.
//
// Two probes reporting the same ID is a real defect, not a cosmetic one: a
// consumer that deduplicates by ID silently loses one, and a consumer that does
// not sees the same signal counted twice.
// probeACPI looks for ACPI tables that only exist on virtual platforms.
//
// Absence is not evidence of bare metal: ACPI is simply missing on many
// embedded and older kernels. These probes therefore only ever produce a
// positive indicator.
func probeACPI(fs FileReader) ([]Indicator, []string) {
	var evidence []string
	if names, err := fs.ReadDir("/sys/firmware/acpi/tables"); err == nil && len(names) > 0 {
		evidence = append(evidence, "ACPI tables present: "+strings.Join(names, ","))
	}
	if fs.Exists("/sys/firmware/efi/efivars") {
		evidence = append(evidence, "EFI variables present")
	}
	if len(evidence) == 0 {
		return nil, nil
	}
	return []Indicator{{
		ID:         "ENV-VM-ACPI",
		Category:   CategoryVirtualisation,
		Severity:   SeverityLow,
		Confidence: 0.25, // weak on its own; ACPI is not a VM signal
		Title:      "ACPI/EFI firmware tables present",
		Evidence:   evidence,
	}}, evidence
}

// probeCgroup looks for container or sandbox runtime markers.
//
// A container is not a VM, but "this analysis is running inside a sandbox
// container" is directly relevant to triage: it bounds what the host can tell
// us and is worth recording as a caveat rather than a finding.
func probeCgroup(fs FileReader) ([]Indicator, []string) {
	var evidence []string
	for _, p := range []string{
		"/run/.containerenv",     // Podman
		"/.dockerenv",            // Docker
		"/run/systemd/container", // systemd-nspawn
	} {
		if fs.Exists(p) {
			evidence = append(evidence, "container marker: "+p)
		}
	}
	if b, err := fs.ReadFile("/proc/1/cgroup"); err == nil {
		s := strings.TrimSpace(string(b))
		for _, marker := range []string{"docker", "lxc", "kubepods", "containerd", "podman"} {
			if strings.Contains(s, marker) {
				evidence = append(evidence, "cgroup mentions "+marker)
				break
			}
		}
	}
	if len(evidence) == 0 {
		return nil, nil
	}
	return []Indicator{{
		ID:         "ENV-SBX-CONTAINER",
		Category:   CategorySandbox,
		Severity:   SeverityInfo,
		Confidence: 0.40,
		Title:      "Running inside a container",
		Evidence:   evidence,
	}}, evidence
}

// macPrefixes maps OUI prefixes to the vendor they belong to. The first three
// bytes of a MAC are the vendor-assigned part, so a virtual adapter's prefix
// identifies the hypervisor even when the guest driver is paravirtualised.
var macPrefixes = map[string]string{
	"00:0c:29": "VMware",
	"00:50:56": "VMware",
	"00:05:69": "VMware",
	"08:00:27": "VirtualBox",
	"0a:00:27": "VirtualBox",
	"52:54:00": "QEMU/KVM",
	"16:3e":    "Xen",
	"00:1c:42": "Parallels",
	"00:15:5d": "Hyper-V",
}

// probeMACAddresses enumerates network interfaces and reports vendor OUI
// prefixes.
//
// Linux exposes this through /sys/class/net/<iface>/address, which avoids
// netlink and therefore keeps the probe injectable and testable.
func probeMACAddresses(fs FileReader) ([]Indicator, []string) {
	ifaces, err := fs.ReadDir("/sys/class/net")
	if err != nil {
		return nil, nil
	}
	counts := make(map[string]int)
	var evidence []string
	for _, iface := range ifaces {
		b, rerr := fs.ReadFile(filepath.Join("/sys/class/net", iface, "address"))
		if rerr != nil {
			continue
		}
		addr := strings.ToLower(strings.TrimSpace(string(b)))
		if len(addr) < 8 {
			continue
		}
		prefix := addr[:8]
		if vendor, ok := macPrefixes[prefix]; ok {
			counts[vendor]++
			evidence = append(evidence, iface+"="+addr+" ("+vendor+" OUI)")
		}
	}
	if len(evidence) == 0 {
		return nil, nil
	}

	// One virtual-looking adapter is suggestive; several independent ones is
	// stronger, because a host with a single bridged interface is common.
	conf := 0.60
	if len(evidence) >= 2 {
		conf = 0.80
	}
	return []Indicator{{
		ID:         "ENV-VM-MAC-OUI",
		Category:   CategoryVirtualisation,
		Severity:   severityFor(conf),
		Confidence: conf,
		Title:      "Network adapter carries a hypervisor OUI prefix",
		Evidence:   evidence,
	}}, evidence
}

// debuggerEnvVars are environment variables that only a debugger or a
// language-level instrumentation framework sets.
var debuggerEnvVars = map[string]string{
	"DYLD_INSERT_LIBRARIES": "DYLD_INSERT_LIBRARIES (Mach injection)",
	"LD_PRELOAD":            "LD_PRELOAD (interposition)",
	"GDB":                   "GDB",
	"LINES":                 "LINES (GDB/LLDB)",
	"LLDB":                  "LLDB",
	"NODE_OPTIONS":          "NODE_OPTIONS (inspector)",
	"PYTHONPATH":            "PYTHONPATH (possible debugger injection)",
	"GOFLAGS":               "GOFLAGS",
}

// instrumentationLibraries are shared objects whose presence indicates a
// hooking or sandboxing framework rather than an ordinary program.
var instrumentationLibraries = []struct {
	path string
	name string
}{
	{"/usr/lib/x86_64-linux-gnu/libsandboxie.so", "Sandboxie"},
	{"/usr/local/lib/libsandboxie.so", "Sandboxie"},
	{"/opt/Cuckoo/cuckoo/analysis/linux/lib/sandbox/linux_intel64.so", "Cuckoo Sandbox"},
	{"/tmp/.cuckoo/agent/lib/cuckoo/libcuckoo.so", "Cuckoo Sandbox"},
	{"/usr/lib/frida/frida-agent.so", "Frida"},
	{"/tmp/frida-gadget.so", "Frida Gadget"},
	{"/usr/lib/libdetours.so", "Microsoft Detours"},
	{"/usr/local/lib/libdetours.so", "Microsoft Detours"},
	{"/usr/lib/i386-linux-gnu/libvboxsf.so", "VirtualBox guest additions"},
	{"/usr/lib/x86_64-linux-gnu/libvboxsf.so", "VirtualBox guest additions"},
	{"/usr/bin/vmtoolsd", "VMware Tools"},
	{"/usr/bin/VBoxService", "VirtualBox Guest Additions"},
}

// probeEnvironmentSignals checks for debugger env vars and instrumentation
// libraries.
func probeEnvironmentSignals(fs FileReader) ([]Indicator, []string) {
	var evidence []string
	for _, key := range sortedKeys(debuggerEnvVars) {
		if os.Getenv(key) != "" {
			evidence = append(evidence, key+" is set ("+debuggerEnvVars[key]+")")
		}
	}
	for _, lib := range instrumentationLibraries {
		if fs.Exists(lib.path) {
			evidence = append(evidence, "instrumentation library present: "+lib.path+" ("+lib.name+")")
		}
	}
	if len(evidence) == 0 {
		return nil, nil
	}
	return []Indicator{{
		ID:         "ENV-INSTR-LIBRARIES",
		Category:   CategoryInstrumentation,
		Severity:   SeverityMedium,
		Confidence: 0.75,
		Title:      "Debugging or instrumentation artefacts present",
		Evidence:   evidence,
	}}, evidence
}

// probeTracerFiles looks at /proc/self/status for a tracee, which is the
// cheapest reliable answer to "is a debugger attached".
func probeTracerFiles(fs FileReader) ([]Indicator, []string) {
	b, err := fs.ReadFile("/proc/self/status")
	if err != nil {
		return nil, nil
	}
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(ln, "TracerPid:") {
			pid := strings.TrimSpace(strings.TrimPrefix(ln, "TracerPid:"))
			if pid != "0" && pid != "" {
				return []Indicator{{
					ID:         "ENV-DBG-TRACERPID",
					Category:   CategoryDebugger,
					Severity:   SeverityCritical,
					Confidence: 0.95,
					Title:      "A tracer is attached to this process (pid " + pid + ")",
					Evidence:   []string{"TracerPid: " + pid},
				}}, []string{"TracerPid: " + pid}
			}
		}
	}
	return nil, nil
}

// probeTimingSamples takes repeated monotonic readings of a fixed busy-wait and
// reports the spread.
//
// Rationale: a debugger that single-steps inflates wall time dramatically, and a
// host under heavy virtualisation overhead shows an unstable distribution. This
// is a weak, noisy signal on its own — hence the low confidence — but it
// corroborates other evidence.
func probeTimingSamples(clock Clock) ([]Indicator, []string) {
	if clock == nil {
		return nil, nil
	}
	const (
		samples   = 16
		busyIters = 20000
	)
	deltas := make([]int64, 0, samples)
	for i := 0; i < samples; i++ {
		start := clock()
		// A small fixed amount of work, so the measurement reflects the host's
		// ability to execute rather than the scheduler's opinion about sleeping.
		acc := 0
		for j := 0; j < busyIters; j++ {
			acc += j
		}
		_ = acc
		deltas = append(deltas, int64(clock()-start))
	}
	min, max, sum := deltas[0], deltas[0], int64(0)
	for _, d := range deltas {
		if d < min {
			min = d
		}
		if d > max {
			max = d
		}
		sum += d
	}
	mean := sum / int64(len(deltas))
	if mean <= 0 {
		// A clock with no resolution -- or one that returned zero for every
		// sample -- yields no usable spread. Reporting an indicator here would
		// be a divide-by-zero dressed up as a measurement, and it must not vary
		// with how goroutines happen to interleave.
		return nil, []string{
			"monotonic samples=" + itoa(int64(len(deltas))),
			"mean=0ns: clock has no usable resolution, spread not computed",
		}
	}
	spread := float64(max-min) / float64(mean)

	evidence := []string{
		"monotonic samples=" + itoa(int64(len(deltas))),
		"min=" + itoa(min) + "ns mean=" + itoa(mean) + "ns max=" + itoa(max) + "ns",
		"relative spread=" + ftoa(spread),
	}
	if spread <= 0 {
		return nil, evidence
	}
	// A very wide spread suggests contention or stepping. Deliberately low
	// confidence: a loaded CI runner looks identical.
	conf := 0.20
	sev := SeverityInfo
	if spread > 1.0 {
		conf = 0.35
		sev = SeverityLow
	}
	return []Indicator{{
		ID:         "ENV-TIMING-SPREAD",
		Category:   CategoryTiming,
		Severity:   sev,
		Confidence: conf,
		Title:      "Monotonic timing spread is elevated",
		Evidence:   evidence,
	}}, evidence
}

// severityFor maps a confidence onto a severity band.
func severityFor(conf float64) Severity {
	switch {
	case conf >= 0.90:
		return SeverityHigh
	case conf >= 0.70:
		return SeverityMedium
	case conf >= 0.40:
		return SeverityLow
	default:
		return SeverityInfo
	}
}

// readTrimmed reads a file and trims surrounding whitespace.
func readTrimmed(fs FileReader, path string) (string, bool) {
	b, err := fs.ReadFile(path)
	if err != nil {
		return "", false
	}
	s := trimSpace(string(b))
	if s == "" {
		return "", false
	}
	return s, true
}

// readHex16 reads a file containing a "0xNNNN" value, as sysfs presents PCI
// vendor and device IDs.
//
// sysfs writes these as "0x1af4\n". Anything else -- truncated, a decimal value,
// a kernel that changed format -- is reported as absent rather than guessed at,
// because a wrong vendor ID would fabricate an indicator.
func readHex16(fs FileReader, path string) (uint16, bool) {
	b, err := fs.ReadFile(path)
	if err != nil {
		return 0, false
	}
	s := trimSpace(string(b))
	if len(s) < 4 {
		return 0, false
	}
	if s[0] != '0' || (s[1] != 'x' && s[1] != 'X') {
		return 0, false
	}
	var v uint32
	for i := 2; i < len(s); i++ {
		c := lower(s[i])
		switch {
		case c >= '0' && c <= '9':
			v = v<<4 | uint32(c-'0')
		case c >= 'a' && c <= 'f':
			v = v<<4 | uint32(c-'a'+10)
		default:
			return 0, false
		}
		if v > 0xFFFF {
			return 0, false
		}
	}
	return uint16(v), true
}
