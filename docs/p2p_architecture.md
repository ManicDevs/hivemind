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

---

## 5. The Symmetric Peer Mesh (Local Transport)

No roles. Every node in `-mode peer` owns one socket file and discovers
equals through the filesystem (`network.go`, `PeerMesh`):

- Address = path: `/tmp/hivemind-<node>.sock`. Node name comes from
  `-node` (sanitized) or defaults to `<hostname>-<pid>`.
- Discovery ticks every 2s (`filepath.Glob`); **the dial rule** — only dial
  peers lexically greater than yourself — gives every pair exactly one
  initiator with no election and no master.
- First line on a new connection is a `{"node": ...}` handshake; self-dials
  are dropped, duplicate races keep the registered link.
- Sockets unreached for >5s may be swept as crash corpses (never before a
  grace period, never on mere refusal).
- The shutdown registry records only links that actually carried a decoded
  frame — conversations, not intent.

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

Confidentiality is explicit about its limits: with `HIVEMIND_CIPHER_KEY`
(a 32-byte env value) set, relay frames are confidential; without it, the
code prints a warning and uses the committed static key — frames are then
**signed-and-public with obfuscation only**. Treat the default relay as a
public broadcast until key management lands. All relay loops are
context-canceled on shutdown; nothing leaks goroutines.

---

## 7. Souls: What Death Keeps

`.hive_memory/<name>.soul`, JSON, written atomically (temp + fsync +
rename: readers see the old soul or the new one, never half of either).
A corrupt file is quarantined beside the living ones (`*.corrupt-<unix>`)
and a fresh mind is born — evidence survives, denial doesn't.

Persisted per mind: birth, lives, fitness, genome, thoughts, known peers,
death pain/stress (which steer the successor's epigenetic mutation),
thoughts-at-birth (fitness is per-life, never double-counted), and the
identity seed. The Overmind additionally persists chronicle offset and
genesis mark, so its patience survives the apocalypse too.

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
