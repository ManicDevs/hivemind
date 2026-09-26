#!/bin/bash
# hivemind audit — formatting, vet, build, tests.
# Exits non-zero on any failure. Every claim below is checked, not narrated.
set -u

echo "🔍 hivemind audit"

unformatted="$(gofmt -l .)"
if [ -n "$unformatted" ]; then
    echo "❌ gofmt: unformatted files:"
    echo "$unformatted"
    exit 1
fi
echo "  ✔ gofmt clean"

if ! go vet ./...; then
    echo "❌ go vet failed"
    exit 1
fi
echo "  ✔ go vet clean"

if ! go build -o bin/hivemind ./cmd/hivemind; then
    echo "❌ build failed"
    exit 1
fi
echo "  ✔ build ok"

RACE_MODE="race"
if go test ./... -race -count=1 > /tmp/hivemind-audit-test.log 2>&1; then
    echo "  ✔ tests (with -race) pass"
elif grep -q "requires cgo" /tmp/hivemind-audit-test.log; then
    echo "  ⚠️  no C compiler here — race detector unavailable, running plain tests instead"
    RACE_MODE="plain (race unavailable here)"
    if ! go test ./... -count=1; then
        echo "❌ tests failed"
        exit 1
    fi
    echo "  ✔ tests (plain) pass — rerun with -race on a gcc machine"
else
    echo "❌ tests failed:"
    cat /tmp/hivemind-audit-test.log
    exit 1
fi

echo "✅ audit clean (fmt, vet, build, $RACE_MODE)"

