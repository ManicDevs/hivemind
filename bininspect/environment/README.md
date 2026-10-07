# environment

Host probes for virtualisation, sandboxing, and analysis instrumentation.

A separate module, deliberately, so importing it is an explicit decision rather
than a transitive side effect of wanting file analysis.

## The distinction

| | asks |
|---|---|
| `github.com/hivemind/bininspect` | does **this binary** contain anti-analysis capability? (reads a file) |
| **this package** | is **this machine** a VM, sandbox, or under a debugger? (inspects the host) |

Both are legitimate triage signals. They are separate products and this one is
isolated on purpose.

## No assembly, no cgo, no external tools

This package is **pure Go**. There are no `.s` files, no cgo, and no shelling out
to `systemd-detect-virt`, `dmidecode`, or `lscpu`. Every vendor table and every
parser is written here, so nothing depends on a third-party detection database
whose contents and update cadence we would not control.

An earlier revision implemented `CPUID` and `RDTSC` in hand-written assembly
behind a `//go:build amd64` tag. That was the wrong call for this codebase:

1. The project's documented invariant is "pure Go, no CGO". (Go assembly does
   *not* require cgo — `CGO_ENABLED=0 go test` passes with a `.s` file — but
   hand-written assembly is still a real departure from the convention.)
2. It bought almost nothing. The hypervisor-present bit
   (`CPUID.01H:ECX[31]`) is already published by the kernel as the `hypervisor`
   flag in `/proc/cpuinfo`, so it is readable with no instruction at all.
3. The only genuinely-unavailable piece was leaf `0x40000000`, the hypervisor
   vendor string — and DMI names the vendor *and* the product, which is stronger.

`RDTSC` is dropped for a second reason: it is not serialising on modern x86, so
it is a poor timing instrument even where it exists.

**Consequence, stated plainly:** a hypervisor that implements leaf `0x40000000`
but is invisible to DMI, PCI, and `/proc/cpuinfo` will not be detected.

## Probes

| Probe | Source | Signal |
|---|---|---|
| DMI | `/sys/class/dmi/id/*` | vendor string — strongest single firmware signal |
| **SMBIOS (own parser)** | `/sys/firmware/dmi/tables/DMI` | walks raw structures, whole string table searchable |
| **PCI (own table)** | `/sys/bus/pci/devices/*/vendor` | virtio `0x1af4`, VMware `0x15ad`, QEMU `0x1b36`, Xen `0x5853`, VirtualBox `0x80ee`… |
| virtio | `/sys/bus/virtio/devices/` | driver-bound paravirtual devices |
| hypervisor flag | `/proc/cpuinfo` | kernel's view of the CPUID hypervisor bit |
| MAC OUI | `/sys/class/net/*/address` | `00:50:56` VMware, `52:54:00` QEMU, `08:00:27` VirtualBox |
| tracer | `/proc/self/status` | `TracerPid` — a debugger is attached |
| instrumentation | env vars + library paths | `LD_PRELOAD`, Frida, Sandboxie, Cuckoo, Detours |
| container | `/.dockerenv`, `/proc/1/cgroup` | container runtime — *not* virtualisation |
| timing | injected monotonic clock | spread across fixed work samples |

### Why virtio needs its own probe

A virtio-net NIC appears as an ordinary `/sys/class/net` entry and a virtio-blk
disk as an ordinary block device — the guest sees paravirtual hardware that
behaves like real hardware. The only reliable tell is the PCI vendor ID. That is
why the PCI probe exists, and why `qemuVirtioHost` in the tests is a fixture with
no vendor string anywhere: it proves the probe is independent of DMI.

### Why we parse SMBIOS ourselves

The kernel exposes a handful of *decoded* DMI attributes. That is lossy — it
reads fixed string slots for fixed structure types — so a vendor string in a slot
the kernel does not surface is invisible even though it is plainly in the table.
The parser walks the raw structures instead, so the whole string table is
searchable and an indicator can name the structure type it came from.

## Usage

```go
snap := environment.New(environment.Options{}).Probe()

if snap.IsVirtualised() {
    for _, ind := range snap.Indicators {
        fmt.Printf("[%s] %s (%.0f%%)\n", ind.Severity, ind.Title, ind.Confidence*100)
        for _, e := range ind.Evidence {
            fmt.Println("   ", e)
        }
    }
}
```

Try it on your own machine:

```bash
cd cmd/probe && go run .
```

## Deterministic in CI

`Options.FS` and `Options.Monotonic` are injected, so the whole probe suite runs
against a **synthetic host** with no reliance on the machine executing it. A test
that passes on a laptop and fails on a cloud runner is not a test — and here the
runner is often a VM, so it would fail *because* it detected one.

```go
snap := environment.New(environment.Options{
    FS:        fakeFS,          // synthetic /proc and /sys
    Monotonic: constantClock(0),
}).Probe()
```

`Options.Clock` must be **safe for concurrent use**. A Prober is safe to share,
but it cannot make an unsafe function safe. `time.Since` satisfies this trivially.

That contract was undocumented until `TestProberIsSafeForConcurrentUse` caught a
fixture whose sequence-returning "clock" produced 8 indicators in one goroutine
and 9 in another: interleaving scrambled the pairing of start and end readings.
The defect was in the fixture, not the Prober — but an undocumented contract is a
trap.

## Unsupported is not negative

`Snapshot.Unsupported` lists probes that could not run. Off Linux every
filesystem probe reports `ErrUnsupported` rather than a plausible-looking zero,
because folding "the question was never asked" into "checked, nothing found"
would let a caller conclude a bare-metal host was verified when it was in fact
never examined.

## Confidence, not proof

A DMI string saying "VMware, Inc." is strong evidence. A CPUID hypervisor bit is
strong evidence. Neither is proof: a machine can be a VM without advertising it,
and a bare-metal host can carry a leftover vendor string from an image. Every
Indicator carries a `Confidence` and its evidence, and the aggregate is
saturating rather than additive, so two weak hints cannot combine into a
certainty.

## Honest limit

From **unprivileged userspace the OS kernel mediates all hardware access**.
CPUID, SMBIOS, and PCI config space are reachable only through `/proc`, `/sys`,
`ioperm`, or `/dev/mem`. There is no userspace-only route. What we own is the
detection logic, the signature tables, and the parsers — not the hardware reads.

## Tests

19 tests, 85.9% coverage. Notable:

- `TestBareMetalHostProducesNoIndicators` — the negative test that makes every
  positive detection meaningful
- `TestVirtioDetectionWithoutVendorString` — proves the PCI probe stands alone
- `TestSMBIOSParserSurvivesMalformedTables` — firmware is assumed malformed
- `TestProbesAreDeterministic` — same host, byte-identical output, 25 runs
- `TestIndicatorIDsAreUnique` — two probes reading one observation once produced
  the same ID twice
- `TestProberIsSafeForConcurrentUse` — 32 concurrent probes, identical results