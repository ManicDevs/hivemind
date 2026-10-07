package bininspect

import "math"

// ShannonEntropy returns the Shannon entropy of b in bits per byte.
//
// The result is in [0,8]: 0 for a single repeated byte, 8 for a uniform
// distribution over all 256 byte values. It is computed from the byte
// histogram, so it measures compression or randomness rather than semantics —
// which is exactly what makes it a packing and encryption indicator.
//
// An empty input returns 0, which callers should treat as "no data" rather than
// "maximally random".
func ShannonEntropy(b []byte) float64 {
	if len(b) == 0 {
		return 0
	}
	var counts [256]uint64
	for _, c := range b {
		counts[c]++
	}
	n := float64(len(b))
	var h float64
	for _, c := range counts {
		if c == 0 {
			continue
		}
		p := float64(c) / n
		h -= p * math.Log2(p)
	}
	return h
}

// EntropyClass is the interpretation of a section's Shannon entropy.
type EntropyClass string

const (
	// EntropyEmpty means the section has no bytes in the file.
	EntropyEmpty EntropyClass = "empty"
	// EntropyStructured covers low entropy: strings, resources, zero-filled
	// padding, or readable data tables.
	EntropyStructured EntropyClass = "structured"
	// EntropyNative is the range typical of compiled machine code.
	EntropyNative EntropyClass = "native-code"
	// EntropyCompressed is the range typical of compressed payloads.
	EntropyCompressed EntropyClass = "compressed-or-packed"
	// EntropyEncrypted is high enough to imply encryption or strong
	// compression, and is the range packers and cryptors produce.
	EntropyEncrypted EntropyClass = "encrypted-or-highly-compressed"
)

// Entropy thresholds. These follow the conventional banding used in PE/ELF
// triage: below ~5.0 is data, ~5.0-6.5 is native code, above ~7.0 is
// compressed, and above ~7.2 is very likely encrypted.
//
// The thresholds are exported so a caller can re-tune without forking, and are
// asserted by the constant-budget tests.
const (
	// EntropyNativeThreshold is the floor for "not plain data".
	EntropyNativeThreshold = 5.0
	// EntropyCompressedThreshold is the floor for "likely compressed".
	EntropyCompressedThreshold = 6.9
	// EntropyEncryptedThreshold is the floor for "likely encrypted".
	EntropyEncryptedThreshold = 7.2
)

// Size thresholds for the packing heuristic.
//
// Entropy bands alone are size-blind, and that matters: XOR or run-length
// encoding — the most common packer transform — leaves an entropy deficit whose
// absolute value shrinks as the payload grows. So the same 7.1 bits/byte means
// different things for a 4 KiB section and a 4 MiB one, and a threshold that
// only looks at the band will under-report large encrypted payloads.
//
// These bounds supply the missing size term: a high-entropy section at or above
// mediumPackedSectionBytes is reported as compressed-or-packed, and at or above
// largePackedSectionBytes it escalates toward "encrypted".
const (
	// mediumPackedSectionBytes is 64 KiB: past this, a high-entropy section is
	// reported as compressed-or-packed.
	mediumPackedSectionBytes = 64 << 10
	// largePackedSectionBytes is 1 MiB: past this, high entropy is treated as
	// encryption regardless of the absolute band.
	largePackedSectionBytes = 1 << 20
	// minPackedSectionBytes is 512 bytes: below this a high-entropy section
	// carries too little data for the measurement to mean anything.
	minPackedSectionBytes = 512
)

// ClassifyEntropy interprets a Shannon entropy value.
//
// The classification is intentionally conservative at the low end: ordinary
// native code on a small section can exceed 6.5, and native text is not
// anomalous. Only the high bands are treated as a packing signal.
func ClassifyEntropy(h float64) EntropyClass {
	switch {
	case h <= 0:
		return EntropyEmpty
	case h < EntropyNativeThreshold:
		return EntropyStructured
	case h < EntropyCompressedThreshold:
		return EntropyNative
	case h < EntropyEncryptedThreshold:
		return EntropyCompressed
	default:
		return EntropyEncrypted
	}
}

// entropyVerdict converts a section's entropy into an actionable statement,
// including how far into the band the section sits. Reporting the distance to
// the threshold lets an analyst judge borderline sections instead of guessing
// from a single bucket.
func entropyVerdict(name string, h float64, size int64) (EntropyClass, string) {
	class := ClassifyEntropy(h)
	switch class {
	case EntropyEncrypted:
		return class, "entropy " + formatFloat(h) +
			" exceeds the encrypted band (>= " + formatFloat(EntropyEncryptedThreshold) +
			"); the section's bytes are indistinguishable from random, which is the signature of encryption or a strong packer"
	case EntropyCompressed:
		return class, "entropy " + formatFloat(h) +
			" sits in the compressed band (>= " + formatFloat(EntropyCompressedThreshold) +
			"); consistent with a packed or compressed payload"
	case EntropyNative:
		return class, "entropy " + formatFloat(h) + " is typical of compiled code"
	case EntropyStructured:
		return class, "entropy " + formatFloat(h) + " indicates structured data (strings, resources, or padding)"
	default:
		return class, "section " + name + " has no bytes in the file"
	}
}

// packingClass is the size-aware packing classification used by the detectors.
//
// The absolute band decides first. Then size supplies the missing term: even
// weak obfuscation (XOR, RLE) converges on random-looking bytes as the payload
// grows, so a multi-megabyte section at 7.1 bits/byte is far more likely to be
// packed or encrypted than native code, while a 4 KiB section at the same
// entropy is not.
//
// Callers must reject sections below minPackedSectionBytes before calling, since
// size cannot rescue a measurement too small to trust.
func packingClass(h float64, size int64) EntropyClass {
	base := ClassifyEntropy(h)
	switch {
	case size >= largePackedSectionBytes && h >= EntropyCompressedThreshold:
		return EntropyEncrypted
	case size >= mediumPackedSectionBytes && h >= EntropyNativeThreshold:
		return EntropyCompressed
	default:
		return base
	}
}

// MaxEntropy returns the theoretical maximum entropy for a byte histogram,
// which is 8 bits per byte. Provided so callers can express thresholds as
// fractions of the maximum rather than as unexplained literals.
func MaxEntropy() float64 { return 8 }
