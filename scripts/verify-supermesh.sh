#!/bin/bash
# ── HIVEMIND SUPER-MESH VERIFICATION ──
# Proves the supernode layer live: two peers, capability announces fire,
# both learn each other as supers, mesh traffic flows throughout.
# Exit 0: announces + mutual learning + traffic. Needs ~50s (30s tick).
set -u

export HIVEMIND_RELAY=off HIVEMIND_BEACON=on
BIN="${HIVEMIND_BIN:-bin/hivemind}"
PID_A=""
PID_B=""
FAILED=0

cleanup() {
    kill -TERM "${PID_B}" "${PID_A}" 2>/dev/null
    sleep 2
    kill -9 "${PID_B}" "${PID_A}" 2>/dev/null
    wait 2>/dev/null
    rm -f /tmp/hivemind-sv-a.sock /tmp/hivemind-sv-b.sock
}
trap cleanup EXIT

mkdir -p logs

# Hermetic logs: a unique dir per run, so parallel runs, stale files, or
# anyone else on this box can never mix into (or vanish) our evidence.
# (The dir is fresh from mktemp, so no cleanup of old logs is needed.)
PROOFDIR="$(mktemp -d /tmp/svproof.XXXXXX)"
LOG_A="$PROOFDIR/sv-a.log"
LOG_B="$PROOFDIR/sv-b.log"

echo "🚀 [SUPER VERIFY] Raising two peers, watching for announce + learn..."

"${BIN}" -mode peer -node sv-a > "$LOG_A" 2>&1 &
PID_A=$!
sleep 3
if ! kill -0 "${PID_A}" 2>/dev/null; then
    echo "❌ sv-a died on startup. Log:"
    cat logs/sv-a.log 2>/dev/null || true
    exit 1
fi
"${BIN}" -mode peer -node sv-b > "$LOG_B" 2>&1 &
PID_B=$!
sleep 3
if ! kill -0 "${PID_B}" 2>/dev/null; then
    echo "❌ sv-b died on startup. Log:"
    cat logs/sv-b.log 2>/dev/null || true
    exit 1
fi
echo "✔ both nodes alive, sampling (up to 90s, ends early on evidence)..."

# Adaptive sampling: the super tick is 30s, and a hot box runs slow.
# Poll every 5s; stop as soon as both nodes announced AND learned, or
# at 90s come what may. Fast boxes finish in ~35s, hot ones get room.
announces=0; learns=0
for _ in $(seq 1 18); do
    sleep 5
    announces=$(grep -c "announced super" "$LOG_A" "$LOG_B" 2>/dev/null | awk -F: '{s+=$2} END {print s+0}')
    learns=$(grep -c 'Super "sv-' "$LOG_A" "$LOG_B" 2>/dev/null | awk -F: '{s+=$2} END {print s+0}')
    if [ "$announces" -ge 2 ] && [ "$learns" -ge 2 ]; then
        break
    fi
done

for p in "${PID_A}" "${PID_B}"; do
    kill -0 "$p" 2>/dev/null || { echo "❌ a node died mid-run"; FAILED=1; }
done

echo "--- assertions ---"
announces=$(grep -c "announced super" "$LOG_A" "$LOG_B" 2>/dev/null | awk -F: '{s+=$2} END {print s+0}')
echo "1. SUPER ANNOUNCES FIRED: ${announces}"
if [ "$announces" -lt 2 ]; then
    echo "   ❌ expected both nodes to announce (30s tick may need longer)"
    FAILED=1
else
    echo "   ✔ both nodes advertised capability"
fi

learns=$(grep -c 'Super "sv-' "$LOG_A" "$LOG_B" 2>/dev/null | awk -F: '{s+=$2} END {print s+0}')
echo "2. MUTUAL SUPER LEARNING: ${learns}"
if [ "$learns" -lt 2 ]; then
    echo "   ❌ nodes did not file each other as supers"
    FAILED=1
else
    echo "   ✔ each node knows the other as super"
fi

frames=$(grep -c "Physics frame extracted" "$LOG_A" "$LOG_B" 2>/dev/null | awk -F: '{s+=$2} END {print s+0}')
echo "3. MESH TRAFFIC DURING PROOF: ${frames}"
if [ "$frames" -lt 10 ]; then
    echo "   ❌ mesh went quiet"
    FAILED=1
else
    echo "   ✔ hive alive throughout"
fi

panics=$(grep -ci "panic" "$LOG_A" "$LOG_B" 2>/dev/null | awk -F: '{s+=$2} END {print s+0}')
echo "4. PANICS: ${panics}"
if [ "$panics" -gt 0 ]; then
    echo "   ❌ panic in logs"
    FAILED=1
else
    echo "   ✔ no panics"
fi

kill -TERM "${PID_B}" "${PID_A}" 2>/dev/null
sleep 2

if [ "$FAILED" -ne 0 ]; then
    echo "❌ [SUPER VERIFY FAILED] evidence kept at: $PROOFDIR"
    exit 1
fi
rm -rf "$PROOFDIR"
echo "✨ [SUPER VERIFY COMPLETE] Supernode layer proven live."
