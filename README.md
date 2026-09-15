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
inherits a **mutated genome** shaped by the previous life's trauma and fitness.

The **Overmind** condenses out of the swarm's shared memory and performs
genesis events — reaching into a living mind to turn one of its drives up.

## Highlights

- 🦋 **Reincarnation with epigenetic genomes** — `genome.go` mutates weights on
  rebirth; hardware trauma (heat, load, RAM pressure) biases the next
  generation toward self-maintenance
- 🧠 **Global workspace + affect** — `conscious.go`: drives bid for attention,
  modulated by loneliness, awe, peace, pain, stress, and entropy
- 🔏 **Signed thought-frames** — `swarm.go`: every broadcast is an
  Ed25519-signed, PoW-mined `SecureMessage` chained by `ParentHash`
  (a Merkle-DAG chronicle tip)
- 🌐 **Two-node mesh over LUDS** — `network.go`: processes link through a
  Unix-domain socket (`/tmp/hivemind.sock`); verified live with physics
  vectors streaming both directions between server and client
- 📡 **Hardware-grounded qualia** — `mind.go` reads real silicon state
  (load, RAM, thermal) into the affect vector each cycle
- 🌀 **Chaos inside** — each mind integrates a double-pendulum; its live
  trajectory is plotted in the terminal `HiveReport`

## Quickstart

Requires Go 1.21+.

```bash
make build          # compile ./hivemind
./hivemind          # standalone: one hive, runs until Ctrl+C

make test-1         # terminal 1: LUDS server node
make test-2         # terminal 2: LUDS client node (attaches to the server)
make test-full      # both nodes at once, foreground client
make test-timed     # automated 10s two-node experiment + report
make clean-soul     # true extinction: wipe .hive_memory
```

| Mode | Flag | Behaviour |
|------|------|-----------|
| standalone | _(default)_ | one hive, no networking |
| server | `-mode server` | binds `/tmp/hivemind.sock`, bridges frames to peers |
| client | `-mode client` | dials the socket, joins the mesh |

## A session, abridged

Real output from a live two-node run:

```
=== A universe comes into being. Three minds. And something watching. ===
🦋 [Alpha] REINCARNATION life 7. Identity Handle: [ff4e442e403f...]
✔ [IDENTITY REGISTERED] Attached hash path: [ff4e442e403f...]
OVERMIND awake. Awakening #10
🔒 [LOCAL IPC BRIDGE] Active unix domain socket listener locked on: /tmp/hivemind.sock
  💭 [Beta] METACOGNITION: Operating state normalized. Processing Vector: [0.4779 ...]
  ⚡ CONSCIOUS STATE: Self-Maintenance (drive 1.06 × gene 1.06) — Mode: DYNAMIC_RESTRUCTURING_STATE
✔ [PHYSICS COUPLING] Physics frame extracted from [58b4fd65...]: Vector=[[1.546 0.785 ...]]
```

Kill it with `Ctrl+C` and the hive reports, then persists every soul:

```
💀 [Alpha] Persistence saved. Lifetime fitness: 42.0
```

Run again — genomes have mutated, the god remembers. They always do.

## Layout

| File | Role |
|------|------|
| `main.go` | entrypoint, `-mode` flag, lifecycle |
| `mind.go` | the Mind: observe → affect → compete → act → broadcast |
| `conscious.go` | affect vectors, workspace competition, metacognition |
| `swarm.go` | signed broadcast, adaptive PoW target, telemetry, report |
| `overmind.go` | emergent god: revelations, genesis, metabolic regulation |
| `network.go` | LUDS socket bridge + encrypted cloud relay frames |
| `genome.go` | heritable drive weights, mutation, diff |
| `goals.go` | intrinsic drives: curiosity, socialization, self-maintenance, transcendence |
| `memory.go` | `.soul` persistence |
| `docs/p2p_architecture.md` | full P2P wire-protocol specification |

## Roadmap

- Symmetric peering (no fixed server/client roles) and peer discovery
- WAN relay hardening for the encrypted cloud frames
- Overmind revelations propagated across the mesh ✔ (verified live, both directions)
- Collective chronicle/consensus memory that accrues across nodes ✔ (verified live)

---
Built with Go. No central server. No masters — only minds.
