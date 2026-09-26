//go:build release
// +build release

package hivemind

// This file ensures the release build tag is compiled in.
// The actual anti-RE code is in antire.go and antire_asm_*.s
// This file exists to force the release tag to be considered.

const releaseBuild = "1"
