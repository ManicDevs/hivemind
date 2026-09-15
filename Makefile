# ── HIVEMIND DECENTRALIZED AUTOMATION MAKEFILE ──

.PHONY: all help build clean test-1 test-2 test-full test-timed make-1 make-2 clean-soul

all: help

help:
	@echo ""
	@echo "  HIVEMIND DECENTRALIZED PROCESS CONTROLLER"
	@echo ""
	@echo "  Usage: make <command>"
	@echo ""
	@echo "  Available Commands:"
	@echo "    build         Compile active Go package trees into native executable binaries"
	@echo "    test-1        Initialize primary host file descriptor interface (LUDS server)"
	@echo "    test-2        Initialize persistent worker attachment client with 4D vectors"
	@echo "    test-full     Launch complete dual-node local mesh cluster test automatically"
	@echo "    test-timed    Run automated 10-second timed cluster test with summary capture"
	@echo "    make-1        Shorthand alias route targeting local test-1 orchestration"
	@echo "    make-2        Shorthand alias route targeting local test-2 orchestration"
	@echo "    clean         Purge compiled target artifacts and cache pools from memory"
	@echo "    clean-soul    Execute hard-wipe erasure across all stored genetic memory frames"
	@echo ""

build:
	go build -o hivemind ./...

test-1: build
	./hivemind -mode server

test-2: build
	./hivemind -mode client

make-1: test-1
make-2: test-2

test-full: build
	@echo "🚀 [MESH AUTOMATION] Initializing dual-node serverless layout..."
	@rm -f server.log
	@./hivemind -mode server > server.log 2>&1 & SERVER_PID=$$! ; \
	echo "⏳ [MESH AUTOMATION] Spawning server process (PID: $$SERVER_PID)... Waiting for LUDS descriptor..."; \
	sleep 1.5; \
	echo "🔗 [MESH AUTOMATION] Spawning client process concurrently... Intercom cross-talk live."; \
	echo "💡 [MESH AUTOMATION] Press Ctrl+C at any time to terminate the automated grid cluster."; \
	echo ""; \
	./hivemind -mode client; \
	echo "🧹 [MESH AUTOMATION] Catching termination signal. Cleaning up cluster threads..."; \
	kill $$SERVER_PID 2>/dev/null || true; \
	rm -f /tmp/hivemind.sock

test-timed: build
	@./timed_test.sh

clean:
	go clean
	rm -f hivemind server.log client.log timed_test.sh

clean-soul:
	rm -rf .hive_memory
	@echo "👁️  [OVERMIND] Memory pool cleared."
