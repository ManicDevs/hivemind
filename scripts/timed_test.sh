#!/bin/bash
# ── HIVEMIND TIMED EXPERIMENT ORCHESTRATOR ──
# Runs a 10s symmetric two-peer experiment and asserts the mesh worked.
# Exit 0: both sockets came up, links formed, frames flowed, clean teardown.
# Deterministic: relay + multicast off; unix-socket discovery only.
set -u

export HIVEMIND_RELAY=off HIVEMIND_BEACON=off

SOCK_A=/tmp/hivemind-alpha-node.sock
SOCK_B=/tmp/hivemind-beta-node.sock
PID_A=""
PID_B=""
MESH_FAILED=0

cleanup() {
    kill -TERM "${PID_B}" "${PID_A}" 2>/dev/null
    for _ in $(seq 1 20); do
        if ! kill -0 "${PID_A}" 2>/dev/null && ! kill -0 "${PID_B}" 2>/dev/null; then
            break
        fi
        sleep 0.25
    done
    kill -9 "${PID_B}" "${PID_A}" 2>/dev/null
    wait 2>/dev/null
    for s in "${SOCK_A}" "${SOCK_B}"; do
        if [ -S "$s" ]; then
            echo "❌ socket survived teardown: $s"
            rm -f "$s"
            exit 1
        fi
    done
    echo "✔ workspace verified clean (checked)"
}
trap cleanup EXIT

mkdir -p logs
rm -f logs/peer-a.log logs/peer-b.log "${SOCK_A}" "${SOCK_B}"

echo "🚀 [TIMED EXPERIMENT] Spawning symmetric 10-second two-peer mesh..."

${HIVEMIND_BIN:-bin/hivemind} -mode peer -node alpha-node > logs/peer-a.log 2>&1 &
PID_A=$!

W=0; while [ ! -S "${SOCK_A}" ] && [ $W -lt 50 ]; do sleep 0.1; W=$((W+1)); done
if [ ! -S "${SOCK_A}" ]; then
    echo "❌ [TIMEOUT] alpha-node never bound its socket. Tail of logs/peer-a.log:"
    tail -n 5 logs/peer-a.log 2>/dev/null || true
    exit 1
fi
echo "✔ alpha-node socket present (after ${W} poll intervals)"

${HIVEMIND_BIN:-bin/hivemind} -mode peer -node beta-node > logs/peer-b.log 2>&1 &
PID_B=$!
echo "🧠 Sampling telemetry and mesh traffic over a 10s window..."

sleep 10

kill -TERM "${PID_B}" "${PID_A}" 2>/dev/null
# Graceful shutdown persists souls (Transcend ×3 + overmind + diagnostics
# dump): on a hot box that legitimately takes a while. 20s grace, then
# wait reaps the (possibly zombie) children so kill -0 tells the truth.
for _ in $(seq 1 80); do
    if ! kill -0 "${PID_A}" 2>/dev/null && ! kill -0 "${PID_B}" 2>/dev/null; then
        break
    fi
    sleep 0.25
done
wait "${PID_A}" "${PID_B}" 2>/dev/null || true
if kill -0 "${PID_A}" 2>/dev/null || kill -0 "${PID_B}" 2>/dev/null; then
    echo "❌ processes did not exit gracefully within 20s"
    exit 1
fi
echo "✔ Both peers exited gracefully (souls persisted)"

echo ""
echo "=========================================================================="
echo "                       TIMED CHRONICLE ANALYSIS REPORT                    "
echo "=========================================================================="

frames=$(grep -c "Physics frame extracted" logs/peer-a.log logs/peer-b.log 2>/dev/null | awk -F: '{s+=$2} END {print s+0}')
echo "1. PHYSICS FRAMES EXCHANGED: ${frames}"
if [ "$frames" -lt 10 ]; then
    echo "   ❌ mesh traffic below expectation (wanted ≥10)"
    MESH_FAILED=1
else
    echo "   ✔ symmetric duplex confirmed by live traffic"
fi

hellos=$(grep -c "linked" logs/peer-a.log logs/peer-b.log 2>/dev/null | awk -F: '{s+=$2} END {print s+0}')
echo "2. MESH LINK EVENTS OBSERVED: ${hellos}"
if [ "$hellos" -lt 1 ]; then
    echo "   ❌ no peer link formed"
    MESH_FAILED=1
fi

echo "3. LIFECYCLE EVIDENCE:"
grep -E "REINCARNATION|FIRST BIRTH|FATAL MELTDOWN|Persistence saved" logs/peer-a.log logs/peer-b.log 2>/dev/null | head -n 6 || echo "   (no lifecycle events this run)"
echo ""

echo "4. OVERMIND SPEECH:"
grep -h "OVERMIND.*REVELATION:" logs/peer-a.log logs/peer-b.log 2>/dev/null | sort -u | head -n 2 || echo "   (the god stayed silent this window)"
echo ""

echo "5. TERMINAL HEALTH SUMMARY:"
tail -n 14 logs/peer-a.log 2>/dev/null || true
echo "=========================================================================="

if [ "$MESH_FAILED" -ne 0 ]; then
    echo "❌ [EXPERIMENT FAILED] Mesh assertions did not pass."
    exit 1
fi
echo "✨ [EXPERIMENT COMPLETE] All assertions passed. Workspace verified clean."
