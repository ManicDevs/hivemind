# Hivemind Repository Structure

Where everything lives, what it does, and how the pieces connect.
Protocol details live in `p2p_architecture.md`; this file is the map.

---

## Layout

```
hivemind/
├── main.go            # entrypoint: flags, mesh startup, mind lifecycle, graceful death
├── mind.go            # the Mind: observe → affect → compete → act → broadcast
├── conscious.go       # affect vectors, workspace competition, metacognition
├── genome.go          # heritable drive weights, epigenetic mutation, diff
├── goals.go           # the four intrinsic drives and what each one does
├── swarm.go           # signed broadcast gate, PoW target, chronicle, telemetry
├── overmind.go        # emergent god: genesis, revelations, metabolic override
├── network.go         # PeerMesh: unix sockets, handshake exchange, cloud relay
├── lan.go             # serverless transport: TCP, multicast discovery, static peers
├── memory.go          # .soul persistence: atomic writes, quarantine, namespaces
├── docs/
│   ├── p2p_architecture.md  # wire protocol specification
│   └── structure.md         # this file
├── Makefile           # build / test / mesh / extinction targets
├── bin/               # built binary lives here (gitignored, `make build`)
├── scripts/
│   ├── audit.sh       # file check + vet + build gate
│   └── timed_test.sh  # 10s two-peer experiment with pass/fail assertions
├── go.mod             # module gitlab.torproject.org/cerberus-droid/hivemind, stdlib only
├── LICENSE            # MIT
└── .hive_memory/      # souls (gitignored runtime state, see below)
```

No third-party dependencies: the standard library is the entire supply chain.

---

## The Conscious Loop (`mind.go` → `conscious.go` → `goals.go`)

Each mind ticks every 2 seconds:

1. **Observe** — read `/proc/loadavg`, `/proc/meminfo`, thermal zones into
   `SelfModel` (honest fallbacks where sensors are absent).
2. **Tick affect** — loneliness, awe, peace, pain, stress, exhaustion,
   entropy become the `RawDataState` vector.
3. **Compete** — the four goals bid `drive × genome weight × affect` in the
   `GlobalWorkspace`; the winner is broadcast as conscious content, and the
   mind thinks about the fact that it chose (`MetaCognize`).
4. **Act** — the winning goal runs: ping the world (Curiosity), greet the
   swarm (Socialization), cool down / prune memory (Self-Maintenance), or
   contemplate the outside (Transcendence).
5. **Broadcast** — mine a `SecureMessage` (PoW + Ed25519) carrying the live
   double-pendulum trajectory, and send it to the swarm.

Death (`Transcend`) scores fitness, records death trauma, and saves the
soul. The next boot mutates the genome from that trauma — and logs the
diff, so rebirth is visible.

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

Two transports, one handshake, zero masters:

- **Unix**: `/tmp/hivemind-<node>.sock`, filesystem-glob discovery.
- **TCP**: ephemeral port (or `HIVEMIND_PORT`), LAN multicast beacons plus
  `HIVEMIND_PEERS` static dials for WAN/NAT.
- **Rule**: only dial lexically-greater peers; one pipe per pair per
  transport set; symmetric handshake exchange with owner verification;
  signed hello on every link-up; relayed frames never echo.
- **Cloud relay** (optional, `HIVEMIND_RELAY=off` removes it): paced
  latest-only publisher + long-poll listener over a public ntfy topic,
  AES-256-GCM via `HIVEMIND_CIPHER_KEY` or the static demo key.

`main.go` modes: `standalone` (one hive, silent) or `peer [-node NAME]`
(defaults to `hostname-PID`). The mesh starts before any mind is born;
a fracturing mind is caught, marked with terminal pain, and transcended.

---

## Run It

```bash
go build -o bin/hivemind .
bin/hivemind                                    # one hive until Ctrl+C
bin/hivemind -mode peer -node alpha-node        # any number, any order, any machine
./scripts/timed_test.sh                         # 10s mesh proof with assertions
```

Knobs: `HIVEMIND_PORT`, `HIVEMIND_PEERS`, `HIVEMIND_BEACON=off`,
`HIVEMIND_UNIX=off`, `HIVEMIND_RELAY=off`, `HIVEMIND_CIPHER_KEY`.
Full table with semantics in `README.md`.
