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

## Architecture

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
