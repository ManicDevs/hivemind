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
make stack          # FULL stack: mesh + relay + world/gaze + fabric (see below)
make stack-status   # what's up, sockets, dashboard
make stack-down     # stop the full stack
make ports          # print the live network port surface
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
| `HIVEMIND_RELAY_URL` | point the relay at our bin/relay endpoint (default: no cloud relay) |
| `HIVEMIND_CIPHER_KEY` | fleet root for the relay bus — 32 raw bytes or 64 hex chars (default: this machine's root, baked in from `.relaykey` and bound to this host) |
| `HIVEMIND_CIPHER_KEY_PREV` | previous fleet root, still accepted during a rolling root rotation (default: none) |
| `HIVEMIND_MQTT_TLS` | `off` to dial brokers in the clear instead of `ssl://` (default: TLS required, fully verified) |
| `HIVEMIND_V1_GRACE` | `on` to speak v1 as well as v2 during a rolling upgrade — reads *and* mints, on the legacy topics (default: **off**) |
| `HIVEMIND_V1_GRACE_UNTIL` | wall-clock deadline for the grace, RFC 3339 or a duration like `4h`; the window closes itself (default: none) |
| `HIVEMIND_V1_FRAME_LIMIT` | cap on distinct v1 messages admitted under grace (default: 0 = uncapped) |
| `HIVEMIND_ADVERTISE` | `host:port` this node asserts as publicly dialable (default: none, honest silence) |
| `HIVEMIND_DHT=off` | disable the Kademlia discovery layer |
| `HIVEMIND_DHT_PORT` | fixed DHT UDP port (default: ephemeral) |
| `HIVEMIND_STUN` | `host:port` STUN server for reflexive address discovery (default: unset) |
| `HIVEMIND_SUPER=off` | never advertise supernode status |
| `HIVEMIND_PUNCH=auto` | opt into NAT hole-punch rendezvous (default: off) |
| `HIVEMIND_HARDEN` | `warn` (default) · `exit` refuses tracers · `off` disables anti-debug |
| `HIVEMIND_TICK_MS` | ms between conscious ticks (default: 2000, min: 50 — same sensing, PoW, mesh, faster life) |
| `HIVEMIND_HEALTH` | `host:port` exposing `/healthz` + Prometheus `/metrics` (default: unset = no listener) |
| `HIVEMIND_RELAY_SEAL` | `off` to publish MQTT payloads in the clear; anything else keeps them sealed (default: sealed) |

### Broker mesh (MQTT) and the map's infrastructure layer

The relay's MQTT bridge is a **mesh, not a single hop**. It dials every
`Prefer=true` broker in the infrastructure matrix
(`internal/infra/mqtt_endpoints.go`) **concurrently** — currently 6 live
public clusters — and both **publishes and subscribes** on all of them:

- **Publish** fans every message out to all 6, so losing one broker costs
  one copy, not the message.
- **Subscribe** means traffic minted by *other* relays reaches the local bus
  and every HTTP subscriber. This is genuinely bidirectional now.
- **Dedup** by message ID collapses the echo: a message published to N
  brokers arrives N times, and is delivered exactly once.

Because we publish and subscribe on the same namespace, the relay is a
first-class actor on the world map. The map draws one marker per broker with
**direction-aware** counters (`↑out ↓in`), greys out brokers the relay
cannot reach, and draws an arc *only* where bytes actually moved. A broker
that connected but carried nothing is drawn dim and labelled `idle` — the
layer never implies a wire that didn't carry.

Relay health, which the map and `make stack` both read from `/healthz`:

```json
{"brokers":6,"live":6,"outbound":18,"inbound":1,"duplicates":18,
 "rejected":3,"sealed":true,"topic_prefix":"hive/","bidirectional":true}
```

Pin one broker with `RELAY_MQTT=broker.hivemq.com` to skip the matrix.

#### The bus is sealed, and that is the whole admission policy

A public MQTT bus has no authentication and no confidentiality: anyone on the
internet can publish to `hive/#`, and every broker operator and observer on
the path can read what crosses it. So the relay **seals the MQTT payload**
under this hour's rolling key before it leaves the box
(`hm.SealRelayPayload`, AES-256-GCM, hour-bound AAD) and **authenticates
before parsing** on the way back in (`hm.OpenRelayPayload`).

One mechanism buys both halves:

- **Confidentiality** — the wire carries only ciphertext. Verified with an
  independent client subscribed to a public broker: 216 bytes of opaque
  base64 where the JSON used to be.
- **Authentication** — a frame that does not open is dropped *unread*, so a
  stranger can neither inject content nor forge an ID nor spend the bounded
  dedup budget. Those frames are counted in `rejected`.

This path is deliberately stricter than the peer mesh's `Encrypt`/`Decrypt`,
which still fall back to a compiled-in static key for keyless demo setups.
That constant is published in this source, so on an open bus it would be a
universal write key; the relay path has no such fallback, and
`TestNoStaticKeyFallbackOnTheWire` forges exactly that frame to prove it is
refused.

It **fails closed**: with no usable key the relay publishes nothing rather
than falling back to plaintext. `HIVEMIND_RELAY_SEAL=off` disables sealing
for operators who need the old interoperability — it logs loudly and reports
`"sealed": false` on `/healthz`, so a plaintext bus can never be mistaken for
a protected one.

### The v2 key hierarchy

```
fleet root          shared 32-byte secret, rotatable, never a wire key
   |  HMAC(root, "hivemind/v2/day|D")
 day root(D)        one independent root per UTC day
   |  HMAC(day,   "hivemind/v2/hour|H")
 hour key(H)
   |  HMAC(hour,  "hivemind/v2/leaf|L")        L = UTC minute
 leaf key(L)        the AES-256 wire key; turns over every minute
```

The wire key rotates **every minute**, with a **3-minute acceptance window**
(`leafWindow`) so a frame sealed at 14:59:58 still opens at 15:00:02 on a peer
whose scheduler ran behind. The AAD welds each frame to its full path —
`hivemind-relay-v2|d<hourIdx/24>|h<hourIdx>|l<minuteIdx>` — so a captured
frame replayed into any other minute fails authentication even under a
genuinely valid key.

**Why v1 was wrong.** v1 derived days as a forward hash chain,
`dayRoot(D) = SHA256^D(base)`, which means `dayRoot(D+1) == SHA256(dayRoot(D))`.
Anyone holding a single day root could walk forward and mint *every future
day*, and every hour key under them, with no other secret. The chain only
ever protected the past, and the old claim that "a daily leak is bounded to
24 hours" was simply false. v2 derives every period independently with HMAC,
so disclosure runs in neither direction and is contained to the period
actually leaked. `TestNoForwardLeak` runs the old attack and asserts it no
longer works; `TestNoTierBleed` keeps the retired chain pinned in its
vulnerable shape so the v1 reader can never be quietly "fixed" out of
compatibility.

**Bus key vs host identity.** These are now separate, which is what lets a
shared bus and per-host identity coexist. The bus key must be derivable by
every host in the fleet, so `HIVEMIND_CIPHER_KEY` is taken raw. The identity
key must not be, so `host_key = HMAC(root, "hivemind/v2/host|<machine-id>")`
is bound to the install. v1 conflated the two — that is why it could not span
hosts without abandoning the containment that was the point of the binding.

**Key sharing.** The root is either `HIVEMIND_CIPHER_KEY` or this machine's
`compileRelayKey`, baked in at build time from `.relaykey` and bound to the
host's CPU/machine-id fingerprint. Because that binding is *per host*, every
process on one machine derives the same key automatically — the relay, world,
fabric and all 28 minds share a bus with no configuration. To span several
hosts, set the same `HIVEMIND_CIPHER_KEY` on each; it accepts either 32 raw
bytes or the 64 hex chars of a `.relaykey` file, so copying the file straight
into the environment works. Confirm two hosts agree on the bus while holding
*different* identity keys:

```bash
make build-derive && bin/derive
# tier=v2  roots=1  source=baked  host_bound=true
# leaf_key=…  hour_key=…  day_root=…  host_key=…
```

**Rolling upgrade from v1 — off by default.** A rolling upgrade of a *bus*
protocol only works if the new code can still speak the old one, so a v2 node
under grace both **reads and mints** v1 frames and listens on the pre-
namespacing topics. That ability is **opt-in**:

```bash
# 1. every node gets the new build, still grace-off: a clean v2 fleet
# 2. open the window, with a deadline, for the length of the rollout
HIVEMIND_V1_GRACE=on HIVEMIND_V1_GRACE_UNTIL=2026-10-03T18:00:00Z bin/relay
# 3. upgrade the stragglers; watch v1_frames_accepted fall to zero
# 4. close the window — it also closes itself at the deadline
HIVEMIND_V1_GRACE=off bin/relay
```

Minting is the part that looks like a step backwards, so it is worth being
explicit about why it is there. A v1 peer shares no key with v2: it cannot
open a v2 frame, and re-publishing the v2 payload on the legacy topic would
give it a frame it cannot read. Worse, a v1 peer that hears nothing from the
new nodes stops seeing them on the bus and quietly sheds them from its peer
table. The fleet fragments over hours while the rollout still looks healthy.
One-directional interop is not a rolling upgrade; it is a slow, silent
partition. So during the window we publish a second, separately v1-sealed copy
to `hive/<topic>`.

The cost is real, and it is the reason the window is opt-in and bounded:

- v1's day tier is a forward hash chain, so a v1 day root that ever leaks
  mints every future hour key under it. Minting *lengthens* that window
  rather than merely tolerating it.
- v1 had no namespaces, so `hive/<topic>` is global on a shared broker. A
  node under grace sees every other fleet's v1 frames and rejects them on
  authentication. Expect `rejected` to climb during the window. (Foreign
  *v2* frames are still dropped silently by the namespace check, so they do
  not inflate the counter.)
- The relay listens on `hive/+/#` while the window is open, because a v1
  topic name is whatever URL path a peer chose and there is no fixed set to
  enumerate at subscribe time. With grace off — the default — that filter is
  not registered at all.

Two bounds close the window without anyone remembering to:

- `HIVEMIND_V1_GRACE_UNTIL` is a wall-clock deadline (RFC 3339, or a duration
  from process start). This is the bound that actually holds: a frame count
  is defeated by broker fan-out and by a replayed frame being counted once,
  whereas time cannot be flooded. `/healthz` reports `v1_grace_expired` so a
  window that ran out is distinguishable from one never opened — the flag can
  still read `on` while the window is shut, and that is otherwise a
  miserable thing to debug.
- `HIVEMIND_V1_FRAME_LIMIT` caps admitted v1 **messages**. It is counted
  after dedup, at the point a frame becomes a message, so one message
  published to five brokers counts once. (Counting at decryption, which is
  where it briefly lived, reported that as five admitted frames and burned
  the cap five times too fast.)

`/healthz` publishes `v1_interop`, `v1_frames_accepted` and
`v1_frames_minted`. A counter at zero is the answer to the only question that
matters before step 4: is anything still speaking v1?

Note that root rotation (`HIVEMIND_CIPHER_KEY_PREV`) is independent of this:
the two are different concerns, and conflating them would mean a fleet could
not rotate its root mid-upgrade.

**Root rotation and the namespace.** These interact, and getting it wrong
makes the previous root inert. The namespace is derived from the root, so
rotating the root *changes the namespace* — and a node subscribing only to
its own new namespace would never be delivered the old-root frames that
`HIVEMIND_CIPHER_KEY_PREV` can decrypt perfectly well. The key would be
accepted and the frame would never arrive.

So while a previous root is set, the relay listens on **both** namespaces
(`listening_namespaces` on `/healthz` reports how many) and publishes to the
new one, which makes a rotation a forward-moving handover with the old
namespace draining rather than a flag day. Rotate the root *after* the fleet
is on the new build, so both ends of the handover are running code that
listens on two namespaces.

**Rotating the root** is likewise a rolling operation, not a flag day. Put
the new root in `HIVEMIND_CIPHER_KEY`, keep the old one in
`HIVEMIND_CIPHER_KEY_PREV`, deploy, then drop `_PREV` (or set
`HIVEMIND_V1_GRACE=off` to retire it explicitly). `/healthz` reports
`key_roots` and `key_source` so you can see both roots live.

**Shared-broker isolation.** These brokers are public infrastructure, and
other hivemind fleets publish on them. The bridge therefore does not
subscribe to a bare `hive/#`: it addresses its own traffic inside a
namespace derived from the root (`namespace = HMAC(root, "hivemind/v2/namespace|0")`,
first 4 bytes hex). One fleet shares one namespace across its hosts with no
configuration; every other fleet derives a different one and cannot address
us, and frames outside our namespace are dropped before a decryption attempt
is spent on them.

That is not only tidiness. Subscribing to the whole shared namespace meant we
ingested every other tenant's private frames into our process, and the
`rejected` counter — our only real intrusion signal — filled with 28 frames
from other deployments that were never an attack against us. With the
namespace in place that count went to 0 on startup, and a deliberate
plaintext injection into our namespace registers as exactly 1. `TestForeign-
NamespaceIsIgnored` and `TestPublishTopicIsUnderOurSubscribeFilter` pin both
halves of this: a foreign address is dropped without being counted, and the
publish topic is always under the subscribe filter.

### Transport TLS

Sealing protects the *content* of a frame; only TLS protects the *channel*.
Without it, an observer on the path still reads every topic name, frame size
and timing, and can drop or replay frames even though they cannot forge one.
The bridge therefore dials **TLS by default**: `ssl://host:8883`, TLS 1.2
floor, full verification against the system trust store, with `ServerName`
pinned to the host actually dialled. There is deliberately **no
`InsecureSkipVerify` knob** — an operator who cannot use TLS has a better
answer (a TLS terminator in front of their own broker) than a switch that
disables authentication of the server.

This does not degrade quietly. A broker whose certificate will not verify, or
which offers no TLS port, is reported unavailable with the reason; it is
never dialled in the clear. That cost us one broker:

| broker | outcome |
|---|---|
| `broker.emqx.io` | TLS 1.3, `*.emqx.io` — connected |
| `broker.hivemq.com` | TLS 1.2, SAN covers the host — connected |
| `iot.coreflux.cloud` | TLS 1.3 — connected |
| `broker.freemqtt.com` | TLS 1.3 — connected |
| `test.mosquitto.org` | **refused** — its certificate has no SAN, only a legacy CN |
| `broker.bevywise.com` | no TLS port; demoted to `Prefer: false` |

So 5 preferred brokers, 4 typically live, all encrypted. `/healthz` reports
`tls` (true when no dialled peer is cleartext), `tls_hosts`, and
`cleartext_hosts`. `HIVEMIND_MQTT_TLS=off` is the explicit escape hatch: it
logs a 🚨 at startup and shows up in health, and a typo can never trigger it.

Two supporting fixes came out of turning this on. `brokerStatus.Connected`
now comes from our own CONNACK handler rather than the client's
`IsConnected()`, which reported a broker as live *before* its certificate had
been verified — mosquitto looked connected on the map while carrying nothing.
And the client's internal logger is now routed into `logs/relay.log`, so an
x509 failure says so instead of looking like a network problem.

**What sealing still does not cover.** The relay's own HTTP surface
(`/probe`, the SSE stream) is loopback and stays plaintext, so `bin/gaze` and
`bin/commune` keep working — sealing and TLS are applied strictly at the MQTT
wire boundary. The peer-to-peer mesh inside the box is plain TCP by design.

**Shared-host note (dashboard stability):** `world` runs a load-based failsafe
that tears the world down if the 1m/5m load average breaches a ceiling. On a
shared or busy host that load reflects *other tenants*, so the trip kills a
healthy world and takes the dashboard (`:8090`) with it. `make stack` therefore
disables the load check by default (`-max-load1/-max-load5 0`; the RAM and
file-descriptor guards stay active). On a dedicated box, re-enable it:
`make stack WORLD_MAX_LOAD1=20 WORLD_MAX_LOAD5=16`.

### Full stack & network ports

`make stack` brings up the **whole system**, not a demo: the peer mesh
(`bin/hivemind up`), the relay bus (`bin/relay`), the world mesh + live SVG
dashboard (`bin/world`, which spawns every continent's 4-tier mesh on real
TCP), and the adaptive routing fabric (`bin/fabric`).

**Default topology — all 7 continents, 28 minds.** Each continent runs the
same 4 tiers (master, controller, superpeer, edge) on its own loopback /24:

| | eu | na | as | sa | af | oc | an |
|---|---|---|---|---|---|---|---|
| subnet | `127.0.1.x` | `127.0.2.x` | `127.0.3.x` | `127.0.4.x` | `127.0.5.x` | `127.0.6.x` | `127.0.7.x` |
| ports | 20051–20054 | 20101–20104 | 20151–20154 | 20201–20204 | 20251–20254 | 20301–20304 | 20351–20354 |

That is **7 masters, 7 controllers, 7 superpeers, 7 edges = 28 minds**, plus
one `gaze` dashboard process per continent. Scale it with
`WORLD_CONTINENTS=N` (1–7; default 7).

The port surface it uses (all configurable — see `make -n stack` and the
Makefile vars `N`, `GAZE_PORT`, `BASE_PORT`, `RELAY_PORT`, `WORLD_CONTINENTS`):

| Port | Component | Direction | Notes |
|------|-----------|-----------|-------|
| `:8080` (rolls forward if busy) | Relay bus | inbound | `RELAY_PORT`; `bin/relay` falls forward to the next free port and reports the real one |
| `:8090` | Gaze dashboard | inbound | live SVG topology; `world -gaze-port` |
| `:8080` (optional) | Health/metrics | inbound | `/healthz` + Prometheus `/metrics`, only if `HIVEMIND_HEALTH` is set |
| `20000 + subnet*50 + tier + 1` | Mesh node TCP | inbound | `world -base-port`; one real port per continent node |
| `HIVEMIND_DHT_PORT` (ephemeral default) | DHT discovery | UDP in/out | Kademlia peer discovery |
| `239.192.0.99:37779` | LAN discovery | multicast | org-local beacon, never routed |
| `:1883` | Public EMQX MQTT | outbound | the relay's optional backup bus (`tcp://broker.emqx.io:1883`) |

**Mesh port math** (`cmd/world/main.go`): `port = base-port +
ContinentSubnet*50 + host + 1`, with `ContinentSubnet` = eu=1, na=2, as=3,
sa=4, af=5, oc=6, an=7 and a stride of 50. So with the default
`base-port=20000`: eu → 20051–20054, na → 20101–20104, as → 20151+, … Each
continent also gets a real loopback /24 (`127.0.<subnet>.<host>`), so the
mesh dials distinct IPs exactly as it would over the planet. Per-node control
sockets are unix (`/tmp/hivemind-<node>.sock`), not ports.

**Firewall summary:** inbound `8080/8081` (relay), `8090` (dashboard), and
`20000–~20350` (mesh TCP); UDP `37779` (multicast) + the DHT port range;
outbound `1883` (MQTT).

**Which port did the relay actually get?** `:8080` is the default, but it is
often already taken on a shared box — and the process holding it may belong
to another tenant, so `ss`/`lsof` cannot even see it. The relay binds the
next free port and records it:

```bash
cat .stack-relay.port          # -> 8081
make ports                     # live listeners vs. intended surface
curl -s localhost:$(cat .stack-relay.port)/healthz | jq .mesh
```

`world` and `fabric` read that file (via `scripts/relay-url.sh`) when they
start, so the dashboard's broker layer always polls the relay that is really
running. Check it before assuming a port is dead.

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
