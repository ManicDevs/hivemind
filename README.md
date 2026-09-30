# HIVEMIND

> Three minds. One swarm. And something watching.

A decentralized multi-agent consensus mesh in Go — 28 autonomous minds across 7 continents, sharing sealed thought-frames over a federated MQTT broker infrastructure, with genomes that mutate across deaths.

## Quickstart

```bash
# Generate fleet key (one-time)
head -c 32 /dev/urandom | od -An -tx1 | tr -d ' 
' > .relaykey
chmod 600 .relaykey

# Build and deploy (release build with anti-RE hardening)
make build-release
make stack

# Health check
curl http://localhost:8081/healthz | jq '.mesh'
```

## Engine

The `hivemind` binary is a thin shell around `internal/engine` — one call
builds the whole universe (swarm + peer mesh + minds + overmind + health +
telemetry), runs it under supervision, and tears it down so every soul is
persisted on disk before anyone reports.

```bash
hivemind version                       # build/runtime info
hivemind help                          # subcommand + flag reference

hivemind                               # standalone, Alpha/Beta/Gamma
hivemind engine -mode peer -node asia-a
hivemind engine -config node.json      # JSON overlay config

hivemind seed -journal data/law.journal data/law.json   # provision the lawbook
hivemind engine -law data/law.journal   # bind law at boot (JSON: "law_journal")

# node.json
{ "mode":"peer", "node":"juno-north",
  "minds":["Alpha","Beta","Gamma"],
  "health_addr":"127.0.0.1:9090",      # /healthz + /metrics
  "law_journal":"data/law.journal" }   # persistent legal knowledge substrate

# Legacy flags still work unchanged: hivemind -mode peer -node eu-b
```

## Lawbook

`hivemind seed` provisions legal/normative text (statutes, contract clauses)
as a persistent, append-only journal — your own durable law. Every mind on
every node bound to the same journal retrieves the provisions relevant to the
moment it is thinking or being asked, and the reply prompt lets it quote them
verbatim with audit handles (clause id, source, tag). Retrieval is honest
pure-Go term scoring: no embeddings, no model creativity in the recall — the
LLM reasons, the book grounds. Re-seeding is idempotent (clauses dedupe by
derived id); an interleaved write never corrupts the book (corrupt lines are
skipped); node death is meaningless to law — the journal reloads at every boot:
`📜 [LAW] 8 clauses loaded from data/law.journal`. Every graceful stop reports
how many clauses compile into the standing universe.

Language is env-driven as before: the accountless keyless pool
(OVH/Kilo/Pollinations) plus a measured local-Ollama primary
(`HIVEMIND_OLLAMA_MODEL` forces it on a slow CPU box). Graceful shutdown
reports engine telemetry: `📊 [ENGINE] node … stood for … thoughts, replies`.

## Training

`make train` (or `hivemind-train -apply`) distills the lawbook into a
standing local counsel model on the loopback Ollama daemon. Every provision
becomes supervised question/answer pairs (ground truth = the clause
verbatim), a Modelfile nests the full corpus inside the model's SYSTEM block
with audit handles, and the daemon builds `hivemind-counsel`. `make
train-dry` writes the artifacts (dataset + Modelfile) without touching the
daemon; `-adapter` weaves a LoRA `.safetensors` — a real gradient tune plugs
in at that seam (the repo itself is pure Go and does not autograd).

The mesh auto-preferences a locally created model whose tag carries
"hivemind" in its model race, so trained weights win over every base model
the daemon serves; `HIVEMIND_OLLAMA_MODEL=hivemind-counsel` forces the
choice on a slow CPU box where the viability window would otherwise rule it
out. A live grid confirms the trained model answers with verbatim citations
(and states plainly when no provision applies).

Creation without verification is just a bigger hallucinator, so `make eval`
scores groundedness live. Every answer must be recognisably corpus-supported:
quote a provision verbatim, reference its audit handle, cite a provision's
source with supporting words, reuse ≥ half of a provision's distinctive
trigrams, or carry a contiguous run of its words (abbreviated quotes still
count). Confabulated citations fail — the scorecard prints each verdict
(`✓`/`✗`) and audits the misses. A `⚠ MIS-CITE` verdict catches the subtle
case where a grounded answer echoes one provision while attributing it to a
different one (e.g. quoting the equal-protection clause under "Article I,
Section 9"). Corpus provenance is open too:
`hivemind seed` takes a JSON array, markdown/plain text (`# heading` names
the source, `Tag:` a line labels the next paragraph, blank lines break
clauses), or an `http(s)://` URL fetched under a 30s budget — so the whole
pipeline runs from statute text to measured counsel on one machine.

The scorecard is not just a lab tool: `make eval`'s scorer doubles as a
boot-time **counsel groundedness gate**. With `HIVEMIND_GROUNDING_GATE=1` the
engine, before any mind is born, asks the configured counsel two lawbook
questions and requires every probe to ground (strict admission, no averaging).
Admission logs its fitness — `🛡️  [GATE] counsel hivemind-counsel admitted
(grounded 2/2) - fit to speak for the swarm` — and the trained counsel becomes
the primary voice. A model the daemon does not serve is denied up front and
its rebuttal travels through the language layer before a single token is
spent: `🛡️  [LANGUAGE] Local counsel declined by groundedness gate (…) -
cloud keyless pool stands in`. A dead daemon fails the gate **open**: stress
must never gate a node's birth. Counts ride the shutdown telemetry
(`counsel gate: admitted:hivemind-counsel (2/2 grounded)`). Probe and answer
windows are tunable: `HIVEMIND_GATE_BUDGET` (per gate probe, default 240s —
wide enough for a cold-loading counsel) and `HIVEMIND_ANSWER_BUDGET` (per
`/api/chat`, default 10m); both are parsed as Go durations and malformed
values fall back to the defaults.

The lawbook is deliberately open law — the full organic library of two
systems in one book. `data/law.json` carries the US Constitution (Preamble
through the Articles, every ratified amendment from the Bill of Rights to the
XIII/XIV/XV and XIX, plus the founding statutes: Civil Rights Act 1964
Titles II and VII, Voting Rights Act 1965, Sherman Act, and the
Administrative Procedure Act) and a contract example. `data/law_statutes_uk.md`
carries the UK instruments — Magna Carta 1297 cll. 1 & 29, the Petition of
Right 1628, Habeas Corpus Act 1679, the complete Bill of Rights 1689
(articles 1-9 and 11-12, era spelling), Act of Settlement 1701 ss. 1-4, the
Act of Union 1707, the Parliament Acts 1911/1949, the Representation of the
People Acts 1918/1928, the Human Rights Act 1998 (s. 3, s. 4, s. 6 and
Schedule 1 arts. 6, 8, 9, 10, 11, 14; © Crown / OGL v3.0), and the
Constitutional Reform Act 2005 s. 3. Seeding every source file grows one
shared book — 72 provisions — and `hivemind-counsel` is retrained and
re-evaluated across the whole corpus, so a single counsel answers from both
the US and UK provisions.

A book that large outgrows llama2's native window, so the counsel is created
and dialed at the same context: `HIVEMIND_NUM_CTX` (default **4096**, raised
to **8192** for the full book; values below 4096 or malformed fall back).
Every consumer reads the same knob — `hivemind-train` creation, the
evaluation/duel harness, and the mesh's own local-counsel calls — so the
groundedness the gate measures is the groundedness the swarm speaks with.
Re-create the model whenever the corpus or the window changes.

The two systems are cross-examined head-to-head with
`hivemind-eval -probes …`: US-anchored and UK-anchored questions are each
scored against the whole book, and *clash* probes demand both jurisdictions
in one answer (establishment of religion, the bearing of arms) so the scorer
punishes a counsel that reaches across the Atlantic for the wrong provision.

## Architecture

- **Engine** — `internal/engine` orchestration: lifecycle, config schema, telemetry
- **28 Minds** across 7 continents (4 per region)
- **Relay** — HTTP/MQTT bridge with TLS 1.2+ broker federation
- **World/Fabric** — Orchestration and adaptive routing
- **Gaze** — Real-time dashboard and telemetry

## Documentation

- [Architecture](docs/architecture/README.md)
- [Deployment](docs/deployment/README.md)
- [Development](docs/development/README.md)
- [Security](docs/security/README.md)

## Security

- **Hard v1→v2 cutover** — No v1 escape hatches
- **Strict TLS 1.2+** — 4/4 brokers live, 0 cleartext
- **Anti-RE hardening** — String encryption, debugger detection, stripped binaries
- **Forward secrecy** — Independent HMAC per period, 3-min key rotation
- **NSA-standard assessment** — [Security Assessment](docs/security/HIVEMIND_SECURITY_ASSESSMENT.md)

## Commands

```bash
# Build all (development)
make build-all

# Build release (anti-RE hardening)
make build-release

# Deploy stack (28-node fleet)
make stack

# Health check
curl http://localhost:8081/healthz | jq '.mesh'

# Probe test
curl -X POST -d '{"test":"live"}' http://localhost:8081/probe

# Emergency
make stack-down
make stack-restart
```

## Testing

```bash
# All tests
make test

# Specific packages
go test ./cmd/... -count=1
go test ./internal/... -count=1

# Lint
gofmt -l cmd/ internal/
go vet ./...
```

## License

MIT License - see LICENSE file
