//go:build !linux

// Non-Linux fallback.
//
// The probes in this package read /proc and /sys, which are Linux-isms. On darwin
// or windows those paths do not exist, so every probe reports ErrUnsupported
// rather than returning a plausible-looking negative.
//
// The distinction matters: "unsupported" means the question was never asked.
// Folding it into "checked, nothing found" would let a caller conclude a bare
// metal host was verified when it was in fact never examined.

package environment

// probeHypervisorFlag cannot run off Linux: it depends on /proc/cpuinfo.
func probeHypervisorFlag(fs FileReader) (bool, []string) {
	return false, nil
}

// platformSupported reports that the Linux probe set is unavailable here.
func platformSupported() bool { return false }

// platformName is the label recorded in the snapshot.
func platformName() string { return "unsupported" }
