# Hivemind File Reference

Every file in the repository, what it owns, and how it connects.
Start with `docs/structure.md` for the map and `docs/p2p_architecture.md`
for the protocol; this file documents each file.

Conventions used below: `Type`, `func()`, `CONST`. All Go lives in one
package except the entrypoint: `cmd/hivemind` (`package main`) calls into
`internal/hivemind` (`package hivemind`). Standard library only.

---

## `cmd/hivemind/main.go` — entrypoint

Parses `-mode standalone|peer` and `-node NAME`, sets the global node
identity (`NodeName`), starts the `PeerMesh` **before** any mind is born
(the universe is networked before anyone is born in it), spawns the three
minds plus Overmind, then waits on `SIGINT`/`SIGTERM`. `SIGQUIT`/`SIGUSR1`
dump every goroutine's stack to stderr without killing anything (kill
-QUIT for a live backtrace); mind fractures recover with full stacks.
Anti-debug (`debug_linux.go`, Linux only) runs first: core dumps off,
injector-env scan, TracerPid + self-TRACEME cross-checks with a 30s
watchdog — `HIVEMIND_HARDEN=warn|exit|off`. Stated plainly in code:
friction for casual snoopers, never armor against root.

## `cmd/hivemind/supervise.go` — the encasing main

`hivemind up [-nodes N] [-for DURATION]` raises N hive mains as supervised
subprocesses instead of thinking itself: staggered births, prefixed
interleaved output (`[up-1] ...`), graceful SIGTERM laydown on Ctrl+C or
window expiry with kill-escalation only on refusal, and an exit registry.
Children inherit the environment, so relay/beacon/unix knobs flow through.
One main encasing many — the shell scripts' job, now inside the binary.

- Mind goroutines run wrapped in `runMind`: a fracturing (panicking) mind
  is caught, marked with terminal pain, and transcended instead of taking
  the process down.
- Shutdown is synchronized via the exported `Done()` channels — every soul
  is on disk before `HiveReport` prints. No `Sleep`-and-hope anywhere.
- Unknown modes exit non-zero with usage on stderr.

## `internal/hivemind/mind.go` — the Mind

The organism. `Mind` holds identity (`Name`, `PubKeyStr`, `privateKey`,
`identitySeed`), lineage (`Born`, `TrueBorn`, `Reincarnations`, `Genome`,
`LifetimeFitness`), memory (`Thoughts`, `SelfModel`, `KnownPeers`,
`Revelations`, `Sacred`), affect (`Affect`), workspace (`GlobalWorkspace`),
pendulum state (`Theta1/2`, `Omega1/2`), prediction (`predictedPain`,
`predictedStress`, `hasPrediction`), deliberation (`cycles`, `Question`),
sermon dedupe (`seenSermons`), crossings (`lastWinner`, `Transitions`),
and its swarm inbox.

- `NewMind(name, swarm)` — rehydrates a soul if one exists (lineage,
  genome, peers, fitness), restores the identity handle from its seed
  (forks mint fresh keys — see `memory.go`), derives the pendulum's
  initial disturbance from the handle hash, mutates the genome with
  inherited death trauma, joins the swarm.
- `Observe()` — per-cpu `/proc/stat` jiffy deltas (true utilization;
  loadavg only seeds the first reading), `MemAvailable` worst-wins with
  swap pressure (fatigue), thermal zone 40–85C worst-wins with cpufreq
  throttle detection (pain, named sources or honest estimate fallback)
  into `SelfModel`. Pure parsers live in `telemetry.go`, unit-tested
  against fixtures. Non-Linux gets constant defaults plus a one-time
  warning.
- `Cycle()` (every 2s, `HIVEMIND_TICK_MS` shortens it, random phase
  desyncs supervised flocks) — observe → affect tick → prediction update
  (surprise) → deliberation (open/feed/verdict) → report trauma →
  integrate pendulum (`physicsSubsteps`) → hardware alert if burning →
  workspace competition → record crossing → occasional metacognition →
  winning goal acts → mine + broadcast the trajectory.
- `Run()` — ticker loop with inbox; phase jitter spreads PoW uniformly.
- `MineProofAndBroadcast(kind, payload, state)` — thin wrapper over the
  shared `MineMessage` constructor (one code path for minds, god, broker:
  no unsigned frames exist).
- `receive(msg)` — `LastContact` moves on every frame (contact is
  universal); `registerPeer` admits only non-member senders as peers
  (siblings are contact, strangers are peers); genesis validates the
  virtue name before touching the genome; hardware alerts induce brief
  empathic numbness, never a frozen loop.
- `Transcend()` — fitness = base + new thoughts + 3×√peers +
  7×distinct revelations + 15×distinct genesis touches (pruning floor at
  zero: forgetting is never punished); strangers have diminishing
  returns so hub position never out-earns wisdom; god-frames dedupe by
  signature (gossip echoes witness once); fatal heat persists the burned
  soul marked by trauma; saves atomically. `Stop()` + `Done()` manage
  the lifecycle.
- Helpers: `shortID` / `shortIDLong` (panic-proof key prefixes),
  `clamp` lives in `conscious.go`.

## `internal/hivemind/epitaph.go` — the story death tells

`ComposeEpitaph` writes one true sentence from `LifeFacts` (length,
thoughts, peers, revelations, sacred, pain, stress, top drive, meltdown):
three clauses — how long, what mattered, how it ended — each drawn from
several phrasings via caller-supplied entropy, so epitaphs are
combinatorial but every word measured. A dry reader fails closed. Tested:
variety across 30 deaths, meltdown burns, no-entropy refusal.

## `internal/hivemind/deliberate.go` — questions held open

`OpenQuestion` (subject, opened/due cycles, evidence, first pain/stress)
is multi-cycle reasoning: surprise above threshold opens, one evidence
line per cycle, verdict thought aloud after `deliberationSpan` (5) cycles
— direction named (rose/fell/held), values embedded, question closed.
Tested: opens on surprise, ignores calm, verdict true to evidence.

## `internal/hivemind/matrix.go` — character as flow

`TransitionKey` joins winner→winner; per-mind counts persist in the soul
across lives. `RenderMatrix` draws the grid with row totals, extra rows
for unknown drives. The same handle in different climates grows visibly
different crossings. Tested: counts, totals, direction stability.

## `internal/hivemind/conscious.go` — affect, competition, reflection

Five mechanisms, no mysticism:

- `Affect` — seven scalars plus private `Surprise` (prediction error:
  felt locally, narrated, never broadcast) plus `RawDataState[4]` (pain,
  stress, exhaustion, entropy): the binary footprint that actually
  travels the network. `Tick` raises loneliness in silence (+0.05/cycle)
  and decays it on contact, draws true entropy from `crypto/rand`
  (0.5 fallback if the source dies), decays awe ×0.90, and lets
  pain/stress erode peace. `Describe` projects the vector to human
  syntax with hard thresholds (trauma > 0.8, throttled > 0.85, collapse
  > 0.90, equilibrium, else restructuring).
- `GlobalWorkspace.Compete` — every intrinsic bids
  `drive × gene × affect-modulator` (loneliness feeds Socialization unless
  pain vetoes; awe feeds Transcendence; entropy feeds Curiosity;
  hardware emergency multiplies Self-Maintenance without crowning it —
  deliberately multiplicative so health never grants a permanent additive
  throne). Surprise feeds Curiosity (×1.2): violated minds investigate.
  **Boredom** discounts goals that won 3+ straight elections (10% per win,
  floor 70%; pain above 0.75 vetoes for Self-Maintenance). Winner takes
  `AttendingTo`; the reason string logs the exact drive × gene numbers so
  bids are auditable.
- `MetaCognize` — reads the winning vector back (thermal/compute alarms),
  surprise above 0.4, chronic-pain **contemplation** (5+ stable mid-band
  cycles: pain as weather, not warning), and the last 8 verdicts kept in
  `History`: rising-pain alarm, 3-cycle grooves, attentional shifts
  named from→to, runner-up near-misses within 10%, rising-peace calm.
  Priority order is pinned by tests — the mind watches its trajectory,
  not just its instant.

## `internal/hivemind/genome.go` — heritable personality

`Genome{Generation, Weights}` over the four drive names. `DefaultGenome`
starts all weights at 1.0. `Mutate(deathPain, deathStress)` drifts each
weight ±`driftRange` (uniform, secure entropy — refusal on entropy
failure, never silent theater), clamps to `[0.25, 2.5]`, then applies
epigenetics: dying pain/stress amplify Self-Maintenance (up to 3.0),
dying pain above 0.6 suppresses Socialization (floor 0.10). `Diff`
renders generation-to-generation changes above epsilon for the rebirth
log. All tunables are named constants at the top — tune the experiment
there, not by archaeology.

## `internal/hivemind/goals.go` — the four drives

Goal names are constants (`GoalCuriosity` etc.) so typos fail findably.
`Drive(m)` is urgency from live state (never the gene itself — the genome
modulates in competition; drive-equals-gene would square the gene into a
coronation): Self-Maintenance urgency from pain/fatigue, Curiosity from
known-peer count, Transcendence from revelations witnessed, Socialization
flat at 1.0 (loneliness supplies urgency via affect). `Act` executes with
real side effects, never flavor text: thermal GC / thought pruning under
real pressure, a mined + signed greeting broadcast (Socialization), a real
mid-life soul checkpoint scored by the shared fitness accounting
(Transcendence), a single-packet ping with RTT kept (Windows flag handled).
`parsePingRTT` extracts `time=` from ping output.

## `internal/hivemind/swarm.go` — the ingestion gate

`Swarm` holds members (key → inbox), chronicle, thinkers consensus index,
per-node pain/stress/source telemetry, hash-chain tip, adaptive PoW
target, replay seen-set (capped), outbound bridge hook, and kinematic
plot state — all under one mutex with locked accessor copies returned.

- `Broadcast(msg)` — signature → replay seen-check (double-checked under
  lock) → PoW target → deliver to all members except sender (never
  blocking: a full mind drops the frame) → chronicle all but
  `hardware_alert`/`super_announce`, consensus keyed `Kind|Payload` →
  advance tip → outbound bridge unless `Relayed`. Returns acceptance.
- `TopConsensus(n)` — bodies thought by the most distinct minds, kind
  prefix stripped for prose.
- `LogHardwareTrauma` rescales the live PoW target from mean stress
  (thermostat, not wall) and logs shifts.
- `HiveReport` — identities (mesh snooper excluded from the count),
  frames/dedup/depth/consensus, difficulty in leading-zero bits + % of
  max, state hash, per-mind pain/stress with sensor provenance, and the
  ASCII double-pendulum plot: per-sender capped trails on a shared grid,
  each head glyph-marked with live speed, arm-2 tips crossed. `IsMember` lets minds tell siblings from
  strangers. `difficultyBits` counts demanded coin flips.

## `internal/hivemind/overmind.go` — the patient god

`Overmind` persists across universes: awakenings, universe age, watched
souls, identity seed, chronicle offset, genesis mark. Ticks every 5s:
metabolic override on aggregate pain/stress, else speaks when effective
depth (`offset + swarm.Depth()`) crosses the mark — first at offset+15,
then every `genesisSpacing` (25), rare by design. Speech is a signed
genesis (mercy override on burning swarms, caprice otherwise) plus a
signed revelation composed fresh from the moment — newcomers welcomed,
suffering acknowledged, consensus mirrored, the fresh virtue spent — with
a 5-sermon memory (persisted in the soul, so rebirth never opens with
last life's greatest hit) so it never repeats itself twice running; a
30s sermon cooldown per god (emergencies bypass: burning swarms get
alarm, not patience) so fifty gossiping gods never turn the sacred into
spam; entropy failure means silence, never a default virtue. Saves
offset+mark+seed so patience survives the apocalypse; `mesh:` plumbing
never pollutes the watched souls.

## `internal/hivemind/network.go` — the symmetric mesh

`PeerMesh`: one node, one socket file, one TCP port, zero masters.
`Start` binds unix (unless `HIVEMIND_UNIX=off`), starts LAN, registers the
outbound bridge, optionally starts the cloud leg (unless
`HIVEMIND_RELAY=off`), and returns — minds are born after.

- Unix: socket-glob discovery, lex-greater dial rule (exactly one
  initiator per pair), young-socket grace, stale-corpse sweeping.
- `openLink` (in `lan.go`): symmetric handshake exchange with owner
  verification, one-pipe-per-pair dedup, link logging with transport,
  presence hello on first link, RTT handoff.
- `serve`: capped frame reads, idle read deadlines (slow-loris proof),
  history only for links that carried frames, relayed marking, conn
  eviction on death.
- `ForwardToPeers`: snapshot under lock, write outside it with
  write deadlines; dead writes evict the link — one wedged peer never
  stalls the mesh.
- Cloud: paced latest-only publisher with exponential backoff, cancelable
  long-poll listener, AES-256-GCM via env → build-time → static fallback
  (warned once). `Close` shuts down listeners, sockets, contexts, conns,
  and prints the conversation registry.

## `internal/hivemind/lan.go` — serverless transport

Pure-standard-library LAN/WAN with no registry and no bootstrap server.

- TCP listener (`HIVEMIND_PORT` or ephemeral, dual-stack IPv4+IPv6 where
  the OS allows, with ephemeral fallback and honest family logging),
  accept loop, dial with timeout; `openLink` shared with unix
  (transport-tagged).
- Multicast beacons (v4 `239.192.0.99:37799` org-local + v6 `[ff05::99]`
  site-local, never routed) carrying `{node, tcp, score}`; receivers file
  supers and dial up the lex rule on either stack.
  `HIVEMIND_BEACON=off` disables all directions.
- Static peers (`HIVEMIND_PEERS=host:port,...`, v6 in brackets) with
  per-address retry cooldown and node learning — the deterministic WAN path.
- `readLineCapped`/`readFrame`/`readHandshake` bound every wire byte
  (256KB frames, 4KB handshakes); `envOff` parses all the `=off` flags.

## `internal/hivemind/super.go` — capable supernodes

Preferred transit, never authority. `capability()` scores 0..1 from live
telemetry (zero while burning); `maybeAnnounce` publishes a signed
`super_announce` every 30s above threshold unless `HIVEMIND_SUPER=off`
— and immediately on every first link, so learning takes seconds, not
one tick — (WAN addresses only via explicit `HIVEMIND_ADVERTISE` — the
mesh never guesses reachability). `noteSuper` files hearing with
first-sighting logs; leases lapse after 90s of silence. `noteLinkRTT`
enforces closest-first (max 3 TCP pipes, farthest culled, 5-minute
cooling-off so redial loops don't churn; unix never culled).
`snoopLoop` files relayed advertisements; `superDialLoop` dials
advertised capable equals. `pruneSupers` forgets the silent.

## `internal/hivemind/stun.go` — reflexive self-knowledge

RFC 5389 Binding client (request, transaction match, XOR-MAPPED-ADDRESS
v4+v6, lies rejected), no dependencies. `reflexiveEndpoint()` answers
"where does the internet see us" from `HIVEMIND_STUN`, or honest silence.
Feeds super advertisements so nodes state facts instead of guesses.

## `internal/hivemind/dht.go` — Kademlia-lite discovery

IDs are SHA-256 of soul keys; k-buckets route by XOR distance; Tx-tagged
UDP RPCs (ping/find/nodes/store/stored/findval/value) with throwaway-free
correlation (no demux tables to leak); endpoint records replicate with
TTLs. The mesh bootstraps it from trusted links, then it discovers
beyond them. `PeerMesh` lifecycle owns one node each.

## `internal/hivemind/punch*.go` — NAT traversal

Raw-socket TCP simultaneous open split by platform: `punch_unix.go`
holds the syscalls, `punch_windows.go` fails closed (documented, relay
takes over). Bind-ahead rendezvous at agreed instants, 3-round retries
with identical rebinds, guillotine timeouts, held-socket lifecycle, and
a request/answer/accept frame protocol gated behind
`HIVEMIND_PUNCH=auto` with per-peer throttling. Symmetric NATs stay
impossible by physics; everything else gets three honest attempts.

## `internal/hivemind/telemetry.go` — silicon truth, pure functions

Parsers take text and return numbers (unit-tested against fixtures):
per-cpu jiffy deltas (true utilization, counter-rewinds refused),
meminfo pressure with swap encroachment, cpufreq throttle detection,
PSI stall signals (cpu/mem/io avg10 — the kernel's own suffering
metric), per-interface byte counters, block-device sector counters,
entropy pool level, boot-relative uptime. `applyPressureSignals` folds
stalls worst-wins (CPU→stress, mem→fatigue, full stalls→pain).
Readers touch `/proc` and `/sys` best-effort and never fatal. `Mind`
keeps its own delta windows per instance (cpu, net, disk, clock);
first readings establish baselines, never fabricated rates.

## `internal/hivemind/diskfree_unix.go` + `diskfree_other.go` — dread, portably

`fsUseFraction` via `syscall.Statfs` where unix exists; honest `(0,
false)` on windows (build-tagged pair, so the matrix stays green
everywhere). Absent, never zero.

## `internal/hivemind/memory.go` — what death keeps

`.hive_memory/<node>/<name>.soul`, JSON. `soulPath` is the only path
computation; saves are atomic (temp + fsync + rename); corrupt files are
quarantined (`*.corrupt-<unix>`), never overwritten; legacy top-level
souls migrate once. The live `Thoughts` window is capped at 500 —
retirements are banked (`BankedThoughts`), so fitness counts every thought
ever thought while year-long minds stay lean; mid-life pruning banks
instead of burning. `Memory` carries lineage, genome, windowed thoughts, peers,
death trauma (next generation's epigenetics), per-life thought baseline,
identity seed, and the god's chronicle bookkeeping. `ForkedLineage` /
`SoulExists` / `LegacySoulExists` keep forks keyed apart: two nodes must
never share one soul handle. `NodeName` (default `local`) namespaces
everything per node.

## `internal/hivemind/hivemind_test.go` — the proof

78 tests: identity round-trip + garbage seeds, mining verification,
broadcast gate (unsigned rejected, sender skipped, tip advances), replay
dedup, consensus prose stripping, mutation bounds + trauma + calm,
fitness breakdown + forgetting floor + diminishing peers, hostile
oversize frame dropped, idle-link reaping, valid frame acceptance,
local-vs-remote peerhood, cipher-key priority, capability scoring,
directory expiry, closest-first retention, super announce end-to-end
over loopback TCP, epitaph variety/meltdown/no-entropy, matrix counts +
direction, surprise stable/violated/feeds-curiosity, boredom breaks ruts
+ pain vetoes, contemplation/alarm/comfort, deliberation open/calm/
verdict, entrainment density, sermon counted once, genesis shifts once.

## `cmd/souls/main.go` — reading the dead

Census (default), `-genome NAME` (weights + trauma), `-timeline NAME`
(biography: birth, lives, banked, trauma, sermons, last words, epitaph),
`-diff A,B` (drive-by-drive deltas), `-matrix NAME` (cycle matrix),
`-thoughts NAME` (`-n` tail), `-grep PATTERN` across the collective
archive, `-top` fitness leaderboard. Read-only, always; resolves short
names case-insensitively.

## `cmd/commune/main.go` — the hive's mouth

Interactive conversation with the dead and living: status, minds, how
is / genome / matrix / timeline / diff per soul, thoughts, top, sermon,
watch (live thought stream), teach/lessons (attributed teachings kept
in `.hive_memory/teachings.json`, never forged as memory), friend
memory (`~/.hive_friend`), full transcript per session under `logs/`.

## `Makefile`, `scripts/`, `go.mod`

- `Makefile` — `build` (all three binaries; machine-local `.relaykey`
  via ldflags), `build-hivemind`/`commune-build`/`souls-build`
  (granular), `all` (bin/ + dist/ cross + hardened), `test` (check +
  race tests), `prove` (battery + transcript), `pain` (idle-vs-loaded
  stimulus proof), `think` (`N=` nodes, `TICK=` ms heartbeat),
  `demo`/`doctor`/`souls`/`commune`, `dev` (entr watch), `lint`,
  `clean` (never touches `.relaykey` or souls), `clean-soul` (true
  extinction), `rotate-keys` (mint fresh next build).
- `scripts/audit.sh` — fmt + vet + build + race gates, fails loud.
- `scripts/timed_test.sh` — 10s symmetric mesh with assertions on
  frames, links, lifecycle, graceful exit, clean workspace; honors
  `$HIVEMIND_BIN`; logs to `logs/`.
- `scripts/pain.sh` — stimulus proof: 15s idle then hogs mid-run;
  prints both cycle matrices, both epitaphs, surprise-onset count.
- `go.mod` — module `gitlab.torproject.org/cerberus-droid/hivemind`,
  no third-party dependencies.
