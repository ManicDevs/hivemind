# Production Deployment Checklist

## Pre-Deployment
- [ ] Fleet key generated and stored offline (`.relaykey`)
- [ ] `.relaykey` permissions set to 0600
- [ ] Go version 1.21+ verified
- [ ] All tests passing (`make test`)
- [ ] Release build successful (`make build-release`)

## Deployment
- [ ] `make stack` completes without errors
- [ ] Health endpoint returns `tls=true`, `cleartext=0`
- [ ] `v1_absent=true` (no v1 escape hatches)
- [ ] Anti-RE logs visible: `🛡️ [ANTI-RE] runtime hardening active`
- [ ] 4/4 brokers connected via TLS

## Post-Deployment
- [ ] Probe test successful (`curl -X POST ... /probe`)
- [ ] Health endpoint shows `sealed=true`
- [ ] Gaze dashboard accessible at `:8090`
- [ ] Logs show `ANTI-RE` initialization
- [ ] `v1_absent=true` in healthz

## Monitoring
- [ ] Health endpoint monitored
- [ ] Broker connectivity alerts configured
- [ ] Anti-RE logs monitored for anomalies
- [ ] Key rotation schedule documented
