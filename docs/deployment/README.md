# Deployment Documentation

## Guides
- [Configuration](configuration.md)
- [Runbook](runbook.md) - Operational procedures
- [Checklist](checklist.md)

## Quick Start
```bash
# Generate fleet key
head -c 32 /dev/urandom | od -An -tx1 | tr -d ' 
' > .relaykey
chmod 600 .relaykey

# Build and deploy
make build-release
make stack
```