//go:build !windows

package hivemind

// Filesystem fullness without cgo: syscall.Statfs exists on unix, not on
// windows — diskfree_other.go covers the rest with an honest "absent".

import "syscall"

// fsUseFraction reports 0..1 of the filesystem holding path.
func fsUseFraction(path string) (float64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, false
	}
	if st.Blocks == 0 {
		return 0, false
	}
	free := st.Bavail
	if free > st.Blocks {
		free = st.Blocks
	}
	return float64(st.Blocks-free) / float64(st.Blocks), true
}
