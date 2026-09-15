# ── HIVEMIND DECENTRALIZED AUTOMATION MAKEFILE ──

.PHONY: all help build test audit clean test-1 test-2 test-full test-timed clean-soul

all: help

help:
	@echo ""
	@echo "  HIVEMIND PROCESS CONTROLLER"
	@echo ""
	@echo "  Usage: make <command>"
	@echo ""
	@echo "  Available Commands:"
	@echo "    build         Compile hivemind"
	@echo "    test          gofmt + go vet + go build + tests (with -race)"
	@echo "    audit         run audit.sh (same gates as test)"
	@echo "    test-1        Run a peer node (terminal 1)"
	@echo "    test-2        Run a peer node (terminal 2; links to any others)"
	@echo "    test-full     Launch a two-peer mesh automatically"
	@echo "    test-timed    Automated 10s two-peer experiment with assertions"
	@echo "    clean         Remove build artifacts and logs"
	@echo "    clean-soul    Wipe .hive_memory (true extinction)"
	@echo ""

build:
	go build -o hivemind .

test:
	@test -z "$$(gofmt -l .)" || { echo "❌ unformatted files:"; gofmt -l .; exit 1; }
	go vet ./...
	go test ./... -race -count=1

audit: test
	./audit.sh

test-1: build
	./hivemind -mode peer -node alpha-node

test-2: build
	./hivemind -mode peer -node beta-node

test-full: build
	@rm -f peer-a.log peer-b.log
	@echo "🚀 [PEER MESH] Spawning a symmetric two-node mesh..."
	@./hivemind -mode peer -node alpha-node > peer-a.log 2>&1 & PID_A=$$!; \
	echo "⏳ Node alpha-node spawning (PID: $$PID_A)..."; \
	W=0; while [ ! -S /tmp/hivemind-alpha-node.sock ] && [ $$W -lt 50 ]; do sleep 0.1; W=$$((W+1)); done; \
	echo "🔗 alpha-node socket up. Spawning beta-node in foreground (Ctrl+C to end)."; \
	./hivemind -mode peer -node beta-node || true; \
	kill -TERM $$PID_A 2>/dev/null || true; \
	sleep 1

test-timed: build
	@./timed_test.sh

clean:
	rm -f hivemind server.log client.log peer-a.log peer-b.log
	go clean -testcache

clean-soul:
	rm -rf .hive_memory
	@echo "👁️  [OVERMIND] Memory pool cleared."

