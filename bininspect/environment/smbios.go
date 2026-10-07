package environment

import "path/filepath"

// Raw SMBIOS parsing.
//
// The kernel exposes a handful of *decoded* DMI attributes under
// /sys/class/dmi/id: sys_vendor, product_name, bios_vendor, and a couple more.
// That is convenient but it is not the table. Decoding is lossy — the kernel
// reads a fixed set of string slots for a fixed set of structure types — so a
// vendor string living in a slot the kernel does not surface is invisible
// through sysfs even though it is plainly present in the table.
//
// This walker parses the raw structures instead, which has three advantages:
// the whole string table becomes searchable, the structure types are visible so
// an indicator can say *where* a match came from, and nothing depends on the
// kernel's interpretation of a specification revision.
//
// Structure layout, per the SMBIOS 2.1+ and 3.x specifications:
//
//	byte  0     type
//	byte  1     length of the formatted area (excluding this header)
//	bytes 2-3   handle (little endian)
//	bytes 4..   formatted area, whose layout depends on type
//	then        the string table: NUL-terminated strings, terminated by a
//	            double NUL. Absent when there are no strings.
//
// A length byte of 0 is legal and means the structure carries only strings.

// smbiosStructure is one parsed structure.
type smbiosStructure struct {
	Type byte
	// Handle is the structure handle from bytes 2-3.
	Handle uint16
	// Formatted is the raw formatted area, useful for type-specific decoding.
	Formatted []byte
	// Strings are the unformatted string table, in order.
	Strings []string
}

// smbiosTypeNames maps the structure types this package cares about.
var smbiosTypeNames = map[byte]string{
	0:  "BIOS Information",
	1:  "System Information",
	2:  "Baseboard Information",
	3:  "Chassis Information",
	4:  "Processor Information",
	7:  "Cache Information",
	11: "OEM Strings",
	12: "System Configuration Options",
	13: "BIOS Language",
}

// parseSMBIOS walks a raw SMBIOS table into structures.
//
// The walk is bounds-checked at every step: a truncated or malformed table ends
// the walk rather than reading out of range. That matters because this data
// comes from firmware and firmware is exactly the kind of input that should be
// assumed malformed until proven otherwise.
func parseSMBIOS(table []byte) []smbiosStructure {
	var out []smbiosStructure
	i := 0
	// A structure is at least 4 bytes (type, length, handle). The bound also
	// stops a zero-length structure from looping forever.
	for i+4 <= len(table) {
		typ := table[i]
		length := int(table[i+1])
		handle := uint16(table[i+2]) | uint16(table[i+3])<<8

		// Guard against a corrupt length pushing us past the end. If the
		// formatted area runs off the table, the remainder is unusable.
		if i+4+length > len(table) {
			break
		}

		structStart := i
		formatted := table[i+4 : i+4+length]
		i = i + 4 + length

		// Walk the string table to its double-NUL terminator.
		var strs []string
		for {
			if i >= len(table) {
				// Truncated: keep what we have rather than discarding the
				// structure, since the formatted area is already parsed.
				break
			}
			if table[i] == 0 {
				// One NUL ends the last string; a second NUL ends the table.
				if i+1 < len(table) && table[i+1] == 0 {
					i += 2
					break
				}
				i++
				continue
			}
			end := i
			for end < len(table) && table[end] != 0 {
				end++
			}
			if end >= len(table) {
				break // unterminated string: the table is truncated
			}
			strs = append(strs, string(table[i:end]))
			// Position AT the NUL, not past it. The next iteration inspects
			// that NUL to decide whether the string table also ends here: the
			// table terminator is a second consecutive NUL. Stepping past it
			// (i = end+1) means the terminator is never observed, so the walk
			// consumes the following structure as if it were another string.
			i = end
		}

		// A zero-length structure is a bare string table; keep it, since that is
		// exactly the shape OEM vendors use for a free-form identification blob.
		if length == 0 && len(strs) == 0 {
			i = structStart + 4
			continue
		}

		out = append(out, smbiosStructure{
			Type:      typ,
			Handle:    handle,
			Formatted: formatted,
			Strings:   strs,
		})
	}
	return out
}

// probeSMBIOS reads and parses the raw firmware tables.
//
// It reports three things: the vendors named anywhere in the string table (the
// strongest firmware-level signal), the Type 1 system manufacturer and product
// specifically, and any Type 11 OEM string block, which many hypervisors use to
// identify themselves.
func probeSMBIOS(fs FileReader) ([]Indicator, []string) {
	raw, _, ok := readSMBIOS(fs)
	if !ok {
		return nil, nil
	}
	structs := parseSMBIOS(raw)
	if len(structs) == 0 {
		return nil, nil
	}

	// Build a searchable corpus with per-structure attribution, so an indicator
	// can name the type it came from.
	type tagged struct {
		structType byte
		text       string
	}
	var corpus []tagged
	var evidence []string
	for _, st := range structs {
		for _, s := range st.Strings {
			if s == "" {
				continue
			}
			corpus = append(corpus, tagged{structType: st.Type, text: s})
			typeName, known := smbiosTypeNames[st.Type]
			if !known {
				typeName = "Type " + itoa(int64(st.Type))
			}
			evidence = append(evidence, "SMBIOS "+typeName+": "+s)
		}
	}
	if len(evidence) == 0 {
		return nil, nil
	}

	seen := make(map[string]bool)
	var out []Indicator
	for _, t := range corpus {
		lower := toLower(t.text)
		for _, sig := range hypervisorSignatures {
			if !containsFold(t.text, sig.match) {
				continue
			}
			if seen[sig.id] {
				continue
			}
			seen[sig.id] = true
			typeName, known := smbiosTypeNames[t.structType]
			if !known {
				typeName = "Type " + itoa(int64(t.structType))
			}
			out = append(out, Indicator{
				ID:         sig.id,
				Category:   CategoryVirtualisation,
				Severity:   severityFor(sig.confidence),
				Confidence: sig.confidence,
				Title:      "SMBIOS " + typeName + " names " + sig.name,
				Evidence:   evidence,
			})
		}
		// A virtio marker can appear in an OEM string block rather than a vendor
		// field, which is where many custom images put it.
		if containsFold(t.text, "virtio") && !seen["ENV-VM-SMBIOS-VIRTIO"] {
			seen["ENV-VM-SMBIOS-VIRTIO"] = true
			out = append(out, Indicator{
				ID:         "ENV-VM-SMBIOS-VIRTIO",
				Category:   CategoryVirtualisation,
				Severity:   SeverityMedium,
				Confidence: 0.75,
				Title:      "SMBIOS string references virtio",
				Evidence:   evidence,
			})
		}
		_ = lower
	}
	return out, evidence
}

// smbiosTables are the locations the raw tables appear, newest format first.
var smbiosTables = []string{
	"/sys/firmware/dmi/tables/DMI",
	"/sys/firmware/dmi/tables/smbios_entry_point",
}

// readSMBIOS finds the first available raw table.
func readSMBIOS(fs FileReader) ([]byte, string, bool) {
	for _, p := range smbiosTables {
		if b, err := fs.ReadFile(filepath.Clean(p)); err == nil && len(b) >= 4 {
			return b, p, true
		}
	}
	return nil, "", false
}

// toLower is a case-folding helper kept local for the same reason as lower().
func toLower(s string) string {
	b := []byte(s)
	for i := range b {
		b[i] = lower(b[i])
	}
	return string(b)
}
