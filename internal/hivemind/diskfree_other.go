//go:build windows

package hivemind

// fsUseFraction is absent on windows: Statfs does not exist there.
// Absent, not zero — the caller degrades, never fabricates.
func fsUseFraction(path string) (float64, bool) {
	return 0, false
}
