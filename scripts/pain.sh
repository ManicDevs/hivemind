#!/bin/bash
# pain.sh — proof that reality changes minds. Same binary, same genome,
mkdir -p logs
# same box: 15s idle, then 20s under CPU hogs. Compare the matrices.
set -u
cd "$(dirname "$0")/.."

BIN=bin/hivemind
if [ ! -x "$BIN" ]; then
	echo "building $BIN..."
	go build -o "$BIN" ./cmd/hivemind
fi

export HIVEMIND_TICK_MS=100 HIVEMIND_BEACON=off HIVEMIND_RELAY=off HIVEMIND_DHT=off

cleanup() {
	pkill -x hivemind 2>/dev/null || true
	if [ -n "${HOGS:-}" ]; then kill $HOGS 2>/dev/null || true; fi
	rm -f /tmp/hivemind-pain-idle.sock /tmp/hivemind-pain-load.sock
}
trap cleanup EXIT

echo "── phase 1: idle (15s) ──"
"$BIN" -mode peer -node pain-idle > logs/pain-idle.log 2>&1 &
sleep 15
pkill -x hivemind; sleep 1

echo "── phase 2: the world turns (10s calm, then 10s loaded) ──"
"$BIN" -mode peer -node pain-load > logs/pain-load.log 2>&1 &
sleep 10
HOGS=""
for _ in $(seq 1 "$(nproc 2>/dev/null || echo 4)"); do
	yes > /dev/null & HOGS="$HOGS $!"
done
sleep 10
pkill -x hivemind; sleep 1
kill $HOGS 2>/dev/null || true
HOGS=""

echo ""
echo "── idle mind ──"
go run ./cmd/souls -matrix pain-idle/Alpha
echo "── loaded mind ──"
go run ./cmd/souls -matrix pain-load/Alpha
echo ""
echo "idle epitaph:  $(go run ./cmd/souls -timeline pain-idle/Alpha | grep epitaph)"
echo "loaded epitaph: $(go run ./cmd/souls -timeline pain-load/Alpha | grep epitaph)"
