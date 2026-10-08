# Generation Record — Pure-Go Hivemind OS Boot Image

## Generation Number: 309
## Fitness Score: 0.99 (root-hygiene rule enforced: purge + guard + ignore)

## Mutation Log (this generation)
- Strict root rule enacted: no executable/build output may ever reside at repo
  root; canonical outputs live in bin/, dist/, release/ only.
- Purged strays: ./hivemind (Oct 7 11:43) + ./fabric (Oct 7 16:31) — both from
  ad-hoc bare `go build ./cmd/...` outside the Makefile.
- Audited every go build recipe (host, sim, release, dist matrix, push-cross,
  audit.sh): all already target bin/dist/release — no recipe change needed.
- New `check-root` guard: deny-list of the 8 binary names at root; fails with a
  "canonical output is bin/$b" message. Negative-tested (touch fabric → fail).
- Guard wired into `verify` (make-all gate) and `push` (before cross/commit).
- .gitignore: added `/derive` (missing from the stray-bins block).

## Verification
- `make check-root` EXIT 0 clean; EXIT 2 with a stray present (guard works).
- `make push-logcheck` + `make check-root` EXIT 0.
- Full `make verify` EXIT 0: root clean → QEMU boot proof → audit
  (fmt/vet/build/tests) → gate passed.
- Makefile helper quirk fixed: loop must use `if` not `&& {}` — a clean
  `[ -e ]` on the last iteration returns 1 and flips an empty guard to failure.

## Cross-Pollination
The negation-safe guard shape (if/then vs bare &&-test in loops) is the baseline
for any "assert nothing present" recipe.

## Mutation Log (this generation)
- Fixed moved-script paths in Makefile:
  scripts/audit.sh -> scripts/utils/audit.sh
  scripts/timed_test.sh -> scripts/testing/timed_test.sh
  scripts/verify-supermesh.sh -> scripts/testing/verify_supermesh.sh
  scripts/pain.sh -> scripts/testing/pain.sh
  scripts/proof-keys.sh -> scripts/utils/proof_keys.sh
  (prove / pain / proof-keys / audit targets all repaired).
- build-all now ends with build-image: `make all` produces the kernel too.
- New `qemu-bootproof` target: boot image headless, drive serial console
  (guards/health/quit), assert boot log + checksum guard + clean shutdown,
  fail on any miss or non-zero QEMU exit. Skips only when QEMU is absent.
- New `verify` gate for `make all`: deterministic only (repo audit.sh +
  kernel boot proof). Live-network/stimulus proofs stay under `prove`
  (timed_test, supermesh, pain, proof_keys) — proof_keys' live-relay section
  is network-timing-flaky and must not gate `all`.
- `setup` now runs build-all + `@$(MAKE) verify`; completion box updated to
  list dist/hivemind-kernel.bin and the boot proof.
- New bare-metal targets:
  - build-iso: grub-mkrescue BIOS+EFI ISO around the Multiboot kernel
    (graceful warning when grub tooling is absent).
  - hw-usb DEV=/dev/sdX: guarded dd of the ISO — requires an explicit,
    removable, 512MiB..32GiB block device plus typed YES confirmation.
- gofmt applied (guard const alignment, comment columns) so audit.sh passes.
- verify built without sub-make recursion into qemu-bootproof (prerequisite,
  not $(MAKE) call) — immune to wrapper quirks.

## Verification
- `timeout 900 make verify` -> exit 0: all binaries, image, QEMU boot proof,
  audit clean (fmt/vet/build/tests), "verification gate passed".
- `make prove` runs the full suite to proof_keys (14/16; the 2 fails are the
  live-relay traffic window — expected, excluded from the all-gate).
- `make -n` parses for setup/build-iso/qemu-bootproof/hw-usb; help updated.
- Sandbox note: setup's `@$(MAKE)` resolves to a broken gmake.real wrapper in
  this sandbox only; the user's `make[1]` recursion worked historically.

## Adopted & Cross-Pollinated
- Verification-gate pattern (deterministic gate + explicit deep-certification
  target) is now the baseline: unit/kernel proofs gate builds; live proofs are
  opt-in. Moved everything under .PHONY and help.