# HIVEMIND Deployment

Facts in this document were read from the machine, not assumed. Anything I could
not measure is marked **UNVERIFIED** rather than asserted.

---

## 1. Measured inventory — control node

Read from `/proc/cpuinfo`, `/sys/class/net`, `/sys/block`, `lscpu`:

| Component | Measured value |
|---|---|
| CPU | Intel Xeon E5-2680 @ 2.70GHz |
| Topology | 1 socket, 8 cores, 16 threads |
| Cache | L2 2 MiB ×8, **L3 20 MiB** |
| RAM | 31.3 GiB (`MemTotal: 32789932 kB`) |
| Storage | `sda` 238.5 GB, rotational=0, **Samsung SSD 850** |
| NIC (data) | `enp1s0` Intel `8086:10d3` (82574L), driver `e1000e`, **up @ 100 Mb/s full duplex** |
| NIC (2nd) | `eno1` Intel `8086:1502` (82574L), driver `e1000e`, **down** |
| AMT / MEI | **absent** — no `8086:15b7` (82579LM), no `/dev/mei*`, no `amttool` |
| Kernel | `6.8.0-142-generic` |
| Link errors | 0 rx/tx errors, 0 CRC, 0 collisions |

## 2. Corrections to the previous topology spec

The earlier "HDCM-INFRA-2026-V2" document was partly real hardware wrapped in
impossible claims. Corrected against measurement:

| Claim | Reality |
|---|---|
| 1 TB local SSD workspace | **238.5 GB** Samsung SSD 850 |
| AMT management via 82579LM (Port 1) | Both NICs are 82574L. **No 82579LM, no AMT.** |
| Wake Z820 from 0 W over AMT | Not possible — no AMT controller exposed |
| IDE-Redirect / Serial-over-LAN boot | AMT features; unusable without AMT |
| "stripped Go kernel image" boot | **Go is userspace, not a kernel.** It cannot boot hardware. |
| 100% diskless vault | This node boots from a real SSD. Spec also contradicts itself (3× HDD vs. referenced "physical SSDs"). |
| 2.5 Gb/s port "bypasses internal traffic" | A 1 Gb/s NIC caps at 1 Gb/s. A 2.5 Gb/s *LAN* port adds nothing, and WAN bandwidth is irrelevant to a LAN mesh. |
| "40 MB+ L3 smart cache" | **20 MiB** |
| 1 Gb/s link | **100 Mb/s** — see below |
| 0 W sleep protects SSD endurance | No SSDs appear in the vault inventory at all |
| "SOVEREIGN OPERATIONAL SPECIFICATION" | No such thing for a private LAN; dropped |

## 3. Blocking issue: the link is 100 Mb/s, not 1 Gb/s

`enp1s0` negotiated **100 Mb/s full duplex** while the spec assumes 1 Gb/s, and
this is the entire mesh + fabric + inference path.

The signature is diagnostic: **exactly 100 Mb/s with zero CRC errors and zero
collisions.** A healthy Gigabit link would either reach 1000 Mb/s or drop/raise
errors. A clean 100 Mb/s link means the physical layer is only wiring **two of
the four pairs** — Gigabit BASE-T requires all four.

Most likely, in order:
1. Patch cable is Cat5, damaged, or badly terminated (check the 8 pins)
2. Switch port is 10/100-only, or a port on an unmanaged switch
3. A NIC is stuck in a forced-100 fallback

Action, cheapest first:
```bash
# force autoneg off/on and re-negotiate
sudo ethtool -r enp1s0
ethtool enp1s0 | grep -E 'Speed|Duplex|Auto-neg'
```
If it still reports 100 Mb/s, swap the patch cable, then the switch port.
100 Mb/s is roughly **10× below** what the fabric and world replication want.

## 4. Second machine

The Z820's CPU, RAM, disks and AMT state are **UNVERIFIED** — it is a separate
host and is not reachable from here. Given no AMT was found on the control node,
remote wake cannot be assumed. It must be checked in person:

```bash
# run ON the Z820
lscpu | grep -E 'Socket|Core|Model name'
grep MemTotal /proc/meminfo
lsblk -d -o NAME,SIZE,ROTA,MODEL
ls /dev/mei*          # absent => no AMT
```

Until that runs, the two-node cluster is a design, not a deployment.

## 5. Running it (this node, verified)

Pure Go, no CGO. Verified under a deliberately hostile environment:

```bash
export HIVEMIND_LLM_ENDPOINT=http://localhost:11434/api/generate
export HIVEMIND_LLM_MODEL=llama2:7b

CGO_ENABLED=1 CGO_LDFLAGS='-L/tmp/llama.cpp/build -lllama' make build-release
make stack N=2 WORLD_CONTINENTS=3 WORLD_MAX_MEM=0.9
```

`make stack` self-heals stale processes and refuses to claim success unless
relay, world, fabric and hivemind are all live.

### Memory ceiling

This node has 31.3 GiB and `cmd/world` refuses to run above `max-mem`. The
default `0.85` trips on a busy desktop; use `WORLD_MAX_MEM=0.9` and keep
`WORLD_CONTINENTS=3` (15 minds). The full 7-continent topology does not fit.

### Verify

```bash
curl -s http://localhost:8080/healthz | jq '.mesh'   # tls, cleartext, live, sealed
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8090/   # dashboard
grep '📉' logs/nodes.log            # learning: loss must trend down
grep '💭' logs/nodes.log            # real LLM speech
grep 'Remote LLM enabled' world-report/logs/*.log | wc -l   # 3 per live mind
```

Geo minds log to `world-report/logs/<node>.log`, **not** `logs/world.log` —
that file only carries the parent's own output.

## 6. Inference capacity

One CPU-bound `llama2:7b` on this 16-thread box serves 15 minds, so requests
queue and speech is slow (measured ~4m47s wall for a 4.5s generation under full
load). Options, in order of value:

1. Reduce mesh size (`N=2 WORLD_CONTINENTS=3`, or fewer)
2. Smaller model (`qwen3:14b` is installed but larger — use a sub-4B model instead)
3. GPU, if one is ever added to the Z820
4. `OLLAMA_NUM_PARALLEL` to raise concurrency once the link is fixed

Note: speech is asynchronous now, so a slow model no longer stalls the
conscious tick — it only reduces how often minds speak.

## 7. What is deliberately not here

- No load balancer, public IPs, or cloud diagrams. The mesh is two machines on
  one private LAN.
- No invented `install.sh`, registry, or domain names.
- No claim that TLS certificates exist; they must be generated per §8 of the
  security assessment and were not created here.
