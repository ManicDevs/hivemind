#!/bin/sh
# ── HIVEMIND RELAY PORT RESOLVER ──
# Print the relay's base URL, preferring the port it actually bound.
#
# The relay rolls forward off a busy port and records the result in
# .stack-relay.port: the port on line 1, the owning pid on line 2. This must
# run *after* the relay starts, so `make stack` invokes it as a script
# rather than a make variable: make expands its own functions once per
# invocation, before the relay has bound.
#
# The pid is checked, not just read. A relay that died without cleanup would
# otherwise leave us pointing at a port nothing is serving, and every caller
# would see a connection error instead of an honest fallback. A second relay
# cannot steal the file either -- the relay only claims it when unowned.
#
# Usage: scripts/relay-url.sh [fallback-url]
port_file=".stack-relay.port"
fallback="${1:-http://localhost:8080}"

if [ -s "$port_file" ]; then
	port=$(sed -n 1p "$port_file" 2>/dev/null)
	owner=$(sed -n 2p "$port_file" 2>/dev/null | tr -d '[:space:]')

	if [ -n "$owner" ] && [ ! -r "/proc/$owner/comm" ]; then
		# Recorded owner is gone: do not vouch for a dead relay.
		printf '%s\n' "$fallback"
		exit 0
	fi
	if [ -n "$owner" ] && [ -r "/proc/$owner/comm" ]; then
		if [ "$(cat "/proc/$owner/comm" 2>/dev/null)" != "relay" ]; then
			# Pid recycled by something that is not a relay.
			printf '%s\n' "$fallback"
			exit 0
		fi
	fi

	case "$port" in
		'' | *[!0-9]*)
			# Unreadable or non-numeric: fall back rather than guess.
			printf '%s\n' "$fallback"
			exit 0
			;;
	esac
	printf 'http://localhost:%s\n' "$port"
	exit 0
fi
printf '%s\n' "$fallback"
