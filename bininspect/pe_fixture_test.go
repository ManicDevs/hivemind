package bininspect

import (
	"encoding/binary"
)

// This file constructs byte-exact PE and ELF images in memory so the format
// parsers are exercised against real structures rather than mocks. Hand-built
// fixtures keep the test hermetic: no compiler, no checked-in binaries, and no
// dependence on the host's own object layout.
//
// Layout of the PE fixture (PE32+, x86-64):
//
//	0x000  DOS header (MZ) with e_lfanew -> 0x80
//	0x080  PE\0\0 signature
//	0x084  COFF file header
//	0x098  PE32+ optional header (240 bytes, 16 data directories)
//	0x188  3 section headers (.text, .rdata, .data)
//	0x200  .text raw  — executable, contains syscall stubs and an RDTSC pair
//	0x400  .rdata raw — read-only, holds the import table and VM marker strings
//	0x800  .data raw  — writable AND executable, high-entropy payload
const (
	peDOSHeaderSize = 0x80
	peCOFFOffset    = peDOSHeaderSize + 4 // after "PE\0\0"
	peOptOffset     = peCOFFOffset + 20
	peOptSize       = 240
	peSectionOffset = peOptOffset + peOptSize
	peHeadersSize   = 0x200

	// Virtual addresses must be laid out the way a linker would: RVA == file
	// offset for the headers, then section-aligned bases.
	peTextRVA  = 0x1000
	peRdataRVA = 0x2000
	peDataRVA  = 0x3000
	peTextRaw  = 0x200
	peRdataRaw = 0x400
	peDataRaw  = 0x800

	peTextSize  = 0x200
	peRdataSize = 0x600
	peDataSize  = 0x200
)

// peFixtureOptions controls which anomalies a fixture image carries, so each
// detection can be tested in isolation instead of only in combination.
type peFixtureOptions struct {
	// WritableExecutable makes .data both writable and executable.
	WritableExecutable bool
	// HighEntropy fills .data with pseudo-random bytes.
	HighEntropy bool
	// DirectSyscall embeds a decoded "mov eax, N; syscall" stub in .text.
	DirectSyscall bool
	// RDTSCPair places two timestamp reads inside the pairing window.
	RDTSCPair bool
	// VMMarkers writes hypervisor marker strings into .rdata.
	VMMarkers bool
	// APIHash embeds a known API-hash constant in .text.
	APIHash bool
	// ZeroTimestamp zeroes TimeDateStamp (anti-forensic).
	ZeroTimestamp bool
	// NoASLR clears DYNAMIC_BASE.
	NoASLR bool
	// RelocsStripped sets IMAGE_FILE_RELOCS_STRIPPED on a DLL.
	RelocsStripped bool
	// PackerSectionNames renames sections to UPX0/UPX1.
	PackerSectionNames bool
	// OrdinalImport encodes one import by ordinal rather than name.
	OrdinalImport bool
	// NoImports omits the import directory entirely.
	NoImports bool
}

// peImportedSymbol describes one symbol to place in the fixture import table.
type peImportedSymbol struct {
	name    string
	ordinal uint16
	byOrd   bool
}

// defaultPEFixture returns options that exercise every detector at once, which
// is the realistic case an analyst would actually see.
func defaultPEFixture() peFixtureOptions {
	return peFixtureOptions{
		WritableExecutable: true,
		HighEntropy:        true,
		DirectSyscall:      true,
		RDTSCPair:          true,
		VMMarkers:          true,
		APIHash:            true,
		NoASLR:             true,
		RelocsStripped:     true,
		OrdinalImport:      true,
	}
}

// buildPE constructs a complete PE32+ image honouring opts.
func buildPE(opts peFixtureOptions) []byte {
	buf := make([]byte, peDataRaw+peDataSize)

	// ---- DOS header ----
	copy(buf[0:], []byte{'M', 'Z'})
	binary.LittleEndian.PutUint32(buf[0x3C:], peDOSHeaderSize)

	// ---- PE signature ----
	copy(buf[peDOSHeaderSize:], []byte{'P', 'E', 0, 0})

	// ---- COFF file header ----
	coff := buf[peCOFFOffset:]
	binary.LittleEndian.PutUint16(coff[0:], 0x8664) // IMAGE_FILE_MACHINE_AMD64
	binary.LittleEndian.PutUint16(coff[2:], 3)      // NumberOfSections
	ts := uint32(0x5F000000)
	if opts.ZeroTimestamp {
		ts = 0
	}
	binary.LittleEndian.PutUint32(coff[4:], ts) // TimeDateStamp
	binary.LittleEndian.PutUint16(coff[16:], peOptSize)
	// Characteristics: EXECUTABLE_IMAGE | LARGE_ADDRESS_AWARE, plus
	// RELOCS_STRIPPED and DLL when requested.
	chars := uint16(0x0022)
	if opts.RelocsStripped {
		chars |= 0x0001
	}
	binary.LittleEndian.PutUint16(coff[18:], chars)

	// ---- Optional header (PE32+) ----
	opt := buf[peOptOffset:]
	binary.LittleEndian.PutUint16(opt[0:], 0x020B)                // PE32+
	binary.LittleEndian.PutUint32(opt[16:], peTextRVA)            // AddressOfEntryPoint
	binary.LittleEndian.PutUint64(opt[24:], 0x140000000)          // ImageBase
	binary.LittleEndian.PutUint32(opt[32:], 0x1000)               // SectionAlignment
	binary.LittleEndian.PutUint32(opt[36:], 0x200)                // FileAlignment
	binary.LittleEndian.PutUint32(opt[56:], peDataRVA+peDataSize) // SizeOfImage
	binary.LittleEndian.PutUint32(opt[60:], peHeadersSize)        // SizeOfHeaders
	binary.LittleEndian.PutUint16(opt[68:], 3)                    // Subsystem = GUI
	dllChars := uint16(0x0140)                                    // DYNAMIC_BASE | NX_COMPAT
	if opts.NoASLR {
		dllChars &^= 0x0040
	}
	binary.LittleEndian.PutUint16(opt[70:], dllChars)
	binary.LittleEndian.PutUint32(opt[108:], 16) // NumberOfRvaAndSizes

	// Data directory 1 = import table.
	if !opts.NoImports {
		dd := opt[112+8:]
		binary.LittleEndian.PutUint32(dd[0:], peRdataRVA) // VirtualAddress
		binary.LittleEndian.PutUint32(dd[4:], 0x40)       // Size
	}

	// ---- Section headers ----
	textName := ".text\x00\x00"
	rdataName := ".rdata\x00"
	dataName := ".data\x00\x00\x00"
	if opts.PackerSectionNames {
		textName = "UPX0\x00\x00\x00\x00"
		rdataName = "UPX1\x00\x00\x00\x00"
		dataName = "UPX2\x00\x00\x00\x00"
	}
	writeSectionHeader(buf[peSectionOffset:], textName, peTextRVA, peTextSize, peTextSize, peTextRaw, 0x60000020)
	writeSectionHeader(buf[peSectionOffset+40:], rdataName, peRdataRVA, peRdataSize, peRdataSize, peRdataRaw, 0x40000040)
	dataChars := uint32(0xC0000040) // READ | WRITE
	if opts.WritableExecutable {
		dataChars |= 0x20000000 // EXECUTE
	}
	writeSectionHeader(buf[peSectionOffset+80:], dataName, peDataRVA, peDataSize, peDataSize, peDataRaw, dataChars)

	// ---- .text: executable payload ----
	text := buf[peTextRaw:]
	// A prologue so the section is not pure padding.
	copy(text, []byte{0x55, 0x48, 0x89, 0xE5, 0x48, 0x83, 0xEC, 0x20})
	if opts.DirectSyscall {
		// "mov eax, 0x50 ; syscall" — a hand-rolled NtQuerySystemInformation-ish stub.
		off := 0x20
		text[off] = 0xB8
		binary.LittleEndian.PutUint32(text[off+1:], 0x50)
		text[off+5] = 0x0F
		text[off+6] = 0x05
		// A second occurrence raises the pattern's confidence to maximum.
		off = 0x40
		text[off] = 0xB8
		binary.LittleEndian.PutUint32(text[off+1:], 0x36)
		text[off+5] = 0x0F
		text[off+6] = 0x05
	}
	if opts.RDTSCPair {
		text[0x80], text[0x81] = 0x0F, 0x31
		text[0xA0], text[0xA1] = 0x0F, 0x31
	}
	if opts.APIHash {
		binary.LittleEndian.PutUint32(text[0xC0:], 0x6A4ABC5B)
	}

	// ---- .rdata: import table plus markers ----
	rdata := buf[peRdataRaw:]
	// The import table allocates names with a bump cursor; everything placed
	// after it must continue from the returned offset, or it overwrites the
	// names the import audit depends on.
	cursor := peNamesRegion
	if !opts.NoImports {
		cursor = writePEImports(rdata, opts)
	}
	if opts.VMMarkers {
		markers := []string{
			"C:\\Program Files\\VMware\\vmtoolsd.exe",
			"C:\\Program Files\\Oracle\\VirtualBox\\VBoxService.exe",
			"vboxguest.sys",
			"qemu-ga",
			"vmmouse",
			"vmhgfs",
			"sbiedll.dll",
		}
		for _, m := range markers {
			if cursor+len(m)+1 >= peRdataSize {
				break
			}
			copy(rdata[cursor:], m)
			cursor += len(m) + 1
		}
	}

	// ---- .data: high-entropy payload ----
	if opts.HighEntropy {
		copy(buf[peDataRaw:], prngBytes(peDataSize, 0xC0FFEE))
	}
	return buf
}

// writeSectionHeader writes one 40-byte section header.
//
// rawSize and rawOffset are separate parameters on purpose. Conflating them —
// using one value for both SizeOfRawData and PointerToRawData — produces a
// section that claims more bytes than the file holds, which silently disables
// its entropy measurement rather than failing loudly.
func writeSectionHeader(dst []byte, name string, rva, virtualSize, rawSize, rawOffset, chars uint32) {
	copy(dst[0:8], name)
	binary.LittleEndian.PutUint32(dst[8:], virtualSize) // VirtualSize
	binary.LittleEndian.PutUint32(dst[12:], rva)        // VirtualAddress
	binary.LittleEndian.PutUint32(dst[16:], rawSize)    // SizeOfRawData
	binary.LittleEndian.PutUint32(dst[20:], rawOffset)  // PointerToRawData
	binary.LittleEndian.PutUint32(dst[24:], 0)          // PointerToRelocations
	binary.LittleEndian.PutUint32(dst[28:], 0)          // PointerToLinenumbers
	binary.LittleEndian.PutUint16(dst[32:], 0)          // NumberOfRelocations
	binary.LittleEndian.PutUint16(dst[34:], 0)          // NumberOfLinenumbers
	binary.LittleEndian.PutUint32(dst[36:], chars)      // Characteristics
}

// peImportLayout is the fixed portion of the import table: two descriptors, a
// NUL terminator, and the two thunk arrays. Names follow, allocated by a bump
// pointer so they can never collide with each other or with the library-name
// strings.
const (
	peDescOff     = 0x00 // 3 descriptors x 20 bytes = 0x3C
	peIntKernel   = 0x40 // 4 thunks x 8 = 0x20
	peIntNtdll    = 0x70 // 3 thunks x 8 = 0x18
	peNamesRegion = 0x90 // bump allocator starts here
)

// peImportBuilder assembles the import table with a monotonic cursor, so every
// string and hint/name entry gets a distinct RVA by construction. A previous
// version placed names at fixed offsets and silently overwrote the import names
// it had just written; the tests caught it as "imports not recovered".
type peImportBuilder struct {
	rdata []byte
	pos   int
}

func (b *peImportBuilder) putBytes(data []byte) uint32 {
	rva := uint32(peRdataRVA + b.pos)
	copy(b.rdata[b.pos:], data)
	b.pos += len(data)
	return rva
}

// putCString writes a NUL-terminated string and returns its RVA.
func (b *peImportBuilder) putCString(s string) uint32 {
	return b.putBytes(append([]byte(s), 0))
}

// putHintName writes an IMAGE_IMPORT_BY_NAME (2-byte hint followed by the
// NUL-terminated name) and returns the RVA the thunk must point at.
//
// The thunk points at the *start of the structure* — the hint — because that is
// what the PE loader dereferences. Returning the name's RVA instead shifts every
// recovered symbol by two bytes ("Sleep" comes back as "eep").
func (b *peImportBuilder) putHintName(name string) uint32 {
	// Align to 2 bytes, as a real linker would, to keep offsets tidy.
	if b.pos%2 != 0 {
		b.pos++
	}
	structRVA := uint32(peRdataRVA + b.pos)
	b.putBytes([]byte{0, 0}) // Hint
	b.putCString(name)
	return structRVA
}

// writePEImports lays out a realistic import table inside the .rdata section and
// returns the offset just past the last byte written, so callers can continue
// allocating after it.
func writePEImports(rdata []byte, opts peFixtureOptions) int {
	b := &peImportBuilder{rdata: rdata, pos: peNamesRegion}

	// Library name strings must be written before descriptors reference them,
	// but their RVAs are only known once the cursor advances; so descriptors are
	// patched after the names are laid out.
	kernelLib := b.putCString("KERNEL32.dll")
	ntdllLib := b.putCString("ntdll.dll")

	kernel := []peImportedSymbol{
		{name: "Sleep"},
		{name: "IsDebuggerPresent"},
		{name: "VirtualAllocEx"},
	}
	ntdll := []peImportedSymbol{
		{name: "NtWriteVirtualMemory"},
		{name: "NtDelayExecution"},
	}
	if opts.OrdinalImport {
		// An ordinal import carries no name, so it must be counted but never
		// matched by a signature.
		kernel = append(kernel, peImportedSymbol{byOrd: true, ordinal: 0x1234})
	}

	// Thunks, each pointing at a freshly allocated hint/name entry.
	for i, sym := range kernel {
		if sym.byOrd {
			// High bit set on a PE32+ thunk means import by ordinal.
			binary.LittleEndian.PutUint64(rdata[peIntKernel+i*8:],
				0x8000000000000000|uint64(sym.ordinal))
			continue
		}
		binary.LittleEndian.PutUint64(rdata[peIntKernel+i*8:], uint64(b.putHintName(sym.name)))
	}
	for i, sym := range ntdll {
		binary.LittleEndian.PutUint64(rdata[peIntNtdll+i*8:], uint64(b.putHintName(sym.name)))
	}

	// Descriptors, patched now that the RVAs are known.
	writeDescriptor(rdata, peDescOff, uint32(peRdataRVA+peIntKernel), kernelLib)
	writeDescriptor(rdata, peDescOff+20, uint32(peRdataRVA+peIntNtdll), ntdllLib)
	// rdata[peDescOff+40:] stays zero: the table terminator.

	return b.pos
}

// writeDescriptor writes one 20-byte IMAGE_IMPORT_DESCRIPTOR. The timestamp and
// forwarder fields are zero, which is what an unbound import table looks like.
func writeDescriptor(rdata []byte, off int, thunkRVA, nameRVA uint32) {
	binary.LittleEndian.PutUint32(rdata[off+0:], thunkRVA)  // OriginalFirstThunk
	binary.LittleEndian.PutUint32(rdata[off+4:], 0)         // TimeDateStamp
	binary.LittleEndian.PutUint32(rdata[off+8:], 0)         // ForwarderChain
	binary.LittleEndian.PutUint32(rdata[off+12:], nameRVA)  // Name
	binary.LittleEndian.PutUint32(rdata[off+16:], thunkRVA) // FirstThunk
}
