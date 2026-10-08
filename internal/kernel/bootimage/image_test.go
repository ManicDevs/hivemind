package bootimage

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestMultibootHeaderRepresentsAV1Image(t *testing.T) {
	img, err := Build()
	if err != nil {
		t.Fatal(err)
	}
	if len(img.Bytes) < 32 {
		t.Fatalf("image too small: %d bytes", len(img.Bytes))
	}
	if got := binary.LittleEndian.Uint32(img.Bytes[0:]); got != 0x1BADB002 {
		t.Fatalf("magic = %#08x, want multiboot v1", got)
	}
	flags := binary.LittleEndian.Uint32(img.Bytes[4:])
	checksum := binary.LittleEndian.Uint32(img.Bytes[8:])
	if 0x1BADB002+flags+checksum != 0 {
		t.Fatalf("checksum invalid: %#x + %#x + %#x", 0x1BADB002, flags, checksum)
	}
	if flags&0x00010003 != 0x00010003 {
		t.Fatalf("flags = %#x, want align+memmap+address fields", flags)
	}
	if got := binary.LittleEndian.Uint32(img.Bytes[12:]); got != 0x00100000 {
		t.Fatalf("header_addr = %#x", got)
	}
	if got := binary.LittleEndian.Uint32(img.Bytes[16:]); got != 0x00100000 {
		t.Fatalf("load_addr = %#x", got)
	}
	if got := binary.LittleEndian.Uint32(img.Bytes[20:]); got != uint32(len(img.Bytes))+0x100000 {
		t.Fatalf("load_end = %#x, want %#x", got, len(img.Bytes)+0x100000)
	}
	entry := binary.LittleEndian.Uint32(img.Bytes[28:])
	if entry < 0x100020 || int(entry-0x100000) >= len(img.Bytes) {
		t.Fatalf("entry %#x outside image", entry)
	}
}

// Guard against the screen-clear regression where rep stosd ran before the
// framebuffer address was loaded, leaving the SeaBIOS banner blended with
// our VGA banner on the QEMU screen.
func TestEntryClearsTheFramebufferBeforePainting(t *testing.T) {
	img, err := Build()
	if err != nil {
		t.Fatal(err)
	}
	entry := binary.LittleEndian.Uint32(img.Bytes[28:])
	expect := []byte{
		0xFA,                         // cli
		0xFC,                         // cld
		0xBC, 0x00, 0x00, 0x11, 0x00, // mov $0x110000, %esp
	}
	got := img.Bytes[int(entry-0x100000):]
	if !bytes.HasPrefix(got, expect) {
		t.Fatalf("entry prologue = %x, want %x", got[:len(expect)], expect)
	}
	// The four anti-analysis guards run next. Boot-time loud mode first:
	// mov $1, %ecx (checksum report) ; mov $1, %edx (code-scan report),
	// then call check_checksum ; call check_breakpoints ; call check_env ;
	// call check_timing (four consecutive 32-bit relative calls).
	pre := []byte{0xB9, 0x01, 0x00, 0x00, 0x00, 0xBA, 0x01, 0x00, 0x00, 0x00}
	if !bytes.HasPrefix(got[len(expect):], pre) {
		t.Fatalf("guard preamble = %x, want %x", got[len(expect):len(expect)+len(pre)], pre)
	}
	callSeq := got[len(expect)+len(pre):]
	if callSeq[0] != 0xE8 || callSeq[5] != 0xE8 || callSeq[10] != 0xE8 || callSeq[15] != 0xE8 {
		t.Fatalf("entry must call the four anti-analysis guards, got %x", callSeq[:20])
	}
}

// The attribute (AH=0x0F) must be re-set on every character, because the
// newline branch clobbers AH while computing the next row. Without this the
// boot log is only legible on the first VGA line.
func TestVgaPrintRefreshesAttributePerCharacter(t *testing.T) {
	img, err := Build()
	if err != nil {
		t.Fatal(err)
	}
	must := []byte{0x8A, 0x06, 0xB4, 0x0F} // mov (%esi),%al; mov $0x0F,%ah
	if !bytes.Contains(img.Bytes, must) {
		t.Fatalf("image missing per-character attribute setup %x", must)
	}
}

func TestImagePaintsAndMirrorsTheBootLog(t *testing.T) {
	img, err := Build()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(img.Bytes, []byte("=== HIVEMIND KERNEL 0.1.0 ===\n")) {
		t.Fatal("VGA message missing from image")
	}
	if !strings.Contains(string(img.Bytes), "PURE-GO MULTIBOOT") {
		t.Fatal("serial message missing from image")
	}
	if !bytes.Contains(img.Bytes, []byte("hivemind> ")) {
		t.Fatal("OS console prompt missing from image")
	}
	for _, cmd := range []string{"help", "boot", "modules", "caps", "uptime", "health", "banner", "guards", "quit", "shutdown"} {
		if !strings.Contains(string(img.Bytes), cmd+"\x00") {
			t.Fatalf("OS command %q missing from image", cmd)
		}
	}
}
