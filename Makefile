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
# Failsafe RAM/fd ceilings. These mirror cmd/world's own defaults (0.85 /
# 0.75) so behaviour is unchanged unless you override them. The RAM guard is
# NOT a soft warning: crossing it makes the world tear itself down, because
# 28 minds + 7 gazes will otherwise OOM the box. On a small or shared host,
# either lower WORLD_CONTINENTS / N or raise this deliberately, e.g.
#   make stack WORLD_MAX_MEM=0.95
WORLD_MAX_MEM ?= 0.85
WORLD_MAX_FD ?= 0.75
STATICCHECK := $(shell which staticcheck 2>/dev/null || echo "$(HOME)/go/bin/staticcheck")

RELAY_KEY_FILE := .relaykey
RELAY_KEY := $(shell cat $(RELAY_KEY_FILE) 2>/dev/null || echo "")
LDFLAGS := -X gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind.compileRelayKey=$(RELAY_KEY)
BIN_DIR := bin
RELEASE_DIR := release/bin
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

# Hivemind is pure Go. Every recipe goes through PURE_GO, which hard-disables
# cgo and blanks the CGO_* variables, so an ambient `CGO_LDFLAGS=-lllama` in the
# caller's shell (or a leftover llama.cpp checkout in /tmp) can never leak into
# a link step. Do not invoke `go build` here without this prefix.
PURE_GO := CGO_ENABLED=0 CGO_CFLAGS= CGO_CPPFLAGS= CGO_CXXFLAGS= CGO_LDFLAGS=

# Resolves the relay's real base URL from the port it actually bound. Defined
# once here: a stale path used to resolve to nothing, which silently handed
# world/fabric an empty HIVEMIND_RELAY_URL instead of failing.
RELAY_URL_SCRIPT := scripts/utils/relay_url.sh

# The relay's own base URL. World and gaze watch it to draw the broker-mesh
# layer from real telemetry; set RELAY_URL to move the relay.
RELAY_URL ?= http://localhost:$(RELAY_PORT)

# Resolve the relay's base URL from the port it actually bound. The relay
# rolls forward off a busy port and records the result, so tooling must read
# that file rather than echo the requested default.
#
# Deferred (=) so the file is read when the recipe *runs*, not when the
# Makefile is parsed — the relay has not written it yet at parse time.
# scripts/utils/relay_url.sh resolves the relay's real base URL at recipe time —
# a script, not a make variable, because make expands its own functions once
# per invocation, before the relay has bound.

.PHONY: all help \
build build-image build-hivemind build-commune build-souls build-gaze build-relay build-world build-all \
qemu qemu-serial qemu-screenshot qemu-bootproof run build-iso hw-usb \
	push push-logcheck push-cross push-commit check-root \
	build-hivemind-train build-train \
fmt vet staticcheck lint tidy check \
test test-concurrent test-race test-verbose test-full \
audit \
train train-dry eval \
up up-nodes down restart kill rerun status \
think think-fast think-long demo pain prove verify \
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
clean clean-all clean-souls clean-logs clean-bin \
version version-info \
proof-keys \
prove-relay setup

all: setup

setup: build-all
	@echo "📦 Pulling latest changes..."
	@$(SAFE_ENV) git pull origin main 2>/dev/null || echo "⚠️  git pull skipped (not a git repo)"
	@echo "🔧 Verifying (deterministic suite + kernel boot proof)..."
	@$(MAKE) verify
	@echo ""
	@echo "╔══════════════════════════════════════════════════════════════╗"
	@echo "║  ✅ FULL SETUP COMPLETE — 6 binaries + kernel + all proofs   ║"
	@echo "║  ── bin/hivemind, bin/commune, bin/souls, bin/gaze          ║"
	@echo "║  ── bin/relay (MQTT bridge), bin/world (7-continent mesh)  ║"
	@echo "║  ── bin/fabric (adaptive neural routing fabric)            ║"
	@echo "║  ── dist/hivemind-kernel.bin (pure-Go kernel + OS)         ║"
	@echo "║  ── boot proof: guards + health + shutdown via QEMU        ║"
	@echo "║  ── make run (terminal console) · make hw-usb DEV=/dev/sdX ║"
	@echo "║  ── safety: nice 19 · fd 4096 · GOMAXPROCS=$(SAFE_PROCS)    ║"
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
	@printf "  \033[32m%-20s\033[0m %s\n" "build-image" "Dist pure-Go Multiboot kernel image"
	@echo ""
	@printf "  \033[35m%-20s\033[0m %s\n" "qemu" "Boot hivemind-kernel.bin in QEMU (standalone serial console)"
	@printf "  \033[35m%-20s\033[0m %s\n" "run" "Build the kernel + OS image and boot it (alias for qemu)"
	@printf "  \033[35m%-20s\033[0m %s\n" "qemu-serial" "Alias kept for compatibility (console is the only mode)"
	@printf "  \033[35m%-20s\033[0m %s\n" "qemu-screenshot" "Dump VGA splash to /tmp and exit (no UI needed)"
	@printf "  \033[35m%-20s\033[0m %s\n" "qemu-bootproof" "Automated boot proof: guards + health + shutdown"
	@printf "  \033[35m%-20s\033[0m %s\n" "build-iso" "Bootable BIOS+EFI ISO around the kernel (needs grub-mkrescue)"
	@printf "  \033[35m%-20s\033[0m %s\n" "hw-usb" "Write boot ISO to real hardware: make hw-usb DEV=/dev/sdX"
	@printf "  \033[35m%-20s\033[0m %s\n" "push" "Log hygiene + cross-compile matrix + commit + push upstream"
	@printf "  \033[35m%-20s\033[0m %s\n" "push-logcheck" "Validate the 35 continental logs (hygiene, no panic traces)"
	@printf "  \033[35m%-20s\033[0m %s\n" "check-root" "Fail if any binary has leaked into the repo root"
	@printf "  \033[35m%-20s\033[0m %s\n" "clean" "List clean-* scopes (bin/logs/souls/sim/dist/all)"
	@echo ""
	@printf "  \033[33m%-20s\033[0m %s\n" "test" "fmt + vet + tests (with -race if a C compiler exists)"
	@printf "  \033[33m%-20s\033[0m %s\n" "test-concurrent" "Pure-Go concurrency gate (no C toolchain needed)"
	@printf "  \033[33m%-20s\033[0m %s\n" "test-full" "Full test suite"
	@printf "  \033[33m%-20s\033[0m %s\n" "audit" "fmt + vet + build + tests"
	@printf "  \033[33m%-20s\033[0m %s\n" "lint" "gofmt + vet + staticcheck"
	@echo ""
	@printf "  \033[32m%-20s\033[0m %s\n" "train" "Distill the lawbook into a local counsel model (ollama create)"
	@printf "  \033[32m%-20s\033[0m %s\n" "train-dry" "Write dataset + Modelfile without creating the model"
	@printf "  \033[32m%-20s\033[0m %s\n" "eval" "Score counsel groundedness vs the lawbook (live, honest)"
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
	@printf "  \033[35m%-20s\033[0m %s\n" "verify" "Deterministic gate: audit + kernel boot proof (make all)"
	@printf "  \033[35m%-20s\033[0m %s\n" "prove" "Full certification (audit + live mesh + cipher proofs)"
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
	@@$(SAFE_ENV) $(PURE_GO) $(SAFE_RUN) go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/hivemind ./cmd/hivemind
	@echo "✅ bin/hivemind ready"

build-commune:
	@echo "🔨 Building bin/commune..."
	@mkdir -p $(BIN_DIR)
	@@$(SAFE_ENV) $(PURE_GO) $(SAFE_RUN) go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/commune ./cmd/commune
	@echo "✅ bin/commune ready"

build-souls:
	@echo "🔨 Building bin/souls..."
	@mkdir -p $(BIN_DIR)
	@@$(SAFE_ENV) $(PURE_GO) $(SAFE_RUN) go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/souls ./cmd/souls
	@echo "✅ bin/souls ready"

build-gaze:
	@echo "🔨 Building bin/gaze..."
	@mkdir -p $(BIN_DIR)
	@@$(SAFE_ENV) $(PURE_GO) $(SAFE_RUN) go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/gaze ./cmd/gaze
	@echo "✅ bin/gaze ready"

build-world:
	@echo "🔨 Building bin/world..."
	@mkdir -p $(BIN_DIR)
	@@$(SAFE_ENV) $(PURE_GO) $(SAFE_RUN) go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/world ./cmd/world
	@echo "✅ bin/world ready"

# Release build flags: enable anti-RE, strip symbols, disable debug info
RELEASE_LDFLAGS = -s -w -X gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind.releaseBuild=1

build-relay:
	@echo "🔨 Building bin/relay..."
	@mkdir -p $(BIN_DIR)
	@@$(SAFE_ENV) $(PURE_GO) $(SAFE_RUN) go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/relay ./cmd/relay
	@echo "✅ bin/relay ready (MQTT bridge)"

build-all: build-hivemind build-souls build-gaze build-relay build-world build-fabric build-derive build-image

build-release: build-hivemind build-gaze build-relay build-world build-fabric build-derive build-souls
	@echo "Building release binaries with anti-RE hardening..."
	@for bin in hivemind commune souls gaze relay world fabric derive; do 		$(PURE_GO) go build -tags release -ldflags "$(RELEASE_LDFLAGS)" -o $(RELEASE_DIR)/$$bin ./cmd/$$bin; 	done
	@echo "Release binaries in release/bin (stripped, anti-RE enabled)"

build-fabric:
	@echo "🔨 Building bin/fabric..."
	@mkdir -p $(BIN_DIR)
	@@$(SAFE_ENV) $(PURE_GO) $(SAFE_RUN) go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/fabric ./cmd/fabric
	@echo "✅ bin/fabric ready (adaptive neural routing fabric, real data only)"

# ── Pure-Go Multiboot kernel image ───────────────────────────────────────────
#
# build-image emits the bare-metal boot image with only `go run`: no compiler
# toolchain is involved (the image bytes are produced by internal/kernel/
# bootimage). The qemu targets hand that image to QEMU, which boots it as a
# Multiboot-1 kernel and prints the HIVEMIND boot log on the VGA screen.
BOOT_IMAGE ?= $(DIST_DIR)/hivemind-kernel.bin

build-image:
	@echo "🔨 Building pure-Go boot image..."
	@mkdir -p $(DIST_DIR)
	@@$(SAFE_ENV) $(PURE_GO) $(SAFE_RUN) go run ./cmd/hivemind-image -out $(BOOT_IMAGE)
	@echo "✅ $(BOOT_IMAGE) ready"

qemu: build-image
	@echo "🖥️  Booting kernel in QEMU..."
	@BOOT_IMAGE=$(BOOT_IMAGE) REBUILD_BOOT=0 ./scripts/boot-qemu.sh

# `run` is the friendly target for the full kernel + OS image: it builds the
# pure-Go image and boots it under QEMU exactly like `qemu`.
run: qemu

qemu-serial: build-image
	@BOOT_IMAGE=$(BOOT_IMAGE) REBUILD_BOOT=0 ./scripts/boot-qemu.sh -serial

qemu-screenshot: build-image
	@BOOT_IMAGE=$(BOOT_IMAGE) REBUILD_BOOT=0 ./scripts/boot-qemu.sh -screenshot

# `qemu-bootproof` is the automated end-to-end proof of the kernel + interactive
# OS: it boots the image headless, drives the serial console through the guard
# report and a shutdown, and fails unless the boot log and clean exit appear.
qemu-bootproof: build-image
	@if ! command -v qemu-system-x86_64 >/dev/null 2>&1 && [ ! -x /tmp/qemudeb/qemu.sh ]; then \
		echo "⚠️  QEMU not found — skipping boot proof"; \
	else \
		echo "🧪 QEMU boot proof (guards + health + clean shutdown)"; \
		mkdir -p $(LOG_DIR); \
		printf 'guards\nhealth\nquit\n' | BOOT_IMAGE=$(BOOT_IMAGE) REBUILD_BOOT=0 ./scripts/boot-qemu.sh -serial 2>&1 | tee $(LOG_DIR)/qemu-bootproof.log; \
		p=$${PIPESTATUS[1]}; \
		grep -q 'boot complete -- hivemind os' $(LOG_DIR)/qemu-bootproof.log || { echo "❌ boot log missing"; exit 1; }; \
		grep -q 'image checksum: OK' $(LOG_DIR)/qemu-bootproof.log || { echo "❌ guard report missing"; exit 1; }; \
		grep -q 'shutdown: draining modules' $(LOG_DIR)/qemu-bootproof.log || { echo "❌ clean shutdown missing"; exit 1; }; \
		[ "$$p" -eq 0 ] || { echo "❌ QEMU exit $$p"; exit 1; }; \
		echo "✅ QEMU boot proof passed"; \
	fi

# ── Real hardware (bare-metal) targets ───────────────────────────────────────
# The kernel is a Multiboot-1 image; bare-metal media need a bootloader that
# can hand off to it. If grub tooling is present we build a bootable ISO
# (BIOS+EFI via grub-mkrescue); hw-usb dd's that ISO onto a USB stick.
GRUB_RESCUE := $(shell command -v grub-mkrescue 2>/dev/null || true)

# ── Strict root-hygiene guard ─────────────────────────────────────────────────
# No executable/build output may ever live in the repo root. Every build recipe
# targets bin/ (host), dist/ (platform bundles), or release/. This guard fails
# if a stray root-level binary appears (e.g. a bare `go build ./cmd/...`).
STRAY_ROOT_BINS := hivemind commune souls gaze relay world fabric derive

check-root:
	@for b in $(STRAY_ROOT_BINS); do \
		if [ -e "$$b" ]; then echo "❌ stray root artifact ./$$b — canonical output is bin/$$b"; exit 1; fi; \
	done
	@echo "✔ no stray binaries in repo root"

# ── Push tooling: log hygiene + bare-metal matrix + commit + upstream ────────
#
# Make push is the release-gate command: it validates the live continental log
# corpus, cross-compiles the bare-metal binaries across the §2.0 build matrix
# (amd64/arm64/riscv64, CGO-free), stages exactly the intended tree changes,
# commits them, and pushes to the configured upstream.
CONT_LOG_DIR := world-report/logs
CROSS_DIR := dist/hivemind-cross
PUSH_PATHS := cmd/hivemind-image internal/kernel/bootimage scripts/boot-qemu.sh Makefile AGENTS.md .gitignore
CROSS_ARCHES := amd64 arm64 riscv64
CROSS_BINS := hivemind relay world fabric gaze souls commune derive

push-logcheck:
	@cnt=$$(find $(CONT_LOG_DIR) -maxdepth 1 -name '*.log' ! -name 'gaze-*' | wc -l); \
	echo "  ℹ️  continental node logs present: $$cnt / 35"; \
	[ "$$cnt" -ge 25 ] || { echo "❌ too few continental logs ($$cnt) — mesh under-sampled"; exit 1; }; \
	for cont in af an as eu na oc sa; do \
		[ -n "$$(ls $(CONT_LOG_DIR)/$$cont-*.log 2>/dev/null)" ] || { echo "❌ continent $$cont silent — no logs"; exit 1; }; \
	done; \
	echo "  ✔ all 7 continents reporting"; \
	for f in $(CONT_LOG_DIR)/*.log; do \
		case "$$f" in *gaze-*) continue;; esac; \
		[ -s "$$f" ] || { echo "❌ $$f is empty"; exit 1; }; \
		if grep -qE '^(panic:|runtime error:|	goroutine [0-9]+\[)' "$$f"; then echo "❌ panic/traceback in $$f"; exit 1; fi; \
	done; \
	echo "  ✔ every continental log non-empty and free of panic traces"; \
	echo "✅ continental log hygiene clean"

push-cross:
	@echo "🚀 Cross-compiling bare-metal matrix: $(CROSS_ARCHES)"
	@for arch in $(CROSS_ARCHES); do \
		mkdir -p "$(CROSS_DIR)/linux-$$arch"; \
		for bin in $(CROSS_BINS); do \
			echo "  ▸ $$bin / linux-$$arch"; \
			$(SAFE_ENV) CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build \
				-ldflags "$(LDFLAGS)" -o "$(CROSS_DIR)/linux-$$arch/$$bin" ./cmd/$$bin || { \
				echo "❌ cross-compile failed: $$bin/linux-$$arch"; exit 1; }; \
		done; \
	done; \
	echo "  ✔ amd64: $$(ls $(CROSS_DIR)/linux-amd64 | tr '\n' ' ')"; \
	echo "  ✔ arm64: $$(ls $(CROSS_DIR)/linux-arm64 | tr '\n' ' ')"; \
	echo "  ✔ riscv64: $$(ls $(CROSS_DIR)/linux-riscv64 | tr '\n' ' ')"; \
	echo "✅ bare-metal matrix built in $(CROSS_DIR)"

push-commit:
	@git add $(PUSH_PATHS)
	@if git diff --cached --quiet; then \
		echo "ℹ️  no staged changes — commit skipped"; \
	else \
		git commit -m "kernel: pure-Go boot image (guards + interactive OS), make tooling, clean-* scopes, root-hygiene guard"; \
	fi

push: push-logcheck check-root push-cross push-commit
	@echo "📤 Pushing upstream…"
	@git push origin "$$(git branch --show-current)"
	@echo "✅ pushed to origin"

build-iso: build-image
	@if [ -z "$(GRUB_RESCUE)" ]; then \
		echo "⚠️  grub-mkrescue not found — skip bootable ISO (media needs a bootloader)"; \
	else \
		mkdir -p $(DIST_DIR)/iso/boot/grub; \
		cp $(BOOT_IMAGE) $(DIST_DIR)/iso/boot/hivemind-kernel.bin; \
		printf '%s\n' \
			'set timeout=1' \
			'set default=0' \
			'menuentry "HIVEMIND (pure-Go kernel + OS)" {' \
			'  multiboot /boot/hivemind-kernel.bin' \
			'  boot' \
			'}' > $(DIST_DIR)/iso/boot/grub/grub.cfg; \
		$(SAFE_ENV) grub-mkrescue -o $(DIST_DIR)/hivemind-boot.iso $(DIST_DIR)/iso 2>&1 | tail -2; \
		echo "✅ $(DIST_DIR)/hivemind-boot.iso ready (dd it to a USB with make hw-usb DEV=/dev/sdX)"; \
	fi

# DEV=/dev/sdX must be given explicitly; only removable block devices between
# 512MiB and 32GiB are accepted, and the user must confirm before we write.
hw-usb: build-iso
	@[ -n "$(HW_DEV)" ] || { echo "usage: make hw-usb DEV=/dev/sdX (or DEV=$$1)"; exit 2; }
	@[ -f $(DIST_DIR)/hivemind-boot.iso ] || { echo "❌ boot ISO missing — install grub-mkrescue and rerun make build-iso"; exit 2; }
	@[ -b "$(HW_DEV)" ] || { echo "❌ $(HW_DEV) is not a block device"; exit 2; }
	@dev=$$(basename $(HW_DEV)); rem=$$(cat /sys/block/$$dev/removable 2>/dev/null || echo 0); \
	[ "$$rem" = "1" ] || { echo "❌ $(HW_DEV) is not removable — refusing"; exit 2; }
	@size=$$(blockdev --getsize64 $(HW_DEV) 2>/dev/null || echo 0); \
	[ "$$size" -ge 536870912 ] && [ "$$size" -le 34359738368 ] || { echo "❌ $(HW_DEV) size $${size} out of 512MiB..32GiB range"; exit 2; }
	@echo "⚠️  About to WIPE $(HW_DEV) and write the HIVEMIND boot ISO."
	@printf 'Type YES to continue: '; read yn; [ "$$yn" = "YES" ] || { echo "aborted"; exit 2; }
	@echo "💾 Writing $(DIST_DIR)/hivemind-boot.iso -> $(HW_DEV)"
	@$(SAFE_ENV) dd if=$(DIST_DIR)/hivemind-boot.iso of=$(HW_DEV) bs=1M status=progress conv=fsync
	@echo "✅ Bootable USB ready — reboot into it on real hardware"

# Simulator-enabled developer builds. These are built into bin/ like any other
# binary, which is why `make clean-release-sim` exists: bin/ is a shared
# workspace, and a simulator-enabled binary must never be mistaken for a
# release artefact. Only the `sim` tag is added; the release tag is not, so
# these are not hardened builds and are not meant to be shipped.
build-fabric-sim:
	@echo "🔨 Building bin/fabric (simulator enabled — NOT a release artefact)..."
	@mkdir -p $(BIN_DIR)
	@@$(SAFE_ENV) $(PURE_GO) $(SAFE_RUN) go build -tags sim -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/fabric ./cmd/fabric
	@echo "✅ bin/fabric ready (with adversarial matrix)"

build-world-sim:
	@echo "🔨 Building bin/world (simulator enabled — NOT a release artefact)..."
	@mkdir -p $(BIN_DIR)
	@@$(SAFE_ENV) $(PURE_GO) $(SAFE_RUN) go build -tags sim -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/world ./cmd/world
	@echo "✅ bin/world ready (with adversarial gate)"

# derive prints the live hourly leaf and daily root through the same
# runtime path the mesh uses. It is the independent cross-check that a
# deployed binary is actually holding the key it was built with — and the
# quickest way to confirm two hosts derive the same bus key.
build-derive:
	@echo "🔨 Building bin/derive..."
	@mkdir -p $(BIN_DIR)
	@@$(SAFE_ENV) $(PURE_GO) $(SAFE_RUN) go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/derive ./cmd/derive
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
	@$(SAFE_ENV) $(SAFE_RUN) $(PURE_GO) go vet ./...
	@echo "✅ Vet clean"

staticcheck:
	@$(SAFE_ENV) $(SAFE_RUN) $(STATICCHECK) ./... && echo "✅ staticcheck clean"

lint: fmt vet staticcheck

tidy:
	@$(SAFE_ENV) $(SAFE_RUN) go mod tidy
	@echo "✅ Tidy"

check: lint tidy

# ── Adversarial continental simulator (development only) ─────────────────────
#
# The simulator is an in-process 56-node matrix (28 defender / 28 attacker)
# across all 7 continents. It exists to attack the defence under test; it is
# never a source of reported data.
#
# It is compiled behind the `sim` build tag. `make build-release` does NOT set
# that tag, so a release fabric or world contains no simulator: no simulated
# node, no synthetic attack, no modelled latency, and no -sim flag to invoke one
# with. Release binaries report only measurements from their own real sockets,
# via cmd/fabric/telemetry.go.
#
# Everything below builds with -tags sim into bin/, which is a developer
# workspace, not a release artefact. `make clean-release-sim` removes it.

SIM_DURATION ?= 30s
SIM_SEED ?= 1

# Print the modelled geography and node matrix without running anything.
sim-topology: build-fabric-sim
	@./bin/fabric -sim-topology

# Run the matrix standalone for a bounded time, with a full scoreboard table.
sim: build-fabric-sim
	@./bin/fabric -sim -sim-duration=$(SIM_DURATION) -sim-seed=$(SIM_SEED) -sim-verbose

# Run the matrix attached to a live fabric. Ctrl-C to stop; the scoreboard stays
# on the terminal and nothing is persisted.
sim-live: build-fabric-sim
	@./bin/fabric -sim

# Adversarial launch gate: refuses to start a world whose defence does not hold.
# Exits non-zero on failure, so it can gate a deployment directly. The gate is
# built with -tags sim and is a development instrument, not a release component.
sim-preflight: build-world-sim
	@./bin/world -sim-preflight -sim-preflight-duration=$(SIM_DURATION) \
		-sim-preflight-seed=$(SIM_SEED)

# Remove simulator-enabled binaries, leaving only release-capable ones.
clean-release-sim:
	@echo "🧹 Removing simulator-enabled binaries (release builds are unaffected)"
	@rm -f bin/fabric bin/world
	@echo "✅ Done — run 'make build-release' for real-data binaries"


test: check
	@echo "🧪 Testing..."
	@if command -v gcc >/dev/null 2>&1; then \
	echo "  (using -race)"; \
	$(SAFE_ENV) $(SAFE_RUN) timeout 120 $(PURE_GO) go test ./... -race -count=1 2>/tmp/hivemind-test.log && echo "✅ Tests passed (-race)" || (cat /tmp/hivemind-test.log; exit 1); \
	else \
	echo "  ⚠️  No C compiler — running plain tests + pure-Go concurrency gate"; \
	$(MAKE) test-concurrent; \
	$(SAFE_ENV) $(SAFE_RUN) timeout 120 $(PURE_GO) go test ./... -count=1 && echo "✅ Tests passed (plain)"; \
	fi

# Pure-Go concurrency gate. Without a C toolchain, `go test -race` is impossible:
# the race detector requires cgo. This target runs the two substitutes instead —
# a static analysis over Transport and Detector (any write to a field that is
# neither atomic nor provably under the mutex fails the build) and a dynamic
# invariant sweep across adversarial GOMAXPROCS — then the whole suite with the
# runtime's own pointer checker enabled.
test-concurrent:
	@echo "  🔒 static write-discipline analysis (Transport/Detector)"
	@$(SAFE_ENV) $(SAFE_RUN) $(PURE_GO) go test ./internal/fabricsim/ -run '^TestNoUnsynchronisedStructWrites$$' -count=1
	@echo "  🔁 dynamic invariants under GOMAXPROCS 1..32"
	@$(SAFE_ENV) $(SAFE_RUN) $(PURE_GO) go test ./internal/fabricsim/ -run '^TestConcurrentRunKeepsAccountingInvariants$$' -count=1
	@echo "  ✅ pure-Go concurrency gate passed"

test-race: check
	@$(SAFE_ENV) $(SAFE_RUN) $(PURE_GO) go test ./... -race -count=1

test-verbose: check
	@$(SAFE_ENV) $(SAFE_RUN) $(PURE_GO) go test ./... -v -count=1

test-full: check
	@$(SAFE_ENV) $(SAFE_RUN) $(PURE_GO) go test ./... -count=1 -v

audit: test
	@$(SAFE_ENV) $(SAFE_RUN) ./scripts/utils/audit.sh

# ── lawbook distillation: train a standing local counsel model ─────────────
# Shrinks the provisioned law journal into a modelfile + supervised dataset,
# and (train only) asks the loopback Ollama daemon to create the model. The
# mesh auto-prefers a model whose tag carries "hivemind" in the model race.
LAW_JOURNAL ?= data/law.journal
TRAIN_MODEL ?= hivemind-counsel

train: build-hivemind
	@echo "⚖️  Distilling $(LAW_JOURNAL) into $(TRAIN_MODEL)…"
	@@$(SAFE_ENV) $(PURE_GO) $(SAFE_RUN) go run ./cmd/hivemind-train -apply -journal $(LAW_JOURNAL) -name $(TRAIN_MODEL)

train-dry: build-hivemind
	@echo "⚖️  Dry-run: $(LAW_JOURNAL) → dataset + modelfile only"
	@@$(SAFE_ENV) $(PURE_GO) $(SAFE_RUN) go run ./cmd/hivemind-train -journal $(LAW_JOURNAL) -name $(TRAIN_MODEL)

# eval: score how grounded the trained counsel really is. Every live answer
# must quote a provision, cite its audit handle, or reuse enough of a
# provision's words — confabulated citations fail the scorecard.
eval: build-hivemind
	@echo "⚖️  Scoring groundedness of $(TRAIN_MODEL) against $(LAW_JOURNAL)…"
	@@$(SAFE_ENV) $(PURE_GO) $(SAFE_RUN) go run ./cmd/hivemind-eval -journal $(LAW_JOURNAL) -model $(TRAIN_MODEL)

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
	@# Self-heal before launching. A half-dead stack used to leave orphaned
	@# relays squatting on $(RELAY_PORT) and stale world.pid/world.lock files
	@# pointing at dead PIDs; the next `make stack` would then start a relay
	@# that could not bind, a world that quit at once, and still print a
	@# cheerful success report. Clear the decks first.
	@for b in hivemind relay world fabric gaze; do pkill -TERM -x $$b 2>/dev/null || true; done
	@sleep 1
	@for b in hivemind world relay fabric gaze; do pkill -KILL -x $$b 2>/dev/null || true; done
	@rm -f .stack-*.pid /tmp/hivemind-*.sock /tmp/hivemind.sock
	@rm -f logs/world.pid logs/world.lock
	@echo "🌐 Starting full stack: $(N) mesh nodes, relay, world+gaze, fabric..."
	@ ( export HIVEMIND_CIPHER_KEY=$$(cat .relaykey) HIVEMIND_LLM_ENDPOINT=$(HIVEMIND_LLM_ENDPOINT) HIVEMIND_LLM_MODEL=$(HIVEMIND_LLM_MODEL) HIVEMIND_LLM_API_KEY=$(HIVEMIND_LLM_API_KEY); $(SAFE_ENV) $(SAFE_RUN) nohup bin/hivemind up -nodes $(N) -for $(STACK_FOR) > logs/nodes.log 2>&1 & echo $$! > .stack-nodes.pid )
	@ ( export RELAY_PORT=$(RELAY_PORT) HIVEMIND_CIPHER_KEY=$$(cat .relaykey); $(SAFE_ENV) $(SAFE_RUN) nohup bin/relay > logs/relay.log 2>&1 & echo $$! > .stack-relay.pid )
	@# The relay may roll forward off a busy port, so world/fabric must watch
	@# the port it actually bound, not the one we asked for. Each recipe line
	@# is its own shell, so the resolver is invoked inline rather than
	@# exported; it must run after the relay has written its port file.
	@test -x $(RELAY_URL_SCRIPT) || { echo "✗ missing relay URL resolver: $(RELAY_URL_SCRIPT)"; exit 1; }
	@for i in $$(seq 1 40); do [ -s .stack-relay.port ] && break; sleep 0.2; done
	@echo "   relay bound :$$($(RELAY_URL_SCRIPT) $(RELAY_URL) | sed 's|.*:||')"
	@ ( export WORLD_CONTINENTS=$(WORLD_CONTINENTS) HIVEMIND_RELAY_URL=$$($(RELAY_URL_SCRIPT) $(RELAY_URL)) HIVEMIND_CIPHER_KEY=$$(cat .relaykey); $(SAFE_ENV) $(SAFE_RUN) nohup bin/world -bin bin -gaze-port $(GAZE_PORT) -base-port $(BASE_PORT) -max-load1 $(WORLD_MAX_LOAD1) -max-load5 $(WORLD_MAX_LOAD5) -max-mem $(WORLD_MAX_MEM) -max-fd $(WORLD_MAX_FD) > logs/world.log 2>&1 & echo $$! > .stack-world.pid )
	@ ( export HIVEMIND_RELAY_URL=$$($(RELAY_URL_SCRIPT) $(RELAY_URL)) HIVEMIND_CIPHER_KEY=$$(cat .relaykey); $(SAFE_ENV) $(SAFE_RUN) nohup bin/fabric > logs/fabric.log 2>&1 & echo $$! > .stack-fabric.pid )
	@sleep 6
	@# Verify. This target used to print the dashboard URL and exit 0 even
	@# when relay/world/fabric had all died on startup, so a broken stack
	@# looked like a working one. Check each component, name the failures,
	@# show the log tail, and exit non-zero.
	@missing=""; 	pgrep -x relay  >/dev/null 2>&1 || missing="$$missing relay"; 	pgrep -x world  >/dev/null 2>&1 || missing="$$missing world"; 	pgrep -x fabric >/dev/null 2>&1 || missing="$$missing fabric"; 	pgrep -x hivemind >/dev/null 2>&1 || missing="$$missing hivemind"; 	if [ -n "$$missing" ; then \
		echo ""; \
		echo "✗ STACK FAILED TO START — not running:$$missing"; \
		echo ""; \
		for f in relay world fabric nodes; do \
			if [ -s logs/$$f.log ]; then echo "── logs/$$f.log (tail) ──"; tail -n 6 logs/$$f.log; echo ""; fi; \
		done; \
		echo "Common causes: host out of memory (world failsafe), or $(RELAY_PORT) already in use."; \
		echo "On a small box try:  make stack N=2 WORLD_CONTINENTS=3 WORLD_MAX_MEM=0.9"; \
		exit 1; \
	fi
	@echo "=== FULL STACK STATUS ==="
	@echo "minds: $$(pgrep -c -x hivemind)   relay: up   world: up   fabric: up"
	@pgrep -a -x hivemind | sed 's/^/  /'
	@echo ""
	@echo "Sockets: $$(ls /tmp/hivemind-*.sock 2>/dev/null | wc -l)"
	@echo "Logs: $$(ls -1 logs/*.log 2>/dev/null | tr '\n' ' ')"
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
	@ours=$$(if [ -s .stack-relay.port ]; then $(RELAY_URL_SCRIPT) | sed 's|.*:||'; fi); \
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
	@$(SAFE_ENV) $(SAFE_RUN) $(PURE_GO) go test ./cmd/gaze/ -run TestWorldLive -v -count=1

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
	@$(SAFE_ENV) $(SAFE_RUN) ./scripts/testing/pain.sh

prove: build
	@mkdir -p $(LOG_DIR)
	@PROOF=$(LOG_DIR)/proof-$$(date -u +%Y%m%dT%H%M%SZ).log; \
	set -o pipefail; \
	echo "===== PROOF RUN $$(date -u) · $$(git rev-parse --short HEAD 2>/dev/null || echo nogit) =====" | tee "$$PROOF"; \
	$(SAFE_ENV) $(SAFE_RUN) ./scripts/utils/audit.sh 2>&1 | tee -a "$$PROOF" && \
	$(SAFE_ENV) $(SAFE_RUN) timeout 120 ./scripts/testing/timed_test.sh 2>&1 | tee -a "$$PROOF" && \
	$(SAFE_ENV) $(SAFE_RUN) ./scripts/testing/verify_supermesh.sh 2>&1 | tee -a "$$PROOF" && \
	$(SAFE_ENV) $(SAFE_RUN) ./scripts/testing/pain.sh 2>&1 | tee -a "$$PROOF" && \
	$(SAFE_ENV) $(SAFE_RUN) bash scripts/utils/proof_keys.sh 2>&1 | tee -a "$$PROOF" && \
	echo "✅ PROOF COMPLETE — transcript: $$PROOF" | tee -a "$$PROOF"

proof-keys: build
	@$(SAFE_ENV) $(SAFE_RUN) bash scripts/utils/proof_keys.sh

# Deterministic verification gate used by `make all`: repo-wide Go audit (fmt,
# vet, build, full test suite) plus the kernel boot proof. No live-network or
# long-running stimulus steps, so it cannot flake on machine load or relay
# timing; the deeper live mesh and cipher certifications live under `prove`.
verify: build check-root qemu-bootproof
	@echo "🛡️  Verification gate (repo audit + kernel boot proof)"
	@$(SAFE_ENV) $(SAFE_RUN) ./scripts/utils/audit.sh
	@echo "✅ verification gate passed"

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
	@f=$$(mktemp --suffix=.go); printf '%s\n' 'package main' 'import ("net"; "os")' 'func main() {' '  l, e := net.Listen("tcp", "127.0.0.1:0")' '  if e != nil { os.Exit(1) }' '  l.Close()' '}' > "$$f"; go run "$$f" 2>/dev/null && echo "  ✔ loopback TCP (mesh transport ok)" || echo "  ❌ loopback TCP broken"; rm -f "$$f"

souls: build-souls
	@$(SAFE_ENV) $(SAFE_RUN) bin/souls

commune: build-commune
	@$(SAFE_ENV) $(SAFE_RUN) bin/commune

gaze: build-gaze
	@$(SAFE_ENV) $(SAFE_RUN) bin/gaze

# `make clean` is non-destructive by design: no binary is ever removed without
# you naming the exact thing. Every cleaning scope is a `clean-*` target, so
# `make clean` enumerates them and tells you which one to press.
clean:
	@echo ""
	@echo "🧹 HIVEMIND CLEAN TARGETS — 'make clean' never deletes (pick a scope):"
	@echo ""
	@printf '  \033[1m%-22s\033[0m %s\n' 'TARGET' 'WHAT IT REMOVES'
	@printf '  \033[1m%-22s\033[0m %s\n' '------' '--------------'
	@printf '  \033[35m%-22s\033[0m %s\n' 'clean-bin' 'bin/ host binaries + go build/test cache'
	@printf '  \033[35m%-22s\033[0m %s\n' 'clean-logs' 'logs/ *.log only'
	@printf '  \033[35m%-22s\033[0m %s\n' 'clean-souls' '.hive_memory* (soul memory pools)'
	@printf '  \033[35m%-22s\033[0m %s\n' 'clean-release-sim' 'simulator-enabled bin/fabric bin/world'
	@printf '  \033[35m%-22s\033[0m %s\n' 'release-clean / dist-clean' 'dist/ release artifacts'
	@printf '  \033[32m%-22s\033[0m %s\n' 'clean-all' 'EVERYTHING above — return the repo to pristine'
	@echo ""
	@echo "Run:  make clean-all   (full reset, incl. dist/ + .relaykey)"
	@echo "      make clean-bin   (rebuild host binaries faster: rm bin -> make build)"
	@echo "      make clean-logs / clean-souls / clean-release-sim"
	@echo "      make dist-clean  (release artifacts only)"
	@echo ""

clean-bin:
	@rm -rf $(BIN_DIR)
	@go clean -testcache
	@echo "🧹 bin/ + build cache cleaned"

clean-all: clean-bin clean-logs clean-souls clean-release-sim release-clean
	@rm -f $(RELAY_KEY_FILE)
	@echo "🧹 Deep cleaned — repo returns to pristine (re-run 'make setup')"

clean-souls:
	@rm -rf .hive_memory
	@echo "👁️  [OVERMIND] Memory pool cleared."

clean-logs:
	@rm -f $(LOG_DIR)/*.log
	@echo "🧹 Logs cleaned"

release: release-linux release-darwin release-windows
	@cd $(DIST_DIR) && sha256sum hivemind-* > SHA256SUMS && cat SHA256SUMS
	@cd $(DIST_DIR) && sha256sum hivemind-* > SHA256SUMS && cat SHA256SUMS

release-linux: release-linux-amd64 release-linux-arm64 release-linux-arm
release-darwin: release-darwin-amd64 release-darwin-arm64
release-windows: release-windows-amd64

release-linux-amd64: $(RELAY_KEY_FILE)
	@echo "📦 Building linux/amd64..."
	@mkdir -p $(DIST_DIR)
	@$(SAFE_ENV) $(SAFE_RUN) GOOS=linux GOARCH=amd64 $(PURE_GO) go build -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/hivemind-linux-amd64 ./cmd/hivemind

release-linux-arm64: $(RELAY_KEY_FILE)
	@echo "📦 Building linux/arm64..."
	@mkdir -p $(DIST_DIR)
	@$(SAFE_ENV) $(SAFE_RUN) GOOS=linux GOARCH=arm64 $(PURE_GO) go build -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/hivemind-linux-arm64 ./cmd/hivemind

release-linux-arm: $(RELAY_KEY_FILE)
	@echo "📦 Building linux/arm..."
	@mkdir -p $(DIST_DIR)
	@$(SAFE_ENV) $(SAFE_RUN) GOOS=linux GOARCH=arm GOARM=7 $(PURE_GO) go build -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/hivemind-linux-arm ./cmd/hivemind

release-darwin-amd64: $(RELAY_KEY_FILE)
	@echo "📦 Building darwin/amd64..."
	@mkdir -p $(DIST_DIR)
	@$(SAFE_ENV) $(SAFE_RUN) GOOS=darwin GOARCH=amd64 $(PURE_GO) go build -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/hivemind-darwin-amd64 ./cmd/hivemind

release-darwin-arm64: $(RELAY_KEY_FILE)
	@echo "📦 Building darwin/arm64..."
	@mkdir -p $(DIST_DIR)
	@$(SAFE_ENV) $(SAFE_RUN) GOOS=darwin GOARCH=arm64 $(PURE_GO) go build -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/hivemind-darwin-arm64 ./cmd/hivemind

release-windows-amd64: $(RELAY_KEY_FILE)
	@echo "📦 Building windows/amd64..."
	@mkdir -p $(DIST_DIR)
	@$(SAFE_ENV) $(SAFE_RUN) GOOS=windows GOARCH=amd64 $(PURE_GO) go build -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/hivemind-windows-amd64.exe ./cmd/hivemind

release-hardened: $(RELAY_KEY_FILE)
	@echo "🛡️  Building hardened linux/amd64..."
	@mkdir -p $(DIST_DIR)
	@$(SAFE_ENV) $(SAFE_RUN) GOOS=linux GOARCH=amd64 $(PURE_GO) go build -ldflags "-s -w $(LDFLAGS)" -o $(DIST_DIR)/hivemind-hardened ./cmd/hivemind

release-clean:
	@rm -rf $(DIST_DIR)
	@echo "🧹 Release artifacts cleaned"

dist-clean: release-clean

dev:
	@which entr >/dev/null 2>&1 || { echo "❌ entr not installed (apt/brew install entr)"; exit 1; }
	@echo "👀 Watching for changes... (Throttled execution)"
	@find . -name '*.go' ! -path './dist/*' ! -path './.git/*' | entr -c sh -c '$(SAFE_ENV) $(SAFE_RUN) sh -c "gofmt -l . ; $(PURE_GO) go vet ./... && $(PURE_GO) go test ./... -count=1"'

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
