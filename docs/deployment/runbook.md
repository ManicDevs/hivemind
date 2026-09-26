# Operational Runbook

## Quick Commands
```bash
# Deploy
make stack

# Health check
curl http://localhost:8081/healthz | jq '.mesh'

# Probe test
curl -s -X POST -H 'Content-Type: application/json' -d '{"test":"live"}' http://localhost:8081/probe

# Anti-RE status
grep "ANTI-RE" logs/relay.log

# TLS status
grep "transport TLS" logs/relay.log

# Emergency shutdown
make stack-down

# Restart
make stack-restart
```

## Monitoring
```bash
# Health
curl http://localhost:8081/healthz | jq '.mesh'

# Anti-RE status
grep "ANTI-RE" logs/relay.log

# TLS status
grep "transport TLS" logs/relay.log

# Broker status
curl -s http://localhost:8081/healthz | jq '.mesh.brokers[]'
```

## Rotation
```bash
# New root
echo "NEW_KEY" > .relaykey

# Or with previous root
HIVEMIND_CIPHER_KEY_PREV=OLD_KEY HIVEMIND_CIPHER_KEY=NEW_KEY make stack-restart
```

## Emergency
```bash
# Immediate shutdown
make stack-down

# Hard restart
make stack-restart
```