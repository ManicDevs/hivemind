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

if ! go build -o hivemind .; then
    echo "❌ build failed"
    exit 1
fi
echo "  ✔ build ok"

if go test ./... -race -count=1 > /tmp/hivemind-audit-test.log 2>&1; then
    echo "  ✔ tests (with -race) pass"
else
    echo "❌ tests failed:"
    cat /tmp/hivemind-audit-test.log
    exit 1
fi

echo "✅ audit clean (fmt, vet, build, race)"

