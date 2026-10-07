# bininspect

Static triage for detecting **anti-analysis capability** in PE and ELF binaries.

A Go module, standard library only. It answers one question for an analyst:
*does this binary try to notice, stall, or evade analysis?* — and returns the
evidence behind the answer, not just a verdict.

It reads binaries. It never executes, unpacks, injects, or modifies them.

---

## Install

Nested module, so it is self-contained and copyable:

```
go get github.com/hivemind/bininspect
```

Or vendor the directory into another module: it has **zero external
dependencies**, so nothing else needs to change.

```go
import "github.com/hivemind/bininspect"
```

---

## Usage

```go
a := bininspect.New(bininspect.Options{})

rep, err := a.AnalyzePath(ctx, "suspicious.exe")
if err != nil {
    return err
}

fmt.Println(rep.Summary())
// sample.exe: pe risk 72/100 (high), 9 findings; top: Direct system-call instruction...

for _, f := range rep.Findings {
    fmt.Printf("[%s] %s (confidence %.2f)\n", f.Severity, f.Title, f.Confidence)
    for _, e := range f.Evidence {
        fmt.Printf("    %s: %s @%#x\n", e.Kind, e.Detail, e.Offset)
    }
}

for _, action := range rep.Risk.RecommendedActions {
    fmt.Println("→", action)
}
```

For bytes already in hand:

```go
rep, err := a.AnalyzeBytes(ctx, image, "upload.bin")
```

Format is detected from file content, never from the extension, so a renamed
sample is still classified correctly.

---

## Cancellation and deadlines

Every entry point takes a `context.Context` first, because scanning a large
image with `FullStringScan` set is long enough that a caller needs a bound.

```go
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()
rep, err := a.AnalyzeReaderAt(ctx, r, size, "sample.bin", modTime)
// err == context.DeadlineExceeded if the scan outran the deadline
```

Cancellation is polled at section granularity inside the scan loops, so a
cancelled context returns in microseconds rather than finishing the work. A scan
cancelled partway through still returns the findings gathered so far, with a
warning marking them partial — a partial report carries real signal to a triage
pipeline, whereas an error discards it.

---

## Exporting for SIEM ingestion

`ExportJSON` is the supported serialisation path, so no consumer has to
reconstruct either the struct shape or the envelope.

```go
payload, err := rep.ExportJSON()
```

Output is a self-describing envelope:

```json
{
  "schema_version": "1",
  "generator": "bininspect/1",
  "format": "pe",
  "exported_at": "2026-10-07T12:00:00Z",
  "report": { "...": "..." }
}
```

Options for finer control:

```go
rep.ExportJSONWith(bininspect.ExportOptions{
    Indent:           false,  // pretty-print (roughly doubles the bytes)
    Envelope:         true,   // false = bare Report, for a pinned consumer
    ExportedAt:       "",     // omitted when zero, keeping output deterministic
    IncludeZeroScore: false,  // see below
})
```

Guarantees, each covered by a test:

- **Deterministic.** Byte-identical across repeated calls, so a golden file or a
  content hash over an event works. Compact output carries no trailing newline.
- **`omitempty` is honoured.** A minimal report does not ship a wall of
  null-valued keys, which is index overhead in most pipelines. Required fields —
  and the booleans `is_library` / `stripped`, so `false` stays distinguishable
  from absent — are always emitted.
- **No HTML escaping.** A Windows path or a marker string containing `<`, `>`,
  or `&` survives intact.
- **Zero-risk is refused by default.** "Analysed, nothing found" and "never
  analysed" must not look identical in a pipeline, so the first requires
  `IncludeZeroScore: true` and says so in the error.

`SchemaFields()` derives the field list by reflection, so the documented shape
cannot drift from the struct.

---

## What it detects

### 1. Static anti-analysis

| Detection | Method |
|---|---|
| **Packed / encrypted code** | Shannon entropy per section, size-aware banding (§ below) |
| **W+X memory** | `IMAGE_SCN_MEM_WRITE\|IMAGE_SCN_MEM_EXECUTE`; ELF `PF_W\|PF_X` |
| **Header anomalies** | Zeroed/future timestamps, `ET_EXEC` without ASLR, missing `DYNAMIC_BASE`, native/EFI subsystems, >96 sections, packer section names |
| **Anti-debugging imports** | `IsDebuggerPresent`, `CheckRemoteDebuggerPresent`, `NtSetInformationThread`, `OutputDebugString*`, invalid-handle probes |
| **Injection / hollowing imports** | `VirtualAllocEx`, `WriteProcessMemory`, `CreateRemoteThread`, `QueueUserAPC`, `SetThreadContext`, `NtMapViewOfSection`, … |

### 2. Dynamic anti-analysis evasion

| Detection | Method |
|---|---|
| **Environment fingerprinting** | VM/hypervisor/sandbox APIs + markers (`vmtoolsd`, `VBoxService`, `qemu`, `kvmkvmkvm`, `sbiedll`, Cuckoo modules) + registry probe strings |
| **Human-interaction checks** | `GetLastInputInfo`, `GetAsyncKeyState`, `GetCursorPos`, `GetForegroundWindow`, `GlobalMemoryStatusEx`, … |
| **Execution delay / stalling** | `Sleep`, `NtDelayExecution`, `SetWaitableTimer`, `NtSetTimerResolution` |
| **Timing checks** | `GetTickCount`, `QueryPerformanceCounter`, and **RDTSC pairs** found by opcode scan |
| **Direct system calls** | `syscall`, `sysenter`, `int 2e`, `int 80`, `svc` (AArch64/ARM) — plus **decoded `mov eax, <ssn> ; syscall` stubs**, which is the high-confidence form |
| **API hashing** | Well-known ROR-13 module hashes, explaining why an import table looks sparse |

The **import audit is hand-rolled**: `debug/pe` exposes imported *library* names
but not *function* names, and those are the half that matters. The analyzer
walks descriptors → INT/ILT → `IMAGE_IMPORT_BY_NAME` itself, handling both PE32
and PE32+ thunk widths and ordinal imports.

---

## Entropy: why it is size-aware

Naïve packing detection thresholds entropy at ~7.0 and calls everything above it
encrypted. That under-reports the most common packer transform there is.

**XOR or RLE encoding of random bytes leaves a measurable entropy deficit** — and
that deficit shrinks as the payload grows, because the plaintext approaches a
uniform distribution. So the *same* 7.1 bits/byte means very different things at
4 KiB and at 4 MiB. An absolute band cannot express that.

`bininspect` therefore combines band with size:

| Section size | ≥ 5.0 bits/byte | ≥ 6.9 bits/byte |
|---|---|---|
| < 64 KiB | native code | compressed |
| ≥ 64 KiB | **compressed** | compressed |
| ≥ 1 MiB | compressed | **encrypted** |

Sections below 512 bytes are never classified — too little data for the
measurement to mean anything.

---

## Risk scoring

```
score = Σ severityWeight × confidence    (capped at 100)
```

Weights are non-linear: `critical 34`, `high 18`, `medium 8`, `low 3`, `info 1`.

Two deliberate properties:

- **Confidence multiplies.** A bare opcode byte-pair at 0.55 confidence cannot
  outweigh a named injection import at 0.95 — so a single weak heuristic never
  dominates the score.
- **Deduplicated by finding ID.** Repeated detections of one technique (the same
  API in two tables) count once, so the score reflects distinct *capability*
  rather than raw hit count.

Findings are sorted most-severe-first and capped at 8 in `TopFindings`; the full
set is always in `Report.Findings`.

### Confidence is not certainty

Every finding carries a `Confidence` in `[0,1]` and the `Evidence` behind it.
Import presence is strong evidence of **capability**; opcode patterns are weaker
evidence of **intent**, and are scored lower for that reason. Nothing here proves
malicious intent — a signed installer will legitimately match several
heuristics.

---

## Options

```go
bininspect.New(bininspect.Options{
    MaxScanBytes:    64 << 20,   // whole-image scan cap; 0 = default
    DisableByteScan: false,      // fast triage: headers + entropy + imports only
    FullStringScan:  false,      // scan entire image for markers
    Logger:          myLogger,   // satisfies the minimal Logger interface
})
```

The zero value is valid and analyses fully.

---

## Thread safety

Every exported value is safe for concurrent use by multiple goroutines.

- `Analyzer` is **immutable after construction** — no cache, no scratch state,
  no lazily-initialised fields. One instance may be shared freely.
- All report construction is **local to the call**; there is no shared buffer.
- Signature tables are package-level and written only during init; after that
  they are read-only, which is safe under concurrent reads.
- `Options` is **copied** at construction, so a caller may reuse or mutate their
  value afterwards without affecting a live `Analyzer`.
- Pure helpers (`ShannonEntropy`, `RiskScore`, `ScanOpcodeSets`, `MatchImports`)
  touch only their arguments.

`TestAnalyzerIsThreadSafe` runs 32 concurrent analyses of both formats through a
single shared `Analyzer` and asserts every result is byte-identical to the
baseline.

---

## Cryptographic identity: read this

`FileIdentity` carries **both** hashes:

```go
MD5   string  // legacy intelligence-key compatibility ONLY
SHA256 string // ← use this for any integrity decision
```

MD5 is computed only because legacy malware-intelligence pipelines key on it, and
the field is accompanied by `MD5CryptographicallyBroken bool`, always `true`, so
a consumer reading the serialised JSON cannot mistake it for a trust anchor.

---

## Tests

45 tests, ~82% statement coverage, standard library only.

Both format parsers are exercised against **byte-exact hand-built PE32+ and ELF64
images** (`pe_fixture_test.go`, `elf_fixture_test.go`) rather than mocks or
checked-in binaries. The fixtures are hermetic: no compiler, no host-toolchain
dependency, no external artifacts.

That approach paid for itself immediately — it caught five real defects:

1. **Nil-pointer panic.** A format analyzer returning `(nil, err)` was
   dereferenced before the error check.
2. **2-byte symbol offset.** Fixture thunks pointed at the import *name* instead
   of the `IMAGE_IMPORT_BY_NAME` struct, so `Sleep` was recovered as `eep`.
3. **Colliding fixture allocations.** Library names were written at fixed
   offsets that overwrote the import names beside them.
4. **Silent entropy blind spot.** A section-header writer used one parameter for
   both `SizeOfRawData` and `PointerToRawData`, making `.data` claim more bytes
   than the file held — its entropy was never measured, and nothing failed
   loudly.
5. **Inverted test assertion.** An invariant test checked a relationship in the
   wrong direction and failed against *correct* code.

**Detectors are also tested in isolation** (`TestPEDetectorsAreIndependent`), so a
passing aggregate test cannot mask a dead detector.

### Running under the race detector

`go test -race` is the single highest-value command for this package:

```bash
go test -race ./...
```

---

## Performance

Measured on a 16-core machine over a 2.5 KB fixture (small-image latency is
dominated by parsing, not scanning):

| Benchmark | Throughput |
|---|---|
| `ShannonEntropy` | ~980 MB/s |
| `ScanOpcodeSets` | ~694 MB/s |
| `AnalyzePE` / `AnalyzeELF` | ~290 µs / ~180 µs |
| `AnalyzeFastPath` | ~174 µs |
| `AnalyzeParallel` | ~128 µs |

The opcode scanner delegates to `bytes.Index`, whose assembly implementation is
~8× faster than a hand-written byte loop. Byte scanning is restricted to
executable sections; marker scanning to data sections.

---

## Limits worth stating

- **This library does not inspect the host.** It answers "does *this binary*
  contain anti-analysis capability?" by reading a file. It does not answer "is
  *this machine* a VM or sandbox right now?", which requires live hardware
  probing (CPUID, ACPI, timing) rather than file parsing. The two are separate
  products; conflating them would also forfeit the property that analysis is
  hermetic and reproducible.
- **No disassembly.** Byte-pattern matching cannot follow control flow, so a
  syscall reached through obfuscated dispatch may be missed.
- **ELF symbol names are not resolved.** `debug/elf` does not decode the
  dynamic symbol table into names, so the rich API-name detections are largely
  PE-oriented. What ELF does get: permissions, entropy, structural anomalies,
  `DT_NEEDED` library linkage, opcode scanning, and marker scanning.
- **Imports can be incomplete by design.** API hashing or runtime resolution
  hides capability from the import table entirely — which is precisely why
  `TRI-APIHASH` is reported as its own finding.
- **Markers can be present benignly.** A product that merely ships a
  `vmtoolsd.exe` path will match; the confidence ladder reflects this by scoring
  one marker low and a cluster high.

---

## License

Same license as the host repository.