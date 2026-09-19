# ── HIVEMIND DECENTRALIZED AUTOMATION MAKEFILE ──

.DEFAULT_GOAL := help
SHELL := /bin/bash

# Configurable variables
N ?= 10
TICK ?= 50
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)

# Derived
RELAY_KEY_FILE := .relaykey
RELAY_KEY := $(shell cat $(RELAY_KEY_FILE) 2>/dev/null || echo "")
LDFLAGS := -X gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind.compileRelayKey=$(RELAY_KEY)
BIN_DIR := bin
DIST_DIR := dist
LOG_DIR := logs

.PHONY: all help build build-hivemind build-commune build-souls build-all \
	test test-race test-all test-verbose audit clean clean-all clean-souls clean-logs \
	fmt vet lint staticcheck tidy check \
	up up-nodes down restart status \
	test-1 test-2 test-full test-timed test-all \
	demo pain prove \
	think think-fast think-long \
	doctor souls souls-build gaze gaze-build \
	commune commune-build \
	rotate-keys new-key \
	release release-all release-clean dist-clean \
	release-linux release-darwin release-windows release-all-arch \
	release-hardened release-signed \
	dev watch install uninstall \
	install-bin install-completion uninstall-completion \
	generate-key list-keys \
	version git-version bump-patch bump-minor bump-major \
	release-tag push-tag \
	ci ci-test ci-build ci-release

# Default target
all: build-all

help:
	@echo ""
	@echo "  HIVEMIND - Decentralized mesh of thinking minds"
	@echo "  One binary, one mesh, proof on demand"
	@echo ""
	@printf "  \033[1m%-20s\033[0m %s\n" "TARGET" "DESCRIPTION"
	@printf "  \033[1m%-20s\033[0m %s\n" "------" "-----------"
	@echo ""
	@printf "  \033[32m%-20s\033[0m %s\n" "build" "Compile all binaries (hivemind + commune + souls + gaze)"
	@printf "  \033[32m%-20s\033[0m %s\n" "build-hivemind" "Compile bin/hivemind only"
	@printf "  \033[32m%-20s\033[0m %s\n" "build-commune" "Compile bin/commune only"
	@printf "  \033[32m%-20s\033[0m %s\n" "build-souls" "Compile bin/souls only"
	@printf "  \033[32m%-20s\033[0m %s\n" "build-all" "Compile all four binaries"
	@echo ""
	@printf "  \033[33m%-20s\033[0m %s\n" "test" "Run tests (fmt + vet + race)"
	@printf "  \033[33m%-20s\033[0m %s\n" "test-race" "Run tests with -race"
	@printf "  \033[33m%-20s\033[0m %s\n" "test-all" "All test suites"
	@printf "  \033[33m%-20s\033[0m %s\n" "test-verbose" "Verbose test output"
	@printf "  \033[33m%-20s\033[0m %s\n" "audit" "Full audit: fmt + vet + test + staticcheck"
	@echo ""
	@printf "  \033[34m%-20s\033[0m %s\n" "up" "Start supervised mesh (default 2 nodes)"
	@printf "  \033[34m%-20s\033[0m %s\n" "up-nodes N=5" "Start N nodes"
	@printf "  \033[34m%-20s\033[0m %s\n" "down" "Stop all nodes"
	@printf "  \033[34m%-20s\033[0m %s\n" "restart" "Restart mesh"
	@printf "  \033[34m%-20s\033[0m %s\n" "status" "Show mesh status"
	@echo ""
	@printf "  \033[35m%-20s\033[0m %s\n" "think" "THINK: N=10 TICK=50ms mesh"
	@printf "  \033[35m%-20s\033[0m %s\n" "think-fast" "THINK: N=25 TICK=10ms"
	@printf "  \033[35m%-20s\033[0m %s\n" "think-long" "THINK: N=100 TICK=100ms"
	@printf "  \033[35m%-20s\033[0m %s\n" "demo" "20s supervised demo"
	@printf "  \033[35m%-20s\033[0m %s\n" "pain" "Idle vs Load stimulus proof"
	@printf "  \033[35m%-20s\033[0m %s\n" "prove" "Full proof: audit + timed + supermesh"
	@echo ""
	@printf "  \033[36m%-20s\033[0m %s\n" "doctor" "Environment check"
	@printf "  \033[36m%-20s\033[0m %s\n" "souls" "Read souls: census, genome, timeline, matrix"
	@printf "  \033[36m%-20s\033[0m %s\n" "commune" "Interactive hive conversation"
	@printf "  \033[36m%-20s\033[0m %s\n" "gaze" "Watch the living mesh (pain bars, thoughts, fame)"
	@printf "  \033[36m%-20s\033[0m %s\n" "doctor" "Environment health check"
	@printf "  \033[36m%-20s\033[0m %s\n" "rotate-keys" "Rotate relay encryption key"
	@echo ""
	@printf "  \033[36m%-20s\033[0m %s\n" "release" "Cross-compile 6 binaries to dist/"
	@printf "  \033[36m%-20s\033[0m %s\n" "release-all" "Release + SHA256SUMS"
	@printf "  \033[36m%-20s\033[0m %s\n" "release-hardened" "Stripped anti-debug binary"
	@printf "  \033[36m%-20s\033[0m %s\n" "release-clean" "Clean dist/"
	@echo ""
	@printf "  \033[36m%-20s\033[0m %s\n" "dev" "Watch mode (needs entr)"
	@printf "  \033[36m%-20s\033[0m %s\n" "install" "Install to ~/bin or /usr/local/bin"
	@printf "  \033[36m%-20s\033[0m %s\n" "clean" "Remove build artifacts"
	@printf "  \033[36m%-20s\033[0m %s\n" "clean-all" "Deep clean including dist/"
	@echo ""
	@echo "  Variables: N=10 TICK=50 (for think/up-nodes)"
	@echo "  Example: make think N=25 TICK=10"
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

build-all: build-hivemind build-commune build-souls gaze-build

# ── Relay Key Management ──
$(RELAY_KEY_FILE):
	@echo "🔑 Generating machine-local relay key ($(RELAY_KEY_FILE))..."
	@head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' > $(RELAY_KEY_FILE)
	@chmod 600 $(RELAY_KEY_FILE)
	@echo "✅ Key generated: $(RELAY_KEY_FILE)"

rotate-keys: clean-souls
	@echo "🔄 Rotating relay key..."
	@rm -f $(RELAY_KEY_FILE)
	@$(MAKE) $(RELAY_KEY_FILE)
	@echo "✅ Key rotated. Rebuild all nodes: make build"

new-key: rotate-keys

list-keys:
	@if [ -f $(RELAY_KEY_FILE) ]; then \
		echo "Current key: $$(cat $(RELAY_KEY_FILE))"; \
	else \
		echo "No key found. Run 'make rotate-keys'"; \
	fi

# ── Code Quality ──
fmt:
	@echo "🎨 Formatting..."
	@gofmt -w .
	@echo "✅ Formatted"

vet:
	@echo "🔍 Vetting..."
	@go vet ./...
	@echo "✅ Vet clean"

staticcheck:
	@which staticcheck >/dev/null 2>&1 && staticcheck ./... && echo "✅ staticcheck clean" || echo "⚠️  staticcheck not installed (go install honnef.co/go/tools/cmd/staticcheck@latest)"

lint: fmt vet staticcheck

tidy:
	@echo "🧹 Tidying modules..."
	@go mod tidy
	@echo "✅ Tidy"

check: lint tidy
	@echo "✅ All checks passed"

# ── Testing ──
test: check
	@echo "🧪 Testing (-race)..."
	@if go test ./... -race -count=1 2> /tmp/hivemind-test.log; then \
		echo "✅ Tests passed (-race)"; \
	else \
		if grep -q "requires cgo" /tmp/hivemind-test.log; then \
			echo "⚠️  No C compiler — race detector unavailable, running plain tests"; \
			go test ./... -count=1 && echo "✅ Tests passed (plain)"; \
		else \
			cat /tmp/hivemind-test.log; \
			exit 1; \
		fi \
	fi

test-race: check
	@echo "🧪 Testing with -race..."
	@go test ./... -race -count=1

test-verbose: check
	@go test ./... -v -count=1

test-all: test-race

# Test targets for manual mesh testing
test-1: build
	@bin/hivemind -mode peer -node alpha-node

test-2: build
	@bin/hivemind -mode peer -node beta-node

test-full: build
	@mkdir -p $(LOG_DIR) && rm -f $(LOG_DIR)/peer-a.log $(LOG_DIR)/peer-b.log
	@echo "🚀 [PEER MESH] Spawning symmetric two-node mesh..."
	@bin/hivemind -mode peer -node alpha-node > $(LOG_DIR)/peer-a.log 2>&1 & PID_A=$$!; \
	echo "⏳ Node alpha-node spawning (PID: $$PID_A)..."; \
	W=0; while [ ! -S /tmp/hivemind-alpha-node.sock ] && [ $$W -lt 50 ]; do sleep 0.1; W=$$((W+1)); done; \
	echo "🔗 alpha-node socket up. Spawning beta-node in foreground (Ctrl+C to end)."; \
	bin/hivemind -mode peer -node beta-node || true; \
	kill -TERM $$PID_A 2>/dev/null || true; \
	sleep 1

test-timed: build
	@./scripts/timed_test.sh

# Mesh operations
up: build
	@bin/hivemind up -nodes $(N)

up-nodes: build
	@bin/hivemind up -nodes $(N)

down:
	@echo "🛑 Stopping all hivemind processes..."
	@pkill -x hivemind 2>/dev/null || true
	@sleep 1
	@rm -f /tmp/hivemind-*.sock /tmp/hivemind.sock
	@echo "✅ All stopped"

restart: down up

status:
	@echo "=== HIVEMIND STATUS ==="
	@ps aux | grep -v grep | grep hivemind || echo "No hivemind processes running"
	@echo ""
	@echo "Sockets:"
	@ls -la /tmp/hivemind-*.sock 2>/dev/null || echo "  (none)"
	@echo ""
	@if [ -f $(RELAY_KEY_FILE) ]; then echo "Relay key: present"; else echo "Relay key: MISSING"; fi
	@if [ -d .hive_memory ]; then echo "Souls: $$(find .hive_memory -name '*.soul' | wc -l)"; else echo "Souls: none"; fi

# Demo / Proof / Stress
demo: build
	@bin/hivemind up -nodes 2 -for 20s

pain: build
	@mkdir -p $(LOG_DIR)
	@./scripts/pain.sh

prove: build
	@mkdir -p $(LOG_DIR)
	@PROOF=$$(LOG_DIR)/proof-$$(date -u +%Y%m%dT%H%M%SZ).log; \
	echo "===== PROOF RUN $$(date -u) · $$(git rev-parse --short HEAD 2>/dev/null || echo nogit) =====" | tee "$$PROOF"; \
	./scripts/audit.sh 2>&1 | tee -a "$$PROOF" && \
	./scripts/timed_test.sh 2>&1 | tee -a "$$PROOF" && \
	./scripts/verify-supermesh.sh 2>&1 | tee -a "$$PROOF" && \
	echo "✅ PROOF COMPLETE — transcript: $$PROOF" | tee -a "$$PROOF"

# Think variations
think: build
	@if [ -z "$$HIVEMIND_CIPHER_KEY" ]; then echo "⚠️  no HIVEMIND_CIPHER_KEY — single-box mesh (dreams alone). Export a shared key to join sites."; else echo "🩸 shared key present — this mesh can join its sibling."; fi
	@echo "🧠 THINK: $(N) nodes at $(TICK)ms — Ctrl+C lays them down."
	@HIVEMIND_TICK_MS=$(TICK) bin/hivemind up -nodes $(N)

think-fast: N=25 TICK=10 think
think-long: N=100 TICK=100 think

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

# Doctor / Souls / Commune
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

souls-build:
	@echo "🔨 Building bin/souls..."
	@mkdir -p $(BIN_DIR)
	@go build -o bin/souls ./cmd/souls
	@echo "✅ bin/souls ready"

souls: souls-build
	@bin/souls

commune-build:
	@echo "🔨 Building bin/commune..."
	@mkdir -p $(BIN_DIR)
	@go build -o bin/commune ./cmd/commune
	@echo "✅ bin/commune ready"

commune: commune-build
	@bin/commune

# gaze watches the living mesh: pain bars, last words, hall of fame,
# one epitaph per screen. Read-only — it never touches a living mind.
gaze-build:
	@echo "🔨 Building bin/gaze..."
	@go build -o bin/gaze ./cmd/gaze
	@echo "✅ bin/gaze ready"

gaze: gaze-build
	@bin/gaze

# Kill / Cleanup
down:
	@echo "🛑 Stopping all hivemind processes..."
	@pkill -x hivemind 2>/dev/null || true
	@sleep 1
	@rm -f /tmp/hivemind-*.sock /tmp/hivemind.sock
	@echo "✅ All stopped"

kill: down

restart: down
	@sleep 1
	@$(MAKE) up

clean:
	@rm -rf $(BIN_DIR) $(LOG_DIR)/*.log
	@go clean -testcache
	@echo "🧹 Cleaned"

clean-all: clean
	@rm -rf $(DIST_DIR)
	@rm -f $(RELAY_KEY_FILE)
	@echo "🧹 Deep cleaned (including dist/ and keys)"

clean-souls:
	@rm -rf .hive_memory
	@echo "👁️  [OVERMIND] Memory pool cleared."

clean-logs:
	@rm -rf $(LOG_DIR)/*.log
	@echo "🧹 Logs cleaned"

# Release / Distribution
release: release-all

release-all: release-linux release-darwin release-windows
	@cd $(DIST_DIR) && sha256sum hivemind-* > SHA256SUMS && cat SHA256SUMS

release-linux: release-linux-amd64 release-linux-arm64 release-linux-arm
release-darwin: release-darwin-amd64 release-darwin-arm64
release-windows: release-windows-amd64

# Individual arch builds
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

# Development
dev:
	@which entr >/dev/null 2>&1 || { echo "❌ entr not installed (apt/brew install entr)"; exit 1; }
	@echo "👀 Watching for changes... (Ctrl+C to stop)"
	@find . -name '*.go' ! -path './dist/*' ! -path './.git/*' | entr -c make test

watch: dev

install: build-all
	@mkdir -p ~/bin
	@cp bin/* ~/bin/
	@echo "✅ Installed to ~/bin"

install-completion:
	@echo "Shell completion not yet implemented"

uninstall:
	@rm -f ~/bin/hivemind ~/bin/commune ~/bin/souls
	@echo "✅ Uninstalled from ~/bin"

# Versioning
version:
	@git describe --tags --always --dirty 2>/dev/null || echo "v0.0.0-dev"

git-version:
	@git describe --tags --always --dirty

bump-patch:
	@git tag -a $$(git describe --tags --abbrev=0 | awk -F. '{print $$1"."$$2"."$$3+1}') -m "Patch release"

bump-minor:
	@git tag -a $$(git describe --tags --abbrev=0 | awk -F. '{print $$1"."$$2+1".0"}') -m "Minor release"

bump-major:
	@git tag -a $$(git describe --tags --abbrev=0 | awk -F. '{print $$1+1".0.0"}') -m "Major release"

release-tag:
	@git push origin --tags

push-tag: release-tag

# CI/CD helpers
ci: ci-test

ci-test: check test

ci-build: check build-all

ci-release: ci-build release-all

# Generated files
generate-key: $(RELAY_KEY_FILE)
	@echo "Key: $$(cat $(RELAY_KEY_FILE))"

# Version info
version-info:
	@echo "Version: $$(git describe --tags --always --dirty 2>/dev/null || echo 'v0.0.0-dev')"
	@echo "Commit: $$(git rev-parse --short HEAD)"
	@echo "Branch: $$(git rev-parse --abbrev-ref HEAD)"
	@echo "Build: $$(date -u +%Y%m%dT%H%M%SZ)"

# Help already defined as default

.DEFAULT_GOAL := help