# Configuration

## Environment Variables

### Relay
| Variable | Description | Default |
|----------|-------------|---------|
| `RELAY_PORT` | HTTP listen port | 8080 |
| `HIVEMIND_CIPHER_KEY` | Fleet root key (64 hex) | Required |
| `HIVEMIND_CIPHER_KEY_PREV` | Previous root for rotation | Optional |
| `RELAY_MQTT` | Pin to specific broker | Optional |
| `HIVEMIND_MQTT_TLS` | Disable TLS (off=disable) | TLS required |

### Minds
| Variable | Description | Default |
|----------|-------------|---------|
| `HIVEMIND_CIPHER_KEY` | Fleet root key | Required |
| `HIVEMIND_CIPHER_KEY_PREV` | Previous root | Optional |

### World
| Variable | Description | Default |
|----------|-------------|---------|
| `HIVEMIND_RELAY_URL` | Relay base URL | Required |
| `WORLD_CONTINENTS` | Number of continents | 7 |
| `WORLD_MAX_LOAD1` | 1m load limit (0=disabled) | 0 |
| `WORLD_MAX_LOAD5` | 5m load limit (0=disabled) | 0 |

### Gaze
| Variable | Description | Default |
|----------|-------------|---------|
| `HIVEMIND_RELAY_URL` | Relay base URL | Required |
| `GAZE_PORT` | HTTP port | 8090 |

## Key Generation
```bash
# Generate fleet key
head -c 32 /dev/urandom | od -An -tx1 | tr -d ' 
' > .relaykey
chmod 600 .relaykey
```