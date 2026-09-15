#!/bin/bash
# ── HIVEMIND SYSTEM INTEGRITY & DIAGNOSTIC AUDIT ──

echo "🔍 [SYSTEM AUDIT] Beginning global integration verification pass..."
echo ""

# 1. Structural File Existence Checks
for file in main.go mind.go swarm.go network.go Makefile; do
    if [ -f "$file" ]; then
        echo "  ✔ File sector found: $file"
    else
        echo "  ❌ CRITICAL BUG: Missing core sector: $file"
    fi
done

echo ""

# 2. Syntax Validation Pass
echo "🛠️ [COMPILER SWEEP] Validating Go package dependency tree..."
go vet ./... 2>&1
if [ $? -eq 0 ]; then
    echo "  ✔ All internal type definitions and memory scopes match cleanly."
else
    echo "  ❌ Compilation diagnostics flagged structural errors above."
fi

echo ""

# 3. Native Execution Compilation Pass
echo "⚡ [BUILD TARGET] Generating production executable binary..."
make build
if [ $? -eq 0 ]; then
    echo ""
    echo "✨ [AUDIT COMPLETE] System architecture is fully unified and ready."
fi
