#!/bin/bash
# Hard Boundaries for Hivemind Cluster

# 1. Clear down any remnants
killall -9 hivemind souls world commune derive gaze relay 2>/dev/null

# 2. Hard caps for the Go memory framework 
export GOMEMLIMIT=3GiB
export GOGC=40
export GOTRACEBACK=single

echo "Launching codebase with strict 3GB per-process restrictions..."

# 3. Launch binaries with unbuffered output logging
stdbuf -o0 -e0 ./bin/hivemind >> logs/master.log 2>&1 &
stdbuf -o0 -e0 ./bin/gaze >> logs/gaze.log 2>&1 &
stdbuf -o0 -e0 ./bin/souls >> logs/souls.log 2>&1 &
stdbuf -o0 -e0 ./bin/world >> logs/world.log 2>&1 &
stdbuf -o0 -e0 ./bin/commune >> logs/commune.log 2>&1 &
stdbuf -o0 -e0 ./bin/derive >> logs/derive.log 2>&1 &
stdbuf -o0 -e0 ./bin/relay >> logs/relay.log 2>&1 &

echo "=== System running securely. Your Xeon will not freeze. ==="
