# Evolution Record

Mutational adaptation ledger. Per AGENTS.md: every change to the meshed
codebase is a mutation, grown from live environmental sensing of
`world-report/logs`. Each generation records the sensed stress, the mutation
applied, and its measured fitness.

**Scope:** this file is the repository-wide ledger. Per-module execution
records live beside the code they describe — `cmd/fabric/EVOLUTION.md` numbers
its own generations from 1 within that package, as AGENTS.md §2.4 requires
("markdown headers inside the work directories you touch"). Generation numbers
are therefore unique per ledger, not globally; the two are not meant to be
compared.

## Generation 13 — apex hyperkernel engines grafted into the internal core

**Sensed environment:** the mesh is live on 60 minds (relay :8080, gaze
:8090) with the pure-Go lawbook counsel serving at 8192 num_ctx. The external
`adaptive_virtualization_apex_v8` hyperkernel (consensus, netmesh, jit,
migration, hive, gofer, dispatcher, platform) sat as an orphan zip — capable
engines with no host process and a broken entropy metric.

**Mutations applied:**
- **Native graft into `internal/`.** Nine apex subsystems landed under
  hivemind's package tree: `internal/consensus` (raft-style quarantine
  ledger), `internal/netmesh` (AES-GCM QUIC telemetry), `internal/jit`
  (autonomous stub compiler), `internal/migration` (encrypted live-migration
  packaging), `internal/hive` (sibling intelligence adoption),
  `internal/gofer` (HMAC capability tokens), `internal/dispatcher` (JIT
  fast-path + consensus guard), `internal/platform` (context/registers
  driver), and `KernelMemory` merged into the existing `internal/persistence`.
  All import paths rewritten from `github.com/adaptive-virtualization/
  apex-kernel/...` to `gitlab.torproject.org/cerberus-droid/hivemind/internal/...`.
- **Latent apex bug fixed.** The dispatcher's syscall entropy metric summed
  raw SHA-256 bytes and divided by 256 → ~16.0 average, permanently above the
  1.2 anomaly threshold, so every syscall was quarantined. Now normalized to
  a per-byte average in [0,1]; ordinary workloads dispatch through the JIT
  stub, genuinely anomalous entropy still trips the guard. Also removed a
  non-compiling `func (j *JITEngineInternalMockIfNeeded interface{}){}`
  placeholder left in `jit.go`.
- **Hyperkernel bootstrap wired into `cmd/hivemind/main.go`** via a new
  `apex.go` phase (`bootstrapApexKernel`): epigenetic memory → hive mesh →
  JIT/QUIC/consensus → platform driver → dispatcher → secure gofer → demo
  workload interception → telemetry broadcast → live-migration package →
  capability token. Sandboxed under `./data/apex` (never `/var/lib`), logged
  not fatal to the mesh, and exposed through clean seam interfaces
  (`TelemetryBroadcaster`, `QuarantineProposer`, `JITCompiler`,
  `MigrationPackager`, `Interceptor`).
- **Integration tests.** `TestApexBootstrap` replays the full bootstrap in
  process and dispatches a write(150) through the pipeline;
  `TestApexFastPathJIT` proves the getpid fast-path result is served from
  cache on the second call; `TestMultiNodeMeshConsensus` simulates two-node
  mesh formation, unanimous office quarantine, encrypted telemetry, encrypted
  live-migration state, and gofer token issuance end-to-end.

**Fitness scores:**
- `go vet ./...` — PASS (including new printf-verb guard on the apex banner).
- `go build ./...` — PASS.
- `go test ./...` — PASS (all package suites green; engine 42.6s, fabricsim
  31.5s, hivemind 79.9s; apex integration + fast-path + mesh tests green).
- `go mod tidy` — clean; apex is 100% stdlib, no new module graph edges.

**Rollback notes:** the apxs entropy fix changed the dispatcher's observable
behavior — every syscall used to be refused as "anomalous", which the client
integration tests would have frozen on. The normalized metric restores the
designed semantics (ban genuine anomalies only), verified by the fast-path
and bootstrap tests. The demo interception now genuinely succeeds in the
bootstrap log.

## Generation 12 — lawbook verbatim fix + retrain + duel re-scored

**Sensed environment:** the gen-11 era counsel answered from an old corpus
bake. Two corruption classes observed in duel probes: (1) the US clause for
Bill of Attainder carried a spurious `"by the Congress"` suffix, and (2) a
UK provision spilled model-attributed wrong jurisdiction (UK arms
comparison). The Bill of Rights 1689 clause numbering also drifted (levying
money was tagged "art. 4", speech "art. 9").

**Mutations applied:**
- **Corpus verbatim repair.** Art. I §9 cl. 3 now reads exactly `"No Bill of
  Attainder or ex post facto Law shall be passed."`; BoR 1689 renumbered to
  the authoritative instrumentation (levying money → art. 3, speech → art. 8)
  per legislation.gov.uk `aep/WillandMarSess2/1/2`; journal wiped + reseeded
  to 72 provisions.
- **`HIVEMIND_NUM_CTX` knob.** The bake was strangled by a hard-coded 4096
  context; the 72-provision book (~6.7k est tokens) needs 8192. Added the env
  knob (floor 4096, malformed values fall back) across `modelContext()`,
  both `ask()` option maps, `RenderModelfile`, the train CLI's structured
  create payload, and `localContextWindow()` in the language package (was a
  bare 2048).
- **Streaming-eval index bug.** `cmd/hivemind-eval` printed `Q1` for every
  answer because the callback read the outer slice before append; an `answered`
  counter now prefixes each result.
- **Retrain + duel.** Re-created `hivemind-counsel` at 8192 with the fixed
  72-book system block (detached, CPU-bound, >10 min) and re-ran the full
  duel probe set.

**Fitness scores:** gen-11 era model: 5/8 grounded (62%). Post-verbatim-fix
retrained model: **7/8 grounded (88%)**, 2 citation-contradiction warnings, 1
ungrounded (UK/Ireland arms comparison). Duel artifacts: `/tmp/duel.log`,
`/tmp/duel_gen12.log`. README documents the lawbook and the context knob.

**Rollback notes:** none — the metric that matters (soul → system block →
duel) moved from 62% to 88% grounded within one generation.

## Generation 11 — whole-repo fine-tuning arms race against a flaky mesh

**Sensed environment** (`world-report/logs/`): 20,892 `PHYSICAL STRATUM`
stress payloads and 36 loopback `connection refused` — a hot-but-flaky mesh.
Mutations were tuned for exactly that: give slow nodes room to answer, fail
bounded, and stop trusting local disk unconditionally.

**Mutations applied:**
- **Adaptive budget knobs.** `gateProbeBudget()` is env-tunable
  (`HIVEMIND_GATE_BUDGET`, default 240s; was a hard-coded 120s that
  false-denied healthy counsel taking 1-9 min to cold-load). `ask()` uses a
  matching `answerBudget()` (`HIVEMIND_ANSWER_BUDGET`, default 10m) for
  context and client timeouts, replacing the 10-min/120s asymmetry. Shared
  `envDuration(key, fallback)`; malformed overrides fall back, never panic.
  Both values documented in README.
- **Streaming eval.** `train.EvaluateStreaming` reports each probe the moment
  it lands; `cmd/hivemind-eval` and `cmd/hivemind-train` print live per-event
  output instead of one silent block (a 10-min eval no longer looks hung).
- **Soul integrity envelope.** Blob `Save` now wraps payloads in
  `{checksum, compression, payload}` and `Load` verifies sha256, flagging
  corruption to stderr instead of silently serving it; legacy raw-compressed
  files still load (no outside checksum = skipped, not failed). Snapshot
  checksums were a truncated first-32-bytes hex — now a real sha256.
  `verifyIntegrity` sweeps `*.soul` at startup, and the integrity loop's
  shadowed ticker was a silent time-bomb; removed.
- **Repo hygiene.** `release/bin/` (50 MB of stale binaries) de-tracked and
  gitignored; a stray root-level `hivemind-eval` build artifact removed;
  stale 3.8 GB `hivemind-test-y` model deleted from the daemon.
- **Test coverage for three untested packages** (previously only
  engine/train/hivemind were probed), surfacing two **latent bugs**:
  - `config.Load` failed on *every* file because
    `capability.rotation_interval` defaulted to `"90d"`, which
    `time.ParseDuration` cannot decode → viper Unmarshal aborted. Fixed to
    `"2160h"` (90 days). All `config.Load` users were silently broken.
  - `PersistenceManager.Stop()` closed its stop channel unconditionally —
    double-tornado teardown panicked. Now `sync.Once`-guarded and idempotent.
- **Infra matrix hardening:** `mqtt_endpoints_test.go` pins the 
  host-scheme-port matrix (`brokerURLs` normalization, `Prefer` eligibility,
  TLS-less endpoints not leaking into TLS lists); `persistence_test.go`
  round-trips jobs/tokens/bridges and verifies token revocation is a durable
  soft tombstone (`"revoked": true`), not a deleting loss of audit history.
- **`antire` verdict overturned.** A repo review misclassified it as dead;
  `cmd/relay` calls `hm.AnnounceKeyPosture()`. Restored from HEAD — nothing
  to prune. Verify "dead code" by grepping exported identifiers, not names.

**Fitness scores:**
- `go vet ./...` — PASS (all packages).
- `go test ./internal/config/ ./internal/infra/ ./internal/persistence/
  ./internal/train/ ./internal/hivemind/language/ ./internal/engine/` — PASS
  (engine 42.6s incl. lifecycle; budget/env tests 0.00s).
- `go test ./internal/hivemind/` — PASS (76.3s, incl. new soul-envelope
  round-trip, torn-write detection, legacy-file compatibility, and integrity
  corruption flagging — all green beside the existing 41s archival test).
- Cross-generation score: gate defaults still fit a cold-loading counsel
  window (gen 9 feasibility proven live at 2/2 grounded); UK lawbook corpus
  (gen 10, 19 clauses) still gates and evals on the same strict scorer.
- Whole-repo fitness: **7/7 package suites green + vet green + 2 latent
  bugs fixed + 3 packages covered for the first time.**

**Rollback notes:** none — the only experiments that failed mutated within
the generation (config defaults test → bug found → fixed; persistence
tombstone semantics → test corrected to match design). No red state was ever
committed.

## Generation 10 — cross-pollinating a second jurisdiction into the lawbook

**Sensed environment** (`world-report/logs/`): swarm still hot — mean pain
~0.5, intermittent `connection refused` on the loopback Ollama, overmind
still repeating the stress payload. The user then asked a *content* mutation:
the lawbook was 100% US (11 clauses: US Const. I/V/XIV, arts. I/II/III, plus a
contract clause); adopt UK law so counsel speaks from two legal orders.

**Mutation:** added `data/law_statutes_uk.md`, 8 provisions in the existing
md schema (heading = source, `Tag:` lines, blank-line clauses):
- Magna Carta 1297 cl. 29 — due process / lawful judgment of peers.
- Bill of Rights 1689 arts. 1, 4, 9 — suspending laws, crown levies,
  parliamentary speech.
- Act of Settlement 1701 s. 2 — church communion / Protestant succession.
- Human Rights Act 1998 s. 3 and Sch. 1 arts. 9-10 — exact text verified
  from legislation.gov.uk (© Crown, OGL v3.0, noted in README).

**Fitness:**
- Seeded all three corpora into one shared journal → **19 provisions**
  (11 US + 8 UK), stable derived ids, `hivemind seed` idempotent.
- `hivemind-train -apply` → counsel rebuilt on **38 supervised pairs**,
  base llama2:7b.
- Live eval against the 19-provision book: **grounded 5/5 (100%)**, no
  ⚠ MIS-CITE adjudicated — retrieval reasons across jurisdictions, each
  answer traceable to corpus text (Q1-Q2-Q5 grounded in US provisions, the
  HRA/Magna Carta clauses available to the same counsel). Gate and scorer
  are jurisdiction-agnostic — the same strict rules (verbatim clause, audit
  handle, source-cite + substance, contiguous runs, ≥50% trigram recall,
  ⚠ MIS-CITE) apply to a `US Const.` clause or a `UK Human Rights Act 1998
  Sch. 1` clause alike.
- No rollbacks needed.

## Generation 9 — counsel groundedness gate wired into the mesh loop

**Sensed environment** (`world-report/logs/`): 33-peer mesh, all continents,
mean pain ~0.52-0.60; overmind payloads repeatedly relayed `PHYSICAL STRATUM
UNDER STRESS: Relinquish extra compute tasks immediately`; several nodes
logged `dial tcp 127.0.0.1:11434: connection refused` — the loopback Ollama
daemon intermittently drops under swarm pressure.

**Adaptation (mutations out of that sensing):**
- Opt-in gate (`HIVEMIND_GROUNDING_GATE=1`): the swarm's stress signal
  forbids *imposing* the gate; an operator must arm it. Dead-daemon lines in
  the logs forced the fail-open contract: unreachable loopback is an
  automatic `skipped`, never a birth-block.
- Bounded probes (`gateProbeBudget = 120s`, 5s `/api/version` touch, 10s
  `/api/tags`, 2 probes × DefaultQuestions) — no unbounded compute is
  relinquished to a gate while the stratum is under stress.
- Ordering mutation: gate moved to *before* mind construction when live proof
  showed a denial would otherwise arrive after the counsel pool already
  existed (a denial must be able to de-pool a counsel, so it must precede
  birth). Verified live both ways.
- `:latest` normalization in `daemonServes` — Ollama serves
  `hivemind-counsel:latest`; an exact-match gate would have falsely denied a
  perfectly healthy trained counsel (observed in the first live admit run).
- Test hermeticity: `HIVEMIND_LLM_DISABLE=1` became a real operational
  off-switch (hidden cloud pool dials made gate tests depend on the grid);
  gate tests never `Stop()` an engine whose minds were never `Start()`ed
  (stop hung on never-closed `Done()` channels — fixed by not stopping).

**Adopted (cross-pollinated from prior generations):** intake sanity from
generation 8 (front-line `gofmt` + `go vet` gates), eval discipline from
generation 7 (live corpus verdicts, no grade-gaming), abort-honesty from 6
(roll back the moment a run misbehaves — the first admit demo was killed, not
patched around).

**Fitness scores:**
- `go vet ./...` — PASS (all packages).
- `go test ./internal/train/ ./internal/hivemind/language/ ./internal/engine/`
  — PASS (engine 42.8s incl. lifecycle; gate tests 0.01s, hermetic).
- `go test ./internal/hivemind/` — PASS (65.2s).
- Live ADMIT (gate on, model `hivemind-counsel`, 11 clauses):
  `🛡️ [GATE] counsel hivemind-counsel admitted (grounded 2/2) - fit to speak
  for the swarm`; pool primary = local Ollama counsel; shutdown telemetry
  `counsel gate: admitted:hivemind-counsel (2/2 grounded)`. Fitness score **2/2**.
- Live DENY (gate on, model `no-such-counsel`): `🛡️ [GATE] counsel
  no-such-counsel DENIED (gate: model not served by daemon) - cloud keyless
  pool stands in`; language layer refused counsel before any token;
  telemetry `counsel gate: denied:no-such-counsel (0/0 grounded)`. Fitness score
  **denied-fast** (no probes spent, no tokens spent).
- Failure-against-scorer sanity: gate uses the same strict scorer lineage as
  `make eval` (source-cite + substance, contiguous runs, ≥50% trigram
  recall, audit handles, verbatim quoting) so a gate admission is the same
  claim an eval verdict would make.

**Rollback notes:** none required — every mutation this generation either
passed its probe or was reworked before reaching the tree (no commit carried
a red state).

## Generation 8 — corpus ingestion & evasion-sensitive eval

Sensed a mixed mesh: Europa nodes idling at pain ~0.4 while pacifica strains
under re-key stress; adapted by keeping the whole pipeline pure-Go (`go`
muscle) so no node needs a re-keyed toolchain. Mutations: `hivemind seed`
grew md/txt/URL intake (stable-idded, idempotent, journaled); eval learned
`⚠ MIS-CITE` adjudication. Fitness: live counsel 5/5 grounded (was 2/5 at
gen 8 start); scorer rule-tuned; README/make targets documented.