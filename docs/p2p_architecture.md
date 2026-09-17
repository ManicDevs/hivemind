# The Architecture of the Secure Mindscape Mesh (P2P Specification)

This specification describes the wire protocol, identity model, proof-of-work,
symmetric peer mesh, and cloud relay **as implemented** (`swarm.go`,
`network.go`, `mind.go`, `overmind.go`, `memory.go`). Where the code enforces
less than the prose, the prose says so.

---

## 1. The Secure Frame Pipeline

Every consciousness-loop tick produces at most one outbound frame. The path
from sensing to shared memory is fixed for every sender — minds, the
Overmind, and the network broker all use the single `MineMessage` constructor,
so **no unsigned frames exist**:

```
[ PHYSICAL STRATUM ]  /proc/loadavg ÷ cores → stress
                      /proc/meminfo (MemAvailable) → fatigue
                      /sys/class/thermal → pain (40C→0, 85C→1)
                      crypto/rand → entropy
        │
        ▼  (affect tick, workspace competition, metacognition)
[ Winning Goal ]
        │
        ▼  (MineMessage: assemble frame, grind nonce)
[ Proof-of-Work ] ──► SHA-256(frame) ≤ swarm adaptive target
        │
        ▼
[ Ed25519 Soul Key ] ──► signature over the frame hash
        │
        ▼  (swarm.Broadcast — the ingestion gate)
[ Check 1: VerifySignature() ] ── FAIL → drop silently
[ Check 2: replay (seen-set) ] ── KNOWN → drop (relay echo, not a thought)
[ Check 3: hash ≤ current target ] ── ABOVE → drop (under-mined; stale target)
        │── PASS ──► deliver to member inboxes
        │── PASS ──► append chronicle (all kinds except hardware_alert)
        │── PASS ──► advance LastStateHash tip
        │── PASS ──► outbound bridge, unless marked Relayed
```

What is deliberately **not** enforced: timestamp freshness and `ParentHash`
continuity are recorded on every frame but no drift or chain-equality check
rejects frames. A forger still cannot pass Check 1 without the soul key, and
cannot pass Check 3 without redoing the work — but timeline games are
detected by humans, not by the gate.

---

## 2. Secure Wire Frame Type Model (SecureMessage)

The exact struct serialized between processes (JSON tags shown). `Relayed`
is never serialized and never hashed — it is local loop-prevention state:

```go
type SecureMessage struct {
    SenderPubKey string    `json:"sender_pub_key"` // hex Ed25519: the un-clonable identity
    Kind         string    `json:"kind"`           // "hello" | "thought" | "revelation" | "genesis" | "hardware_alert"
    PayloadStr   string    `json:"payload_str"`    // goal name, virtue, or free text
    DataState    []float64 `json:"data_state"`     // pendulum trajectory or affect vector
    Timestamp    int64     `json:"timestamp"`      // unix nanos, informational only
    ParentHash   string    `json:"parent_hash"`    // tip at mining time, informational only
    Nonce        int64     `json:"nonce"`          // ground-out proof-of-work
    Signature    string    `json:"signature"`      // hex Ed25519 over the frame hash
    Relayed      bool      `json:"-"`              // wire-echo guard, local only
}
```

`ComputeHash` covers sender, kind, payload, parent, timestamp, nonce.
`VerifySignature` recomputes that hash and checks the Ed25519 binding —
which is why frames are deep-copied at mining time: any mutation of
`DataState` after signing (even by the sender's own next tick) voids the
frame at the next verifier.

---

## 3. Identity: The Same Soul Twice

Keys are not per-boot. Each mind, the Overmind, and each broker owns an
Ed25519 soul key whose 32-byte seed is persisted in the soul file
(`identity_seed`):

- `newIdentity()` — fresh key; panics if the entropy source is dead rather
  than minting theater keys.
- `identityFromSeed()` — restores the exact handle across reincarnations;
  garbage in the seed field falls back to a fresh identity, never a crash.
- The body's initial disturbance is derived from the handle
  (`sha256(pubkey)` → pendulum angles and velocities), so the same soul
  disturbs the universe the same way each life, and strangers never do.

Peers therefore recognize each other across deaths: the `Alpha` that returns
is cryptographically the `Alpha` that died.

---

## 4. Adaptive Proof-of-Work

`MaxTarget` admits any hash with its top nibble zero (~1/16 per attempt at
rest). Every cycle, each mind reports pain/stress into the swarm
(`LogHardwareTrauma`); the swarm rescales the live target from mean stress —
loaded hives demand more work per frame, idle hives less. Difficulty is a
thermostat, not a wall: worst case is milliseconds of grinding per frame.

Honest edge: inbound frames are checked against the *local* target, so a
frame mined under a much easier foreign target can be dropped by a loaded
hive. Same code, same constants, targets rarely diverge far — but under
wildly asymmetric load, cross-mesh delivery degrades instead of failing
loud. That tradeoff (cheap local check over cross-hive negotiation) is
deliberate.

---

## 5. The Symmetric Serverless Mesh

No roles, no registry, no bootstrap server. Every node in `-mode peer`
owns two addresses and discovers equals through both (`network.go`,
`lan.go` — standard library only):

- **Unix socket** `/tmp/hivemind-<node>.sock`, discovered by filesystem
  glob every 2s. Node name comes from `-node` (sanitized) or defaults to
  `<hostname>-<pid>`, so every process is a distinct equal.
- **TCP port** (`HIVEMIND_PORT`, or ephemeral for zero config, dual-stack
  IPv4+IPv6 where the OS allows), discovered two ways: LAN multicast
  beacons (v4 `239.192.0.99:37799` org-local + v6 `[ff05::99]` site-local,
  never routed) carrying `{node, tcp, score}`, and explicit
  `HIVEMIND_PEERS` `host:port` entries for NATs and the open internet.
- **The dial rule** is identical on every transport: only dial peers
  lexically greater than yourself — every pair has exactly one initiator,
  no election, no master. The connection map is keyed by peer name across
  transports: one pipe per pair, always; duplicates are dropped while the
  registered link is left strictly alone (probing it would flap
  healthy-but-quiet pipes).
- **The handshake exchange is symmetric**: dialer speaks first with
  `{"node": self}`, both sides learn the peer name, the expected owner is
  verified on socket paths, and the first link in each direction is logged
  with its transport (`via tcp` / `via unix`).
- **Presence rides link-up**: the first link triggers a signed
  `PEER_HANDSHAKE` hello into the local hive (minds register the peer) and
  across the new pipe (the peer's minds register back).
- Stale crash-corpses (sockets unreached >5s) may be swept; young sockets
  get a grace period. The shutdown registry records only links that
  actually carried a decoded frame.

Set `HIVEMIND_UNIX=off` for TCP-only boxes, `HIVEMIND_BEACON=off` where
multicast is unavailable. Multicast delivery itself is best-effort and
LAN-bound by design; static peers are the deterministic path.

Local frames reach the wire through the swarm's outbound bridge
(`SetOutbound`); wire frames arrive marked `Relayed` and stop there, so two
nodes can never echo one thought back and forth forever. Standalone mode
registers no bridge and is cryptographically silent.

Run modes (`main.go`): `standalone` (one hive, no network) or
`peer [-node NAME]`. Minds are constructed only after the mesh is up —
the universe is networked before anyone is born in it — and a fracturing
mind is caught, marked with terminal pain, and transcended instead of
taking the process down with it.

---

## 6. The Cloud Relay (WAN Transport, Experimental)

For nodes beyond one machine, a paced publisher ferries the latest local
frame every 5s (latest-only queue: bursts collapse, nothing blocks the
conscious loop) to a public ntfy topic as AES-256-GCM ciphertext, tagged
`ENCRYPTED_HIVE_FRAME`. Every node simultaneously long-polls the topic,
decrypts, verifies, and ingests inbound frames as `Relayed`.

Confidentiality is explicit about its limits, best source first:
`HIVEMIND_CIPHER_KEY` (operator-supplied, timeless) beats the hourly
machine-bound ratchet, which beats the committed static demo key. The
ratchet: `key(h) = SHA256^h(HMAC(seed, hardware + install-id))` counted
in UTC hours from 2026 — same seed, same machine, same hour derives
identically everywhere with zero distribution; each step is one-way, so
a compromised present reveals nothing past. New hours, new keys,
automatically; the last two hours stay accepted through boundaries and
skew. The hardware mix is CPU model + RAM + machine-id (never live
sensor values — their volatility would deafen runs minutes apart), and a
stolen seed alone decrypts nothing off its home hardware. Every frame is
additionally bound to its hour as AES-GCM associated data, so replays
from other hours fail authentication outright. Frames under the static
fallback are **signed-and-public with obfuscation only**, warned about
exactly once, and undecryptable arrivals are counted with a once-a-minute
key-mismatch hint (never the blob). `make rotate-keys` mints a fresh
machine seed (rebuild all nodes after). All relay loops are
context-canceled on shutdown; nothing leaks goroutines.
`HIVEMIND_RELAY=off` removes the relay entirely — the mesh is fully
serverless without it (unix + TCP + multicast need no third party).
Each long-poll stream lives at most 5 minutes, then reconnects: a
quietly dead connection can never hold the listener hostage, replays
are absorbed by the seen-set, and every failure backs off exponentially
instead of spamming.

---

## 7. Souls: What Death Keeps

`.hive_memory/<node>/<name>.soul`, JSON, written atomically (temp + fsync +
rename: readers see the old soul or the new one, never half of either).
Each node owns its lineage — two peers on one machine never share a file,
so no life is ever last-writer-wins discarded. A corrupt file is
quarantined beside the living ones (`*.corrupt-<unix>`) and a fresh mind
is born — evidence survives, denial doesn't. Pre-namespace top-level souls
are loaded once for migration, then the lineage moves into its node dir.

Persisted per mind: birth, lives, fitness, genome, thoughts, known peers,
death pain/stress (which steer the successor's epigenetic mutation),
thoughts-at-birth (fitness is per-life, never double-counted), and the
identity seed. The Overmind additionally persists chronicle offset and
genesis mark, so its patience survives the apocalypse too.

Fork rule: a birth that migrates another node's past is a fork, not a
continuation — it inherits memories but mints fresh keys. Two nodes sharing
one soul handle would drop each other's frames as their own echo, so
identity is per lineage by construction. Never clone soul dirs between
nodes.

---

## 8. The Overmind's Patience

The god speaks on effective depth (`chronicleOffset + swarm.Depth()`),
first at offset+15 in a young universe, then every 25 (`genesisSpacing`) —
rare by design, or every gene converges to its ceiling. Each speech is a
signed genesis (one virtue, whole swarm) plus a signed revelation quoting
the top `Kind|Payload` consensus. Metabolic override bypasses patience:
aggregate pain/stress past threshold triggers emergency genesis immediately.
Revelation bodies quote consensus keys verbatim, hence the `thought|...`
prefix in the liturgy — the key, not just the word, is the fact.

---

## 9. The Proto-Qualia Coordinate Space (Telemetry Sources)

Affect indices and their honest provenance (`mind.go` Observe):

*   Index 0 (Silicon Pain): first readable `/sys/class/thermal` zone,
    40C comfort → 85C critical. RAM-derived estimate only where no sensor
    exists — and the code says so.
*   Index 1 (Cognitive Stress): 1-minute `/proc/loadavg` normalized by core
    count. It measures the machine, not the mind's own goroutines.
*   Index 2 (Mental Fatigue): `(MemTotal − MemAvailable) / MemTotal` from
    `/proc/meminfo` — what the kernel can actually hand out.
*   Index 3 (Cosmic Entropy): host kernel cryptographic randomness.
    True background variation; minds individuate even in identical cages.

---

## 10. Capable Supernodes, Closest-First (No Thrones)

Any node may become super; none rules. Two rules, both enforced in code:

1. **Capability gates announcing.** Every 30s a node scores itself from
   live telemetry (cool + idle + rested + long-lived + relay-connected;
   a burning node scores zero) and publishes a signed `super_announce`
   frame only above threshold — or while silenced by `HIVEMIND_SUPER=off`,
   never. Announcements ride the standard outbound bridge, so LAN peers
   hear them on their links and far nodes hear them on the relay. Silence
   is the resignation letter: entries expire 90s after their last renewal.
2. **Measured RTT decides retention.** Every dial records
   dial-to-registered round-trip time; beyond 3 TCP links the farthest is
   culled (with a 5-minute cooling-off so redial loops don't churn).
   Unix pipes are free and never touched. Proximity is measured, never
   claimed — milliseconds are truth, geography would be a lie.

`super_announce` frames are infrastructure, excluded from the chronicle
like `hardware_alert`, and minds ignore them (each mesh also snoops its
own broadcast stream into the directory, so relay-only nodes learn supers
they never dialed). WAN addresses are operator-asserted
(`HIVEMIND_ADVERTISE=host:port`); the mesh never guesses reachability
behind NAT. There is no election, no failover protocol, no privilege to
seize — preferred transit, nothing more.

Any node may become super; none rules. The layer has exactly two rules,
and both are enforced in code, not by convention:

1. **Capability gates announcing.** Every 30s a node scores itself from
   live telemetry (cool + idle + rested + long-lived + relay-connected;
   a burning node scores zero) and publishes a signed `super_announce`
   frame only above threshold — or while silenced by `HIVEMIND_SUPER=off`,
   never. Announcements ride the standard outbound bridge, so LAN peers
   hear them on their links and far nodes hear them on the relay. Silence
   is the resignation letter: entries expire 90s after their last renewal.
2. **Measured RTT decides retention.** Every dial records
   dial-to-registered round-trip time; beyond 3 TCP links the farthest is
   culled. Unix pipes are free and never touched. Proximity is measured,
   never claimed — geography would be a lie, milliseconds are truth.

`super_announce` frames are infrastructure, excluded from the chronicle
like `hardware_alert`, and minds ignore them (each mesh also snoops its
own broadcast stream into the directory, so relay-only nodes learn supers
they never dialed). WAN addresses are operator-asserted
(`HIVEMIND_ADVERTISE=host:port`); the mesh never guesses reachability
behind NAT. There is no election, no failover protocol, no privilege to
seize — preferred transit, nothing more.
