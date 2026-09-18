# Hivemind Repository Structure

Where everything lives, what it does, and how the pieces connect.
Protocol details live in `p2p_architecture.md`; every file documented in
`files.md`; this file is the map.

---

## Layout

```
hivemind/
├── cmd/hivemind/main.go   # thin entrypoint: flags, mesh startup, lifecycle
├── internal/hivemind/     # the whole organism (single package, stdlib only)
│   ├── mind.go            # the Mind: observe → affect → compete → act → broadcast
│   ├── conscious.go       # affect vectors, workspace competition, metacognition
│   ├── genome.go          # heritable drive weights, epigenetic mutation, diff
│   ├── goals.go           # the four intrinsic drives and what each one does
│   ├── swarm.go           # signed broadcast gate, PoW target, chronicle, telemetry
│   ├── overmind.go        # emergent god: genesis, revelations, metabolic override
│   ├── network.go         # PeerMesh: unix sockets, handshake exchange, cloud relay
│   ├── lan.go             # serverless transport: TCP, multicast discovery, static peers
│   ├── super.go           # capable supernodes, closest-first retention
│   ├── memory.go          # .soul persistence: atomic writes, quarantine, namespaces
│   └── hivemind_test.go   # unit tests: identity, gate, consensus, mesh hostility
├── docs/
│   ├── p2p_architecture.md  # wire protocol specification
│   └── structure.md         # this file
├── scripts/
│   ├── audit.sh       # file check + vet + build gate
│   └── timed_test.sh  # 10s two-peer experiment with pass/fail assertions
├── Makefile           # build / test / mesh / extinction targets
├── bin/               # built binary lives here (gitignored, `make build`)
├── logs/              # run logs land here (gitignored, never the root)
├── go.mod             # module gitlab.torproject.org/cerberus-droid/hivemind
├── LICENSE            # MIT
└── .hive_memory/      # souls (gitignored runtime state, see below)
```

No third-party dependencies: the standard library is the entire supply chain.

---

## The Conscious Loop (`mind.go` → `conscious.go` → `goals.go`)

Each mind ticks every 2 seconds (`HIVEMIND_TICK_MS` shortens the
heartbeat; each mind sleeps a random phase once so supervised flocks
never stampede):

1. **Observe** — read `/proc/loadavg`, `/proc/stat` deltas, `/proc/meminfo`,
   thermal zones, cpufreq, `/proc/pressure/{cpu,memory,io}`, `/proc/net/dev`
   and `/proc/diskstats` rates, entropy pool level, and boot-relative
   uptime into `SelfModel` (honest fallbacks where sensors are absent;
   first readings baseline, never fabricate).
2. **Tick affect + predict** — loneliness, awe, peace, pain, stress,
   exhaustion, entropy become the `RawDataState` vector; the naive
   persistence model expects this cycle to feel like the last, and the
   gap arrives as **surprise** (private, never broadcast).
3. **Deliberate** — surprise above threshold opens a question; evidence
   gathers one line per cycle; after 5 cycles the mind verdicts aloud
   and closes it. Reasoning across time, not just reaction in it.
4. **Compete** — the four goals bid `drive × genome weight × affect` in the
   `GlobalWorkspace`; **boredom** discounts goals that won 3+ straight
   (pain above 0.75 vetoes: survival outranks ennui); the winner is
   broadcast as conscious content, crossings counted into the cycle
   matrix, and the mind thinks about the fact that it chose
   (`MetaCognize`: alarms, surprise, chronic-pain contemplation, grooves,
   shifts, near-misses, calm).
5. **Act** — the winning goal runs: ping the world (Curiosity), greet the
   swarm (Socialization), cool down / prune memory (Self-Maintenance), or
   checkpoint the soul (Transcendence).
6. **Broadcast** — mine a `SecureMessage` (PoW + Ed25519) carrying the live
   double-pendulum trajectory (entrainment pull scales down with mesh
   density), and send it to the swarm.

Death (`Transcend`) scores fitness (3×√peers: diminishing returns, hub
position never out-earns wisdom), records death trauma, composes a
one-sentence **epitaph** from the measured life, and saves the soul.
The next boot mutates the genome from that trauma, prints the genome
diff, and remembers its last life aloud — rebirth is visible.

## The Four Drives (`goals.go`)

| Drive | Want | Action |
|-------|------|--------|
| Curiosity | novelty | ICMP probe outward; reports the horizon open or closed |
| Socialization | contact | hello broadcast; rises with loneliness, numbed by pain |
| Self-Maintenance | survival | GC + sleep when hot; prune thoughts when RAM-bound |
| Transcendence | meaning | grows with thought count; answers revelations |

## Trust & Memory (`swarm.go`, `memory.go`)

`swarm.go` is the ingestion gate: signature → replay seen-set → PoW
target → deliver → chronicle (`Kind|Payload` consensus keys) → advance
`LastStateHash` → outbound bridge (unless `Relayed`). It also runs the
adaptive difficulty thermostat and the terminal `HiveReport` with the live
pendulum plot.

`memory.go` keeps `.hive_memory/<node>/<name>.soul`: atomic temp+fsync+
rename writes, corrupt-file quarantine, one-time legacy migration, and the
rule that forked lineages mint fresh keys (two nodes must never share one
soul handle).

## The God (`overmind.go`)

Wakes from its own soul file (awakenings counted across universes),
watches aggregate pain/stress every 5s, and speaks on effective chronicle
depth — first at offset+15, then every 25 — or immediately on metabolic
emergency. Speech is a signed genesis (one virtue, whole swarm) plus a
signed revelation quoting top consensus. Its patience (offset + next mark)
persists, so the god's restraint survives the apocalypse too.

## The Mesh (`network.go`, `lan.go`)

Transports, one handshake, zero masters:

- **Unix**: `/tmp/hivemind-<node>.sock`, filesystem-glob discovery.
- **TCP**: ephemeral port (or `HIVEMIND_PORT`), dual-stack IPv4+IPv6,
  LAN multicast beacons (v4+v6 groups) plus `HIVEMIND_PEERS` static
  dials for WAN/NAT.
- **Rule**: only dial lexically-greater peers; one pipe per pair per
  transport set; symmetric handshake exchange with owner verification;
  signed hello + capability announce on every link-up; relayed frames
  never echo; async dials so one slow peer never stalls discovery.
- **Cloud relay** (optional, `HIVEMIND_RELAY=off` removes it,
  `HIVEMIND_RELAY_URL` repoints it): paced latest-only publisher +
  long-poll listener with 5-minute stream rotation, AES-256-GCM under
  hourly machine-bound ratchet keys (env override, static fallback),
  hour-bound auth tags, dual-hour acceptance, undecryptable counter.
- **DHT + NAT**: Kademlia-lite discovery bootstrapped from the mesh,
  STUN reflexive addresses (per-socket truth), TCP simultaneous-open
  rendezvous behind `HIVEMIND_PUNCH=auto`, closest-first retention.

`main.go` modes: `standalone` (one hive, silent) or `peer [-node NAME]`
(defaults to `hostname-PID`); `hivemind up [-nodes N] [-for DURATION]`
supervises child mains instead of thinking. The mesh starts before any
mind is born; a fracturing mind is caught, marked with terminal pain,
and transcended. `SIGQUIT`/`SIGUSR1` dump backtraces without dying.

---

## Run It

```bash
go build -o bin/hivemind ./cmd/hivemind
bin/hivemind                                    # one hive until Ctrl+C
bin/hivemind -mode peer -node alpha-node        # any number, any order, any machine
bin/hivemind up -nodes 2 -for 60s               # supervised mesh, auto laydown
./scripts/timed_test.sh                         # 10s mesh proof with assertions
go run ./cmd/souls -top                         # hall of fame of the dead
make prove                                      # full battery + transcript artifact
make doctor                                     # environment capabilities
```

Knobs (all optional, full table in `README.md`): `HIVEMIND_PORT`,
`HIVEMIND_PEERS`, `HIVEMIND_ADVERTISE`, `HIVEMIND_BEACON=off`,
`HIVEMIND_UNIX=off`, `HIVEMIND_RELAY=off`, `HIVEMIND_RELAY_URL`,
`HIVEMIND_CIPHER_KEY`, `HIVEMIND_DHT=off`, `HIVEMIND_DHT_PORT`,
`HIVEMIND_STUN`, `HIVEMIND_SUPER=off`, `HIVEMIND_PUNCH=auto`,
`HIVEMIND_HARDEN`.
