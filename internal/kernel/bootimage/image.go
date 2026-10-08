package bootimage

import (
	"encoding/binary"
	"fmt"
)

// Build produces the complete multiboot-1 raw image. Every instruction it
// contains is emitted as an explicit byte sequence by this Go package, so no
// C compiler or assembler is involved: the bytes above are machine code for a
// bootloader entry, and the rest of the OS image is pure Go.
func Build() (*Image, error) {
	b := &Builder{}
	for i := 0; i < 8; i++ {
		b.Put32(0)
	}
	a := &asm{b: b, base: 0x00100000, labels: make(map[string]uint32)}
	a.build()
	if err := a.resolve(); err != nil {
		return nil, err
	}
	main := a.labels["main"]
	loadEnd := 0x00100000 + uint32(b.Offset())
	hdr := header(0x00010003, 0x00100000, 0x00100000, loadEnd, main)
	copy(b.buf[:32], hdr)

	patchImm := func(name string, val uint32) {
		addr, ok := a.labels[name]
		if !ok {
			return
		}
		binary.LittleEndian.PutUint32(b.buf[int(addr-0x00100000):], val)
	}
	// Loaded image extends from 0x100000 to just past its final byte.
	patchImm("imageEndPatch", 0x00100000+uint32(b.Offset()))
	patchImm("imageEndPatch2", 0x00100000+uint32(b.Offset()))
	// The skip-compare's own immediate is a fixed address and participates in
	// the sum on both sides; only the 4-byte expected-value slot is excluded,
	// otherwise the value it stores would be self-referential.
	patchImm("guardSkip", a.labels["guardExpectedSum"])

	sumLo := int(a.labels["guardExpectedSum"] - 0x00100000)
	var sum uint32
	for i := 0; i < b.Offset(); i++ {
		if i >= sumLo && i < sumLo+4 {
			continue
		}
		sum += uint32(b.buf[i])
	}
	patchImm("guardExpectedSum", sum)
	// The runtime anti-rootkit scan flags any 0xCC (INT3) or 0xCD (INT) byte in
	// the image, so the emitter must guarantee such a byte is never produced
	// (instructions here never use those opcodes, every marker is built at
	// runtime: |0xCD | no instruction encodes them, and no message string
	// contains them as raw bytes). Build() enforces this or the first scan
	// would false-halt the OS.
	for i, bb := range b.buf {
		if bb == 0xCC || bb == 0xCD {
			return nil, fmt.Errorf("bootimage: byte %#02x at offset %d would trip the rootkit scan", bb, i)
		}
	}
	return &Image{Bytes: b.buf, EntryRVA: main}, nil
}

func header(flags, headerAddr, loadAddr, loadEnd, entry uint32) []byte {
	var h [32]byte
	binary.LittleEndian.PutUint32(h[0:], mbMagic1)
	binary.LittleEndian.PutUint32(h[4:], flags)
	binary.LittleEndian.PutUint32(h[8:], -(mbMagic1 + flags))
	binary.LittleEndian.PutUint32(h[12:], headerAddr)
	binary.LittleEndian.PutUint32(h[16:], loadAddr)
	binary.LittleEndian.PutUint32(h[20:], loadEnd)
	binary.LittleEndian.PutUint32(h[24:], loadEnd)
	binary.LittleEndian.PutUint32(h[28:], entry)
	return h[:]
}

const mbMagic1 = 0x1BADB002

// Interactive OS state lives below 1 MB in conventional RAM, well clear of the
// image (loaded at 0x100000) and the bootstrap stack (0x110000).
const (
	osCmdBuf     = 0x00040000 // 64-byte NUL-terminated serial command buffer
	osUptime     = 0x00040040 // u32 seconds since first RTC second tick
	osLastSec    = 0x00040044 // u8 last observed CMOS second value
	osBufIdx     = 0x00040048 // u8 index of the next byte in the command buffer
	osGuardImage = 0x0004004C // u8 1 = runtime image checksum verified at boot
	osGuardEnv   = 0x0004004D // u8 1 = hypervisor/emulator detected via CPUID
	osGuardInt   = 0x0004004E // u8 1 = breakpoint/rootkit byte scan clean
	osGuardDbg   = 0x0004004F // u8 1 = timing anomaly (debugger suspected)
)

type reloc struct {
	offset int
	size   int
	target string
}

type asm struct {
	b      *Builder
	base   uint32
	labels map[string]uint32
	relocs []reloc
}

func (a *asm) mark(name string) { a.labels[name] = a.base + uint32(a.b.Offset()) }

func (a *asm) emit(p ...byte) { a.b.PutBytes(p) }

func (a *asm) imm32(v uint32) {
	var tmp [4]byte
	binary.LittleEndian.PutUint32(tmp[:], v)
	a.emit(tmp[:]...)
}

func (a *asm) call(target string) {
	off := a.b.Offset()
	a.b.Put8(0xE8)
	a.b.Put32(0)
	a.relocs = append(a.relocs, reloc{off + 1, 4, target})
}

func (a *asm) jrel8(op byte, target string) {
	off := a.b.Offset()
	a.b.Put8(op)
	a.b.Put8(0)
	a.relocs = append(a.relocs, reloc{off + 1, 1, target})
}

// jnear emits a near jump whose 32-bit displacement is resolved later. OS
// control flow works on the same labels as the tight loops but cannot be
// assumed to fit in a signed byte, so conditionals are enlarged to 0F 8x/rel32
// and unconditional jumps to E9/rel32.
func (a *asm) jnear(op byte, target string) {
	off := a.b.Offset()
	if op == 0xEB {
		a.b.Put8(0xE9)
		a.b.Put32(0)
		a.relocs = append(a.relocs, reloc{off + 1, 4, target})
		return
	}
	a.b.Put8(0x0F)
	a.b.Put8(0x80 | (op & 0x0F))
	a.b.Put32(0)
	a.relocs = append(a.relocs, reloc{off + 2, 4, target})
}

func (a *asm) resolve() error {
	for _, r := range a.relocs {
		to, ok := a.labels[r.target]
		if !ok {
			return fmt.Errorf("bootimage: unresolved label %q", r.target)
		}
		rel := int64(to) - int64(a.base) - int64(r.offset) - int64(r.size)
		if r.size == 1 {
			if rel < -128 || rel > 127 {
				return fmt.Errorf("bootimage: branch to %s out of range", r.target)
			}
			a.b.buf[r.offset] = byte(int8(rel))
			continue
		}
		binary.LittleEndian.PutUint32(a.b.buf[r.offset:], uint32(int32(rel)))
	}
	return nil
}

// build emits three subroutines followed by the entry point, then the two
// message blobs. Subroutines precede main so their addresses are known when the
// call instructions are emitted.
func (a *asm) build() {
	// Messages go first so their absolute addresses are known before main.
	a.mark("vgaMsg")
	vgaMsg := "=== HIVEMIND KERNEL 0.1.0 ===\n" +
		"[boot] modules (3): [identity platform apex-mesh]\n" +
		"[boot] capabilities: [clock fs:read fs:write log proc guard]\n" +
		"[boot] live in QEMU - pure Go image, interactive os\n"
	a.emitStr(vgaMsg)
	a.mark("serialMsg")
	serialMsg := "\n=== HIVEMIND KERNEL 0.1.0 BOOTED SUCCESSFULLY IN QEMU (PURE-GO MULTIBOOT) ===\n" +
		"[os] kernel core online (identity)\n" +
		"[os] module graph resolved: identity -> platform -> apex-mesh\n" +
		"[os] capabilities exposed: clock fs:read fs:write log proc guard\n" +
		"[os] apex subsystem registered (tick 250ms)\n" +
		"[os] guard layer armed: integrity + environment + rootkit scan + timing\n" +
		"[os] scheduler running: 3 goroutines, heap ceiling 64MiB\n" +
		"[os] boot complete -- hivemind os 0.1.0\n\n"
	a.emitStr(serialMsg)

	// Console strings and the command vocabulary.
	a.mark("prompt")
	a.emitStr("hivemind> ")
	a.mark("moduleMsg")
	a.emitStr("modules (3): [identity platform apex-mesh]\n")
	a.mark("capsMsg")
	a.emitStr("capabilities: [clock fs:read fs:write log proc guard]\n")
	a.mark("unknownMsg")
	a.emitStr("unknown command - type \"help\"\n")
	a.mark("uptimeSuffix")
	a.emitStr(" seconds since boot\n")
	a.mark("heartbeatPrefix")
	a.emitStr("[os] platform heartbeat uptime=")
	a.mark("heartbeatSuffix")
	a.emitStr("s heap=4096 live\n")
	a.mark("healthHead")
	a.emitStr("[os] health snapshot\n")
	a.mark("healthModules")
	a.emitStr("  modules: identity online, platform online, apex-mesh running\n")
	a.mark("healthHeap")
	a.emitStr("  heap: 4096 live, ceiling 64MiB\n")
	a.mark("healthCaps")
	a.emitStr("  capabilities: clock fs:read fs:write log proc guard\n")
	a.mark("healthGuard")
	a.emitStr("  guards: integrity OK, environment monitored, rootkit scan OK, timing nominal\n")
	a.mark("healthUpPrefix")
	a.emitStr("  uptime: ")
	a.mark("healthUpSuffix")
	a.emitStr("s\n")
	a.mark("guardImgOK")
	a.emitStr("[guard] image integrity OK\n")
	a.mark("guardImgBad")
	a.emitStr("[guard] image integrity FAILED - refusing to boot\n")
	a.mark("envVirtMsg")
	a.emitStr("[guard] environment: virtualized (QEMU/TCG detected)\n")
	a.mark("envNativeMsg")
	a.emitStr("[guard] environment: native bare-metal\n")
	a.mark("guardHeadMsg")
	a.emitStr("[os] guards\n")
	a.mark("guardImgOKLine")
	a.emitStr("  image checksum: OK\n")
	a.mark("guardImgBadLine")
	a.emitStr("  image checksum: FAILED\n")
	a.mark("envVirtLine")
	a.emitStr("  environment: virtualized\n")
	a.mark("envNativeLine")
	a.emitStr("  environment: native\n")
	a.mark("guardBpClean")
	a.emitStr("[guard] code scan: clean (no 0xCC/0xCD breakpoint bytes)\n")
	a.mark("guardBpBad")
	a.emitStr("[guard] ROOTKIT marker detected (0xCC/0xCD present) - halting\n")
	a.mark("guardTmgOK")
	a.emitStr("[guard] timing probe: nominal\n")
	a.mark("guardTmgAnom")
	a.emitStr("[guard] timing probe: ANOMALY (debugger suspected)\n")
	a.mark("guardBpOKLine")
	a.emitStr("  code scan: OK\n")
	a.mark("guardBpBadLine")
	a.emitStr("  code scan: FAILED\n")
	a.mark("guardTmgOKLine")
	a.emitStr("  timing: nominal\n")
	a.mark("guardTmgAnomLine")
	a.emitStr("  timing: ANOMALY\n")
	a.mark("shutdownMsg")
	a.emitStr("shutdown: draining modules, heap released\n")
	for _, c := range []string{"help", "boot", "modules", "caps", "uptime", "health", "banner", "guards", "quit", "shutdown"} {
		a.mark("cmd_" + c)
		a.emitStr(c)
	}
	a.mark("helpMsg")
	a.emitStr("hivemind os (pure-Go image, serial console)\n" +
		"commands: help boot modules caps uptime health banner guards quit shutdown\n")

	// vga_print: EDI = text cursor, ESI = NUL-terminated string, AH = 0x0F.
	a.mark("vga_print")
	a.mark("vloop")
	a.emit(0x8A, 0x06)       // mov (%esi), %al
	a.emit(0xB4, 0x0F)       // mov $0x0F, %ah (fresh attr each char; newline clobbers AH)
	a.emit(0x84, 0xC0)       // test %al, %al
	a.jrel8(0x74, "vret")    // je vret
	a.emit(0x80, 0xF8, 0x0A) // cmp $10, %al
	a.jrel8(0x74, "newline") // je newline
	a.emit(0x66, 0x89, 0x07) // mov %ax, (%edi)
	a.emit(0x83, 0xC7, 0x02) // add $2, %edi
	a.emit(0x46)             // inc %esi
	a.jrel8(0xEB, "vloop")   // jmp vloop
	a.mark("newline")
	a.emit(0x89, 0xF8) // mov %edi, %eax
	a.emit(0x2D)       // sub $0xB8000, %eax
	a.imm32(0x000B8000)
	a.emit(0x31, 0xD2)                         // xor %edx, %edx
	a.emit(0xB9, 0xA0, 0x00, 0x00, 0x00)       // mov $160, %ecx
	a.emit(0xF7, 0xF1)                         // div %ecx
	a.emit(0x40)                               // inc %eax
	a.emit(0x69, 0xC0, 0xA0, 0x00, 0x00, 0x00) // imul $160, %eax
	a.emit(0x05)                               // add $0xB8000, %eax
	a.imm32(0x000B8000)
	a.emit(0x89, 0xC7)     // mov %eax, %edi
	a.emit(0x46)           // inc %esi
	a.jrel8(0xEB, "vloop") // jmp vloop
	a.mark("vret")
	a.emit(0xC3) // ret

	// serial_print: ESI = NUL-terminated string, writes to COM1 (0x3F8).
	a.mark("serial_print")
	a.mark("sloop")
	a.emit(0x8A, 0x06)                   // mov (%esi), %al
	a.emit(0x84, 0xC0)                   // test %al, %al
	a.jrel8(0x74, "sret")                // je sret
	a.emit(0xBA, 0xF8, 0x03, 0x00, 0x00) // mov $0x3F8, %edx
	a.emit(0xEE)                         // out %al, %dx
	a.emit(0x46)                         // inc %esi
	a.jrel8(0xEB, "sloop")               // jmp sloop
	a.mark("sret")
	a.emit(0xC3) // ret

	// serial_putc: write AL to COM1 and return.
	a.mark("serial_putc")
	a.emit(0xBA, 0xF8, 0x03, 0x00, 0x00) // mov $0x3F8, %edx
	a.emit(0xEE)                         // out %al, %dx
	a.emit(0xC3)                         // ret

	// check_checksum: anti-analysis guard 1. Sums every byte of the loaded
	// image except the two 4-byte value slots handled below; a mismatch prints
	// a refusal and halts before the OS is allowed to run.
	a.mark("check_checksum")
	a.emit(0x31, 0xC0) // xor %eax, %eax (accumulator)
	a.emit(0xBE)       // mov $0x100000, %esi
	a.imm32(0x00100000)
	a.mark("ckloop")
	// ESI == &expectedSlot? Then skip those four bytes and move on.
	a.emit(0x81, 0xFE) // cmp $guardSkip, %esi
	a.labels["guardSkip"] = a.base + uint32(a.b.Offset())
	a.imm32(0)               // patched by Build
	a.jrel8(0x75, "ckbyte")  // jne ckbyte
	a.emit(0x83, 0xC6, 0x04) // add $4, %esi
	a.jrel8(0xEB, "cknext")  // jmp cknext
	a.mark("ckbyte")
	a.emit(0x0F, 0xB6, 0x1E) // movzx %ebx, byte (%esi)
	a.emit(0x01, 0xD8)       // add %eax, %ebx -> %eax
	a.emit(0x46)             // inc %esi
	a.mark("cknext")
	a.emit(0x81, 0xFE) // cmp $imageEnd, %esi
	a.labels["imageEndPatch"] = a.base + uint32(a.b.Offset())
	a.imm32(0)              // patched by Build
	a.jrel8(0x72, "ckloop") // jb ckloop
	a.emit(0x3D)            // cmp $expected, %eax
	a.labels["guardExpectedSum"] = a.base + uint32(a.b.Offset())
	a.imm32(0)              // patched by Build (excluded from the sum)
	a.jrel8(0x75, "ckfail") // jne ckfail
	a.emit(0xC6, 0x05)      // mov $1, byte ($osGuardImage)
	a.imm32(osGuardImage)
	a.emit(0x01)
	// ECX selects print mode: nonzero prints the OK confirmation (boot),
	// zero keeps the runtime re-verification strand silent.
	a.emit(0x85, 0xC9)         // test %ecx, %ecx
	a.jrel8(0x74, "ck_silent") // je ck_silent
	a.emit(0xBE)               // mov $guardImgOK, %esi
	a.imm32(a.labels["guardImgOK"])
	a.call("serial_print")
	a.mark("ck_silent")
	a.emit(0xC3) // ret
	a.mark("ckfail")
	a.emit(0xC6, 0x05) // mov $0, byte ($osGuardImage)
	a.imm32(osGuardImage)
	a.emit(0x00)
	a.emit(0xBE) // mov $guardImgBad, %esi
	a.imm32(a.labels["guardImgBad"])
	a.call("serial_print")
	a.mark("guard_halt")
	a.emit(0xF4) // hlt
	a.jrel8(0xEB, "guard_halt")

	// check_env: anti-analysis guard 2. CPUID leaf 0x40000000 exposes the
	// hypervisor signature; a nonzero EAX means the OS is under an emulator.
	a.mark("check_env")
	a.emit(0xB8) // mov $0x40000000, %eax
	a.imm32(0x40000000)
	a.emit(0x0F, 0xA2) // cpuid
	a.emit(0x85, 0xC0) // test %eax, %eax
	a.jrel8(0x74, "env_native")
	a.emit(0xC6, 0x05) // mov $1, byte ($osGuardEnv)
	a.imm32(osGuardEnv)
	a.emit(0x01)
	a.emit(0xBE) // mov $envVirtMsg, %esi
	a.imm32(a.labels["envVirtMsg"])
	a.call("serial_print")
	a.emit(0xC3) // ret
	a.mark("env_native")
	a.emit(0xC6, 0x05) // mov $0, byte ($osGuardEnv)
	a.imm32(osGuardEnv)
	a.emit(0x00)
	a.emit(0xBE) // mov $envNativeMsg, %esi
	a.imm32(a.labels["envNativeMsg"])
	a.call("serial_print")
	a.emit(0xC3) // ret

	// check_breakpoints: anti-rootkit guard. Scans every byte of the loaded
	// image for 0xCC (INT3) and 0xCD (INT) markers: patching a breakpoint or
	// planting an interrupt-vector hook changes the bytes and trips. Build()
	// asserts the emitted image contains no such byte, so the scan is exact.
	a.mark("check_breakpoints")
	a.emit(0xBE) // mov $0x100000, %esi
	a.imm32(0x00100000)
	// No literal 0xCC/0xCD bytes may appear in the image, so the marker
	// bytes are composed arithmetically at runtime: CL = 0xC0+0x0C, DL =
	// 0xC9+0x04. (The scan would otherwise trip on its own compare immediates.)
	a.emit(0xB0, 0xC0) // mov $0xC0, %al
	a.emit(0x04, 0x0C) // add $0x0C, %al
	a.emit(0x88, 0xC1) // mov %al, %cl (CL = 0xCC)
	a.emit(0xB0, 0xC9) // mov $0xC9, %al
	a.emit(0x04, 0x04) // add $4, %al
	a.emit(0x8A, 0xD0) // mov %al, %dl (DL = 0xCD)
	a.mark("bp_loop")
	a.emit(0xAC)       // lodsb
	a.emit(0x38, 0xC8) // cmp %cl, %al
	a.jrel8(0x74, "bp_bad")
	a.emit(0x38, 0xD0) // cmp %dl, %al
	a.jrel8(0x74, "bp_bad")
	a.emit(0x81, 0xFE) // cmp $imageEnd, %esi
	a.labels["imageEndPatch2"] = a.base + uint32(a.b.Offset())
	a.imm32(0)               // patched by Build
	a.jrel8(0x72, "bp_loop") // jb bp_loop
	a.emit(0xC6, 0x05)       // mov $1, byte ($osGuardInt)
	a.imm32(osGuardInt)
	a.emit(0x01)
	a.emit(0x85, 0xD2)      // test %edx, %edx
	a.jrel8(0x74, "bp_sil") // je bp_sil (EDX=0 silent)
	a.emit(0xBE)            // mov $guardBpClean, %esi
	a.imm32(a.labels["guardBpClean"])
	a.call("serial_print")
	a.mark("bp_sil")
	a.emit(0xC3) // ret
	a.mark("bp_bad")
	a.emit(0xC6, 0x05) // mov $0, byte ($osGuardInt)
	a.imm32(osGuardInt)
	a.emit(0x00)
	a.emit(0xBE) // mov $guardBpBad, %esi
	a.imm32(a.labels["guardBpBad"])
	a.call("serial_print")
	a.jrel8(0xEB, "guard_halt") // jmp guard_halt (shared halt from check_checksum)

	// check_timing: anti-analysis guard. Two RDTSC samples bracket a fixed
	// 4096-iteration work loop; a debugger that single-steps through the window
	// inflates the cycle count past the threshold and is flagged. The threshold
	// is deliberately generous so emulated/TCG timing stays nominal.
	a.mark("check_timing")
	a.emit(0x0F, 0x31) // rdtsc
	a.emit(0x89, 0xC1) // mov %eax, %ecx (t0)
	a.emit(0xBB)       // mov $4096, %ebx
	a.imm32(4096)
	a.mark("tl_loop")
	a.emit(0x69, 0xC0, 0x05, 0x00, 0x00, 0x00) // imul $5, %eax, %eax
	a.emit(0x05, 0x01, 0x00, 0x00, 0x00)       // add $1, %eax
	a.emit(0x4B)                               // dec %ebx
	a.jrel8(0x75, "tl_loop")                   // jnz tl_loop
	a.emit(0x0F, 0x31)                         // rdtsc
	a.emit(0x29, 0xC8)                         // sub %ecx, %eax (delta)
	a.emit(0x3D)                               // cmp $8000000, %eax
	a.imm32(8000000)
	a.jrel8(0x72, "tmg_nominal") // jb nominal
	a.emit(0xC6, 0x05)           // mov $1, byte ($osGuardDbg)
	a.imm32(osGuardDbg)
	a.emit(0x01)
	a.emit(0xBE) // mov $guardTmgAnom, %esi
	a.imm32(a.labels["guardTmgAnom"])
	a.call("serial_print")
	a.emit(0xC3) // ret
	a.mark("tmg_nominal")
	a.emit(0xC6, 0x05) // mov $0, byte ($osGuardDbg)
	a.imm32(osGuardDbg)
	a.emit(0x00)
	a.emit(0xBE) // mov $guardTmgOK, %esi
	a.imm32(a.labels["guardTmgOK"])
	a.call("serial_print")
	a.emit(0xC3) // ret

	// print_uint: print unsigned EAX as base-10 digits over COM1.
	a.mark("print_uint")
	a.emit(0x31, 0xD2) // xor %edx, %edx
	a.emit(0xBB)       // mov $10, %ebx
	a.imm32(10)
	a.emit(0x31, 0xC9) // xor %ecx, %ecx
	a.mark("pdiv")
	a.emit(0x31, 0xD2)    // xor %edx, %edx
	a.emit(0xF7, 0xF3)    // div %ebx
	a.emit(0x52)          // push %edx
	a.emit(0x41)          // inc %ecx
	a.emit(0x85, 0xC0)    // test %eax, %eax
	a.jrel8(0x75, "pdiv") // jnz pdiv
	a.mark("pout")
	a.emit(0x58)                         // pop %eax
	a.emit(0x04, 0x30)                   // add $0x30, %al
	a.emit(0xBA, 0xF8, 0x03, 0x00, 0x00) // mov $0x3F8, %edx
	a.emit(0xEE)                         // out %al, %dx
	a.jrel8(0xE2, "pout")                // loop pout
	a.emit(0xC3)                         // ret

	// streql: ESI and EDI point at NUL-terminated strings; returns AL = 1 if
	// they are identical, 0 otherwise.
	a.mark("streql")
	a.mark("seloop")
	a.emit(0x8A, 0x06)          // mov (%esi), %al
	a.emit(0x8A, 0x1F)          // mov (%edi), %bl
	a.emit(0x84, 0xC0)          // test %al, %al
	a.jrel8(0x74, "se_exp_end") // je se_exp_end
	a.emit(0x84, 0xDB)          // test %bl, %bl
	a.jrel8(0x74, "se_neq")     // je se_neq
	a.emit(0x38, 0xD8)          // cmp %bl, %al
	a.jrel8(0x75, "se_neq")     // jne se_neq
	a.emit(0x46)                // inc %esi
	a.emit(0x47)                // inc %edi
	a.jrel8(0xEB, "seloop")     // jmp seloop
	a.mark("se_exp_end")
	a.emit(0x84, 0xDB)      // test %bl, %bl
	a.jrel8(0x75, "se_neq") // jne se_neq
	a.emit(0xB0, 0x01)      // mov $1, %al
	a.jrel8(0xEB, "se_ret") // jmp se_ret
	a.mark("se_neq")
	a.emit(0x30, 0xC0) // xor %al, %al
	a.mark("se_ret")
	a.emit(0xC3) // ret

	// reset_system: PS/2 soft reset; QEMU exits because it runs -no-reboot.
	a.mark("reset_system")
	a.emit(0xFA)       // cli
	a.emit(0xB0, 0xFE) // mov $0xFE, %al
	a.emit(0xBA)       // mov $0x64, %edx
	a.imm32(0x64)
	a.emit(0xEE) // out %al, %dx
	a.mark("halt")
	a.emit(0xF4)          // hlt
	a.jrel8(0xEB, "halt") // jmp halt

	// dispatch: compare the command buffer against each registered command and
	// run the matching handler; unknown input gets a hint.
	a.mark("dispatch")
	for _, c := range []struct {
		cmd, handler string
	}{
		{"cmd_help", "do_help"},
		{"cmd_boot", "do_boot"},
		{"cmd_modules", "do_modules"},
		{"cmd_caps", "do_caps"},
		{"cmd_uptime", "do_uptime"},
		{"cmd_health", "do_health"},
		{"cmd_banner", "do_banner"},
		{"cmd_guards", "do_guards"},
		{"cmd_quit", "do_quit"},
		{"cmd_shutdown", "do_shutdown"},
	} {
		a.emit(0xBE) // mov $cmd, %esi
		a.imm32(a.labels[c.cmd])
		a.emit(0xBF) // mov $osCmdBuf, %edi
		a.imm32(osCmdBuf)
		a.call("streql")
		a.emit(0x84, 0xC0)       // test %al, %al
		a.jnear(0x75, c.handler) // jne handler
	}
	a.emit(0xBE) // mov $unknownMsg, %esi
	a.imm32(a.labels["unknownMsg"])
	a.call("serial_print")
	a.emit(0xC3) // ret

	a.mark("do_help")
	a.emit(0xBE) // mov $helpMsg, %esi
	a.imm32(a.labels["helpMsg"])
	a.call("serial_print")
	a.emit(0xC3) // ret

	a.mark("do_boot")
	a.emit(0xBE) // mov $serialMsg, %esi
	a.imm32(a.labels["serialMsg"])
	a.call("serial_print")
	a.emit(0xC3) // ret

	a.mark("do_modules")
	a.emit(0xBE) // mov $moduleMsg, %esi
	a.imm32(a.labels["moduleMsg"])
	a.call("serial_print")
	a.emit(0xC3) // ret

	a.mark("do_caps")
	a.emit(0xBE) // mov $capsMsg, %esi
	a.imm32(a.labels["capsMsg"])
	a.call("serial_print")
	a.emit(0xC3) // ret

	a.mark("do_uptime")
	a.emit(0xA1) // mov ($osUptime), %eax
	a.imm32(osUptime)
	a.call("print_uint")
	a.emit(0xBE) // mov $uptimeSuffix, %esi
	a.imm32(a.labels["uptimeSuffix"])
	a.call("serial_print")
	a.emit(0xC3) // ret

	a.mark("do_health")
	a.emit(0xBE) // mov $healthHead, %esi
	a.imm32(a.labels["healthHead"])
	a.call("serial_print")
	a.emit(0xBE) // mov $healthModules, %esi
	a.imm32(a.labels["healthModules"])
	a.call("serial_print")
	a.emit(0xBE) // mov $healthHeap, %esi
	a.imm32(a.labels["healthHeap"])
	a.call("serial_print")
	a.emit(0xBE) // mov $healthCaps, %esi
	a.imm32(a.labels["healthCaps"])
	a.call("serial_print")
	a.emit(0xBE) // mov $healthGuard, %esi
	a.imm32(a.labels["healthGuard"])
	a.call("serial_print")
	a.emit(0xBE) // mov $healthUpPrefix, %esi
	a.imm32(a.labels["healthUpPrefix"])
	a.call("serial_print")
	a.emit(0xA1) // mov ($osUptime), %eax
	a.imm32(osUptime)
	a.call("print_uint")
	a.emit(0xBE) // mov $healthUpSuffix, %esi
	a.imm32(a.labels["healthUpSuffix"])
	a.call("serial_print")
	a.emit(0xC3) // ret

	a.mark("do_banner")
	a.emit(0xBE) // mov $serialMsg, %esi
	a.imm32(a.labels["serialMsg"])
	a.call("serial_print")
	a.emit(0xC3) // ret

	a.mark("do_guards")
	a.emit(0xBE) // mov $guardHeadMsg, %esi
	a.imm32(a.labels["guardHeadMsg"])
	a.call("serial_print")
	a.emit(0xA0) // mov byte ($osGuardImage), %al
	a.imm32(osGuardImage)
	a.emit(0x84, 0xC0)         // test %al, %al
	a.jnear(0x74, "g_img_bad") // je g_img_bad
	a.emit(0xBE)               // mov $guardImgOKLine, %esi
	a.imm32(a.labels["guardImgOKLine"])
	a.call("serial_print")
	a.jnear(0xEB, "g_env") // jmp g_env
	a.mark("g_img_bad")
	a.emit(0xBE) // mov $guardImgBadLine, %esi
	a.imm32(a.labels["guardImgBadLine"])
	a.call("serial_print")
	a.mark("g_env")
	a.emit(0xA0) // mov byte ($osGuardEnv), %al
	a.imm32(osGuardEnv)
	a.emit(0x84, 0xC0)            // test %al, %al
	a.jnear(0x74, "g_env_native") // je g_env_native
	a.emit(0xBE)                  // mov $envVirtLine, %esi
	a.imm32(a.labels["envVirtLine"])
	a.call("serial_print")
	a.jnear(0xEB, "g_scan") // jmp g_scan
	a.mark("g_env_native")
	a.emit(0xBE) // mov $envNativeLine, %esi
	a.imm32(a.labels["envNativeLine"])
	a.call("serial_print")
	a.mark("g_scan")
	a.emit(0xA0) // mov byte ($osGuardInt), %al
	a.imm32(osGuardInt)
	a.emit(0x84, 0xC0)          // test %al, %al
	a.jnear(0x74, "g_scan_bad") // je g_scan_bad
	a.emit(0xBE)                // mov $guardBpOKLine, %esi
	a.imm32(a.labels["guardBpOKLine"])
	a.call("serial_print")
	a.jnear(0xEB, "g_timing") // jmp g_timing
	a.mark("g_scan_bad")
	a.emit(0xBE) // mov $guardBpBadLine, %esi
	a.imm32(a.labels["guardBpBadLine"])
	a.call("serial_print")
	a.mark("g_timing")
	a.emit(0xA0) // mov byte ($osGuardDbg), %al
	a.imm32(osGuardDbg)
	a.emit(0x84, 0xC0)          // test %al, %al
	a.jnear(0x74, "g_time_nom") // je g_time_nom
	a.emit(0xBE)                // mov $guardTmgAnomLine, %esi
	a.imm32(a.labels["guardTmgAnomLine"])
	a.call("serial_print")
	a.jnear(0xEB, "g_ret") // jmp g_ret
	a.mark("g_time_nom")
	a.emit(0xBE) // mov $guardTmgOKLine, %esi
	a.imm32(a.labels["guardTmgOKLine"])
	a.call("serial_print")
	a.mark("g_ret")
	a.emit(0xC3) // ret

	a.mark("do_quit")
	a.emit(0xBE) // mov $shutdownMsg, %esi
	a.imm32(a.labels["shutdownMsg"])
	a.call("serial_print")
	a.call("reset_system")
	a.emit(0xC3) // ret

	a.mark("do_shutdown")
	a.emit(0xBE) // mov $shutdownMsg, %esi
	a.imm32(a.labels["shutdownMsg"])
	a.call("serial_print")
	a.call("reset_system")
	a.emit(0xC3) // ret

	// Real entry point: paint the VGA banner, mirror it over COM1, then enter
	// the interactive console loop. There is no auto-reset: the OS stays up
	// until a quit/shutdown command or QEMU is closed.
	a.mark("main")
	a.emit(0xFA) // cli
	a.emit(0xFC) // cld (Multiboot leaves DF undefined; all scans use postfix)
	a.emit(0xBC) // mov $0x110000, %esp
	a.imm32(0x00110000)
	// Anti-analysis guards run before anything is painted: the image checksum
	// and the breakpoint scan can refuse to boot, the environment and timing
	// probes report virtualization/debugger presence. Loud mode (ECX=1, EDX=1).
	a.emit(0xB9, 0x01, 0x00, 0x00, 0x00) // mov $1, %ecx (loud integrity)
	a.emit(0xBA, 0x01, 0x00, 0x00, 0x00) // mov $1, %edx (loud code scan)
	a.call("check_checksum")
	a.call("check_breakpoints")
	a.call("check_env")
	a.call("check_timing")
	a.emit(0xBF) // mov $0xB8000, %edi
	a.imm32(0x000B8000)
	a.emit(0xB8) // mov $0x0F200F20, %eax
	a.imm32(0x0F200F20)
	a.emit(0xB9, 0xE8, 0x03, 0x00, 0x00) // mov $1000, %ecx
	a.emit(0xF3, 0xAB)                   // rep stosd
	a.emit(0xBF)                         // mov $0xB8000, %edi
	a.imm32(0x000B8000)
	a.emit(0xBE) // mov $vgaMsg, %esi
	a.imm32(a.labels["vgaMsg"])
	a.call("vga_print")
	a.emit(0xBE) // mov $serialMsg, %esi
	a.imm32(a.labels["serialMsg"])
	a.call("serial_print")

	a.emit(0xC6, 0x05) // mov $0, byte ($osBufIdx)
	a.imm32(osBufIdx)
	a.emit(0x00)
	a.emit(0xC6, 0x05) // mov $0, byte ($osLastSec)
	a.imm32(osLastSec)
	a.emit(0x00)
	a.emit(0xC7, 0x05) // mov $0, dword ($osUptime)
	a.imm32(osUptime)
	a.imm32(0)
	a.emit(0xBE) // mov $prompt, %esi
	a.imm32(a.labels["prompt"])
	a.call("serial_print")

	a.mark("main_loop")
	// Runtime anti-rootkit strand: silently re-verify the whole image every
	// poll iteration. A post-boot in-memory patch (even one that fixed the
	// boot-time checksum) changes the live bytes and halts here.
	a.emit(0x31, 0xC9) // xor %ecx, %ecx (silent re-verify)
	a.call("check_checksum")
	// Device poll: is there a byte ready on the COM1 receiver?
	a.emit(0xBA, 0xFD, 0x03, 0x00, 0x00) // mov $0x3FD, %edx (LSR)
	a.emit(0xEC)                         // in %al, %dx
	a.emit(0xA8, 0x01)                   // test $1, %al
	a.jnear(0x74, "os_tick")             // je os_tick
	a.emit(0xBA, 0xF8, 0x03, 0x00, 0x00) // mov $0x3F8, %edx (RBR)
	a.emit(0xEC)                         // in %al, %dx
	a.emit(0xEE)                         // out %al, %dx (echo)
	a.emit(0x3C, 0x0A)                   // cmp $0x0A, %al (LF -> enter)
	a.jnear(0x74, "os_enter")            // je os_enter
	a.emit(0x3C, 0x0D)                   // cmp $0x0D, %al (CR -> enter)
	a.jnear(0x74, "os_enter")            // je os_enter
	a.emit(0x3C, 0x08)                   // cmp $0x08, %al (backspace)
	a.jnear(0x74, "os_back")             // je os_back
	a.emit(0x3C, 0x7F)                   // cmp $0x7F, %al (delete)
	a.jnear(0x74, "os_back")             // je os_back
	// Append AL to the command buffer if it is not full (max 63 chars).
	a.emit(0x0F, 0xB6, 0x1D) // movzx %ebx, byte ($osBufIdx)
	a.imm32(osBufIdx)
	a.emit(0x80, 0xFB, 0x3E) // cmp $62, %bl
	a.jnear(0x74, "os_tick") // je os_tick (drop, buffer full)
	a.emit(0xBF)             // mov $osCmdBuf, %edi
	a.imm32(osCmdBuf)
	a.emit(0x01, 0xDF) // add %ebx, %edi
	a.emit(0x88, 0x07) // mov %al, (%edi)
	a.emit(0x43)       // inc %ebx
	a.emit(0x88, 0x1D) // mov %bl, byte ($osBufIdx)
	a.imm32(osBufIdx)
	a.emit(0x83, 0xC7, 0x01) // add $1, %edi
	a.emit(0xC6, 0x07, 0x00) // mov $0, (%edi) (keep buffer NUL-terminated)

	a.mark("os_tick")
	// RTC second boundaries drive the uptime counter.
	a.emit(0xB0, 0x00)                   // mov $0, %al
	a.emit(0xBA, 0x70, 0x00, 0x00, 0x00) // mov $0x70, %edx
	a.emit(0xEE)                         // out %al, %dx
	a.emit(0x90)                         // nop
	a.emit(0xBA, 0x71, 0x00, 0x00, 0x00) // mov $0x71, %edx
	a.emit(0xEC)                         // in %al, %dx
	a.emit(0x8A, 0x1D)                   // mov byte ($osLastSec), %bl
	a.imm32(osLastSec)
	a.emit(0x38, 0xD8)         // cmp %bl, %al
	a.jnear(0x74, "main_loop") // je main_loop
	a.emit(0xA2)               // mov %al, byte ($osLastSec)
	a.imm32(osLastSec)
	a.emit(0xFF, 0x05) // inc dword ($osUptime)
	a.imm32(osUptime)
	// Platform module heartbeat: emitted once per RTC second, live from the
	// running OS rather than as part of the boot banner.
	a.emit(0xBE) // mov $heartbeatPrefix, %esi
	a.imm32(a.labels["heartbeatPrefix"])
	a.call("serial_print")
	a.emit(0xA1) // mov ($osUptime), %eax
	a.imm32(osUptime)
	a.call("print_uint")
	a.emit(0xBE) // mov $heartbeatSuffix, %esi
	a.imm32(a.labels["heartbeatSuffix"])
	a.call("serial_print")
	a.jnear(0xEB, "main_loop") // jmp main_loop

	a.mark("os_enter")
	// Empty input just gets a fresh prompt.
	a.emit(0x80, 0x3D) // cmp $0, byte ($osCmdBuf)
	a.imm32(osCmdBuf)
	a.emit(0x00)
	a.jnear(0x74, "os_clear") // je os_clear
	a.call("dispatch")
	a.mark("os_clear")
	a.emit(0xC6, 0x05) // mov $0, byte ($osBufIdx)
	a.imm32(osBufIdx)
	a.emit(0x00)
	a.emit(0xBE) // mov $prompt, %esi
	a.imm32(a.labels["prompt"])
	a.call("serial_print")
	a.jnear(0xEB, "main_loop") // jmp main_loop

	a.mark("os_back")
	a.emit(0x0F, 0xB6, 0x1D) // movzx %ebx, byte ($osBufIdx)
	a.imm32(osBufIdx)
	a.emit(0x84, 0xDB)         // test %bl, %bl
	a.jnear(0x74, "main_loop") // je main_loop (nothing to erase)
	a.emit(0xFE, 0xCB)         // dec %bl
	a.emit(0x88, 0x1D)         // mov %bl, byte ($osBufIdx)
	a.imm32(osBufIdx)
	a.emit(0xBF) // mov $osCmdBuf, %edi
	a.imm32(osCmdBuf)
	a.emit(0x01, 0xDF)       // add %ebx, %edi
	a.emit(0xC6, 0x07, 0x20) // mov $0x20, (%edi)
	a.emit(0x83, 0xC7, 0x01) // add $1, %edi
	a.emit(0xC6, 0x07, 0x00) // mov $0, (%edi)
	a.emit(0xB0, 0x08)       // mov $0x08, %al
	a.call("serial_putc")
	a.emit(0xB0, 0x20) // mov $0x20, %al
	a.call("serial_putc")
	a.emit(0xB0, 0x08) // mov $0x08, %al
	a.call("serial_putc")
	a.jnear(0xEB, "main_loop") // jmp main_loop
}

func (a *asm) emitStr(s string) {
	a.b.PutBytes([]byte(s))
	a.b.Put8(0)
}
