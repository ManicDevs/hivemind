package bininspect

import (
	"debug/elf"
	"encoding/binary"
)

// This file constructs a byte-exact ELF64 image in memory, mirroring the PE
// fixture, so the ELF parser is exercised against a real structure.
//
// Layout:
//
//	0x000  ELF header (64 bytes)
//	0x100  2 program headers (56 bytes each): one PT_LOAD RX, one PT_LOAD RWX
//	0x200  section headers: .text, .rodata, .data, .shstrtab
//	0x400  .text raw  — executable, syscall stubs and an RDTSC pair
//	0x600  .rodata raw — read-only, VM marker strings
//	0x800  .data raw  — writable+executable, high-entropy payload
const (
	elfHeaderSize  = 0x40
	elfPhentSize   = 56
	elfShentSize   = 64
	elfPhnum       = 2
	elfShnum       = 4
	elfPhOffset    = 0x100
	elfShOffset    = 0x200
	elfTextOff     = 0x400
	elfTextSize    = 0x200
	elfRodataOff   = 0x600
	elfRodataSize  = 0x200
	elfDataOff     = 0x800
	elfDataSize    = 0x200
	elfTextVAddr   = 0x401000
	elfRodataVAddr = 0x402000
	elfDataVAddr   = 0x403000
	elfEntryPoint  = 0x401000
)

// elfFixtureOptions controls which anomalies the ELF fixture carries.
type elfFixtureOptions struct {
	WritableExecutable bool
	HighEntropy        bool
	DirectSyscall      bool
	RDTSCPair          bool
	VMMarkers          bool
	APIHash            bool
	WithSymtab         bool
	StaticallyLinked   bool
	PackerSectionName  bool
}

// defaultELFFixture exercises every ELF-reachable detector at once.
func defaultELFFixture() elfFixtureOptions {
	return elfFixtureOptions{
		WritableExecutable: true,
		HighEntropy:        true,
		DirectSyscall:      true,
		RDTSCPair:          true,
		VMMarkers:          true,
		APIHash:            true,
	}
}

// buildELF constructs a complete ELF64 image honouring opts.
func buildELF(opts elfFixtureOptions) []byte {
	buf := make([]byte, elfDataOff+elfDataSize)

	// ---- ELF header ----
	copy(buf[0:], []byte{0x7F, 'E', 'L', 'F'})
	buf[4] = 2 // ELFCLASS64
	buf[5] = 1 // ELFDATA2LSB
	buf[6] = 1 // EV_CURRENT
	elfType := uint16(elf.ET_EXEC)
	binary.LittleEndian.PutUint16(buf[16:], elfType)
	binary.LittleEndian.PutUint16(buf[18:], uint16(elf.EM_X86_64))
	binary.LittleEndian.PutUint32(buf[20:], 1) // e_version
	binary.LittleEndian.PutUint64(buf[24:], elfEntryPoint)
	binary.LittleEndian.PutUint64(buf[32:], elfPhOffset)
	binary.LittleEndian.PutUint64(buf[40:], elfShOffset)
	binary.LittleEndian.PutUint16(buf[52:], uint16(elfHeaderSize))
	binary.LittleEndian.PutUint16(buf[54:], uint16(elfPhentSize))
	binary.LittleEndian.PutUint16(buf[56:], elfPhnum)
	binary.LittleEndian.PutUint16(buf[58:], uint16(elfShentSize))
	binary.LittleEndian.PutUint16(buf[60:], elfShnum)
	binary.LittleEndian.PutUint16(buf[62:], 3) // e_shstrndx

	// ---- program headers ----
	// PT_LOAD | R+X for .text
	ph := buf[elfPhOffset:]
	binary.LittleEndian.PutUint32(ph[0:], uint32(elf.PT_LOAD))
	binary.LittleEndian.PutUint32(ph[4:], uint32(elf.PF_R|elf.PF_X))
	binary.LittleEndian.PutUint64(ph[8:], elfTextOff)
	binary.LittleEndian.PutUint64(ph[16:], elfTextVAddr)
	binary.LittleEndian.PutUint64(ph[24:], elfTextVAddr) // p_paddr
	binary.LittleEndian.PutUint64(ph[32:], elfTextSize)
	binary.LittleEndian.PutUint64(ph[40:], elfTextSize)
	binary.LittleEndian.PutUint64(ph[48:], 0x1000)

	// PT_LOAD | R+W+X for .data — the W^X violation under test.
	ph2 := buf[elfPhOffset+elfPhentSize:]
	binary.LittleEndian.PutUint32(ph2[0:], uint32(elf.PT_LOAD))
	flags := uint32(elf.PF_R | elf.PF_W)
	if opts.WritableExecutable {
		flags |= uint32(elf.PF_X)
	}
	binary.LittleEndian.PutUint32(ph2[4:], flags)
	binary.LittleEndian.PutUint64(ph2[8:], elfDataOff)
	binary.LittleEndian.PutUint64(ph2[16:], elfDataVAddr)
	binary.LittleEndian.PutUint64(ph2[24:], elfDataVAddr)
	binary.LittleEndian.PutUint64(ph2[32:], elfDataSize)
	binary.LittleEndian.PutUint64(ph2[40:], elfDataSize)
	binary.LittleEndian.PutUint64(ph2[48:], 0x1000)

	// ---- .text content ----
	text := buf[elfTextOff:]
	copy(text, []byte{0x31, 0xC0, 0x50}) // xor eax,eax; push rax
	if opts.DirectSyscall {
		// "mov eax, 0x39 ; syscall" plus two bare int 80h gates so the
		// pattern-recurrence rule raises confidence.
		off := 0x20
		text[off] = 0xB8
		binary.LittleEndian.PutUint32(text[off+1:], 0x39)
		text[off+5] = 0x0F
		text[off+6] = 0x05
		off = 0x40
		text[off] = 0xB8
		binary.LittleEndian.PutUint32(text[off+1:], 0x3B)
		text[off+5] = 0x0F
		text[off+6] = 0x05
		off = 0x60
		text[off], text[off+1] = 0xCD, 0x80
		off = 0x70
		text[off], text[off+1] = 0xCD, 0x80
	}
	if opts.RDTSCPair {
		text[0x90], text[0x91] = 0x0F, 0x31
		text[0xB0], text[0xB1] = 0x0F, 0x31
	}
	if opts.APIHash {
		binary.LittleEndian.PutUint32(text[0xD0:], 0x7267744C)
	}

	// ---- .rodata markers ----
	if opts.VMMarkers {
		markers := []string{
			"/usr/bin/vmtoolsd",
			"/opt/VirtualBox/VBoxService",
			"vboxguest",
			"qemu-system-x86_64",
			"sbiedll",
		}
		pos := 0
		for _, m := range markers {
			if pos+len(m) >= elfRodataSize {
				break
			}
			copy(buf[elfRodataOff+pos:], m)
			pos += len(m) + 1
		}
	}

	// ---- .data high-entropy payload ----
	if opts.HighEntropy {
		copy(buf[elfDataOff:], prngBytes(elfDataSize, 0x5EED))
	}

	// ---- section headers ----
	textName := ".text"
	rodataName := ".rodata"
	dataName := ".data"
	shstrName := ".shstrtab"
	if opts.PackerSectionName {
		textName = ".upx"
		rodataName = ".upx1"
		dataName = ".data"
	}
	// The string table lives right after the section headers.
	strtab := uint64(elfShOffset + elfShnum*elfShentSize)
	nameOffs := map[string]uint32{}
	pos := uint32(0)
	for _, n := range []string{textName, rodataName, dataName, shstrName, ".symtab"} {
		nameOffs[n] = pos
		pos += uint32(len(n)) + 1
	}
	for _, n := range []string{textName, rodataName, dataName, shstrName, ".symtab"} {
		copy(buf[strtab+uint64(nameOffs[n]):], n)
	}

	writeELFShdr(buf[elfShOffset:], nameOffs[textName], elf.SHT_PROGBITS,
		uint64(elf.SHF_ALLOC|elf.SHF_EXECINSTR), elfTextVAddr, elfTextOff, elfTextSize, 16)
	writeELFShdr(buf[elfShOffset+elfShentSize:], nameOffs[rodataName], elf.SHT_PROGBITS,
		uint64(elf.SHF_ALLOC), elfRodataVAddr, elfRodataOff, elfRodataSize, 4)
	writeELFShdr(buf[elfShOffset+2*elfShentSize:], nameOffs[dataName], elf.SHT_PROGBITS,
		uint64(elf.SHF_ALLOC|elf.SHF_WRITE), elfDataVAddr, elfDataOff, elfDataSize, 8)
	writeELFShdr(buf[elfShOffset+3*elfShentSize:], nameOffs[shstrName], elf.SHT_STRTAB,
		0, 0, strtab, uint64(pos), 1)

	// A .symtab section is only reachable by extending the header's e_shnum,
	// so a stripped-image fixture simply omits it — which is the common case and
	// what the Stripped field should report.
	_ = opts.WithSymtab
	return buf
}

// writeELFShdr writes one 64-byte section header.
func writeELFShdr(dst []byte, nameOff uint32, shtype elf.SectionType, flags, addr, off, size uint64, align uint64) {
	binary.LittleEndian.PutUint32(dst[0:], nameOff)
	binary.LittleEndian.PutUint32(dst[4:], uint32(shtype))
	binary.LittleEndian.PutUint64(dst[8:], flags)
	binary.LittleEndian.PutUint64(dst[16:], addr)
	binary.LittleEndian.PutUint64(dst[24:], off)
	binary.LittleEndian.PutUint64(dst[32:], size)
	binary.LittleEndian.PutUint64(dst[48:], align)
}
