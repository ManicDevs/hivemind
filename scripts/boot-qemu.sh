#!/usr/bin/env bash
#
# boot-qemu.sh — build the pure-Go HIVEMIND kernel image and boot it under QEMU.
#
# The image is emitted byte-by-byte by internal/kernel/bootimage using pure Go:
# no external toolchain and no host GUI/display dependency is involved. The
# OS boots as a Multiboot-1 kernel, paints its splash on the virtual VGA text
# screen (0xB8000), and runs the interactive console (full boot log, heartbeat,
# hivemind> prompt) over COM1 serial — which is exactly what the terminal sees.
#
# Usage:
#   scripts/boot-qemu.sh              # standalone serial console (no GUI)
#   scripts/boot-qemu.sh -screenshot  # dump VGA to /tmp, no UI, then exit

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BOOT_IMAGE="${BOOT_IMAGE:-${TMPDIR:-/tmp}/hivemind-kernel.bin}"

# `make qemu` prebuilds the image into dist/ and passes REBUILD_BOOT=0.
if [ "${REBUILD_BOOT:-1}" = "1" ] || [ ! -s "$BOOT_IMAGE" ]; then
	(cd "$ROOT" && CGO_ENABLED=0 go run ./cmd/hivemind-image -out "$BOOT_IMAGE")
fi
IMAGE="$BOOT_IMAGE"

# --- qemu ----------------------------------------------------------------
QEMU="$(command -v qemu-system-x86_64 2>/dev/null || true)"
QEMU_EXTRA=()
if [ -x /tmp/qemudeb/qemu.sh ]; then
	QEMU=/tmp/qemudeb/qemu.sh
	export QEMU_MODULE_DIR=/tmp/qemudeb/usr/lib/x86_64-linux-gnu/qemu
	QEMU_EXTRA=(-L /tmp/qemudeb/usr/share/qemu)
fi
if [ -z "$QEMU" ]; then
	echo "error: no qemu-system-x86_64 and no /tmp/qemudeb/qemu.sh" >&2
	exit 1
fi

SCREENSHOT_ONLY=0
for arg in "$@"; do
	case "$arg" in
		-screenshot) SCREENSHOT_ONLY=1 ;;
	esac
done

if [ "$SCREENSHOT_ONLY" -eq 1 ]; then
	rm -f /tmp/hivemind-screen.ppm
	{
		sleep 2.5
		printf '%s\n' '{"execute":"qmp_capabilities"}'
		printf '%s\n' '{"execute":"human-monitor-command","arguments":{"command-line":"screendump /tmp/hivemind-screen.ppm"}}'
		sleep 0.5
		printf '%s\n' '{"execute":"quit"}'
	} | "$QEMU" "${QEMU_EXTRA[@]}" -machine pc -m 64 \
		-display none -vga std -nic none \
		-serial file:/tmp/hivemind-serial.log \
		-qmp stdio -no-reboot -kernel "$IMAGE" >/dev/null 2>&1
	echo "VGA screen captured: $(ls -l /tmp/hivemind-screen.ppm | awk '{print $5}') bytes"
	echo "serial log:"
	sed -n '1,20p' /tmp/hivemind-serial.log
	exit 0
fi

# The OS console lives on COM1 and the terminal is the UI. No host display,
# no GTK, no curses: this is fully standalone. VGA exists only inside the VM
# for -screenshot capture.
exec "$QEMU" "${QEMU_EXTRA[@]}" -machine pc -m 64 \
	-display none -vga std -serial stdio -monitor none \
	-no-reboot -nic none -kernel "$IMAGE"