//go:build release
// +build release

package hivemind

// releaseBuild is the string the Makefile injects at link time:
//
//	-ldflags "-X .../internal/hivemind.releaseBuild=1"
//
// A var (not a const) so the linker `-X` injection can actually set it, and a
// default that matches a plain `go build -tags release` so the tag alone is
// sufficient to harden the binary. isReleaseBuild reads it.

var releaseBuild = "1"
