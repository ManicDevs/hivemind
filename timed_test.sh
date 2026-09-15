#!/bin/bash
# ── HIVEMIND TIMED EXPERIMENT ORCHESTRATOR ──
# Runs a 10s two-node experiment and asserts the mesh actually worked.
# Exit code 0 means: socket came up, frames flowed, teardown verified.
set -u

SERVER_PID=""
CLIENT_PID=""
MESH_FAILED=0

cleanup() {
    kill -TERM "${CLIENT_PID}" "${SERVER_PID}" 2>/dev/null
    for _ in $(seq 1 20); do
        if ! kill -0 "${SERVER_PID}" 2>/dev/null && ! kill -0 "${CLIENT_PID}" 2>/dev/null; then
            break
        fi
        sleep 0.25
    done
    # Escalate only if graceful death refused.
    kill -9 "${CLIENT_PID}" "${SERVER_PID}" 2>/dev/null
    wait 2>/dev/null
    if [ -S /tmp/hivemind.sock ]; then
        echo "❌ socket survived teardown"
        rm -f /tmp/hivemind.sock
        exit 1
    fi
    echo "✔ workspace verified clean (checked)"
}
trap cleanup EXIT

rm -f server.log client.log /tmp/hivemind.sock

echo "🚀 [TIMED EXPERIMENT] Spawning automated 10-second mesh cluster grid..."

./hivemind -mode server > server.log 2>&1 &
SERVER_PID=$!

wait_count=0
while [ ! -S /tmp/hivemind.sock ]; do
    sleep 0.1
    wait_count=$((wait_count + 1))
    if [ "$wait_count" -gt 50 ]; then
        echo "❌ [TIMEOUT] Server failed to create LUDS descriptor within 5s. Last server.log lines:"
        tail -n 5 server.log 2>/dev/null || true
        exit 1
    fi
done
echo "✔ Server socket present: /tmp/hivemind.sock (after ${wait_count} poll intervals)"

./hivemind -mode client > client.log 2>&1 &
CLIENT_PID=$!
echo "🧠 Sampling telemetry and mesh traffic over a 10s window..."

sleep 10

# Graceful death — persist souls — then wait for both to actually exit.
kill -TERM "${CLIENT_PID}" "${SERVER_PID}" 2>/dev/null
for _ in $(seq 1 40); do
    if ! kill -0 "${SERVER_PID}" 2>/dev/null && ! kill -0 "${CLIENT_PID}" 2>/dev/null; then
        break
    fi
    sleep 0.25
done
if kill -0 "${SERVER_PID}" 2>/dev/null || kill -0 "${CLIENT_PID}" 2>/dev/null; then
    echo "❌ processes did not exit gracefully within 10s"
    exit 1
fi
echo "✔ Both nodes exited gracefully (souls persisted)"

echo ""
echo "=========================================================================="
echo "                       TIMED CHRONICLE ANALYSIS REPORT                    "
echo "=========================================================================="

# Assertions, not narration:

frames=$(grep -c "Physics frame extracted" client.log server.log 2>/dev/null | awk -F: '{s+=$2} END {print s+0}')
echo "1. PHYSICS FRAMES EXCHANGED: ${frames}"
if [ "$frames" -lt 10 ]; then
    echo "   ❌ mesh traffic below expectation (wanted ≥10)"
    MESH_FAILED=1
else
    echo "   ✔ LUDS duplex confirmed by live traffic"
fi

hellos=$(grep -c "linked to the collective" client.log server.log 2>/dev/null | awk -F: '{s+=$2} END {print s+0}')
echo "2. MESH HANDSHAKES OBSERVED: ${hellos}"
if [ "$hellos" -lt 1 ]; then
    echo "   ❌ no peer link formed"
    MESH_FAILED=1
fi

echo "3. LIFECYCLE EVIDENCE:"
grep -E "REINCARNATION|FIRST BIRTH|FATAL MELTDOWN|Persistence saved" client.log server.log 2>/dev/null | head -n 6 || echo "   (no lifecycle events this run)"
echo ""

echo "4. ADAPTIVE SCALING EVENTS:"
grep "ADAPTIVE SCALING" server.log client.log 2>/dev/null | sort -u | head -n 3 || echo "   (load stayed in the zero-difficulty band — as expected on an idle host)"
echo ""

echo "5. TERMINAL HEALTH SUMMARY:"
tail -n 14 server.log 2>/dev/null || true
echo "=========================================================================="

if [ "$MESH_FAILED" -ne 0 ]; then
    echo "❌ [EXPERIMENT FAILED] Mesh assertions did not pass."
    exit 1
fi
echo "✨ [EXPERIMENT COMPLETE] All assertions passed. Workspace verified clean."

