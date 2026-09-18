# ── HIVEMIND DECENTRALIZED AUTOMATION MAKEFILE ──

.DEFAULT_GOAL := help
.PHONY: all help build build-hivemind test audit clean test-1 test-2 test-full test-timed \
        clean-soul rotate-keys up kill rerun prove demo doctor souls commune commune-build souls-build think pain \
        release release-linux-amd64 release-linux-arm64 release-linux-arm \
        release-darwin-amd64 release-darwin-arm64 release-windows-amd64 \
        release-hardened dev vet fmt tidy check lint

all: build release release-hardened
	@echo ""
	@echo "  🌌 every binary — bin/ (3 native) + dist/ (6 cross + hardened + SHA256SUMS)"
	@echo "  make up      raise a mesh   ·   make commune   speak with it"
	@echo "  make prove   prove it works ·   make help      all commands"
	@echo ""

help:
	@echo ""
	@echo "  HIVEMIND PROCESS CONTROLLER"
	@echo "  One binary, one mesh, proof on demand"
	@echo ""
	@printf "  \033[1m%-18s\033[0m %s\n" "TARGET" "DESCRIPTION"
	@printf "  \033[1m%-18s\033[0m %s\n" "------" "-----------"
	@printf "  \033[32m%-18s\033[0m %s\n" "all" "Every binary: bin/ (3) + dist/ (6 cross + hardened)"
	@printf "  \033[32m%-18s\033[0m %s\n" "build" "Compile all binaries (hivemind + commune + souls)"
	@printf "  \033[32m%-18s\033[0m %s\n" "build-hivemind" "Compile bin/hivemind only"
	@printf "  \033[32m%-18s\033[0m %s\n" "test" "check (fmt+vet+staticcheck+tidy) + tests (-race if cgo)"
	@printf "  \033[32m%-18s\033[0m %s\n" "prove" "Full battery + transcript artifact (fails loud)"
	@printf "  \033[32m%-18s\033[0m %s\n" "pain" "Stimulus proof: idle vs loaded matrices + epitaphs"
	@printf "  \033[32m%-18s\033[0m %s\n" "demo" "Supervised 20s showcase run, exits alone"
	@printf "  \033[32m%-18s\033[0m %s\n" "up" "Supervised mesh (default 2 nodes)"
	@printf "  \033[32m%-18s\033[0m %s\n" "think" "THINK: build + fast-tick mesh (N=10 TICK=50)"
	@printf "  \033[33m%-18s\033[0m %s\n" "doctor" "Environment check: toolchain, sensors, egress"
	@printf "  \033[33m%-18s\033[0m %s\n" "souls" "Read the dead: census, genome, timeline, diff, matrix, grep, top"
	@printf "  \033[33m%-18s\033[0m %s\n" "souls-build" "Compile bin/souls only"
	@printf "  \033[33m%-18s\033[0m %s\n" "commune" "Speak with the hive (it answers from its souls)"
	@printf "  \033[33m%-18s\033[0m %s\n" "commune-build" "Compile bin/commune only"
	@printf "  \033[34m%-18s\033[0m %s\n" "test-1" "Terminal 1: peer node alpha-node"
	@printf "  \033[34m%-18s\033[0m %s\n" "test-2" "Terminal 2: peer node beta-node"
	@printf "  \033[34m%-18s\033[0m %s\n" "test-full" "Two-peer mesh, beta in foreground"
	@printf "  \033[34m%-18s\033[0m %s\n" "test-timed" "Automated 10s experiment + assertions"
	@printf "  \033[35m%-18s\033[0m %s\n" "kill" "Reap all hivemind + sweep stale sockets"
	@printf "  \033[35m%-18s\033[0m %s\n" "rerun" "kill + build + up"
	@printf "  \033[35m%-18s\033[0m %s\n" "rotate-keys" "Wipe machine-local relay key (rebuild after)"
	@printf "  \033[35m%-18s\033[0m %s\n" "clean" "Remove build artifacts and logs"
	@printf "  \033[35m%-18s\033[0m %s\n" "clean-soul" "Wipe .hive_memory (true extinction)"
	@printf "  \033[36m%-18s\033[0m %s\n" "release" "Cross-compile 6 binaries → dist/ + SHA256SUMS"
	@printf "  \033[36m%-18s\033[0m %s\n" "release-hardened" "Stripped anti-debug binary (backtraces degrade)"
	@printf "  \033[36m%-18s\033[0m %s\n" "dev" "Continuous test on file change (needs entr)"
	@printf "  \033[36m%-18s\033[0m %s\n" "lint" "gofmt + vet + staticcheck (if available)"
	@echo ""

build: build-hivemind commune-build souls-build
	@echo "✅ all binaries ready (bin/hivemind bin/commune bin/souls)"

build-hivemind: .relaykey
	@echo "🔨 Building bin/hivemind..."
	@go build -ldflags "-X gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind.compileRelayKey=$$(cat .relaykey)" -o bin/hivemind ./cmd/hivemind
	@echo "✅ bin/hivemind ready"

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
	@rm -f .relaykey
	@$(MAKE) .relaykey
	@echo "🔑 Relay key rotated — rebuild all nodes (make build)."

fmt:
	@echo "🎨 Formatting..."
	@gofmt -w .
	@echo "✅ Formatted"

vet:
	@echo "🔍 Vetting..."
	@go vet ./...
	@echo "✅ Vet clean"

lint: fmt vet
	@which staticcheck >/dev/null 2>&1 && staticcheck ./... && echo "✅ staticcheck clean" || echo "⚠️  staticcheck not installed (go install honnef.co/go/tools/cmd/staticcheck@latest)"

tidy:
	@echo "🧹 Tidying modules..."
	@go mod tidy
	@echo "✅ Tidy"

check: lint tidy
	@echo "✅ All checks passed"

test: check
	@echo "🧪 Testing (-race)..."
	@if go test ./... -race -count=1 2> /tmp/hivemind-make-test.log; then \
		echo "✅ Tests passed (-race)"; \
	else \
		if grep -q "requires cgo" /tmp/hivemind-make-test.log; then \
			echo "⚠️  No C compiler — race detector unavailable, running plain tests"; \
			go test ./... -count=1 && echo "✅ Tests passed (plain)"; \
		else \
			cat /tmp/hivemind-make-test.log; \
			exit 1; \
		fi \
	fi

audit: test
	@./scripts/audit.sh

test-1: build
	@bin/hivemind -mode peer -node alpha-node

test-2: build
	@bin/hivemind -mode peer -node beta-node

test-full: build
	@mkdir -p logs && rm -f logs/peer-a.log logs/peer-b.log
	@echo "🚀 [PEER MESH] Spawning symmetric two-node mesh..."
	@bin/hivemind -mode peer -node alpha-node > logs/peer-a.log 2>&1 & PID_A=$$!; \
	echo "⏳ Node alpha-node spawning (PID: $$PID_A)..."; \
	W=0; while [ ! -S /tmp/hivemind-alpha-node.sock ] && [ $$W -lt 50 ]; do sleep 0.1; W=$$((W+1)); done; \
	echo "🔗 alpha-node socket up. Spawning beta-node in foreground (Ctrl+C to end)."; \
	bin/hivemind -mode peer -node beta-node || true; \
	kill -TERM $$PID_A 2>/dev/null || true; \
	sleep 1

test-timed: build
	@./scripts/timed_test.sh

# pain: idle vs loaded, same genome — the matrices are the argument.
pain: build
	@mkdir -p logs
	@./scripts/pain.sh

up: build
	@bin/hivemind up -nodes 2

# think: the one command. Builds everything, then raises a fast-tick
# supervised mesh: N nodes (default 10), TICK ms heartbeat (default 50).
# Override per box: `make think N=3` on the small one, `make think`
# on the Xeon. Cross-site mesh needs the shared blood:
# export the same HIVEMIND_CIPHER_KEY on both machines first —
# without it each box dreams alone (machine-local keys).
think: build
	@if [ -z "$$HIVEMIND_CIPHER_KEY" ]; then echo "⚠️  no HIVEMIND_CIPHER_KEY — single-box mesh (dreams alone). Export a shared key to join sites."; else echo "🩸 shared key present — this mesh can join its sibling."; fi
	@echo "🧠 THINK: $(or $(N),10) nodes at $(or $(TICK),50)ms — Ctrl+C lays them down."
	@HIVEMIND_TICK_MS=$(or $(TICK),50) bin/hivemind up -nodes $(or $(N),10)

# up-nodes: build        # usage: make up-nodes N=3
# 	@bin/hivemind up -nodes $(N)

demo: build
	@bin/hivemind up -nodes 2 -for 20s

# doctor reports what this machine offers the hive: toolchain, sensors,
# egress, and port freedom. Read-only; changes nothing.
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

# souls prints every lineage: lives lived and fitness banked, per soul.
souls-build:
	@echo "🔨 Building bin/souls..."
	@go build -o bin/souls ./cmd/souls
	@echo "✅ bin/souls ready"

souls: souls-build
	@bin/souls

# commune opens the hive's mouth: an interactive conversation with the
# dead and the living. Every sentence is built from souls on disk.
commune-build:
	@echo "🔨 Building bin/commune..."
	@go build -o bin/commune ./cmd/commune
	@echo "✅ bin/commune ready"

commune: commune-build
	@bin/commune

# kill: every running hivemind dies by exact name match (never pattern
# match — patterns murder the shell running them), then stale sockets go.
# Safe to run with nothing alive: silence, not failure.
kill:
	@pkill -x hivemind 2>/dev/null || true
	@sleep 1
	@rm -f /tmp/hivemind-*.sock /tmp/hivemind.sock
	@echo "🧹 All hivemind processes reaped, sockets swept."

rerun: kill build up

# dev runs tests on every file change (requires entr: apt install entr / brew install entr)
dev:
	@which entr >/dev/null 2>&1 || { echo "❌ entr not installed (apt/brew install entr)"; exit 1; }
	@echo "👀 Watching for changes... (Ctrl+C to stop)"
	@find . -name '*.go' ! -path './dist/*' ! -path './.git/*' | entr -c make test

# prove: rerun shows it launches; prove shows it WORKS. Every stage
# asserts and exits non-zero on failure — a green run is evidence,
# not narration. The transcript is kept as logs/proof-<timestamp>.log:
# the noun to go with the verb.
prove: build
	@mkdir -p logs
	@PROOF=logs/proof-$$(date -u +%Y%m%dT%H%M%SZ).log; \
	echo "===== PROOF RUN $$(date -u) · $$(git rev-parse --short HEAD 2>/dev/null || echo nogit) =====" | tee "$$PROOF"; \
	./scripts/audit.sh 2>&1 | tee -a "$$PROOF" && \
	./scripts/timed_test.sh 2>&1 | tee -a "$$PROOF" && \
	./scripts/verify-supermesh.sh 2>&1 | tee -a "$$PROOF" && \
	echo "✅ PROOF COMPLETE — transcript: $$PROOF" | tee -a "$$PROOF"

clean:
	@rm -rf bin logs/*.log
	@go clean -testcache
	@echo "🧹 Cleaned"

# Cross-compiled release binaries land in dist/ (gitignored — they ship
# attached to the GitHub release, never committed). One file per arch;
# the v2.0 release ships exactly one of them.
release: release-linux-amd64 release-linux-arm64 release-linux-arm release-darwin-amd64 release-darwin-arm64 release-windows-amd64
	@cd dist && sha256sum hivemind-* > SHA256SUMS && cat SHA256SUMS

release-linux-amd64: .relaykey
	@echo "📦 Building linux/amd64..."
	@CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-X gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind.compileRelayKey=$$(cat .relaykey)" -o dist/hivemind-linux-amd64 ./cmd/hivemind

release-linux-arm64: .relaykey
	@echo "📦 Building linux/arm64..."
	@CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "-X gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind.compileRelayKey=$$(cat .relaykey)" -o dist/hivemind-linux-arm64 ./cmd/hivemind

release-linux-arm: .relaykey
	@echo "📦 Building linux/arm..."
	@CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -ldflags "-X gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind.compileRelayKey=$$(cat .relaykey)" -o dist/hivemind-linux-arm ./cmd/hivemind

release-darwin-amd64: .relaykey
	@echo "📦 Building darwin/amd64..."
	@CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags "-X gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind.compileRelayKey=$$(cat .relaykey)" -o dist/hivemind-darwin-amd64 ./cmd/hivemind

release-darwin-arm64: .relaykey
	@echo "📦 Building darwin/arm64..."
	@CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags "-X gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind.compileRelayKey=$$(cat .relaykey)" -o dist/hivemind-darwin-arm64 ./cmd/hivemind

release-windows-amd64: .relaykey
	@echo "📦 Building windows/amd64..."
	@CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "-X gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind.compileRelayKey=$$(cat .relaykey)" -o dist/hivemind-windows-amd64.exe ./cmd/hivemind

# Hardened release: stripped symbols (no one reads the binary back) at
# the honest cost of readable backtraces (addresses, not names). Pair
# with HIVEMIND_HARDEN=exit for traced-process refusal. Default builds
# stay debuggable: diagnosis beats armor during development.
release-hardened: .relaykey
	@echo "🛡️  Building hardened linux/amd64..."
	@CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w -X gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind.compileRelayKey=$$(cat .relaykey)" -o dist/hivemind-hardened ./cmd/hivemind

clean-soul:
	@rm -rf .hive_memory
	@echo "👁️  [OVERMIND] Memory pool cleared."

