# ── HIVEMIND DECENTRALIZED AUTOMATION MAKEFILE ──

.PHONY: all help build test audit clean test-1 test-2 test-full test-timed clean-soul rotate-keys up

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
	@echo "    audit         run scripts/audit.sh (same gates as test)"
	@echo "    test-1        Run a peer node (terminal 1)"
	@echo "    test-2        Run a peer node (terminal 2; links to any others)"
	@echo "    test-full     Launch a two-peer mesh automatically"
	@echo "    test-timed    Automated 10s two-peer experiment with assertions"
	@echo "    up            Supervised mesh: raise peer nodes as child mains (Ctrl+C lays them down)"
	@echo "    rotate-keys   Wipe the machine-local relay key (next build mints fresh)"
	@echo "    clean         Remove build artifacts and logs"
	@echo "    clean-soul    Wipe .hive_memory (true extinction)"
	@echo ""

build: .relaykey
	go build -ldflags "-X gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind.compileRelayKey=$$(cat .relaykey)" -o bin/hivemind ./cmd/hivemind

# Machine-local relay key: generated once, never committed, baked into
# every build. Distinct machines get distinct keys out of the box, so
# relay frames stop sharing one global static key. Rotate with
# `make rotate-keys` (then rebuild + redistribute HIVEMIND_CIPHER_KEY
# out of band if far nodes must keep reading each other).
.relaykey:
	@echo "🔑 Generating machine-local relay key (.relaykey, never committed)..."
	@head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' > .relaykey
	@chmod 600 .relaykey

rotate-keys:
	rm -f .relaykey
	@echo "🔑 Relay key wiped — next build mints a fresh one. Rebuild all nodes."

test:
	@test -z "$$(gofmt -l .)" || { echo "❌ unformatted files:"; gofmt -l .; exit 1; }
	go vet ./...
	@go test ./... -race -count=1 2> /tmp/hivemind-make-test.log || \
	if grep -q "requires cgo" /tmp/hivemind-make-test.log; then \
		echo "⚠️  no C compiler — race detector unavailable, running plain tests"; \
		go test ./... -count=1; \
	else \
		cat /tmp/hivemind-make-test.log; \
		exit 1; \
	fi

audit: test
	./scripts/audit.sh

test-1: build
	bin/hivemind -mode peer -node alpha-node

test-2: build
	bin/hivemind -mode peer -node beta-node

test-full: build
	@mkdir -p logs && rm -f logs/peer-a.log logs/peer-b.log
	@echo "🚀 [PEER MESH] Spawning a symmetric two-node mesh..."
	@bin/hivemind -mode peer -node alpha-node > logs/peer-a.log 2>&1 & PID_A=$$!; \
	echo "⏳ Node alpha-node spawning (PID: $$PID_A)..."; \
	W=0; while [ ! -S /tmp/hivemind-alpha-node.sock ] && [ $$W -lt 50 ]; do sleep 0.1; W=$$((W+1)); done; \
	echo "🔗 alpha-node socket up. Spawning beta-node in foreground (Ctrl+C to end)."; \
	bin/hivemind -mode peer -node beta-node || true; \
	kill -TERM $$PID_A 2>/dev/null || true; \
	sleep 1

test-timed: build
	@./scripts/timed_test.sh

up: build
	bin/hivemind up -nodes 2

clean:
	rm -rf bin logs/*.log
	go clean -testcache

clean-soul:
	rm -rf .hive_memory
	@echo "👁️  [OVERMIND] Memory pool cleared."

