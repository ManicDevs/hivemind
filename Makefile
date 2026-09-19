# ── HIVEMIND MAKEFILE ──
# One binary, one mesh, proof on demand.
# Variables: make think N=25 TICK=10 — N nodes, TICK ms heartbeat.

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
	build build-hivemind build-commune build-souls build-gaze build-all \
	fmt vet staticcheck lint tidy check \
	test test-race test-verbose test-1 test-2 test-full test-timed \
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
	version version-info

all: build-all

help:
	@echo ""
	@echo "  HIVEMIND — one binary, one mesh, proof on demand"
	@echo ""
	@printf "  \033[1m%-20s\033[0m %s\n" "TARGET" "DESCRIPTION"
	@printf "  \033[1m%-20s\033[0m %s\n" "------" "-----------"
	@echo ""
	@printf "  \033[32m%-20s\033[0m %s\n" "build" "All four binaries"
	@printf "  \033[32m%-20s\033[0m %s\n" "all" "Same as build"
	@printf "  \033[32m%-20s\033[0m %s\n" "build-hivemind" "bin/hivemind only"
	@printf "  \033[32m%-20s\033[0m %s\n" "build-commune" "bin/commune only"
	@printf "  \033[32m%-20s\033[0m %s\n" "build-souls" "bin/souls only"
	@printf "  \033[32m%-20s\033[0m %s\n" "build-gaze" "bin/gaze only"
	@echo ""
	@printf "  \033[33m%-20s\033[0m %s\n" "test" "fmt + vet + tests (-race if cgo)"
	@printf "  \033[33m%-20s\033[0m %s\n" "test-race" "Tests with -race, no fallback"
	@printf "  \033[33m%-20s\033[0m %s\n" "test-verbose" "Verbose test output"
	@printf "  \033[33m%-20s\033[0m %s\n" "audit" "fmt + vet + build + tests"
	@printf "  \033[33m%-20s\033[0m %s\n" "lint" "gofmt + vet + staticcheck"
	@echo ""
	@printf "  \033[34m%-20s\033[0m %s\n" "up" "Supervised mesh, N nodes (default 2)"
	@printf "  \033[34m%-20s\033[0m %s\n" "down" "Reap all hivemind, sweep sockets"
	@printf "  \033[34m%-20s\033[0m %s\n" "restart" "down + up"
	@printf "  \033[34m%-20s\033[0m %s\n" "status" "Procs, sockets, key, souls"
	@printf "  \033[34m%-20s\033[0m %s\n" "test-1 / test-2" "One peer node (two terminals)"
	@printf "  \033[34m%-20s\033[0m %s\n" "test-full" "Two-peer mesh, beta foreground"
	@printf "  \033[34m%-20s\033[0m %s\n" "test-timed" "10s experiment + assertions"
	@echo ""
	@printf "  \033[35m%-20s\033[0m %s\n" "think" "Fast-tick mesh: N nodes, TICK ms"
	@printf "  \033[35m%-20s\033[0m %s\n" "think-fast" "N=25 TICK=10"
	@printf "  \033[35m%-20s\033[0m %s\n" "think-long" "N=100 TICK=100"
	@printf "  \033[35m%-20s\033[0m %s\n" "demo" "2 nodes, 20s, exits alone"
	@printf "  \033[35m%-20s\033[0m %s\n" "pain" "Idle-vs-loaded stimulus proof"
	@printf "  \033[35m%-20s\033[0m %s\n" "prove" "audit + timed + supermesh + transcript"
	@echo ""
	@printf "  \033[36m%-20s\033[0m %s\n" "doctor" "Environment capabilities"
	@printf "  \033[36m%-20s\033[0m %s\n" "souls" "Read the dead"
	@printf "  \033[36m%-20s\033[0m %s\n" "commune" "Speak with the hive"
	@printf "  \033[36m%-20s\033[0m %s\n" "gaze" "Watch the living mesh"
	@printf "  \033[36m%-20s\033[0m %s\n" "rotate-keys" "Mint fresh relay key (rebuild after)"
	@printf "  \033[36m%-20s\033[0m %s\n" "list-keys" "Show current relay key"
	@echo ""
	@printf "  \033[36m%-20s\033[0m %s\n" "release" "6 cross binaries + SHA256SUMS"
	@printf "  \033[36m%-20s\033[0m %s\n" "release-hardened" "Stripped binary (backtraces degrade)"
	@printf "  \033[36m%-20s\033[0m %s\n" "release-clean" "Remove dist/"
	@echo ""
	@printf "  \033[36m%-20s\033[0m %s\n" "dev" "Retest on change (needs entr)"
	@printf "  \033[36m%-20s\033[0m %s\n" "install" "Copy bin/* to ~/bin"
	@printf "  \033[36m%-20s\033[0m %s\n" "uninstall" "Remove from ~/bin"
	@printf "  \033[36m%-20s\033[0m %s\n" "clean" "bin + logs + testcache"
	@printf "  \033[36m%-20s\033[0m %s\n" "clean-all" "clean + dist + relay key"
	@printf "  \033[36m%-20s\033[0m %s\n" "clean-souls" "Wipe .hive_memory (extinction)"
	@printf "  \033[36m%-20s\033[0m %s\n" "version" "git describe or v0.0.0-dev"
	@echo ""
	@echo "  make think N=25 TICK=10 · make up N=3 · make prove"
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

build-all: build-hivemind build-commune build-souls build-gaze
	@echo "✅ all binaries ready (bin/hivemind bin/commune bin/souls bin/gaze)"

# ── Relay key: generated once, never committed, baked into every build.
# Distinct machines get distinct keys out of the box. Rotate with
# `make rotate-keys`, then rebuild + share HIVEMIND_CIPHER_KEY out of
# band if far nodes must keep reading each other.
$(RELAY_KEY_FILE):
	@echo "🔑 Generating machine-local relay key ($(RELAY_KEY_FILE), never committed)..."
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
	@which staticcheck >/dev/null 2>&1 && staticcheck ./... && echo "✅ staticcheck clean" || echo "⚠️  staticcheck not installed (go install honnef.co/go/tools/cmd/staticcheck@latest)"

lint: fmt vet staticcheck

tidy:
	@go mod tidy
	@echo "✅ Tidy"

check: lint tidy
	@echo "✅ All checks passed"

# ── Testing (check first, -race with plain fallback where cgo is absent) ──
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

test-1: build
	@bin/hivemind -mode peer -node alpha-node

test-2: build
	@bin/hivemind -mode peer -node beta-node

test-full: build
	@mkdir -p $(LOG_DIR) && rm -f $(LOG_DIR)/peer-a.log $(LOG_DIR)/peer-b.log
	@echo "🚀 [PEER MESH] Spawning symmetric two-node mesh..."
	@bin/hivemind -mode peer -node alpha-node > $(LOG_DIR)/peer-a.log 2>&1 & PID_A=$$!; \
	echo "⏳ alpha-node spawning (PID: $$PID_A)..."; \
	W=0; while [ ! -S /tmp/hivemind-alpha-node.sock ] && [ $$W -lt 50 ]; do sleep 0.1; W=$$((W+1)); done; \
	echo "🔗 alpha-node up. beta-node in foreground (Ctrl+C to end)."; \
	bin/hivemind -mode peer -node beta-node || true; \
	kill -TERM $$PID_A 2>/dev/null || true; \
	sleep 1

test-timed: build
	@./scripts/timed_test.sh

audit: test
	@./scripts/audit.sh

# ── Mesh operations ──
up: build
	@bin/hivemind up -nodes $(N)

up-nodes: up

down:
	@echo "🛑 Stopping all hivemind processes..."
	@pkill -x hivemind 2>/dev/null || true
	@sleep 1
	@rm -f /tmp/hivemind-*.sock /tmp/hivemind.sock
	@echo "✅ All stopped"

restart: down up

kill: down # historic alias — the ritual stays

rerun: restart # historic alias: kill + build + up (up rebuilds)

status:
	@echo "=== HIVEMIND STATUS ==="
	@pgrep -a -x hivemind || echo "No hivemind processes running"
	@echo ""
	@echo "Sockets:"
	@ls /tmp/hivemind-*.sock 2>/dev/null || echo "  (none)"
	@echo ""
	@if [ -f $(RELAY_KEY_FILE) ]; then echo "Relay key: present"; else echo "Relay key: MISSING (run make build)"; fi
	@if [ -d .hive_memory ]; then echo "Souls: $$(find .hive_memory -name '*.soul' | wc -l)"; else echo "Souls: none"; fi

# ── Think: supervised fast-tick mesh. Cross-site join needs the shared
# blood: export the same HIVEMIND_CIPHER_KEY on both machines first —
# without it each box dreams alone (machine-local keys).
think: build
	@if [ -z "$$HIVEMIND_CIPHER_KEY" ]; then echo "⚠️  no HIVEMIND_CIPHER_KEY — single-box mesh (dreams alone). Export a shared key to join sites."; else echo "🩸 shared key present — this mesh can join its sibling."; fi
	@echo "🧠 THINK: $(N) nodes at $(TICK)ms — Ctrl+C lays them down."
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
	echo "✅ PROOF COMPLETE — transcript: $$PROOF" | tee -a "$$PROOF"

# ── Read / speak / watch ──
doctor:
	@echo "🩺 hivemind doctor"
	@go version
	@gcc --version 2>/dev/null | head -1 || echo "  ⚠️  no C compiler (-race unavailable)"
	@[ -r /proc/loadavg ] && echo "  ✔ /proc/loadavg (cpu)" || echo "  ❌ no /proc/loadavg"
	@[ -r /proc/meminfo ] && echo "  ✔ /proc/meminfo (ram)" || echo "  ❌ no /proc/meminfo"
	@ls /sys/class/thermal/thermal_zone*/temp >/dev/null 2>&1 && echo "  ✔ thermal sensor (pain)" || echo "  ⚠️  no thermal sensor (estimates only)"
	@[ -r /sys/devices/system/cpu/cpu0/cpufreq/scaling_cur_freq ] && echo "  ✔ cpufreq (throttle)" || echo "  ⚠️  no cpufreq (no throttle sense)"
	@curl -s -o /dev/null --max-time 8 https://ntfy.sh && echo "  ✔ relay reachable (ntfy.sh)" || echo "  ⚠️  no relay egress (mesh still fully local-capable)"
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
	@echo "🧹 Deep cleaned (including dist/ and relay key)"

clean-souls:
	@rm -rf .hive_memory
	@echo "👁️  [OVERMIND] Memory pool cleared."

clean-logs:
	@rm -f $(LOG_DIR)/*.log
	@echo "🧹 Logs cleaned"

# ── Release: cross-compiled hivemind binaries land in dist/ (gitignored —
# they ship attached to the release, never committed). Hardened strips
# symbols at the honest cost of readable backtraces.
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
	@echo "🛡️  Building hardened linux/amd64 (backtraces degrade)..."
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
	@rm -f ~/bin/hivemind ~/bin/commune ~/bin/souls ~/bin/gaze
	@echo "✅ Uninstalled from ~/bin"

version:
	@git describe --tags --always --dirty 2>/dev/null || echo "v0.0.0-dev"

version-info:
	@echo "Version: $$(git describe --tags --always --dirty 2>/dev/null || echo 'v0.0.0-dev')"
	@echo "Commit: $$(git rev-parse --short HEAD 2>/dev/null || echo nogit)"
	@echo "Branch: $$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo nogit)"
	@echo "Build: $$(date -u +%Y%m%dT%H%M%SZ)"
