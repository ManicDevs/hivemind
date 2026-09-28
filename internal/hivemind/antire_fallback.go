//go:build !release
// +build !release

package hivemind

// AnnounceKeyPosture is a no-op in non-release builds.
func AnnounceKeyPosture() {}
