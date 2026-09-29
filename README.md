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

# node.json
{ "mode":"peer", "node":"juno-north",
  "minds":["Alpha","Beta","Gamma"],
  "health_addr":"127.0.0.1:9090" }     # /healthz + /metrics

# Legacy flags still work unchanged: hivemind -mode peer -node eu-b
```

Language is env-driven as before: the accountless keyless pool
(OVH/Kilo/Pollinations) plus a measured local-Ollama primary
(`HIVEMIND_OLLAMA_MODEL` forces it on a slow CPU box). Graceful shutdown
reports engine telemetry: `📊 [ENGINE] node … stood for … thoughts, replies`.

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
