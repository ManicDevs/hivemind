// Package bininspect is a thread-safe static triage library for detecting
// anti-analysis capability in PE and ELF binaries.
//
// It answers one question for an analyst: *does this binary try to notice,
// stall, or evade analysis?* It reports what it found and how much that should
// worry you, with the evidence attached, so a finding can be triaged without
// opening a disassembler.
//
// # Scope and posture
//
// This is a defensive triage tool. It reads binaries; it never executes,
// unpacks, injects, or modifies them. It infers intent from static artifacts —
// section entropy, header field combinations, imported API names, and opcode
// byte patterns — and it is explicit about the confidence of each inference.
//
// # Detection classes
//
//   - Static anti-analysis: packed or encrypted sections (Shannon entropy),
//     W+X memory combinations, header anomalies, and anti-debugging or
//     process-injection imports.
//   - Dynamic anti-analysis evasion: environment fingerprinting (VM,
//     hypervisor, sandbox, human-interaction probes), execution delay and
//     stalling (timing checks, RDTSC pairs, sleep), and direct system calls or
//     API hashing that bypasses user-mode API monitoring.
//
// # Thread safety
//
// Every exported value is safe for concurrent use by multiple goroutines.
//
//   - Analyzer holds an immutable configuration; it has no mutable state and no
//     cache, so a single Analyzer may be shared without synchronisation.
//   - All report construction is local to the call. No shared mutable buffers.
//   - Signature tables are package-level and are initialised once during
//     package init; after that they are read-only, which is safe under
//     concurrent reads.
//   - The pure helpers (ShannonEntropy, RiskScore, MatchImports, ScanOpcodeSets)
//     operate only on their arguments.
//
// # Confidence, not certainty
//
// Every Finding carries a Confidence in [0,1] and the Evidence that produced
// it. Import presence is strong evidence of *capability*; opcode patterns are
// weaker evidence of *intent* and are scored lower for that reason. Nothing
// here proves malicious intent, and a signed installer will legitimately match
// several heuristics.
//
// # Cryptographic identity caveat
//
// FileIdentity carries both MD5 and SHA-256. MD5 is computed only because
// legacy malware-intelligence pipelines key on it, and it is marked
// MD5CryptographicallyBroken. It must never be used for integrity or
// authenticity decisions: SHA-256 is the identity field to trust.
package bininspect
