// Package bootimage builds the bare-metal HIVEMIND kernel image byte by byte.
//
// Every byte in the produced binary is emitted by this Go package: the
// multiboot-1 header, the 32-bit machine code for the VGA/serial boot log, and
// the message blobs. No C compiler, no GNU assembler, and no external toolchain
// are involved; the same bytes are what the test suite inspects and QEMU boots.
//
// The bootloader enters the image in 32-bit protected mode with paging off and
// no stack. The image therefore sets up its own stack in low memory, writes the
// boot log to the VGA text buffer and COM1, waits a few RTC seconds, then
// soft-resets through the PS/2 controller so QEMU's -no-reboot exits cleanly.
package bootimage

import "encoding/binary"

// LoadBase is where the multiboot loader places the whole image.
const LoadBase = 0x00100000

// Image is a built kernel image.
type Image struct {
	Bytes []byte
	// EntryRVA is where the bootloader jumps: the 32-bit entry point.
	EntryRVA uint32
}

// Builder assembles the image in memory.
type Builder struct {
	buf []byte
}

// Put32 appends a 32-bit little-endian value.
func (b *Builder) Put32(v uint32) {
	var tmp [4]byte
	binary.LittleEndian.PutUint32(tmp[:], v)
	b.buf = append(b.buf, tmp[:]...)
}

// Put8 appends a byte.
func (b *Builder) Put8(v byte) { b.buf = append(b.buf, v) }

// PutBytes appends raw bytes.
func (b *Builder) PutBytes(p []byte) { b.buf = append(b.buf, p...) }

// Offset returns the number of bytes emitted so far.
func (b *Builder) Offset() int { return len(b.buf) }

// Bytes returns the image built so far.
func (b *Builder) Bytes() []byte { return b.buf }
