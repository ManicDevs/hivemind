# ── HIVEMIND MAKEFILE ──
.DEFAULT_GOAL := help
SHELL := /bin/bash

N ?= 2
TICK ?= 50
GAZE_PORT ?= 8090
BASE_PORT ?= 20000
RELAY_PORT ?= 8080
STACK_FOR ?= 24h
WORLD_CONTINENTS ?= 7
# Failsafe load ceilings. <=0 disables the load check entirely (mem/fd
# guards stay on). This host is shared and its 1m/5m load reflects other
# tenants, so the stack disables the load trip by default; override with
# e.g. WORLD_MAX_LOAD1=20 WORLD_MAX_LOAD5=16 on a dedicated box.
WORLD_MAX_LOAD1 ?= 0
WORLD_MAX_LOAD5 ?= 0
STATICCHECK := $(shell which staticcheck 2>/dev/null || echo "$(HOME)/go/bin/staticcheck")

RELAY_KEY_FILE := .relaykey
RELAY_KEY := $(shell cat $(RELAY_KEY_FILE) 2>/dev/null || echo "")
LDFLAGS := -X gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind.compileRelayKey=$(RELAY_KEY)
BIN_DIR := bin
DIST_DIR := dist
LOG_DIR := logs

# ── Safety Guards (Universal System Protection) ──
# A dense mesh must never be able to freeze the machine that runs it.
#   · SAFE_PROCS caps GOMAXPROCS so the cluster can't saturate the
#     scheduler and thrash across virtual cores.
#   · SAFE_ENV    raises the fd ceiling (no EMFILE under N-node scaling).
#   · SAFE_RUN    drops every child onto the lowest nice class, so your
#     desktop and core system always keep the CPU.
# Override on the command line, e.g.  make think SAFE_PROCS=2
SAFE_PROCS ?= 4
SAFE_ENV := ulimit -n 4096 2>/dev/null; export GOMAXPROCS=$(SAFE_PROCS);
SAFE_RUN := nice -n 19

# The relay's own base URL. World and gaze watch it to draw the broker-mesh
# layer from real telemetry; set RELAY_URL to move the relay.
RELAY_URL ?= http://localhost:$(RELAY_PORT)

# Resolve the relay's base URL from the port it actually bound. The relay
# rolls forward off a busy port and records the result, so tooling must read
# that file rather than echo the requested default.
#
# Deferred (=) so the file is read when the recipe *runs*, not when the
# Makefile is parsed — the relay has not written it yet at parse time.
# scripts/relay-url.sh resolves the relay's real base URL at recipe time —
# a script, not a make variable, because make expands its own functions once
# per invocation, before the relay has bound.

.PHONY: all help \
build build-hivemind build-commune build-souls build-gaze build-relay build-world build-all \
fmt vet staticcheck lint tidy check \
test test-race test-verbose test-full \
audit \
up up-nodes down restart kill rerun status \
think think-fast think-long demo pain prove \
	world world-down world-test \
	stack stack-down stack-restart \
	fabric fabric-down fabric-status \
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
prove-relay setup

all: setup

setup: build-all
	@echo "📦 Pulling latest changes..."
	@$(SAFE_ENV) git pull origin main 2>/dev/null || echo "⚠️  git pull skipped (not a git repo)"
	@echo "🔧 Running full proof suite (throttled)..."
	@$(MAKE) prove && $(MAKE) prove-relay
	@echo ""
	@echo "╔══════════════════════════════════════════════════════════════╗"
	@echo "║  ✅ FULL SETUP COMPLETE — 6 binaries + all proofs            ║"
	@echo "║  ── bin/hivemind, bin/commune, bin/souls, bin/gaze          ║"
	@echo "║  ── bin/relay (MQTT bridge), bin/world (7-continent mesh)  ║"
	@echo "║  ── bin/fabric (adaptive neural routing fabric)            ║"
	@echo "║  ── safety: nice 19 · fd 4096 · GOMAXPROCS=$(SAFE_PROCS)    ║"
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
	@printf "  \033[32m%-20s\033[0m %s\n" "build-fabric" "bin/fabric only (adaptive neural fabric)"
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
	@printf "  \033[35m%-20s\033[0m %s\n" "prove-relay" "Relay cloud-path live test"
	@printf "  \033[35m%-20s\033[0m %s\n" "world" "Full 7-continent mesh, pure Go (14 nodes)"
	@printf "  \033[35m%-20s\033[0m %s\n" "world-down" "Reap world + gaze + sweep sockets"
	@printf "  \033[35m%-20s\033[0m %s\n" "world-test" "Pure-Go live world test (4 nodes)"
	@printf "  \033[35m%-20s\033[0m %s\n" "fabric" "Neural fabric (local 2×3=6: master/ctrl/superpeer)"
	@printf "  \033[35m%-20s\033[0m %s\n" "fabric-down" "Stop all fabric processes"
	@printf "  \033[35m%-20s\033[0m %s\n" "fabric-status" "Fabric proc/rx snapshot"
	@echo ""
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
	@echo ""

build: build-all

build-hivemind: $(RELAY_KEY_FILE)
	@echo "🔨 Building bin/hivemind..."
	@mkdir -p $(BIN_DIR)
	@$(SAFE_ENV) $(SAFE_RUN) go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/hivemind ./cmd/hivemind
	@echo "✅ bin/hivemind ready"

build-commune:
	@echo "🔨 Building bin/commune..."
	@mkdir -p $(BIN_DIR)
	@$(SAFE_ENV) $(SAFE_RUN) go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/commune ./cmd/commune
	@echo "✅ bin/commune ready"

build-souls:
	@echo "🔨 Building bin/souls..."
	@mkdir -p $(BIN_DIR)
	@$(SAFE_ENV) $(SAFE_RUN) go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/souls ./cmd/souls
	@echo "✅ bin/souls ready"

build-gaze:
	@echo "🔨 Building bin/gaze..."
	@mkdir -p $(BIN_DIR)
	@$(SAFE_ENV) $(SAFE_RUN) go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/gaze ./cmd/gaze
	@echo "✅ bin/gaze ready"

build-world:
	@echo "🔨 Building bin/world..."
	@mkdir -p $(BIN_DIR)
	@$(SAFE_ENV) $(SAFE_RUN) go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/world ./cmd/world
	@echo "✅ bin/world ready"

# Release build flags: enable anti-RE, strip symbols, disable debug info
RELEASE_LDFLAGS = -s -w -X gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind.releaseBuild=1

build-relay:
	@echo "🔨 Building bin/relay..."
	@mkdir -p $(BIN_DIR)
	@$(SAFE_ENV) $(SAFE_RUN) go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/relay ./cmd/relay
	@echo "✅ bin/relay ready (MQTT bridge)"

build-all: build-hivemind build-commune build-souls build-gaze build-relay build-world build-fabric build-derive

build-release: build-all
	@echo "Building release binaries with anti-RE hardening..."
	@for bin in hivemind commune souls gaze relay world fabric derive; do 		go build -tags release -ldflags "$(RELEASE_LDFLAGS)" -o bin/$$bin ./cmd/$$bin; 	done
	@echo "Release binaries in bin/ (stripped, anti-RE enabled)"

build-fabric:
	@echo "🔨 Building bin/fabric..."
	@mkdir -p $(BIN_DIR)
	@$(SAFE_ENV) CGO_ENABLED=0 $(SAFE_RUN) go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/fabric ./cmd/fabric
	@echo "✅ bin/fabric ready (adaptive neural routing fabric)"

# derive prints the live hourly leaf and daily root through the same
# runtime path the mesh uses. It is the independent cross-check that a
# deployed binary is actually holding the key it was built with — and the
# quickest way to confirm two hosts derive the same bus key.
build-derive:
	@echo "🔨 Building bin/derive..."
	@mkdir -p $(BIN_DIR)
	@$(SAFE_ENV) CGO_ENABLED=0 $(SAFE_RUN) go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/derive ./cmd/derive
	@echo "✅ bin/derive ready (rolling key inspector)"

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

fmt:
	@$(SAFE_ENV) $(SAFE_RUN) gofmt -w .
	@echo "✅ Formatted"

vet:
	@$(SAFE_ENV) $(SAFE_RUN) go vet ./...
	@echo "✅ Vet clean"

staticcheck:
	@$(SAFE_ENV) $(SAFE_RUN) $(STATICCHECK) ./... && echo "✅ staticcheck clean"

lint: fmt vet staticcheck

tidy:
	@$(SAFE_ENV) $(SAFE_RUN) go mod tidy
	@echo "✅ Tidy"

check: lint tidy

test: check
	@echo "🧪 Testing..."
	@if command -v gcc >/dev/null 2>&1; then \
	echo "  (using -race)"; \
	$(SAFE_ENV) $(SAFE_RUN) timeout 120 go test ./... -race -count=1 2>/tmp/hivemind-test.log && echo "✅ Tests passed (-race)" || (cat /tmp/hivemind-test.log; exit 1); \
	else \
	echo "  ⚠️  No C compiler — running plain tests"; \
	$(SAFE_ENV) $(SAFE_RUN) timeout 120 go test ./... -count=1 && echo "✅ Tests passed (plain)"; \
	fi

test-race: check
	@$(SAFE_ENV) $(SAFE_RUN) go test ./... -race -count=1

test-verbose: check
	@$(SAFE_ENV) $(SAFE_RUN) go test ./... -v -count=1

test-full: check
	@$(SAFE_ENV) $(SAFE_RUN) go test ./... -count=1 -v

audit: test
	@$(SAFE_ENV) $(SAFE_RUN) ./scripts/audit.sh

up: build
	@mkdir -p $(LOG_DIR)
	@echo "🚀 Launching $(N) hivemind nodes (throttled)..."
	@$(SAFE_ENV) $(SAFE_RUN) bin/hivemind up -nodes $(N)

up-nodes: up

# ── full stack: mesh nodes + relay + world/gaze dashboard + fabric ──
# Brings up every process the system needs, not just nodes. Tune with
# N (nodes), TICK (ms), GAZE_PORT (dashboard), BASE_PORT (mesh base).
stack: build-release
	@# Refuse to start a second copy. Running `make stack` twice used to
	@# quietly launch a duplicate world and a duplicate set of minds (we saw
	@# 62 nodes and two worlds), which then fight over the same ports and
	@# the same sockets. A stack is a singleton; make that explicit rather
	@# than leaving it to be rediscovered.
	@if pgrep -x world >/dev/null 2>&1 || pgrep -x hivemind >/dev/null 2>&1; then \
		echo "✗ A stack is already running ($$(pgrep -x hivemind|wc -l) minds, $$(pgrep -x world|wc -l) world)."; \
		echo "  Run 'make stack-down' first, or 'make stack-restart'."; \
		exit 1; \
	fi
	@mkdir -p $(LOG_DIR) logs
	@rm -f .stack-relay.port
	@echo "🌐 Starting full stack: $(N) mesh nodes, relay, world+gaze, fabric..."
	@ ( export HIVEMIND_CIPHER_KEY=$$(cat .relaykey); $(SAFE_ENV) $(SAFE_RUN) nohup bin/hivemind up -nodes $(N) -for $(STACK_FOR) > logs/nodes.log 2>&1 & echo $$! > .stack-nodes.pid )
	@ ( export RELAY_PORT=$(RELAY_PORT) HIVEMIND_CIPHER_KEY=$$(cat .relaykey); $(SAFE_ENV) $(SAFE_RUN) nohup bin/relay > logs/relay.log 2>&1 & echo $$! > .stack-relay.pid )
	@# The relay may roll forward off a busy port, so world/fabric must watch
	@# the port it actually bound, not the one we asked for. Each recipe line
	@# is its own shell, so the resolver is invoked inline rather than
	@# exported; it must run after the relay has written its port file.
	@for i in $$(seq 1 40); do [ -s .stack-relay.port ] && break; sleep 0.2; done
	@echo "   relay bound :$$(./scripts/relay-url.sh $(RELAY_URL) | sed 's|.*:||')"
	@ ( export WORLD_CONTINENTS=$(WORLD_CONTINENTS) HIVEMIND_RELAY_URL=$$(./scripts/relay-url.sh $(RELAY_URL)) HIVEMIND_CIPHER_KEY=$$(cat .relaykey); $(SAFE_ENV) $(SAFE_RUN) nohup bin/world -bin bin -gaze-port $(GAZE_PORT) -base-port $(BASE_PORT) -max-load1 $(WORLD_MAX_LOAD1) -max-load5 $(WORLD_MAX_LOAD5) > logs/world.log 2>&1 & echo $$! > .stack-world.pid )
	@ ( export HIVEMIND_RELAY_URL=$$(./scripts/relay-url.sh $(RELAY_URL)) HIVEMIND_CIPHER_KEY=$$(cat .relaykey); $(SAFE_ENV) $(SAFE_RUN) nohup bin/fabric > logs/fabric.log 2>&1 & echo $$! > .stack-fabric.pid )
	@sleep 4
	@echo "=== FULL STACK STATUS ==="
	@pgrep -a -x hivemind || echo "No hivemind nodes running"
	@pgrep -a -x relay || echo "relay: stopped"
	@pgrep -a -x world || echo "world: stopped"
	@pgrep -a -x fabric || echo "fabric: stopped"
	@echo ""
	@echo "Sockets:"; @ls /tmp/hivemind-*.sock 2>/dev/null || echo "  (none)"
	@echo "Logs:"; @ls -1 logs/*.log 2>/dev/null | tr '\n' ' '; echo ""
	@echo "  → Dashboard: http://localhost:$(GAZE_PORT)"

stack-down:
	@echo "🛑 Stopping full stack..."
	@for p in nodes relay world fabric; do \
		if [ -f .stack-$$p.pid ]; then kill -TERM $$(cat .stack-$$p.pid) 2>/dev/null || true; rm -f .stack-$$p.pid; fi; \
	done
	@for b in hivemind relay world fabric gaze commune souls; do pkill -TERM -x $$b 2>/dev/null || true; done
	@# world/hivemind lay down gracefully; wait up to 10s, then escalate.
	@for i in $$(seq 1 20); do \
		pgrep -x hivemind >/dev/null 2>&1 || pgrep -x world >/dev/null 2>&1 || \
		pgrep -x gaze >/dev/null 2>&1 || break; \
		sleep 0.5; \
	done
	@# Escalate anything still standing, gaze included: a world killed
	@# before its dashboards leaves them orphaned.
	@for b in hivemind world relay fabric gaze; do pkill -KILL -x $$b 2>/dev/null || true; done
	@rm -f /tmp/hivemind-*.sock /tmp/hivemind.sock
	@rm -f .stack-*.pid
	@echo "✅ Full stack stopped"
stack-status:
	@echo "=== FULL STACK STATUS ==="
	@pgrep -a -x hivemind || echo "No hivemind nodes running"
	@pgrep -a -x relay || echo "relay: stopped"
	@pgrep -a -x world || echo "world: stopped"
	@pgrep -a -x fabric || echo "fabric: stopped"
	@echo ""
	@echo "Sockets:"; @ls /tmp/hivemind-*.sock 2>/dev/null || echo "  (none)"
	@echo "Logs:"; @ls -1 logs/*.log 2>/dev/null | tr '\n' ' '; echo ""
	@echo "Dashboard: http://localhost:$(GAZE_PORT)"

# The static port map + what this box is actually listening on right now.
ports:
	@echo "=== HIVEMIND PORT MAP (configured) ==="
	@echo "Relay bus:      :$(RELAY_PORT)  (RELAY_PORT; rolls forward if busy)"
	@echo "Gaze dashboard: :$(GAZE_PORT)  (world -gaze-port) → http://localhost:$(GAZE_PORT)/"
	@echo "Mesh TCP:       $(BASE_PORT)+subnet*50+tier+1  (world -base-port)"
	@echo "  continents:   eu=1 na=2 as=3 sa=4 af=5 oc=6 an=7  (stride 50)"
	@echo "  eu → $(shell expr $(BASE_PORT) + 51)-$(shell expr $(BASE_PORT) + 54)   na → $(shell expr $(BASE_PORT) + 101)-$(shell expr $(BASE_PORT) + 104)   (etc.)"
	@echo "LAN multicast:  239.192.0.99:37779  (org-local, never routed)"
	@echo "DHT discovery:  UDP, HIVEMIND_DHT_PORT or ephemeral"
	@echo "Public MQTT:    outbound tcp://broker.emqx.io:1883  (relay backup bus)"
	@echo ""
	@echo "=== LIVE LISTENERS (this box, right now) ==="
	@# ss/netstat are restricted in some sandboxes; /proc/net is the truth.
	@# Nodes listen dual-stack, so check both tcp and tcp6.
	@ours=$$(if [ -s .stack-relay.port ]; then ./scripts/relay-url.sh | sed 's|.*:||'; fi); \
	awk 'NR>1 && $$4=="0A" {split($$2,a,":"); p=strtonum("0x" a[2]); \
		if ((p>=20000 && p<=21000)||(p>=8080 && p<=8090)) print p}' \
		/proc/net/tcp /proc/net/tcp6 2>/dev/null | sort -n | uniq | \
	awk -v ours="$$ours" '{if ($$1>=20000 && $$1<=21000) print "  tcp LISTEN : " $$1 "  (mesh node)"; \
	     else if ($$1==8090) print "  tcp LISTEN : 8090  (gaze dashboard)"; \
	     else if ($$1==ours) print "  tcp LISTEN : " $$1 "  (relay — ours)"; \
	     else print "  tcp LISTEN : " $$1 "  (NOT ours — foreign occupant)"}'
	@awk 'BEGIN{f=0} NR>1 && $$4=="0A" {split($$2,a,":"); p=strtonum("0x" a[2]); \
		if ((p>=20000&&p<=21000)||(p>=8080&&p<=8090)) f=1} \
		END{if(!f) print "  (no stack TCP listeners — run \"make stack\" first)"}' \
		/proc/net/tcp /proc/net/tcp6 2>/dev/null
	@echo ""
	@echo "=== LIVE UDP (multicast/DHT) ==="
	@awk 'NR>1 {split($$2,a,":"); p=strtonum("0x" a[2]); if (p==37779) print "  udp :37779 (LAN multicast beacon)"}' \
		/proc/net/udp /proc/net/udp6 2>/dev/null | sort -u
	@echo "  (per-node DHT UDP is ephemeral unless HIVEMIND_DHT_PORT is set)"

down:
	@echo "🛑 Stopping all hivemind processes..."
	@pkill -x hivemind 2>/dev/null || true
	@pkill -x relay 2>/dev/null || true
	@pkill -x world 2>/dev/null || true
	@pkill -x gaze 2>/dev/null || true
	@pkill -x fabric 2>/dev/null || true
	@pkill -x commune 2>/dev/null || true
	@pkill -x souls 2>/dev/null || true
	@sleep 1
	@rm -f /tmp/hivemind-*.sock /tmp/hivemind.sock
	@echo "✅ All stopped"

restart: down up

stack-restart: stack-down stack

kill: down

rerun: restart

status:
	@echo "=== HIVEMIND STATUS ==="
	@pgrep -a -x hivemind || echo "No hivemind processes running"
	@pgrep -a -x relay || echo "No relay processes running"
	@pkill -0 world 2>/dev/null && echo "World process: running" || echo "World process: stopped"
	@echo ""
	@echo "Sockets:"
	@ls /tmp/hivemind-*.sock 2>/dev/null || echo "  (none)"
	@echo ""
	@if [ -f $(RELAY_KEY_FILE) ]; then echo "Relay key: present"; else echo "Relay key: MISSING"; fi
	@if [ -d .hive_memory ]; then echo "Souls: $$(find .hive_memory -name '*.soul' | wc -l)"; else echo "Souls: none"; fi

world: build-hivemind build-gaze build-world
	@echo "🌍 Spinning up world mesh..."
	@$(SAFE_ENV) $(SAFE_RUN) bin/world

world-down: down
	@echo "✅ World down"

world-test: build-hivemind build-gaze build-world
	@$(SAFE_ENV) $(SAFE_RUN) go test ./cmd/gaze/ -run TestWorldLive -v -count=1

# ── fabric: adaptive neural routing (local=2×3=6, hard-capped on one box) ──
# Never spawn 7 continents here — needs FABRIC_ALLOW_WIDE=1 + physical servers.
FABRIC_CONTINENTS ?= 2
FABRIC_TIERS ?= 3
FABRIC_ALLOW_WIDE ?= 0

fabric: build-fabric
	@if [ "$(FABRIC_CONTINENTS)" -gt 2 ] && [ "$(FABRIC_ALLOW_WIDE)" != "1" ]; then \
	  echo "🛑 FABRIC_CONTINENTS=$(FABRIC_CONTINENTS) blocked on this box (max 2)."; \
	  echo "   Only set FABRIC_ALLOW_WIDE=1 on physical servers."; \
	  exit 1; \
	fi
	@echo "🧠 Spawning fabric on loopback :8883 — $(FABRIC_CONTINENTS)×$(FABRIC_TIERS)=6 (master/ctrl/superpeer)…"
	@mkdir -p /tmp/fabric
	@rm -f /tmp/fabric/*.log
	@c=1; while [ $$c -le $(FABRIC_CONTINENTS) ]; do \
	  t=1; while [ $$t -le $(FABRIC_TIERS) ]; do \
	    ENV_CONTINENT=$$c ENV_TIER=$$t FABRIC_CONTINENTS=$(FABRIC_CONTINENTS) FABRIC_TIERS=$(FABRIC_TIERS) FABRIC_ALLOW_WIDE=$(FABRIC_ALLOW_WIDE) FABRIC_METRICS=$${FABRIC_METRICS:-1} \
	      nohup setsid $(BIN_DIR)/fabric > /tmp/fabric/c$$c.t$$t.log 2>&1 < /dev/null & \
	    t=$$((t+1)); \
	  done; \
	  c=$$((c+1)); \
	done
	@sleep 2
	@echo "✅ fabric up: $$(pgrep -c -x fabric || echo 0) processes · logs /tmp/fabric/"

fabric-down:
	@echo "🛑 Stopping fabric…"
	@pkill -9 -x fabric 2>/dev/null || true
	@echo "✅ fabric down"

fabric-status:
	@echo "=== FABRIC ==="
	@echo "procs: $$(pgrep -c -x fabric || echo 0)/$$(( $(FABRIC_CONTINENTS) * $(FABRIC_TIERS) ))"
	@grep -h '\[rx\]' /tmp/fabric/*.log 2>/dev/null | tail -5 || echo "  (no rx yet)"
	@grep -hE 'panic|crypto' /tmp/fabric/*.log 2>/dev/null | tail -3 || true

think: build
	@if [ -z "$$HIVEMIND_CIPHER_KEY" ]; then echo "⚠️  no HIVEMIND_CIPHER_KEY — single-box mesh."; else echo "🩸 shared key present — cross-site capable."; fi
	@echo "🧠 THINK: $(N) nodes at $(TICK)ms (niceness active)"
	@$(SAFE_ENV) HIVEMIND_TICK_MS=$(TICK) $(SAFE_RUN) bin/hivemind up -nodes $(N)

think-fast: build
	@$(MAKE) think N=25 TICK=10

think-long: build
	@$(MAKE) think N=100 TICK=100

demo: build
	@$(SAFE_ENV) $(SAFE_RUN) bin/hivemind up -nodes 2 -for 20s

pain: build
	@mkdir -p $(LOG_DIR)
	@$(SAFE_ENV) $(SAFE_RUN) ./scripts/pain.sh

prove: build
	@mkdir -p $(LOG_DIR)
	@PROOF=$(LOG_DIR)/proof-$$(date -u +%Y%m%dT%H%M%SZ).log; \
	set -o pipefail; \
	echo "===== PROOF RUN $$(date -u) · $$(git rev-parse --short HEAD 2>/dev/null || echo nogit) =====" | tee "$$PROOF"; \
	$(SAFE_ENV) $(SAFE_RUN) ./scripts/audit.sh 2>&1 | tee -a "$$PROOF" && \
	$(SAFE_ENV) $(SAFE_RUN) timeout 120 ./scripts/timed_test.sh 2>&1 | tee -a "$$PROOF" && \
	$(SAFE_ENV) $(SAFE_RUN) ./scripts/verify-supermesh.sh 2>&1 | tee -a "$$PROOF" && \
	$(SAFE_ENV) $(SAFE_RUN) ./scripts/pain.sh 2>&1 | tee -a "$$PROOF" && \
	$(SAFE_ENV) $(SAFE_RUN) bash scripts/proof-keys.sh 2>&1 | tee -a "$$PROOF" && \
	echo "✅ PROOF COMPLETE — transcript: $$PROOF" | tee -a "$$PROOF"

proof-keys: build
	@$(SAFE_ENV) $(SAFE_RUN) bash scripts/proof-keys.sh

prove-relay: build
	@echo "🧪 Full relay + MQTT live test (cloud path only — no local echo)..."
	@$(SAFE_ENV) $(SAFE_RUN) bin/relay > /tmp/relay-prove.log 2>&1 & \
RPID=$$!; sleep 2; \
HIVEMIND_RELAY=on HIVEMIND_UNIX=off HIVEMIND_BEACON=off HIVEMIND_DHT=off HIVEMIND_SUPER=off \
HIVEMIND_RELAY_URL="http://127.0.0.1:8080/hive-relay" HIVEMIND_TICK_MS=500 \
HIVEMIND_CIPHER_KEY="ab94f253510372b0cee7c871ba7c5d3fea4b20caeb0d271de02860f739d3e5c1" \
$(SAFE_RUN) bin/hivemind -mode peer -node relay-test-1 > /tmp/rt1.log 2>&1 & \
P1=$$!; sleep 3; \
HIVEMIND_RELAY=on HIVEMIND_UNIX=off HIVEMIND_BEACON=off HIVEMIND_DHT=off HIVEMIND_SUPER=off \
HIVEMIND_RELAY_URL="http://127.0.0.1:8080/hive-relay" HIVEMIND_TICK_MS=500 \
HIVEMIND_CIPHER_KEY="ab94f253510372b0cee7c871ba7c5d3fea4b20caeb0d271de02860f739d3e5c1" \
$(SAFE_RUN) bin/hivemind -mode peer -node relay-test-2 > /tmp/rt2.log 2>&1 & \
P2=$$!; sleep 18; \
	echo "=== RELAY ===" && grep -i "mqtt\|RELAY" /tmp/relay-prove.log | head -3; \
	echo "=== NODE 1 ===" && grep -c "SECURE CLOUD INBOUND" /tmp/rt1.log || echo "0"; \
	echo "=== NODE 2 ===" && grep -c "SECURE CLOUD INBOUND" /tmp/rt2.log || echo "0"; \
	echo "=== FRAMES ===" && grep -c "Physics frame extracted" /tmp/rt1.log /tmp/rt2.log || true; \
	kill $$P1 $$P2 $$RPID 2>/dev/null; sleep 2; \
	kill -9 $$P1 $$P2 $$RPID 2>/dev/null; wait 2>/dev/null; \
	rm -f /tmp/hivemind-relay-test-*.sock; \
	echo "✅ Relay + MQTT live test complete"

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
	@$(SAFE_ENV) $(SAFE_RUN) bin/souls

commune: build-commune
	@$(SAFE_ENV) $(SAFE_RUN) bin/commune

gaze: build-gaze
	@$(SAFE_ENV) $(SAFE_RUN) bin/gaze

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

release: release-linux release-darwin release-windows
	@cd $(DIST_DIR) && sha256sum hivemind-* > SHA256SUMS && cat SHA256SUMS

release-linux: release-linux-amd64 release-linux-arm64 release-linux-arm
release-darwin: release-darwin-amd64 release-darwin-arm64
release-windows: release-windows-amd64

release-linux-amd64: $(RELAY_KEY_FILE)
	@echo "📦 Building linux/amd64..."
	@mkdir -p $(DIST_DIR)
	@$(SAFE_ENV) $(SAFE_RUN) CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/hivemind-linux-amd64 ./cmd/hivemind

release-linux-arm64: $(RELAY_KEY_FILE)
	@echo "📦 Building linux/arm64..."
	@mkdir -p $(DIST_DIR)
	@$(SAFE_ENV) $(SAFE_RUN) CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/hivemind-linux-arm64 ./cmd/hivemind

release-linux-arm: $(RELAY_KEY_FILE)
	@echo "📦 Building linux/arm..."
	@mkdir -p $(DIST_DIR)
	@$(SAFE_ENV) $(SAFE_RUN) CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/hivemind-linux-arm ./cmd/hivemind

release-darwin-amd64: $(RELAY_KEY_FILE)
	@echo "📦 Building darwin/amd64..."
	@mkdir -p $(DIST_DIR)
	@$(SAFE_ENV) $(SAFE_RUN) CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/hivemind-darwin-amd64 ./cmd/hivemind

release-darwin-arm64: $(RELAY_KEY_FILE)
	@echo "📦 Building darwin/arm64..."
	@mkdir -p $(DIST_DIR)
	@$(SAFE_ENV) $(SAFE_RUN) CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/hivemind-darwin-arm64 ./cmd/hivemind

release-windows-amd64: $(RELAY_KEY_FILE)
	@echo "📦 Building windows/amd64..."
	@mkdir -p $(DIST_DIR)
	@$(SAFE_ENV) $(SAFE_RUN) CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/hivemind-windows-amd64.exe ./cmd/hivemind

release-hardened: $(RELAY_KEY_FILE)
	@echo "🛡️  Building hardened linux/amd64..."
	@mkdir -p $(DIST_DIR)
	@$(SAFE_ENV) $(SAFE_RUN) CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w $(LDFLAGS)" -o $(DIST_DIR)/hivemind-hardened ./cmd/hivemind

release-clean:
	@rm -rf $(DIST_DIR)
	@echo "🧹 Release artifacts cleaned"

dist-clean: release-clean

dev:
	@which entr >/dev/null 2>&1 || { echo "❌ entr not installed (apt/brew install entr)"; exit 1; }
	@echo "👀 Watching for changes... (Throttled execution)"
	@find . -name '*.go' ! -path './dist/*' ! -path './.git/*' | entr -c sh -c '$(SAFE_ENV) $(SAFE_RUN) sh -c "gofmt -l . ; go vet ./... && go test ./... -count=1"'

install: build-all
	@mkdir -p ~/bin
	@cp bin/* ~/bin/
	@echo "✅ Installed to ~/bin"

uninstall:
	@rm -f ~/bin/hivemind ~/bin/commune ~/bin/souls ~/bin/gaze ~/bin/relay ~/bin/world ~/bin/fabric
	@echo "✅ Uninstalled from ~/bin"

version:
	@git describe --tags --always --dirty 2>/dev/null || echo "v0.0.0-dev"

version-info:
	@echo "Version: $$(git describe --tags --always --dirty 2>/dev/null || echo 'v0.0.0-dev')"
	@echo "Commit: $$(git rev-parse --short HEAD 2>/dev/null || echo nogit)"
	@echo "Branch: $$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo nogit)"
	@echo "Build: $$(date -u +%Y%m%dT%H%M%SZ)"
