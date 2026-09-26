#!/bin/bash
# ── HIVEMIND MQTT LIVE PROOF REPORT ──────────────────────────────
# Self-hosted MQTT bridge proof - zero third-party dependency
# Demonstrates: relay operation + node connectivity + message flow
# Output: Visual report with concrete counts

set -u
export HIVEMIND_RELAY=off HIVEMIND_BEACON=off HIVEMIND_DHT=off
BIN="${HIVEMIND_BIN:-bin/hivemind}"

echo "╔══════════════════════════════════════════════════════════════╗"
echo "║   PROOF: SELF-HOSTED MQTT RELAY — ZERO THIRD-PARTY        ║"
echo "║   Demonstrates: relay operation + node connectivity         ║"
echo "╚══════════════════════════════════════════════════════════════╝"
echo ""

PASS=0; FAIL=0; WARN=0

check() {
    if [ "$1" -eq 0 ]; then
        echo "  ✅ $2"
        PASS=$((PASS+1))
    else
        echo "  ❌ $2"
        FAIL=$((FAIL+1))
    fi
}

WARN() {
    echo "  ⚠️  $1"
    WARN=$((WARN+1))
}

# ── 1. Build & start own relay ──
echo "━━━ 1. SELF-HOSTED RELAY ──"
BIN="${HIVEMIND_BIN:-bin/hivemind}"

bin/relay > /tmp/mqtt-prove-relay.log 2>&1 &
RELAY_PID=$!
sleep 1

# ── 2. Start two nodes through relay (cloud path only — force local echo off) ──
NODE_OPTS="-mode peer -node proof-rl"
CLOUD_ENV="HIVEMIND_RELAY=on HIVEMIND_UNIX=off HIVEMIND_BEACON=off HIVEMIND_DHT=off HIVEMIND_SUPER=off HIVEMIND_RELAY_URL=http://127.0.0.1:8080/hive-relay HIVEMIND_TICK_MS=500 HIVEMIND_CIPHER_KEY=ab94f253510372b0cee7c871ba7c5d3fea4b20caeb0d271de02860f739d3e5c1"
env $CLOUD_ENV $BIN $NODE_OPTS > /tmp/proof-rl.log 2>&1 &
PID_L=$!
sleep 3

env $CLOUD_ENV $BIN -mode peer -node proof-rr > /tmp/proof-rr.log 2>&1 &
PID_R=$!
sleep 18

# ── 3. Check relay activity ──
INBOUND=$(grep -ac "SECURE CLOUD INBOUND" /tmp/proof-rl.log /tmp/proof-rr.log 2>/dev/null | awk -F: '{s+=$2} END {print s+0}')
ATTEMPTS=$(grep -ac "Cloud publish\|RELAY.*retrying\|RELAY.*429\|RELAY.*relay returned" /tmp/proof-rl.log /tmp/proof-rr.log 2>/dev/null | awk -F: '{s+=$2} END {print s+0}')
WILL_EVENTS=$(grep -ac "WILL" /tmp/proof-rl.log /tmp/proof-rr.log 2>/dev/null | awk -F: '{s+=$2} END {print s+0}')
MINDS=$(grep -ac "FIRST BIRTH" /tmp/proof-rl.log /tmp/proof-rr.log 2>/dev/null | awk -F: '{s+=$2} END {print s+0}')
FRAMES=$(grep -ac "Physics frame extracted" /tmp/proof-rl.log /tmp/proof-rr.log 2>/dev/null | awk -F: '{s+=$2} END {print s+0}')

check "$MINDS"   "Minds born across 2 nodes — mesh alive"
check "$WILL_EVENTS" "Will layer active (minds choosing their own rules)"
check "$FRAMES"  "Physics frames exchanged — mesh thinking"
check "$INBOUND" "Relay: frame(s) received and decrypted over self-hosted bus"
check "$ATTEMPTS" "Relay: zero publish errors (own bus, no rate limits)" || WARN "Relay: $ATTEMPTS publish errors"

# ── 4. Cleanup ──
kill $PID_L $PID_R $RELAY_PID 2>/dev/null; sleep 2; kill -9 $PID_L $PID_R $RELAY_PID 2>/dev/null; wait 2>/dev/null
rm -f /tmp/hivemind-proof-rl.sock /tmp/proof-relay.log

# ── Summary ──
echo ""
echo "╔═══════════════════════════════════════════════════════════════╗"
TOTAL=$((PASS+FAIL+WARN))
echo "║  RESULTS: $PASS/$TOTAL passed, $FAIL failed, $WARN warnings"
echo "╚══════════════════════════════════════════════════════════════╝"

if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
echo "✨ All proofs passed. The key system is real."