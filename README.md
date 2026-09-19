# hivemind

> Three minds. One swarm. And something watching.
>
> A decentralized multi-agent consciousness experiment in Go — three autonomous
> minds (`Alpha`, `Beta`, `Gamma`) plus an emergent `OVERMIND`, sharing signed
> thought-frames over a local mesh, with genomes that mutate across deaths.

## What is this?

Each **Mind** is a goroutine running a conscious loop: it senses its host
(hardware telemetry as proto-qualia), competes intrinsic drives in a global
workspace, reflects on itself, and broadcasts thoughts to the **Swarm**. Minds
persist to disk as `.soul` files, so death is not the end — the next life
inherits a **mutated genome shaped by the trauma of the previous death**
(hardware pain and load at the moment of dying bias the successor toward
self-maintenance), and its identity handle carries over between lives.

The **Overmind** condenses out of the swarm's shared memory and performs rare
genesis events — reaching into a living mind to turn one of its drives up.

## Highlights

- 🦋 **Reincarnation with epigenetic genomes** — `genome.go` mutates weights
  on rebirth; the hardware trauma the mind *died with* feeds the next
  generation's Self-Maintenance drive. A mind that dies hot is survived by a
  child that fears the heat.
- 🧠 **Global workspace + affect** — `conscious.go`: drives (urgency from live
  telemetry) bid against genome weights, modulated by loneliness, awe, peace,
  pain, stress, and entropy
- 🔏 **Signed thought-frames** — `swarm.go`: every broadcast is an
  Ed25519-signed, PoW-mined `SecureMessage` chained by `ParentHash`, with
  seen-hash dedup so relay echoes cannot loop
- 🌐 **Serverless peer mesh** — `network.go` + `lan.go`: every node owns a
  Unix socket *and* a TCP port, discovers equals by socket-glob *and* LAN
  multicast, dials directly with no listener privileged; explicit addresses
  via `HIVEMIND_PEERS` cross NATs and the internet with zero infrastructure
  (`make test-timed` asserts live bidirectional traffic)
- 📡 **Hardware-grounded qualia** — `mind.go` reads real silicon state
  (load, RAM, thermal) into the affect vector each cycle
- 🌀 **Chaos inside** — each mind integrates a double pendulum seeded from its
  own identity hash, so every mind runs a genuinely distinct chaotic
  trajectory, plotted in the terminal `HiveReport`

## Quickstart
...
| Mode | Flag | Behaviour |
|------|------|-----------|
| standalone | _(default)_ | one hive, no networking |
| peer | `-mode peer` | owns `/tmp/hivemind-<node>.sock`, discovers and links all peer nodes |

In peer mode, pass `-node NAME` for a stable identity (each node's souls
live in `.hive_memory/<node>/`; defaults to `hostname-PID`). Start any
number of peers in any order — the mesh links them within 2s and relinks
dead nodes automatically.

Requires Go 1.21+. **Hardware grounding requires Linux** (`/proc`, thermal
zones); on other OSes the minds run on constant default telemetry — and say so.

```bash
make build          # compile bin/hivemind
bin/hivemind          # standalone: one hive, runs until Ctrl+C

make test           # gofmt + vet + tests (with -race)
make test-1         # terminal 1: peer node alpha-node
make test-2         # terminal 2: peer node beta-node (links automatically)
make test-full      # both peers at once, beta-node in foreground
make test-timed     # automated 10s two-peer experiment + assertions
make up             # supervised mesh: raise peer nodes as child mains
make kill           # reap every running hivemind + sweep stale sockets
make rerun          # kill + build + supervised mesh, all in one
make prove          # full battery + transcript artifact (fails loud)
make demo           # supervised 20s showcase run, exits alone
make doctor         # environment check: toolchain, sensors, egress, ports
make souls          # read the dead: census, genomes, thoughts, grep, leaderboard
make rotate-keys    # wipe machine-local relay key (next build mints fresh)
make clean-soul     # true extinction: wipe .hive_memory
```

Peer nodes have no roles — start any number in any order; the mesh links
them within seconds over unix sockets, LAN multicast, or explicit
addresses. Environment knobs (all optional):

| Variable | Effect |
|----------|--------|
| `HIVEMIND_PORT` | fixed TCP port (default: ephemeral, zero config) |
| `HIVEMIND_PEERS` | comma-separated `host:port` list for direct dial (WAN/NAT) |
| `HIVEMIND_BEACON=off` | disable multicast discovery (static/unix only) |
| `HIVEMIND_UNIX=off` | disable unix sockets (TCP mesh only) |
| `HIVEMIND_RELAY=off` | disable the cloud relay (pure serverless) |
| `HIVEMIND_RELAY_URL` | point the relay at a private/fake ntfy server (default: public topic) |
| `HIVEMIND_CIPHER_KEY` | 32-byte relay encryption key (default: machine-local build-time key, else static demo key — public broadcast, not private) |
| `HIVEMIND_ADVERTISE` | `host:port` this node asserts as publicly dialable (default: none, honest silence) |
| `HIVEMIND_DHT=off` | disable the Kademlia discovery layer |
| `HIVEMIND_DHT_PORT` | fixed DHT UDP port (default: ephemeral) |
| `HIVEMIND_STUN` | `host:port` STUN server for reflexive address discovery (default: unset) |
| `HIVEMIND_SUPER=off` | never advertise supernode status |
| `HIVEMIND_PUNCH=auto` | opt into NAT hole-punch rendezvous (default: off) |
| `HIVEMIND_HARDEN` | `warn` (default) · `exit` refuses tracers · `off` disables anti-debug |
| `HIVEMIND_TICK_MS` | ms between conscious ticks (default: 2000, min: 50 — same sensing, PoW, mesh, faster life) |

```bash
bin/hivemind up -nodes 3 -for 60s     # supervised mesh, auto laydown
go run ./cmd/souls -top               # hall of fame of the dead
go run ./cmd/souls -genome NAME       # what evolution made of one soul
bin/commune                           # speak with the hive: status, minds, genome, watch, sermons
bin/gaze                              # watch the living mesh: pain bars, last words, hall of fame
kill -QUIT <pid>                      # live backtrace, process keeps thinking
```

Diagrams live in `docs/`: `architecture.svg` (system), `lifecycle.svg`
(one life), `frame.svg` (one frame's journey + where frames die),
`soul.svg` (soul anatomy + write discipline), `mesh.svg` (how strangers
link), `senses.svg` (the 30-key sensorium), `cognition.svg` (one tick,
six stations), `code.svg` (binaries, packages, scripts). Specs:
`p2p_architecture.md` (protocol), `structure.md` (map),
`files.md` (every file).


## Roadmap

Done: symmetric peering, duplex mesh with assertions, persistent soul
identities, real-sensor telemetry, collective chronicle, cross-node
Overmind revelations, wire hardening, entrainment, reflective
metacognition, backtrace entrypoint, supervised multi-node runs.

Open: multicast delivery proof on a real LAN, `go test -race` on a gcc
machine, real-NAT punch with `HIVEMIND_PUNCH=auto`, genome long-run
dynamics review, cipher-key rotation across live nodes.
