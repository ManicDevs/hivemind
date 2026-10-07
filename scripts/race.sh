#!/usr/bin/env bash
# race.sh — run `go test -race` on a machine with no C compiler.
#
# Why this exists
# ---------------
# AGENTS.md requires `go test -race` on every change, but the race detector needs
# cgo and therefore a C toolchain. Some environments (a Flatpak runtime, for
# instance) ship no gcc/clang/tcc and have no package manager to install one, so
# the requirement looks permanently unmet.
#
# This script builds a throwaway toolchain in a temp directory and links the
# race binary with it. Nothing is installed system-wide: the only residue is a
# temp directory, which you can delete.
#
#   ./scripts/race.sh                 # both modules
#   ./scripts/race.sh ./cmd/fabric/   # specific packages
#
# The three pieces that make it work
# ---------------------------------
#   1. CC=<wrapper>       Go invokes "$(CC) -E" directly, so CC must be a
#                         `zig cc` shim rather than the zig binary itself.
#   2. libbuiltins.a      zig cc does not export __popcountdi2, which Go's
#                         race runtime links against. A two-function
#                         compiler-rt substitute is compiled and passed via
#                         CGO_LDFLAGS.
#   3. -linkmode=external Go's internal linker cannot see symbols supplied by
#                         cgo archives; the external linker can.
#
# If a real gcc is available, none of this is needed: just `go test -race ./...`.

set -euo pipefail

ZIG_VERSION="${ZIG_VERSION:-0.13.0}"
TOOLCHAIN="${TOOLCHAIN:-${TMPDIR:-/tmp}/hivemind-race}"
CC_WRAPPER="$TOOLCHAIN/bin/zigcc"
BUILTINS="$TOOLCHAIN/lib/libbuiltins.a"

repo_root() {
	cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd
}

# fetch_toolchain downloads and unpacks zig once, then reuses it.
fetch_toolchain() {
	local zig_dir="$TOOLCHAIN/zig-linux-x86_64-$ZIG_VERSION"

	if [[ -x "$zig_dir/zig" ]]; then
		echo "==> toolchain present: $zig_dir"
		return
	fi

	mkdir -p "$TOOLCHAIN"
	echo "==> fetching zig $ZIG_VERSION (~45 MB, into $TOOLCHAIN)"
	local tarball="$TOOLCHAIN/zig.tar.xz"
	if [[ ! -s "$tarball" ]]; then
		curl -fsSL -o "$tarball" \
			"https://ziglang.org/download/$ZIG_VERSION/zig-linux-x86_64-$ZIG_VERSION.tar.xz"
	fi
	tar -C "$TOOLCHAIN" -xf "$tarball"

	# Go probes the compiler with -E, so it needs the `cc` subcommand.
	mkdir -p "$(dirname "$CC_WRAPPER")"
	cat >"$CC_WRAPPER" <<EOF
#!/bin/sh
exec "$zig_dir/zig" cc "\$@"
EOF
	chmod +x "$CC_WRAPPER"
}

# build_builtins supplies the compiler-rt symbols the race runtime expects.
build_builtins() {
	if [[ -s "$BUILTINS" ]]; then
		echo "==> builtins present: $BUILTINS"
		return
	fi

	local zig="$TOOLCHAIN/zig-linux-x86_64-$ZIG_VERSION/zig"
	mkdir -p "$(dirname "$BUILTINS")"
	local src="$TOOLCHAIN/builtins.c"

	cat >"$src" <<'EOF'
/* Minimal compiler-rt builtins that Go's race runtime links against but that
   zig cc does not export. These are the popcount helpers; they are pure integer
   code with no libc dependency. */
int __popcountdi2(unsigned long long a) {
	int c = 0;
	while (a) { c += (int)(a & 1ULL); a >>= 1; }
	return c;
}
int __popcountti2(unsigned long long a, unsigned long long b) {
	return __popcountdi2(a) + __popcountdi2(b);
}
EOF

	echo "==> building compiler-rt substitutes"
	"$CC_WRAPPER" -c -O2 -o "$TOOLCHAIN/builtins.o" "$src"
	"$zig" ar rcs "$BUILTINS" "$TOOLCHAIN/builtins.o"
}

main() {
	# If a real C compiler is present, use it and skip the whole dance.
	if command -v gcc >/dev/null 2>&1; then
		echo "==> gcc found; using the system toolchain"
		(cd "$(repo_root)" && CGO_ENABLED=1 go test -race "$@")
		return
	fi

	fetch_toolchain
	build_builtins

	export CC="$CC_WRAPPER"
	export CGO_ENABLED=1
	export CGO_LDFLAGS="$BUILTINS"

	local root
	root="$(repo_root)"

	# A bare `go test` with no package pattern tests the current directory, which
	# is the repo root — not a Go package. Default to ./... instead.
	local pkgs=("$@")
	if [[ ${#pkgs[@]} -eq 0 ]]; then
		pkgs=(./...)
	fi

	# bininspect is a nested module and needs its own test run.
	echo "==> hivemind module"
	(cd "$root" && go test -race -ldflags=-linkmode=external "${pkgs[@]}")

	if [[ ${#pkgs[@]} -eq 1 && "${pkgs[0]}" == "./..." && -d "$root/bininspect" ]]; then
		echo "==> bininspect module"
		(cd "$root/bininspect" && go test -race -ldflags=-linkmode=external ./...)
	fi
}

main "$@"