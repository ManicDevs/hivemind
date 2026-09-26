# Threat Model

## Assets
| Asset | Classification | Impact if Compromised |
|-------|----------------|----------------------|
| Fleet Root Key (HIVEMIND_CIPHER_KEY) | TOP SECRET | Total fleet compromise (28 nodes) |
| Per-Minute Leaf Keys | SECRET | Frame decryption (3-min window) |
| Identity Handles / Genomes | SECRET | Node impersonation; swarm manipulation |
| Swarm State / Telemetry | SECRET | Operational intelligence |

## Threat Actors
| Actor | Capability | Intent |
|-------|------------|--------|
| APT / Nation-State | Network intercept, broker compromise, supply chain | Persistent access, traffic analysis, key extraction |
| Malicious Broker Operator | Infrastructure control | Traffic analysis, selective dropping, injection |
| Insider Threat | Source access | Backdoor insertion, key exfiltration |
| Opportunistic Attacker | Automated scanning | Disruption, resource exhaustion |

## Mitigations
| Threat | Mitigation |
|--------|------------|
| Network intercept | TLS 1.2+, AES-256-GCM sealing |
| Broker compromise | Namespace isolation; sealed frames |
| Key exfiltration | Anti-RE; stripped binaries; key rotation |
| Supply chain | Reproducible builds; pinned deps |
