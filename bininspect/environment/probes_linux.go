//go:build linux

// Linux host probes.
//
// This file is tagged by GOOS rather than GOARCH, because that is the axis that
// actually determines what can be read: /proc and /sys are Linux-isms. On darwin
// or windows every path below simply does not exist, so those builds get
// probes_other.go and report ErrUnsupported.
//
// There is deliberately no architecture split and no assembly here.
//
// An earlier revision of this package implemented CPUID and RDTSC in hand-written
// assembly behind a `//go:build amd64` tag, because the task that prompted it
// asked for `cpuidLowLevel`/`rdtscLowLevel`. That was the wrong call for this
// codebase on three counts, and the reasons are recorded here so the choice is
// not silently reversed later:
//
//   1. The repository's documented invariant is "pure Go, no CGO", and
//      hand-written assembly is a real departure from it even though it does not
//      require cgo. Go assembly compiles fine with CGO_ENABLED=0 -- that part is
//      not the objection.
//   2. It bought almost nothing. The hypervisor-present bit (CPUID.01H:ECX[31])
//      is already exposed by the kernel as the "hypervisor" flag in
//      /proc/cpuinfo, so probeCPUFlags below gets it with no instruction at all.
//   3. The only genuinely-unavailable piece was leaf 0x40000000, the hypervisor
//      vendor string -- and DMI already names the vendor *and* the product, which
//      is a stronger and more specific signal than that leaf.
//
// RDTSC is dropped for a second reason: it is not serialising on modern x86, so
// it is a poor timing instrument even where it exists. The monotonic clock is
// both portable and better.
//
// Consequence, stated plainly: a hypervisor that implements leaf 0x40000000 but
// is invisible to DMI and /proc/cpuinfo will not be detected by this package.

package environment

// probeHypervisorFlag reports whether the kernel observed the CPUID
// hypervisor-present bit.
//
// This is the pure-Go equivalent of reading CPUID.01H:ECX[31] directly: the
// Linux kernel sets X86_FEATURE_HYPERVISOR when that bit is set and publishes it
// in /proc/cpuinfo, so no instruction needs to be executed on the caller's
// behalf.
func probeHypervisorFlag(fs FileReader) (bool, []string) {
	b, err := fs.ReadFile("/proc/cpuinfo")
	if err != nil {
		return false, nil
	}
	for _, ln := range splitLines(string(b)) {
		if !containsFold(ln, "flags") && !containsFold(ln, "features") {
			continue
		}
		if containsFold(ln, "hypervisor") {
			return true, []string{"cpuinfo flags: " + trimSpace(ln)}
		}
	}
	return false, nil
}

// platformSupported reports that Linux probes are available on this build.
func platformSupported() bool { return true }

// platformName is the label recorded in the snapshot.
func platformName() string { return "linux" }
