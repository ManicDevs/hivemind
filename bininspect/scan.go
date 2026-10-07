package bininspect

import (
	"bytes"
	"encoding/binary"
	"sort"
	"strings"
)

// OpcodeHit is one matched instruction-sequence pattern.
type OpcodeHit struct {
	// Kind names the pattern, e.g. "syscall", "rdtsc-pair".
	Kind string
	// Offset is the byte offset within the scanned buffer.
	Offset int
	// Detail explains the match, including any decoded operand.
	Detail string
	// Confidence is the per-pattern confidence.
	Confidence float64
}

// syscallPatterns are the architecture-specific instruction encodings that
// reach the kernel.
//
// Confidence is deliberately below that of a named import: an opcode byte
// sequence can also appear as data, in a table, or inside an embedded blob. It
// is strong evidence when it recurs in an executable section and weak evidence
// when it appears once.
type syscallPattern struct {
	kind string
	seq  []byte
	// required is how many times the pattern must recur before confidence is
	// raised to maxConfidence.
	required      int
	minConfidence float64
	maxConfidence float64
	detail        string
}

// x86SyscallPrefix is the "mov eax, imm32" form (opcode B8) that precedes a
// syscall in a hand-rolled syscall stub.
const x86SyscallPrefix = 0xB8

// syscallPatterns is the read-only pattern table, initialised once.
var syscallPatterns = []syscallPattern{
	{
		kind: "syscall", seq: []byte{0x0F, 0x05},
		required: 2, minConfidence: 0.55, maxConfidence: 0.90,
		detail: "x86-64 SYSCALL instruction",
	},
	{
		kind: "sysenter", seq: []byte{0x0F, 0x34},
		required: 2, minConfidence: 0.50, maxConfidence: 0.85,
		detail: "SYSCALL via SYSENTER",
	},
	{
		kind: "int-2e", seq: []byte{0xCD, 0x2E},
		required: 2, minConfidence: 0.50, maxConfidence: 0.85,
		detail: "legacy NT syscall gate INT 2Eh",
	},
	{
		kind: "int-80", seq: []byte{0xCD, 0x80},
		required: 3, minConfidence: 0.40, maxConfidence: 0.75,
		detail: "legacy Linux syscall gate INT 80h",
	},
	{
		kind: "svc-aarch64", seq: []byte{0x01, 0x00, 0x00, 0xD4},
		required: 2, minConfidence: 0.55, maxConfidence: 0.90,
		detail: "AArch64 SVC #0",
	},
	{
		kind: "svc-arm", seq: []byte{0x00, 0x00, 0x00, 0xEF},
		required: 2, minConfidence: 0.50, maxConfidence: 0.85,
		detail: "ARM SVC #0",
	},
}

// ScanOpcodeSets searches buf for system-call and timing instruction patterns.
//
// The caller is responsible for scoping this to executable bytes: running it
// over data sections produces noise, because data can contain any byte.
//
// Confidence scales with recurrence. A single SYSCALL byte pair is weak
// evidence — it may be data — while the pattern appearing many times in a code
// section is how a hand-written syscall dispatcher actually looks.
func ScanOpcodeSets(buf []byte) []OpcodeHit {
	var hits []OpcodeHit
	if len(buf) == 0 {
		return hits
	}

	for _, pat := range syscallPatterns {
		offsets := indexAll(buf, pat.seq)
		if len(offsets) == 0 {
			continue
		}
		conf := pat.minConfidence
		if len(offsets) >= pat.required {
			conf = pat.maxConfidence
		}
		// Report at most a handful of sample offsets so a report stays readable
		// on a binary that uses the pattern thousands of times.
		samples := offsets
		if len(samples) > 5 {
			samples = samples[:5]
		}
		for _, off := range samples {
			hits = append(hits, OpcodeHit{
				Kind:       pat.kind,
				Offset:     off,
				Detail:     pat.detail + " (recurrence " + itoa(len(offsets)) + "x)",
				Confidence: conf,
			})
		}
	}

	// Decode hand-rolled stubs: "mov eax, <ssn>" immediately followed by
	// SYSCALL. The immediate is the system service number, which is what makes
	// this a direct syscall rather than an incidental opcode pair.
	stubPat := []byte{0x0F, 0x05}
	for _, off := range indexAll(buf, stubPat) {
		if off < 5 {
			continue
		}
		if buf[off-5] == x86SyscallPrefix {
			ssn := binary.LittleEndian.Uint32(buf[off-4 : off])
			// Plausible system service numbers are small; anything else is far
			// more likely to be a coincidental byte pattern.
			if ssn > 0 && ssn < 0x1000 {
				hits = append(hits, OpcodeHit{
					Kind:   "direct-syscall-stub",
					Offset: off - 5,
					Detail: "mov eax, " + itoa(int(ssn)) + " ; syscall — hand-rolled system service " + itoa(int(ssn)),
					// A decoded service number is much stronger than a bare pair.
					Confidence: 0.92,
				})
			}
		}
	}

	// RDTSC pairs inside a short window indicate a timing check: read the
	// timestamp, do work, read it again, and branch on the difference.
	tscOffsets := indexAll(buf, []byte{0x0F, 0x31})
	for i := 0; i+1 < len(tscOffsets); i++ {
		first := tscOffsets[i]
		for j := i + 1; j < len(tscOffsets); j++ {
			second := tscOffsets[j]
			if second-first <= timingPairWindow {
				hits = append(hits, OpcodeHit{
					Kind:   "rdtsc-pair",
					Offset: first,
					Detail: "RDTSC pair " + itoa(second-first) + " bytes apart — elapsed-time check candidate",
					// A tight pair is the classic single-step detection shape.
					Confidence: 0.70,
				})
				break
			}
		}
	}

	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Offset != hits[j].Offset {
			return hits[i].Offset < hits[j].Offset
		}
		return hits[i].Kind < hits[j].Kind
	})
	return hits
}

// timingPairWindow is the maximum byte distance between two RDTSC reads for
// the pair to be treated as a timing check. It is generous enough to cover a
// small instrumented block and tight enough that two unrelated uses of RDTSC in
// a large function are not paired.
const timingPairWindow = 512

// indexAll returns every offset at which needle occurs in haystack.
//
// It delegates each probe to bytes.Index, whose assembly implementation scans
// far faster than a hand-written byte loop — the difference is roughly an order
// of magnitude on large code sections. Occurrences are reported
// non-overlapping, advancing by len(needle) on a match so that a repeated
// pattern is not counted once per overlapping shift.
func indexAll(haystack, needle []byte) []int {
	if len(needle) == 0 || len(haystack) < len(needle) {
		return nil
	}
	var out []int
	// Fast path for single-byte needles: the common case in opcode tables.
	if len(needle) == 1 {
		for off := 0; off < len(haystack); {
			i := bytes.IndexByte(haystack[off:], needle[0])
			if i < 0 {
				break
			}
			out = append(out, off+i)
			off += i + 1
		}
		return out
	}
	for off := 0; off+len(needle) <= len(haystack); {
		i := bytes.Index(haystack[off:], needle)
		if i < 0 {
			break
		}
		out = append(out, off+i)
		off += i + len(needle)
	}
	return out
}

// ScanAPIHashConstants looks for well-known API-hashing magic constants.
//
// A match means the image may resolve imports by hash rather than by name,
// which makes the import table an incomplete picture of its capabilities. This
// is reported as a distinct finding rather than folded into the import audit,
// because it explains *why* the imports look sparse.
func ScanAPIHashConstants(buf []byte) []OpcodeHit {
	var hits []OpcodeHit
	for _, c := range apiHashConstants {
		var pattern [4]byte
		binary.LittleEndian.PutUint32(pattern[:], c.Value)
		for _, off := range indexAll(buf, pattern[:]) {
			hits = append(hits, OpcodeHit{
				Kind:       "api-hash",
				Offset:     off,
				Detail:     "API-hash constant " + hex32(c.Value) + " (" + c.Name + ")",
				Confidence: 0.65,
			})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Offset < hits[j].Offset })
	return hits
}

// ScanVMMarkers searches data regions for hypervisor, VM, and sandbox markers.
//
// It operates on printable ASCII runs rather than raw bytes: matching against
// extracted strings avoids reporting a marker that straddles two unrelated
// bytes in a data blob, which is what raw substring search would do.
//
// Confidence rises with the number of distinct markers, because one marker is
// often a false positive (a product that merely mentions "vmware") while
// several distinct ones indicate deliberate fingerprinting.
func ScanVMMarkers(buf []byte) (findings []Finding, hits int) {
	stringsFound := ExtractStrings(buf, minMarkerStringLen)

	seen := make(map[string]bool)
	var evidence []Evidence
	distinct := 0
	for _, s := range stringsFound {
		if containsAny(s, vmMarkers) {
			for _, m := range vmMarkers {
				if seen[m] {
					continue
				}
				ls := strings.ToLower(s)
				lm := strings.ToLower(m)
				if !strings.Contains(ls, lm) {
					continue
				}
				seen[m] = true
				distinct++
				evidence = append(evidence, Evidence{
					Kind:   "string",
					Detail: "hypervisor/sandbox marker \"" + m + "\" in string \"" + clip(s, 64) + "\"",
				})
			}
			hits++
		}
	}

	// Registry keys are matched against the concatenated string space because
	// they frequently span separate strings or are stored as wide strings.
	joined := strings.Join(stringsFound, "\n")
	for _, k := range vmRegistryKeys {
		if strings.Contains(joined, k) {
			distinct++
			evidence = append(evidence, Evidence{
				Kind:   "registry",
				Detail: "VM/sandbox registry probe \"" + k + "\"",
			})
		}
	}

	if len(evidence) == 0 {
		return nil, 0
	}
	conf := 0.45
	switch {
	case distinct >= 6:
		conf = 0.92
	case distinct >= 3:
		conf = 0.78
	case distinct >= 2:
		conf = 0.62
	}
	return []Finding{{
		ID:          "TRI-ENV-MARKERS",
		Category:    CategoryDynamic,
		Vector:      VectorEnvironmentProbe,
		Severity:    severityForConfidence(conf),
		Confidence:  conf,
		Title:       "Hypervisor or sandbox fingerprint markers in image",
		Description: "The image contains " + itoa(distinct) + " distinct hypervisor, VM, or analysis-sandbox markers. Multiple independent markers indicate deliberate environment fingerprinting to detect virtualised or monitored execution.",
		Remediation: "Detonate only in an environment where these artefacts are absent or spoofed, or the sample may suppress its payload.",
		Evidence:    evidence,
	}}, hits
}

// minMarkerStringLen is the minimum printable-run length considered a string
// when hunting markers. Short runs produce constant false positives.
const minMarkerStringLen = 4

// ExtractStrings returns printable ASCII runs of at least minLen bytes.
//
// This is the same extraction debug/elf uses for its own string table, applied
// to arbitrary data so that markers can be found in a PE's .rdata or an ELF's
// .rodata without needing to know the section layout.
func ExtractStrings(buf []byte, minLen int) []string {
	var out []string
	start := -1
	for i := 0; i < len(buf); i++ {
		c := buf[i]
		if c >= 0x20 && c < 0x7F {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 && i-start >= minLen {
			out = append(out, string(buf[start:i]))
		}
		start = -1
	}
	if start >= 0 && len(buf)-start >= minLen {
		out = append(out, string(buf[start:]))
	}
	return out
}

// clip shortens s for display in an evidence line.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// hex32 renders a uint32 as 0x-prefixed lowercase hex.
func hex32(v uint32) string {
	const digits = "0123456789abcdef"
	var b strings.Builder
	b.WriteString("0x")
	for shift := 28; shift >= 0; shift -= 4 {
		b.WriteByte(digits[(v>>uint(shift))&0xF])
	}
	return b.String()
}

// itoa is strconv.Itoa without the import, kept local because it appears in
// hot formatting paths inside scan loops.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
