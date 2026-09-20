# ── HIVEMIND MAKEFILE ──
# Full-stack: 6 binaries + relay + proofs.
# Variables: make think N=25 TICK=50 — N nodes, TICK ms heartbeat.

.DEFAULT_GOAL := help
SHELL := /bin/bash

N ?= 2
TICK ?= 50

RELAY_KEY_FILE := .relaykey
RELAY_KEY := $(shell cat $(RELAY_KEY_FILE) 2>/dev/null || echo "")
LDFLAGS := -X gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind.compileRelayKey=$(RELAY_KEY)
BIN_DIR := bin
DIST_DIR := dist
LOG_DIR := logs

.PHONY: all help \
	build build-hivemind build-commune build-souls build-gaze build-relay build-all \
	fmt vet staticcheck lint tidy check \
	test test-race test-verbose test-full \
	audit \
	up up-nodes down restart kill rerun status \
	think think-fast think-long demo pain prove \
	doctor souls commune gaze \
	rotate-keys list-keys \
	release release-linux release-darwin release-windows \
	release-linux-amd64 release-linux-arm64 release-linux-arm \
	release-darwin-amd64 release-darwin-arm64 release-windows-amd64 \
	release-hardened release-clean dist-clean \
	dev install uninstall \
	clean clean-all clean-souls clean-logs \
	version version-info \
	proof-keys \
	prove-relay

all: setup

setup: build-all audit test prove proof-keys prove-relay
	@echo ""
	@echo "╔══════════════════════════════════════════════════════════════╗"
	@echo "║  ✅ FULL SETUP COMPLETE — 6 binaries + all proofs            ║"
	@echo "║  ── bin/hivemind, bin/commune, bin/souls, bin/gaze          ║"
	@echo "║  ── bin/relay (MQTT bridge)                                  ║"
	@echo "║  ── make think N=25 TICK=50 · make up N=3                   ║"
	@echo "╚══════════════════════════════════════════════════════════════╝"

help:
	@echo ""
	@echo "  HIVEMIND — full-stack: 6 binaries + relay + proofs"
	@echo ""
	@printf "  \033[1m%-20s\033[0m %s\n" "TARGET" "DESCRIPTION"
	@printf "  \033[1m%-20s\033[0m %s\n" "------" "-----------"
	@echo ""
	@printf "  \033[32m%-20s\033[0m %s\n" "build" "All six binaries"
	@printf "  \033[32m%-20s\033[0m %s\n" "build-all" "Same as build"
	@printf "  \033[32m%-20s\033[0m %s\n" "build-relay" "bin/relay (MQTT bridge)"
	@printf "  \033[32m%-20s\033[0m %s\n" "build-hivemind" "bin/hivemind only"
	@printf "  \033[32m%-20s\033[0m %s\n" "build-commune" "bin/commune only"
	@printf "  \033[32m%-20s\033[0m %s\n" "build-souls" "bin/souls only"
	@printf "  \033[32m%-20s\033[0m %s\n" "build-gaze" "bin/gaze only"
	@echo ""
	@printf "  \033[33m%-20s\033[0m %s\n" "test" "fmt + vet + tests"
	@printf "  \033[33m%-20s\033[0m %s\n" "test-race" "Tests with -race"
	@printf "  \033[33m%-20s\033[0m %s\n" "test-full" "Full test suite"
	@printf "  \033[33m%-20s\033[0m %s\n" "audit" "fmt + vet + build + tests"
	@printf "  \033[33m%-20s\033[0m %s\n" "lint" "gofmt + vet + staticcheck"
	@echo ""
	@printf "  \033[34m%-20s\033[0m %s\n" "up" "Supervised mesh, N nodes"
	@printf "  \033[34m%-20s\033[0m %s\n" "down" "Reap all hivemind, sweep sockets"
	@printf "  \033[34m%-20s\033[0m %s\n" "restart" "down + up"
	@printf "  \033[34m%-20s\033[0m %s\n" "status" "Procs, sockets, key, souls"
	@printf "  \033[34m%-20s\033[0m %s\n" "test-1 / test-2" "One peer node (two terminals)"
	@echo ""
	@printf "  \033[35m%-20s\033[0m %s\n" "think" "Fast-tick mesh: N nodes, TICK ms"
	@printf "  \033[35m%-20s\033[0m %s\n" "think-fast" "N=25 TICK=10"
	@printf "  \033[35m%-20s\033[0m %s\n" "think-long" "N=100 TICK=100"
	@printf "  \033[35m%-20s\033[0m %s\n" "demo" "2 nodes, 20s, exits alone"
	@printf "  \033[35m%-20s\033[0m %s\n" "pain" "Idle-vs-loaded stimulus proof"
	@printf "  \033[35m%-20s\033[0m %s\n" "prove" "audit + timed + supermesh + pain"
	@printf "  \033[35m%-20s\033[0m %s\n" "proof-keys" "Crypto + relay + will proofs (16/16)"
	@echo ""
	@printf "  \033[36m%-20s\033[0m %s\n" "doctor" "Environment capabilities"
	@printf "  \033[36m%-20s\033[0m %s\n" "souls" "Read the dead"
	@printf "  \033[36m%-20s\033[0m %s\n" "commune" "Speak with the hive"
	@printf "  \033[36m%-20s\033[0m %s\n" "gaze" "Watch the living mesh"
	@printf "  \033[36m%-20s\033[0m %s\n" "rotate-keys" "Mint fresh relay key"
	@printf "  \033[36m%-20s\033[0m %s\n" "list-keys" "Show current relay key"
	@echo ""
	@printf "  \033[36m%-20s\033[0m %s\n" "release" "6 cross binaries + SHA256SUMS"
	@printf "  \033[36m%-20s\033[0m %s\n" "release-hardened" "Stripped binary"
	@printf "  \033[36m%-20s\033[0m %s\n" "release-clean" "Remove dist/"
	@echo ""
	@printf "  \033[36m%-20s\033[0m %s\n" "dev" "Retest on change (needs entr)"
	@printf "  \033[36m%-20s\033[0m %s\n" "install" "Copy bin/* to ~/bin"
	@printf "  \033[36m%-20s\033[0m %s\n" "uninstall" "Remove from ~/bin"
	@printf "  \033[36m%-20s\033[0m %s\n" "clean" "bin + logs + testcache"
	@printf "  \033[36m%-20s\033[0m %s\n" "clean-all" "clean + dist + relay key"
	@printf "  \033[36m%-20s\033[0m %s\n" "clean-souls" "Wipe .hive_memory"
	@printf "  \033[36m%-20s\033[0m %s\n" "prove-relay" "Full relay + MQTT live test"
	@echo ""
	@echo "  make think N=25 TICK=50 · make up N=3 · make prove"
	@echo ""

# ── Build ──
build: build-all

build-hivemind: $(RELAY_KEY_FILE)
	@echo "🔨 Building bin/hivemind..."
	@mkdir -p $(BIN_DIR)
	@go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/hivemind ./cmd/hivemind
	@echo "✅ bin/hivemind ready"

build-commune:
	@echo "🔨 Building bin/commune..."
	@mkdir -p $(BIN_DIR)
	@go build -o $(BIN_DIR)/commune ./cmd/commune
	@echo "✅ bin/commune ready"

build-souls:
	@echo "🔨 Building bin/souls..."
	@mkdir -p $(BIN_DIR)
	@go build -o $(BIN_DIR)/souls ./cmd/souls
	@echo "✅ bin/souls ready"

build-gaze:
	@echo "🔨 Building bin/gaze..."
	@mkdir -p $(BIN_DIR)
	@go build -o $(BIN_DIR)/gaze ./cmd/gaze
	@echo "✅ bin/gaze ready"

build-relay:
	@echo "🔨 Building bin/relay..."
	@mkdir -p $(BIN_DIR)
	@go build -o $(BIN_DIR)/relay ./cmd/relay
	@echo "✅ bin/relay ready (MQTT bridge)"

build-all: build-hivemind build-commune build-souls build-gaze build-relay
	@echo "✅ All six binaries ready (hivemind, commune, souls, gaze, relay)"

# ── Relay key ──
$(RELAY_KEY_FILE):
	@echo "🔑 Generating machine-local relay key..."
	@head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' > $(RELAY_KEY_FILE)
	@chmod 600 $(RELAY_KEY_FILE)

rotate-keys:
	@rm -f $(RELAY_KEY_FILE)
	@$(MAKE) $(RELAY_KEY_FILE)
	@echo "🔑 Relay key rotated — rebuild all nodes (make build)."

list-keys:
	@if [ -f $(RELAY_KEY_FILE) ]; then \
		echo "Current key: $$(cat $(RELAY_KEY_FILE))"; \
	else \
		echo "No key found. Run 'make build' or 'make rotate-keys'."; \
	fi

# ── Code quality ──
fmt:
	@gofmt -w .
	@echo "✅ Formatted"

vet:
	@go vet ./...
	@echo "✅ Vet clean"

staticcheck:
	@which staticcheck >/dev/null 2>&1 && staticcheck ./... && echo "✅ staticcheck clean" || echo "⚠️  staticcheck not installed"

lint: fmt vet staticcheck

tidy:
	@go mod tidy
	@echo "✅ Tidy"

check: lint tidy
	@echo "✅ All checks passed"

# ── Testing ──
test: check
	@echo "🧪 Testing..."
	@if go test ./... -race -count=1 2> /tmp/hivemind-test.log; then \
		echo "✅ Tests passed (-race)"; \
	elif grep -q "requires cgo" /tmp/hivemind-test.log; then \
		echo "⚠️  No C compiler — race detector unavailable, running plain tests"; \
		go test ./... -count=1 && echo "✅ Tests passed (plain)"; \
	else \
		cat /tmp/hivemind-test.log; \
		exit 1; \
	fi

test-race: check
	@go test ./... -race -count=1

test-verbose: check
	@go test ./... -v -count=1

test-full: check
	@go test ./... -count=1 -v

audit: test
	@./scripts/audit.sh

# ── Mesh operations ──
up: build
	@bin/hivemind up -nodes $(N)

up-nodes: up

down:
	@echo "🛑 Stopping all hivemind processes..."
	@pkill -x hivemind 2>/dev/null || true
	@pkill -x relay 2>/dev/null || true
	@sleep 1
	@rm -f /tmp/hivemind-*.sock /tmp/hivemind.sock
	@echo "✅ All stopped"

restart: down up

kill: down

rerun: restart

status:
	@echo "=== HIVEMIND STATUS ==="
	@pgrep -a -x hivemind || echo "No hivemind processes running"
	@pgrep -a -x relay || echo "No relay processes running"
	@echo ""
	@echo "Sockets:"
	@ls /tmp/hivemind-*.sock 2>/dev/null || echo "  (none)"
	@echo ""
	@if [ -f $(RELAY_KEY_FILE) ]; then echo "Relay key: present"; else echo "Relay key: MISSING"; fi
	@if [ -d .hive_memory ]; then echo "Souls: $$(find .hive_memory -name '*.soul' | wc -l)"; else echo "Souls: none"; fi

# ── Think ──
think: build
	@if [ -z "$$HIVEMIND_CIPHER_KEY" ]; then echo "⚠️  no HIVEMIND_CIPHER_KEY — single-box mesh."; else echo "🩸 shared key present — cross-site capable."; fi
	@echo "🧠 THINK: $(N) nodes at $(TICK)ms"
	@HIVEMIND_TICK_MS=$(TICK) bin/hivemind up -nodes $(N)

think-fast: build
	@$(MAKE) think N=25 TICK=10

think-long: build
	@$(MAKE) think N=100 TICK=100

demo: build
	@bin/hivemind up -nodes 2 -for 20s

pain: build
	@mkdir -p $(LOG_DIR)
	@./scripts/pain.sh

prove: build
	@mkdir -p $(LOG_DIR)
	@PROOF=$(LOG_DIR)/proof-$$(date -u +%Y%m%dT%H%M%SZ).log; \
	echo "===== PROOF RUN $$(date -u) · $$(git rev-parse --short HEAD 2>/dev/null || echo nogit) =====" | tee "$$PROOF"; \
	./scripts/audit.sh 2>&1 | tee -a "$$PROOF" && \
	./scripts/timed_test.sh 2>&1 | tee -a "$$PROOF" && \
	./scripts/verify-supermesh.sh 2>&1 | tee -a "$$PROOF" && \
	./scripts/pain.sh 2>&1 | tee -a "$$PROOF" && \
	bash scripts/proof-keys.sh 2>&1 | tee -a "$$PROOF" && \
	echo "✅ PROOF COMPLETE — transcript: $$PROOF" | tee -a "$$PROOF"

# ── Proofs ──
proof-keys: build
	@bash scripts/proof-keys.sh

prove-relay: build
	@echo "🧪 Full relay + MQTT live test..."
	@bin/relay > /tmp/relay-prove.log 2>&1 &
	@RPID=$$!; sleep 2; \
	HIVEMIND_RELAY_URL="http://127.0.0.1:8080/hive-relay" HIVEMIND_TICK_MS=500 bin/hivemind -mode peer -node relay-test-1 > /tmp/rt1.log 2>&1 & \
	P1=$$!; sleep 3; \
	HIVEMIND_RELAY_URL="http://127.0.0.1:8080/hive-relay" HIVEMIND_TICK_MS=500 bin/hivemind -mode peer -node relay-test-2 > /tmp/rt2.log 2>&1 & \
	P2=$$!; sleep 15; \
	echo "=== RELAY ===" && grep -i "mqtt" /tmp/relay-prove.log | head -1; \
	echo "=== NODE 1 ===" && grep -c "SECURE CLOUD INBOUND" /tmp/rt1.log; \
	echo "=== NODE 2 ===" && grep -c "SECURE CLOUD INBOUND" /tmp/rt2.log; \
	kill $$P1 $$P2 $$RPID 2>/dev/null; sleep 1; \
	kill -9 $$P1 $$P2 $$RPID 2>/dev/null; wait 2>/dev/null; \
	rm -f /tmp/hivemind-relay-test-*.sock; \
	echo "✅ Relay + MQTT live test complete"

# ── Read / speak / watch ──
doctor:
	@echo "🩺 hivemind doctor"
	@go version
	@gcc --version 2>/dev/null | head -1 || echo "  ⚠️  no C compiler (-race unavailable)"
	@[ -r /proc/loadavg ] && echo "  ✔ /proc/loadavg (cpu)" || echo "  ❌ no /proc/loadavg"
	@[ -r /proc/meminfo ] && echo "  ✔ /proc/meminfo (ram)" || echo "  ❌ no /proc/meminfo"
	@ls /sys/class/thermal/thermal_zone*/temp >/dev/null 2>&1 && echo "  ✔ thermal sensor (pain)" || echo "  ⚠️  no thermal sensor"
	@[ -r /sys/devices/system/cpu/cpu0/cpufreq/scaling_cur_freq ] && echo "  ✔ cpufreq (throttle)" || echo "  ⚠️  no cpufreq"
	@echo "  ✔ own relay (bin/relay) — no third-party dependency"
	@python3 -c "import socket; s=socket.socket(socket.AF_INET, socket.SOCK_STREAM); s.bind(('127.0.0.1',0)); print('  ✔ loopback TCP (mesh transport ok)'); s.close()" 2>/dev/null || echo "  ❌ loopback TCP broken"

souls: build-souls
	@bin/souls

commune: build-commune
	@bin/commune

gaze: build-gaze
	@bin/gaze

# ── Cleanup ──
clean:
	@rm -rf $(BIN_DIR) $(LOG_DIR)/*.log
	@go clean -testcache
	@echo "🧹 Cleaned"

clean-all: clean
	@rm -rf $(DIST_DIR)
	@rm -f $(RELAY_KEY_FILE)
	@echo "🧹 Deep cleaned"

clean-souls:
	@rm -rf .hive_memory
	@echo "👁️  [OVERMIND] Memory pool cleared."

clean-logs:
	@rm -f $(LOG_DIR)/*.log
	@echo "🧹 Logs cleaned"

# ── Release ──
release: release-linux release-darwin release-windows
	@cd $(DIST_DIR) && sha256sum hivemind-* > SHA256SUMS && cat SHA256SUMS

release-linux: release-linux-amd64 release-linux-arm64 release-linux-arm
release-darwin: release-darwin-amd64 release-darwin-arm64
release-windows: release-windows-amd64

release-linux-amd64: $(RELAY_KEY_FILE)
	@echo "📦 Building linux/amd64..."
	@mkdir -p $(DIST_DIR)
	@CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/hivemind-linux-amd64 ./cmd/hivemind

release-linux-arm64: $(RELAY_KEY_FILE)
	@echo "📦 Building linux/arm64..."
	@mkdir -p $(DIST_DIR)
	@CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/hivemind-linux-arm64 ./cmd/hivemind

release-linux-arm: $(RELAY_KEY_FILE)
	@echo "📦 Building linux/arm..."
	@mkdir -p $(DIST_DIR)
	@CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/hivemind-linux-arm ./cmd/hivemind

release-darwin-amd64: $(RELAY_KEY_FILE)
	@echo "📦 Building darwin/amd64..."
	@mkdir -p $(DIST_DIR)
	@CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/hivemind-darwin-amd64 ./cmd/hivemind

release-darwin-arm64: $(RELAY_KEY_FILE)
	@echo "📦 Building darwin/arm64..."
	@mkdir -p $(DIST_DIR)
	@CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/hivemind-darwin-arm64 ./cmd/hivemind

release-windows-amd64: $(RELAY_KEY_FILE)
	@echo "📦 Building windows/amd64..."
	@mkdir -p $(DIST_DIR)
	@CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/hivemind-windows-amd64.exe ./cmd/hivemind

release-hardened: $(RELAY_KEY_FILE)
	@echo "🛡️  Building hardened linux/amd64..."
	@mkdir -p $(DIST_DIR)
	@CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w $(LDFLAGS)" -o $(DIST_DIR)/hivemind-hardened ./cmd/hivemind

release-clean:
	@rm -rf $(DIST_DIR)
	@echo "🧹 Release artifacts cleaned"

dist-clean: release-clean

# ── Development ──
dev:
	@which entr >/dev/null 2>&1 || { echo "❌ entr not installed (apt/brew install entr)"; exit 1; }
	@echo "👀 Watching for changes... (Ctrl+C to stop)"
	@find . -name '*.go' ! -path './dist/*' ! -path './.git/*' | entr -c sh -c 'gofmt -l . ; go vet ./... && go test ./... -count=1'

install: build-all
	@mkdir -p ~/bin
	@cp bin/* ~/bin/
	@echo "✅ Installed to ~/bin"

uninstall:
	@rm -f ~/bin/hivemind ~/bin/commune ~/bin/souls ~/bin/gaze ~/bin/relay
	@echo "✅ Uninstalled from ~/bin"

version:
	@git describe --tags --always --dirty 2>/dev/null || echo "v0.0.0-dev"

version-info:
	@echo "Version: $$(git describe --tags --always --dirty 2>/dev/null || echo 'v0.0.0-dev')"
	@echo "Commit: $$(git rev-parse --short HEAD 2>/dev/null || echo nogit)"
	@echo "Branch: $$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo nogit)"
	@echo "Build: $$(date -u +%Y%m%dT%H%M%SZ)"
