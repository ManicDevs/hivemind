# HIVEMIND SECURITY ASSESSMENT

**Status:** Draft — self-assessment, not an independent audit
**Applies to:** `gitlab.torproject.org/cerberus-droid/hivemind` at the current working tree
**Method:** source review plus the adversarial simulation under `internal/fabricsim`

---

## 0. What this document is, and is not

An earlier revision of this file was formatted as an independent technical
evaluation and opened with an authorisation for production deployment. That was
not a real assessment. No independent team reviewed this code, nobody signed an
authorisation, and several of its claims did not match the source. This rewrite
drops the framing and states only what can be checked against the tree.

Two consequences worth being blunt about:

- Nothing here should be read as a compliance attestation, a penetration test
  result, or a certification. A qualified third party has not evaluated this
  system.
- Claims are marked with how they were established. Anything unverified is
  labelled unverified rather than omitted.

---

## 1. Scope and verification method

| Aspect | Value |
|---|---|
| Language | Go, pure-Go release builds (`CGO_ENABLED=0`) |
| Transport | MQTT over TLS 1.2+ to broker federation |
| Cell crypto | ChaCha20-Poly1305 over AES-256-GCM, hourly keys, ed25519 node keys |
| Simulation | 56 in-process nodes (28 defender / 28 attacker), 7 zones × 4 tiers |
| Verification | `go vet`, `go test ./...` in both build modes, `make sim-preflight`, cross-compilation, source review |

Every figure in section 5 comes from a command in this repository. Commands are
given so the numbers can be reproduced rather than taken on trust.

---

## 2. Cryptography

### 2.1 Key hierarchy (as implemented)

`internal/hivemind/keys.go`:

```
root (HIVEMIND_CIPHER_KEY, 32 bytes)
  └── day(D)  = HMAC-SHA256(root, "hivemind/v2/day|" || D)
        └── hour(H) = HMAC-SHA256(day(H/24), "hivemind/v2/hour|" || H)
              └── leaf(L) = HMAC-SHA256(hour, "hivemind/v2/leaf|" || L)
```

Each tier is derived independently from its parent rather than chained to its
predecessor, so holding one hour key does not reveal the next day's. The label
`v2` is embedded in each derivation string, so a v1 and a v2 peer fail against
each other diagnostically instead of as generic garbage.

**Leaf acceptance window:** the live UTC minute plus the two before it
(`leafWindow = 3`). A frame sealed under a key outside that window is refused.

**Correction against the previous revision of this document:** it described a
`hivemind/v2/minute` tier and a "±3 minutes (±1 current)" window. The code uses
the label `hivemind/v2/leaf`, and the window is a fixed three-minute lookback
with no narrower mode. The earlier description did not match the source.

### 2.2 Cell protection

`cmd/fabric/main.go`: `sealCell` applies ChaCha20-Poly1305 (`AAD="L1"`) and then
AES-256-GCM (`AAD="L2"`), each with an independently derived nonce.
`openCell` reverses both layers and rejects any plaintext outside
`[headerSize, maxFrame]`.

This is layered encryption, not forward secrecy: the two layers share one key
material, so the inner layer does not protect against a compromise of the outer
key. The per-hour key turnover is what bounds the exposure window.

### 2.3 Frame authentication

Frames are signed with ed25519. `cmd/fabric/main.go` derives a per-node key with
`deriveEd25519` and verifies the claimed sender's frame before trusting its
contents.

**Limitation:** cell *signing* and cell *encryption* are separate. Authentication
happens after decryption, so a node must allocate and decrypt before it can
reject a forged frame. The structural length check in `unpackCell` runs first,
which is what prevents a hostile length field from driving a large allocation;
without it, this ordering would be an amplification vector.

---

## 3. Transport

MQTT connections require TLS 1.2 or higher with full certificate verification
against system roots (`cmd/relay`). Relay payloads are sealed before they leave
the node; `TestSealHidesPayloadFromTheWire` asserts that a sealed frame contains
none of the plaintext canary.

**Verified:** TLS is mandatory in the relay path.

**Not verified:** broker endpoint inventory, certificate pinning, and cipher
suite configuration were not independently tested. The previous revision listed
twelve specific broker endpoints as a verified fact; that inventory has not been
re-confirmed and is not repeated here.

---

## 4. Release build

`make build-release` builds every binary with `-tags release -ldflags "-s -w"`,
stripping symbols and enabling the anti-RE path. All binaries build with
`CGO_ENABLED=0`.

The adversarial simulator is **excluded from release builds**. It compiles only
under the `sim` build tag (`cmd/fabric/sim_dev.go`, `cmd/world/sim_dev.go`), with
inert counterparts in `sim_off.go`. Verified for `bin/fabric` and `bin/world`:

- zero `fabricsim` symbols (`go tool nm`)
- no `-sim` flag in `-h` output
- `./bin/fabric -sim` fails with `flag provided but not defined`

Release nodes report only measurements from their own sockets, via
`cmd/fabric/telemetry.go`.

---

## 5. Adversarial simulation

### 5.1 Why the detector's scores mean something

`internal/fabricsim` models a 56-node matrix and runs four hostile vectors
against the defence. Its central property is that the detector **cannot see the
attack label**.

`AttackKind` records how the simulator built a frame. It is used for scoreboard
bookkeeping only. An earlier revision consulted it in four places — the malformed
check, the signature check, the fail-closed seal, and the dwell counter — which
meant every rejection was correct by construction and the run proved nothing
about the defence. `signaturePlausible` in particular was
`return p.Kind != KindForgery`, a check that agreed with the label it was given.

Frames are now signed with real ed25519 keys derived per node from the run seed
(`internal/fabricsim/keys.go`). Verification uses the public key registered for
the identity the frame **claims**, so an attacker with a genuine key still
fails by claiming someone else's identity. `TestDetectorIgnoresTheKindLabel`
relabels valid frames with every hostile kind and asserts the verdict does not
change, and asserts the converse: a forged frame relabelled honest is still
refused.

### 5.2 Measured results

`make sim-preflight` (`build-world-sim` + 56-node matrix, fail-closed gate):

| Seed | Honest pass | Hostile refused | False positives | Seal |
|---|---|---|---|---|
| 1 | 100.00% | 100.00% | 0 | engaged |
| 2 | 100.00% | 100.00% | 0 | engaged |
| 3 | 100.00% | 100.00% | 0 | engaged |
| 99 | 100.00% | 100.00% | 0 | engaged |
| 12345 | 100.00% | 100.00% | 0 | engaged |

**What "refused" means here.** Recall counts only the vectors where refusing an
individual frame is the correct outcome: a structural overrun (`DeclaredLen`
disagreeing with actual length) and a broken signature. Two vectors are excluded
because refusing their frames would be wrong:

- *Flood* — the attacker's frame is well-formed and correctly signed. A flood is
  a valid frame sent in volume, so the defence catches the damage through the
  transport's loss modelling and receiver-side sequence-gap detection.
- *Clock drift* — only an attack outside the ±3-minute window. The attacker
  deliberately aims inside the window about half the time, and those frames must
  be served: refusing every 2-minute clock offset would drop honest nodes on
  ordinary drift. The count of frames served this way is published as
  `DriftInWindow` so the cost of the tolerance is measured rather than hidden.

An earlier revision reported ~98% recall using a denominator that included both
excluded vectors. That number was inflated by the ground-truth shortcut: it
counted frames the defence correctly let through as if it had caught them.

### 5.3 Gate behaviour

The gate fails (exit 1) when honest consensus drops below 99%, when any honest
frame is refused, when recall falls below 90%, when the fail-closed seal never
engages, or when any disk write occurs. Verified negatively: disabling the
signature check drops recall to 77.71%, prevents the seal from engaging, and
exits 1.

### 5.4 What the simulation does not establish

- **Loss-detection precision is low.** Sequence-gap inference reports
  substantially more loss than the transport actually caused (precision around
  0.17–0.32 depending on run). This is inherent to inferring loss from
  missing sequence numbers: reordering and coalescing look like loss. Treat the
  detector as directional, not quantitative.
- **No adversarial input reaches production code.** The simulation exercises
  `internal/fabricsim`'s model of the defence. It does not fuzz
  `unpackCell`, the relay, or the MQTT path.
- **Keys are derived by a scheme chosen for the simulation.** `nodeKey` uses a
  SHA-256 chain, not the production derivation and not a password KDF. It makes
  forgery detectable in-process; it is not a protection mechanism.
- **Single-box assumptions.** The simulation is single-process and in-memory.
  It cannot exhibit the concurrency, partial failure, or partition behaviour of
  a real distributed mesh.

---

## 6. Resource and side-effect properties

`TestCorePackageCannotTouchTheHost` parses the imports of the seven core
simulator files and fails if any of them imports an I/O package. Presentation
files (`scoreboard.go`, `output.go`, `tty_unix.go`, `tty_windows.go`) are the
only ones permitted to render, and they write to a terminal and to `io.Writer`,
never to a named file. The preflight gate additionally fails on any nonzero
`DiskWrites` count.

This is a structural guarantee about the simulator's core, checked by a test. It
is not a sandbox, and it says nothing about the production binaries.

---

## 7. Outstanding issues

| # | Issue | Severity | Status |
|---|---|---|---|
| 1 | Loss-detection precision far below 1.0 | Medium | Known; documented in 5.4 |
| 2 | Key window serves clock-drift frames inside ±3 min | Low | Measured, published as `DriftInWindow` |
| 3 | Authentication occurs after decryption | Low | Mitigated by the pre-allocation length check |
| 4 | Cell encryption is layered, not forward-secret | Low | Accepted; hourly turnover bounds exposure |
| 5 | No adversarial input reaches production parsing paths | Medium | Open |
| 6 | No independent security review has occurred | High | Open; this document is a self-assessment |
| 7 | Single Ollama model queues requests for 15 minds | Operational | Open |
| 8 | No verified physical multi-server or public fabric | Medium | Open |

Items 6 and 8 are the material ones. No amount of simulation substitutes for
reviewing the code that actually runs, or for observing the system under real
network conditions.

---

## 8. Reproducing these results

```sh
make build-release          # release binaries, no simulator
make clean-release-sim      # remove simulator-enabled binaries
make build-fabric-sim       # dev binary with the matrix
make build-world-sim
make sim-preflight          # adversarial gate; non-zero if the defence fails

CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go test -gcflags=all=-d=checkptr ./... -count=1
CGO_ENABLED=0 go vet -tags sim ./...
CGO_ENABLED=0 go test -tags sim ./... -count=1
CGO_ENABLED=0 go test ./internal/fabricsim/ -run TestNoUnsynchronisedStructWrites -count=1
CGO_ENABLED=0 go test ./internal/fabricsim/ -run TestConcurrentRunKeepsAccountingInvariants -count=1
```

`go test -race` has not been run: the environment has no C compiler, and the race
detector requires cgo. In its place, `internal/fabricsim/concurrency_static_test.go`
applies a pure-Go static analysis over `Transport` and `Detector`, walking every
method in statement order and failing on any write to a field that is neither
atomic nor provably under the mutex — including cross-method guarantees resolved
through the in-file call graph. Because the walker cannot observe runtime state, it
is deliberately conservative: any string-typed precondition, a leaked-atomically
plain-field write under a differing lock, or a single-touch race outside these two
types is out of scope, and stays out of scope until a C toolchain provides `-race`.
`TestConcurrentRunKeepsAccountingInvariants` drives the same concurrent worker
pools with adversarial GOMAXPROCS, asserting `delivered ≤ enqueued` and
`accepted + rejected ≤ offered` per kind on every interleaving; `-d=checkptr`
guards unsafe-pointer misuse in the same pure-Go run.
