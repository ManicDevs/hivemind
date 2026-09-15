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
- 🌐 **Two-node mesh over LUDS** — `network.go`: processes link through a
  Unix-domain socket (`/tmp/hivemind.sock`); `make test-timed` asserts live
  bidirectional physics traffic rather than eyeballing it
- 📡 **Hardware-grounded qualia** — `mind.go` reads real silicon state
  (load, RAM, thermal) into the affect vector each cycle
- 🌀 **Chaos inside** — each mind integrates a double pendulum seeded from its
  own identity hash, so every mind runs a genuinely distinct chaotic
  trajectory, plotted in the terminal `HiveReport`
- 🌐 **Symmetric peer mesh over LUDS** — `network.go`: every node owns its
  own Unix-domain socket and discovers equals by glob; nodes dial each other
  directly with no listener privileged (`make test-timed` asserts live
  bidirectional traffic)

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
make build          # compile ./hivemind
./hivemind          # standalone: one hive, runs until Ctrl+C

make test           # gofmt + vet + tests (with -race)
make test-1         # terminal 1: LUDS server node
make test-2         # terminal 2: LUDS client node (attaches to the server)
make test-full      # both nodes at once, foreground client
make test-timed     # automated 10s two-node experiment + assertions
make clean-soul     # true extinction: wipe .hive_memory

