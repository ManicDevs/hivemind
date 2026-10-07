# fabric — Mutational Adaptation Pipeline

Package-local execution record for `cmd/fabric`, as AGENTS.md §2.4 requires.
Generation numbers here are unique to this file and start at 1; the
repository-wide ledger is [`docs/EVOLUTION.md`](../../docs/EVOLUTION.md), whose
generations are numbered independently and are not comparable to these.

**Generation Number:** 3
**Fitness Score:** 10/10 — the race detector now runs. See Generation 3.

## Generation 3 — the race detector, unlocked without a system compiler

**Sensed environment:** unchanged and still cold, but the *verification*
environment was the real constraint. Generations 1 and 2 both closed with
`go test -race` unmet: the detector needs cgo, this box is a Flatpak runtime
(`Freedesktop SDK 25.08`) with no `gcc`/`clang`/`tcc` and no package manager
(`apt-get`, `pacman`, `apk`, `dnf` all absent). That left four commits'
concurrency claims resting on logic and leak-tests alone.

**Mutations applied:**

- **`scripts/race.sh`.** A self-contained toolchain in a temp directory: no
  system install, no residue beyond a deletable directory. Three things were
  needed to make `-race` link:
  1. `CC` must be a `zig cc` shim, because Go probes the compiler with
     `$(CC) -E` and therefore never reaches zig's `cc` subcommand on its own.
  2. zig does not export `__popcountdi2`, which Go's race runtime links
     against. A two-function compiler-rt substitute is compiled and passed via
     `CGO_LDFLAGS`.
  3. `-linkmode=external` — Go's internal linker cannot resolve symbols from a
     cgo archive; the external linker can.
  If a real `gcc` is present the script detects it and uses it, so this is a
  fallback, not a requirement.
- The script defaults to `./...` rather than passing an empty package list,
  which would otherwise have tested the repo root — not a Go package.

**Measured, both modules:**

| check | result |
|---|---|
| `./scripts/race.sh` (hivemind, 15 suites) | PASS — race-clean |
| `./scripts/race.sh` (bininspect) | PASS — race-clean |
| `internal/fabricsim` under `-race` | PASS (226s, ~3.5x the non-race cost) |
| `internal/hivemind` under `-race` | PASS (105s) |
| `cmd/fabric` under `-race` | PASS (1.3s) |

The generations 1 and 2 substitute evidence — the exact-baseline goroutine-leak
test and the 6,400-iteration limiter contention test — is now backed by the real
detector rather than standing in for it.

**Defect caught in the tooling while writing it:** the first version of
`race.sh` ran bare `go test` when given no arguments, which tested the repo root
and failed with `setup failed`. Only visible by actually running the script
end to end; `bash -n` would not have caught it.

**Fitness Score: 10/10.** No outstanding deduction.

**Next mutation (Gen 4):** the sensor is still absent. With the fabric now
emitting `node_id`, `peer`, `loss`, and per-hop backoff as slog fields, the
cross-pollination target remains `auth_fail` versus `probes`: a node whose
authentication failures climb while its probes stall is rotating its hourly key
against a peer that has not, and that is now a two-field query over real logs
once the stack is up.

## Generation 2 — full standard: budgets, typed context, bounded fan-out

**Sensed environment:** still cold — `world-report/logs/` absent, `logs/` empty,
no mesh processes. Generation 1 spent itself on lifecycle; this generation is
the *standards* pass AGENTS.md §1 demands, aimed at the guarantees a reader
cannot currently verify by reading the code.

**Defects found by reading for reviewability (not by the absent sensor):**

1. **Unreviewable magic numbers.** `3*time.Second`, `5*time.Second`,
   `30*time.Second`, `64`, `8192`, `1<<20` appeared as bare literals at 14
   sites. A reviewer cannot tell a dial timeout from a handshake timeout from a
   read deadline — so changing one silently changes a guarantee another was
   protecting. The node's entire liveness budget was implicit.
2. **Unbounded fan-out.** `acceptLoop` spawned a goroutine per connection with
   no ceiling: a node accepting faster than it could hand off grew without
   limit. An unbounded queue under load is a slow OOM that looks like healthy
   throughput.
3. **`ctx` absent from the data path.** `forwardData`, `handleWire`,
   `onDeliver`, `emitFeedback`, `routeBackward`, `getOrCreatePeer`,
   `softmaxNextHop` all took no context and reached for the `f.ctx` field. The
   data path could not be cancelled or scoped independently of the node.
4. **Logging could not carry state.** Every loop called `f.logger()` +
   `f.ctx` by hand. Nothing could pass a per-call logger, so a peer loop
   could not be attributed without a struct field that is nil in every test
   that builds a bare peer.
5. **Credential in the log.** `apex.go` printed `token[:25]` — a fragment of
   an HMAC capability token. A logged prefix is a logged credential.

**Mutations applied:**

- **`hardening.go`: every liveness constant named.** `dialTimeout`,
  `handshakeTimeout`, `readIdleTimeout`, `drainGrace`, `keyCheckInterval`,
  `metricsInterval`, `trafficInterval`, `backoffBase`/`backoffCeiling`,
  `peerQueueDepth`, `readBufferSize`, `telemetryRingCapacity`,
  `maxConcurrentStreams`. The comment on each states the guarantee it carries,
  so the constants double as the liveness contract.
- **Typed context key (`ctxKey`, unexported).** `WithLogger` / `loggerFrom`.
  An unexported key type makes the namespace collision-proof against any other
  package, and a missing logger degrades to `slog.Default()` rather than
  panicking on a production path. The node logger is bound into the group
  context in `start()`, so every loop resolves it instead of remembering.
- **`streamLimiter`: bounded inbound concurrency.** A counting semaphore
  (capacity `maxConcurrentStreams` = 256) on the accept path. Over-budget
  streams are closed and logged at warn — shed fast, stay honest — instead of
  growing the process.
- **`ctx` threaded first through the whole data path** (AGENTS.md §1), with
  `getOrCreatePeer(ctx, …)` now actually using the `ctx` it was handed for
  `DialContext`/`HandshakeContext` rather than the struct field.
- **Credential logging removed.** `apex.go` logs
  `fingerprint_sha256` (first 8 bytes of SHA-256) instead of the token. An
  operator can identify a token; an attacker cannot replay a fingerprint.
- **Apex packages moved to `slog`.** `consensus` and `hive` took
  `WithLogger(lg)` (defaulting to `slog.Default()`) and replaced
  `fmt.Printf` with leveled structured calls. The consensus quarantine line
  was previously unsuppressible and carried no fields, so a quarantine sweep
  was a substring match; it is now `target_node=` / `term=` / `votes=`.
- **Concurrency budget proven.** `streamLimiter` exercised by 64 goroutines ×
  100 acquire/release under a mutex-guarded peak counter that asserts the
  budget never over-admits; `maxConcurrentStreams` asserted ≥ 1.

### Two defects this generation's own tests caught

- **`handshakeTimeout == drainGrace` (5s/5s).** A wedged handshake could
  consume the *entire* drain budget, leaving every other loop un-drained. The
  comment I had written claimed the opposite relationship, so the test
  disagreed with the prose. Fixed by dropping `handshakeTimeout` to 2s and
  making the ordering an assertion.
- **Inverted assertion.** My first `TestConstantBudgetInvariants` checked
  `handshakeTimeout <= drainGrace` — the wrong direction — so it failed
  against the *correct* constant. The test was wrong, not the code; the
  assertion now reads `handshakeTimeout >= drainGrace` → fail.

Both are recorded because a ledger that only shows passes is not a ledger.

### Fitness score

| check | result |
|---|---|
| `go build ./...` + `-tags sim` | PASS |
| `go vet ./...` | PASS |
| `gofmt -l cmd internal` | clean |
| `go test ./...` (15 suites) | PASS |
| `go test ./cmd/fabric/ -count=1` | PASS (0.22s) |
| live 4-node smoke + SIGTERM | PASS, no drain timeout |

**Concurrency evidence.** `go test -race` still cannot run here (no C
toolchain in this Flatpak runtime; no package manager to install one) — an
**unmet requirement, not a pass**, and the sole deduction. Substitute: the
generation-1 leak proof plus this generation's contention test
(`TestStreamLimiterIsRaceFreeUnderContention`, 6,400 acquire/release pairs
from 64 goroutines, peak asserted ≤ capacity) and `TestAcceptRefusesBeyondStreamBudget`.
The contention test is written specifically so that running it under `-race`
on a capable machine is the highest-value single command for this package.

**Fitness Score: 9/10** — one point held back for the `-race` gap, which is
environmental and cannot be closed from inside this session.

**Next mutation (Gen 3), contingent on the sensor returning:** with
`node_id`, `peer`, `loss`, and `target_node` now queryable, the cross-
pollination target is the `auth_fail` vs `probes` relationship — a node whose
auth failures climb while probes stall is rotating its hourly key against a
peer that has not, and that is now a two-field query rather than a guess.

---

## Generation 1 — errgroup lifecycle + structured slog in the fabric node

### 1. Sensed environment (AGENTS.md §2.1)

Sought the designated sensor first: `world-report/logs/`.

```
$ ls world-report/logs/     → does not exist
$ ls logs/                  → empty (0 files)
$ pgrep -af hivemind|...    → no mesh processes
```

**Anomaly:** the sensor itself is dead. `world-report/logs/` is gitignored
(`.gitignore:15,29`) and `logs/` (`gitignored:3`) holds nothing because the
`make stack` fleet was torn down. There are no per-continent node logs
(`an-master-mcmurdo.log` et al.) to diff, so no capacity-variant or
connection-drop mutation can be cross-pollinated this generation.

**Adaptive consequence:** with no live temperature signal, the correct
mutation is defensive. A fabric node cannot learn latency and drop rates from
an absent observer, so the priority shifts to what the node owes its own
process when the world goes away: it must stop cleanly, leave no goroutine
behind, and never block a cancellation. That is precisely the errgroup +
contextual-logging mandate, and it is the one thing that does not depend on
sensor data. So Generation 1 spends its whole budget there.

**Latent anomaly found by reading the code instead of the logs** — the
strongest argument for the refactor, and a genuine correctness bug that the
empty sensor let us notice:

`cmd/fabric/main.go` spawns nine bare `go` statements with no supervisor:

| site | goroutine | leak risk |
|---|---|---|
| `main.go:883-884` | `writeLoop`, `readLoop` per peer | peer-scoped |
| `main.go:1305-1306` | `writeLoop`, `readLoop` per peer (inbound) | peer-scoped |
| `main.go:1248` | accept loop, bare `for { ln.Accept() }` | node-scoped |
| `main.go:1260` | `serveConn` per connection | unbounded |
| `main.go:1330` | `preconnect` dial storm | node-scoped |
| `main.go:1477-1479` | `keyRotator`, `metricsLoop`, `trafficLoop` | node-scoped |

Consequences, all of them invisible until the mesh returns:

1. **No cancellation propagation.** The loops select on `f.ctx.Done()`, but
   `writeLoop`/`readLoop` return silently; nothing aggregates their exit, so
   `main` cannot know the node drained. On `SIGTERM`, `shutdown()` cancels
   and exits with peers mid-write.
2. **`time.After` in a hot loop.** `keyRotator` allocates a fresh timer every
   minute; `metricsLoop`/`trafficLoop` are fine (ticker) but the accept loop
   burns CPU on `continue` when `ln.Accept()` returns a non-shutdown error.
   `sim_dev.go:115` also spawns an unmanaged goroutine.
3. **A dying peer leaves its `sendCh` producer stranded.** `sendOn`
   (`main.go:967`) selects on `p.sendCh`/`p.closed`/`f.ctx.Done()`, so it is
   safe, but `readLoop`'s deferred `p.close()` + `dropPeer` race
   `writeLoop`'s `f.dropPeer` — two goroutines racing `dropPeer` for the same
   peer, resolved only by the map's own `if ok` check.
4. **No structured telemetry.** 20+ `log.Printf` calls format node identity
   into strings (`[link] ↑ c2.t1`), so the log cannot be filtered or joined
   on `node_id`/`peer`/`loss`. A `connection refused` sweep across continents
   — the exact anomaly gen 11 spent itself on — is impossible today.

### 2. Mutation applied

- **errgroup supervisor.** `fabric` gains a `*errgroup.Group` and a `spawn`
  helper. `listen` now returns the accept loop as an error-returning func;
  `main` runs `keyRotator`, `metricsLoop`, `trafficLoop`, and `acceptLoop`
  under `errgroup.WithContext`, so the first error cancels the siblings and
  `g.Wait()` is the node's real drain barrier. Peer `writeLoop`/`readLoop` move
  onto the same group with a peer-scoped context, closing the
  `readLoop`/`writeLoop` `dropPeer` race and making peer teardown ordered.
- **`time.After` → ticker.** `keyRotator` uses `time.NewTicker` with a
  deferred `Stop`, killing the per-minute timer allocation.
- **slog everywhere.** Every `log.Printf` becomes a contextual
  `slog.InfoContext`/`WarnContext`/`ErrorContext` call carrying structured
  `slog.Attr` pairs: `node_id`, `peer`, `weight`, `loss`, `latency_ms`,
  `error`. The node builds one base `*slog.Logger` in `main` and threads it
  through `fabric`, so every line is attributable to a node without regex.
- **`log.Fatal` → returned error.** `main` becomes `run() error`; fatal paths
  return and the top level logs once with `slog.Error` and exits.
- **`slog.Level` control.** `FABRIC_LOG_LEVEL` selects the default level; a
  silent sensor should not mean a silent node.

### 3. Fitness score (Generation 1)

**Measured:**

| check | result |
|---|---|
| `go build ./cmd/fabric/` | PASS |
| `go build -tags sim ./cmd/fabric/` (dev matrix build) | PASS |
| `go vet ./...` (whole repo) | PASS |
| `gofmt -l cmd/fabric internal` | clean |
| `go test ./cmd/fabric/ -count=1` | PASS (0.21s) |
| `go test ./...` (whole repo, 15 suites) | PASS (engine 42.4s, gaze 48.0s) |
| `-self-test` (pack/seal/softmax/mTLS) | PASS |
| live 2-node smoke, SIGTERM drain | PASS, no leak warning |

**Goroutine spawn sites: 9 bare `go` → 1 supervised group.**
`writeLoop`/`readLoop`/`serveConn`/`acceptLoop`/`preconnect`/`keyRotator`/
`metricsLoop`/`trafficLoop` are all `f.spawn` members now; the only remaining
bare `go` is the drain watcher inside `shutdown`, which is bounded by a 5s
timeout and cannot outlive the process.

**Concurrency evidence — and its one gap, stated plainly.**

`go test -race` **could not run in this environment.** The race detector
requires a C toolchain; this box is a Flatpak runtime (`Freedesktop SDK
25.08`) with no `gcc`, `clang`, or `tcc` on `PATH` and no package manager
(`apt-get`, `pacman`, `apk`, `dnf` all absent) to install one. AGENTS.md
requires `-race` on every change, so this is an **unmet requirement, not a
pass**, and it is the one deduction below.

Substitute proof, since `-race` was unavailable: `TestLifecycleDrainsWithoutLeaks`
starts a real node, makes it bind, accept an inbound mTLS peer, and fail an
outbound dial, then tears it down and asserts the goroutine count returns to
its **exact** pre-start baseline. A leak is a fact about goroutine lifetime,
so this catches the regression class the refactor targets without needing a
race detector.

The proof was itself mutation-tested, because a test that cannot fail is not
evidence:
- Sabotaging `keyRotator` to ignore `ctx.Done()` → **FAIL** (`got 4, want <= 2`).
- Removing only the drain *wait* → still passes, correctly: cancellation
  already frees the goroutines, so the wait is a barrier, not the mechanism.
- The bound was first written as `settle+2`, which **hid the single leaked
  loop** (got 4 vs want 4). Tightened to exact baseline. A loose tolerance in
  a leak test is a test that silently tests nothing.

**Bug this generation caught in its own mutation:** `nextBackoff` tested the
ceiling *before* doubling, so 1.28s → 2.56s escaped the 2s clamp —
`TestNextBackoffIsBounded` failed on first run and the clamp was moved after
the doubling. Introduced and caught inside one generation; recorded here
because the ledger is only useful if it also records the failures.

**Anomaly → adaptation map (AGENTS.md §2.2):**

| sensed | adaptation |
|---|---|
| sensor absent (`world-report/logs/` missing, `logs/` empty) | defensive mutation; priority shifted to clean shutdown, which needs no live telemetry |
| `connection refused` appears as an unstructured string | `preconnect`/`acceptLoop` emit `slog.Any("error", err)` + `peer` attr → sweep is `grep 'peer=c2.t1'`, not a regex |
| node identity baked into message text (`[link] ↑ c2.t1`) | `node_id` is a base attribute on every line, so a 60-node join is possible |
| silent sensor should not mean silent node | `FABRIC_LOG_LEVEL` knob added alongside `slog` |

**Fitness Score (Generation 1): 9/10.**

Full marks withheld for the one thing that could not be executed: `-race`.
Everything else — build (both tags), vet, fmt, whole-repo tests, self-test,
live drain, and a mutation-tested leak proof — is green.

**Next mutation (Gen 2), contingent on the sensor returning:** the
`peer.tel` field is assigned in `getOrCreatePeer`/`serveConn` but the inbound
`serveConn` path never called `recordPeerAdded()` before this change (it did
not, and now does). With live logs, the next cross-pollination target is the
`AUTH_FAILURE` rate: a node whose `auth_fail` climbs while `probes` stalls is
rotating its hourly key against a peer that has not, which the new structured
fields make a one-line query.