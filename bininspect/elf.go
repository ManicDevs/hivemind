package bininspect

import (
	"context"
	"debug/elf"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// analyzeELF parses and audits an ELF image.
//
// ELF carries no import table in the PE sense: a dynamic binary resolves
// symbols through DT_NEEDED libraries and the PLT/GOT, and debug/elf does not
// decode symbol names for us. So the dynamic-evasion detections that rely on
// named APIs (anti-debug, injection, VM fingerprinting) are largely
// PE-oriented. What this analyzer can do reliably, and does, is:
//
//   - section and segment permission analysis, including the PF_W|PF_X
//     violation that is the ELF counterpart of a W+X section;
//   - per-section entropy for packing and encryption;
//   - header anomalies (fixed load address, missing PIE, stripped symbols,
//     unusual ELF types);
//   - imported library-name analysis, which is the strongest ELF signal for
//     sandbox and VM-agent linkage;
//   - opcode scanning of executable segments for direct system calls, which is
//     especially relevant on ELF where the syscall convention is direct;
//   - marker scanning for hypervisor and sandbox strings.
func analyzeELF(ctx context.Context, rsrc io.ReaderAt, size int64, name string, modTime time.Time, st *internalState) (*Report, error) {
	f, err := elf.NewFile(rsrc)
	if err != nil {
		return nil, fmt.Errorf("elf: %w", err)
	}
	defer f.Close()

	id, err := identify(name, io.NewSectionReader(rsrc, 0, size), modTime)
	if err != nil {
		return nil, fmt.Errorf("elf identity: %w", err)
	}

	a := &elfAnalysis{file: f, rsrc: rsrc, size: size, st: st}
	a.warn = func(format string, args ...any) { st.warnf("elf: "+format, args...) }
	opts := st.opts

	rep := &Report{
		Format:     "elf",
		Identity:   id,
		EntryPoint: f.Entry,
		IsLibrary:  f.Type == elf.ET_DYN,
		Stripped:   f.Section(".symtab") == nil,
		Stats:      AnalysisStats{BytesHashed: size},
	}

	rep.Arch = elfArchName(f)

	rep.Sections, rep.Stats.SectionsParsed = a.sections()
	rep.Segments = a.segments()

	rep.Libraries, rep.Stats.ImportsParsed = a.libraries()

	var findings []Finding
	findings = append(findings, a.segmentPermissionFindings(rep)...)
	findings = append(findings, a.entropyFindings(rep)...)
	findings = append(findings, a.headerFindings(rep)...)
	findings = append(findings, a.libraryFindings(rep)...)

	if !opts.DisableByteScan {
		findings = append(findings, a.byteScanFindings(rep)...)
		findings = append(findings, a.markerFindings()...)
	}
	if opts.FullStringScan && !st.cancelled() {
		if buf := readRange(rsrc, 0, min64(size, opts.MaxScanBytes)); len(buf) > 0 {
			fs, _ := ScanVMMarkers(buf)
			findings = append(findings, fs...)
		}
	} else if opts.FullStringScan {
		// Cancelled mid-analysis. The report is still returned, with a warning,
		// rather than discarding the findings gathered so far — a partial report
		// is more useful to a triage pipeline than an error.
		st.warnf("analysis cancelled during scan; findings are partial")
	}

	rep.Findings = findings
	rep.Risk = buildRisk(rep.Findings)
	rep.Stats.Duration = st.elapsed()
	return rep, nil
}

// elfAnalysis carries the shared reader for ELF helpers.
type elfAnalysis struct {
	file *elf.File
	rsrc io.ReaderAt
	size int64
	warn func(format string, args ...any)
	st   *internalState
}

// elfArchName renders a human-readable architecture string.
func elfArchName(f *elf.File) string {
	switch f.Machine {
	case elf.EM_386:
		return "x86"
	case elf.EM_X86_64:
		return "x86-64"
	case elf.EM_ARM:
		return "ARM"
	case elf.EM_AARCH64:
		return "AArch64"
	case elf.EM_MIPS:
		return "MIPS"
	case elf.EM_RISCV:
		return "RISC-V"
	case elf.EM_PPC64:
		return "PowerPC64"
	case elf.EM_S390:
		return "s390x"
	case elf.EM_LOONGARCH:
		return "LoongArch"
	default:
		return f.Machine.String()
	}
}

// sections builds SectionReports with entropy and decoded permission bits.
func (a *elfAnalysis) sections() ([]SectionReport, int) {
	out := make([]SectionReport, 0, len(a.file.Sections))
	parsed := 0
	for _, s := range a.file.Sections {
		if a.st != nil && a.st.cancelled() {
			break
		}
		// SHT_NOBITS sections (BSS) occupy no file bytes, so there is nothing
		// to measure; report them as empty rather than reading past the file.
		if s.Type == elf.SHT_NOBITS {
			out = append(out, SectionReport{
				Name: s.Name, Offset: int64(s.Offset), Size: int64(s.Size),
				VirtualSize: int64(s.Size), EntropyClass: EntropyEmpty,
				Read: s.Flags&elf.SHF_WRITE == 0, Write: s.Flags&elf.SHF_WRITE != 0,
				Execute: s.Flags&elf.SHF_EXECINSTR != 0,
			})
			continue
		}
		sec := SectionReport{
			Name:        s.Name,
			Offset:      int64(s.Offset),
			Size:        int64(s.FileSize),
			VirtualSize: int64(s.Size),
			Read:        s.Flags&elf.SHF_ALLOC != 0,
			Write:       s.Flags&elf.SHF_WRITE != 0,
			Execute:     s.Flags&elf.SHF_EXECINSTR != 0,
		}
		sec.WritableAndExecutable = sec.Write && sec.Execute

		if s.FileSize > 0 && int64(s.Offset+s.FileSize) <= a.size {
			if buf := make([]byte, s.FileSize); len(buf) > 0 {
				if _, err := a.rsrc.ReadAt(buf, int64(s.Offset)); err == nil {
					sec.Entropy = ShannonEntropy(buf)
					sec.EntropyClass = ClassifyEntropy(sec.Entropy)
					parsed++
				}
			}
		} else {
			sec.EntropyClass = EntropyEmpty
		}
		out = append(out, sec)
	}
	return out, parsed
}

// segments renders program headers, flagging W^X violations.
func (a *elfAnalysis) segments() []ProgramSegmentReport {
	out := make([]ProgramSegmentReport, 0, len(a.file.Progs))
	for _, p := range a.file.Progs {
		seg := ProgramSegmentReport{
			Type:   p.Type.String(),
			Offset: int64(p.Off),
			Vaddr:  p.Vaddr,
			Size:   p.Filesz,
		}
		var flags []string
		if p.Flags&elf.PF_R != 0 {
			flags = append(flags, "R")
		}
		if p.Flags&elf.PF_W != 0 {
			flags = append(flags, "W")
			seg.WritableAndExecutable = p.Flags&elf.PF_X != 0
		}
		if p.Flags&elf.PF_X != 0 {
			flags = append(flags, "X")
		}
		seg.Flags = strings.Join(flags, "")
		out = append(out, seg)
	}
	return out
}

// libraries recovers DT_NEEDED entries, the ELF analogue of an import library
// list. A guest-agent library name is a strong environment-fingerprint signal.
func (a *elfAnalysis) libraries() ([]ImportedLibrary, int) {
	libs, err := a.file.ImportedLibraries()
	if err != nil {
		a.warn("imported libraries unavailable: %v", err)
		return nil, 0
	}
	out := make([]ImportedLibrary, 0, len(libs))
	for _, l := range libs {
		out = append(out, ImportedLibrary{Name: l, SymbolCount: 0})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, len(libs)
}

// segmentPermissionFindings flags PT_LOAD segments that are writable and
// executable, the ELF counterpart of a PE W+X section.
func (a *elfAnalysis) segmentPermissionFindings(rep *Report) []Finding {
	var evidence []Evidence
	var names []string
	for _, s := range rep.Segments {
		if !s.WritableAndExecutable {
			continue
		}
		evidence = append(evidence, Evidence{
			Kind:   "segment",
			Detail: "segment " + s.Type + " has PF_W|PF_X: writable and executable memory",
			Offset: s.Offset,
		})
		names = append(names, fmt.Sprintf("%s@%#x", s.Type, s.Vaddr))
	}
	if len(evidence) == 0 {
		return nil
	}
	return []Finding{{
		ID:          "TRI-SEC-WX",
		Category:    CategoryStatic,
		Vector:      VectorSectionPermissions,
		Severity:    SeverityHigh,
		Confidence:  0.90,
		Title:       "Writable+executable segment: " + strings.Join(names, ", "),
		Description: "The program requests a loadable segment that is both writable and executable. Modern toolchains produce W^X segments; W+X is required for runtime-generated code, in-process unpacking, and JIT-style payload staging.",
		Remediation: "Enforce W^X with a hardened kernel or seccomp policy; unpack under instrumentation rather than granting W+X by default.",
		Evidence:    dedupeEvidence(evidence),
	}}
}

// entropyFindings flags packed or encrypted sections and segments.
func (a *elfAnalysis) entropyFindings(rep *Report) []Finding {
	var evidence []Evidence
	var names []string
	anyEncrypted := false
	for _, s := range rep.Sections {
		if s.EntropyClass != EntropyCompressed && s.EntropyClass != EntropyEncrypted {
			continue
		}
		if s.Size < 64 {
			continue
		}
		if s.EntropyClass == EntropyEncrypted {
			anyEncrypted = true
		}
		_, verdict := entropyVerdict(s.Name, s.Entropy, s.Size)
		evidence = append(evidence, Evidence{
			Kind:    "section",
			Detail:  s.Name + ": " + verdict,
			Offset:  s.Offset,
			Section: s.Name,
		})
		names = append(names, s.Name+" ("+formatFloat(s.Entropy)+")")
	}
	if len(evidence) == 0 {
		return nil
	}
	conf := 0.70
	if anyEncrypted {
		conf = 0.88
	}
	return []Finding{{
		ID:          "TRI-ENT-HIGH",
		Category:    CategoryStatic,
		Vector:      VectorPackedOrEncrypted,
		Severity:    severityForConfidence(conf),
		Confidence:  conf,
		Title:       "High-entropy section suggests packing or encryption: " + strings.Join(names, ", "),
		Description: "One or more ELF sections have byte entropy consistent with compressed or encrypted content. A stripped, statically linked, high-entropy executable is a common shape for a packed Linux payload.",
		Remediation: "Unpack in a disposable sandbox; static analysis of a packed image sees only the decompression stub.",
		Evidence:    dedupeEvidence(evidence),
	}}
}

// headerFindings inspects ELF header and program structure for anomalies.
func (a *elfAnalysis) headerFindings(rep *Report) []Finding {
	var evidence []Evidence
	f := a.file

	// ET_EXEC loads at a fixed address and cannot be relocated: no ASLR.
	if f.Type == elf.ET_EXEC {
		evidence = append(evidence, Evidence{
			Kind:   "header",
			Detail: "Type=ET_EXEC: fixed load address, no ASLR",
		})
	}
	if f.Type == elf.ET_REL {
		evidence = append(evidence, Evidence{
			Kind:   "header",
			Detail: "Type=ET_REL: relocatable object, not a runnable image",
		})
	}

	// No PT_INTERP means no dynamic loader, i.e. a fully static binary. Legitimate
	// for minimal systems; also how a payload avoids loader-based inspection.
	hasInterp := false
	hasDynamic := false
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			hasInterp = true
		}
		if p.Type == elf.PT_DYNAMIC {
			hasDynamic = true
		}
	}
	if !hasInterp && hasDynamic == false && rep.EntryPoint != 0 {
		evidence = append(evidence, Evidence{
			Kind:   "header",
			Detail: "no PT_INTERP and no PT_DYNAMIC: statically linked, bypassing the dynamic loader",
		})
	}

	// Retained symbol and debug tables are a gift to an analyst; their absence
	// is worth recording so the report says the image was harder to read.
	if !rep.Stripped {
		evidence = append(evidence, Evidence{
			Kind:   "header",
			Detail: "symbol table present (.symtab): image is not stripped",
		})
	}
	if s := f.Section(".gdb_index"); s != nil {
		evidence = append(evidence, Evidence{
			Kind:   "header",
			Detail: "debug index present (.gdb_index)",
		})
	}

	// Packer section names.
	for _, s := range f.Sections {
		if packer, ok := packerSectionNames[strings.ToLower(s.Name)]; ok {
			evidence = append(evidence, Evidence{
				Kind:    "section",
				Detail:  "section name " + clip(s.Name, 16) + " is characteristic of " + packer,
				Offset:  int64(s.Offset),
				Section: s.Name,
			})
		}
	}

	if len(evidence) == 0 {
		return nil
	}
	return []Finding{{
		ID:          "TRI-HDR-ANOM",
		Category:    CategoryStatic,
		Vector:      VectorHeaderAnomalies,
		Severity:    SeverityLow,
		Confidence:  0.50,
		Title:       "ELF header or layout anomalies",
		Description: "Object-file structure deviates from a conventional toolchain output, which can indicate static linking, packing, or deliberate anti-analysis.",
		Remediation: "Establish behaviour from execution rather than declared structure; these are indicators, not conclusions.",
		Evidence:    dedupeEvidence(evidence),
	}}
}

// libraryFindings audits DT_NEEDED entries for sandbox and VM-agent linkage.
//
// This is the ELF counterpart of the PE import-library audit: linking a guest
// agent or hooking library is strong evidence of environment awareness.
func (a *elfAnalysis) libraryFindings(rep *Report) []Finding {
	var out []Finding
	var evidence []Evidence
	for _, l := range rep.Libraries {
		if sig, ok := tables.libraries.lookup(l.Name); ok {
			evidence = append(evidence, Evidence{
				Kind:   "import",
				Detail: l.Name + " — " + sig.Technique,
			})
			out = append(out, Finding{
				ID:          "TRI-ENV-LIB",
				Category:    CategoryDynamic,
				Vector:      VectorEnvironmentProbe,
				Severity:    severityForConfidence(sig.Confidence),
				Confidence:  sig.Confidence,
				Title:       "Sandbox or hooking library linked: " + l.Name,
				Description: "The image links " + l.Name + ", which provides: " + sig.Technique + ".",
				Remediation: "Dynamic analysis in an instrumented environment will be intercepted; expect incomplete or misleading behaviour.",
				Evidence:    dedupeEvidence(evidence),
			})
		}
	}
	return out
}

// byteScanFindings scans executable segments for direct syscalls, timing
// patterns, and API-hash constants.
func (a *elfAnalysis) byteScanFindings(rep *Report) []Finding {
	var findings []Finding
	var execBytes int64

	for _, p := range a.file.Progs {
		if p.Type != elf.PT_LOAD || p.Flags&elf.PF_X == 0 || p.Filesz == 0 {
			continue
		}
		if a.st != nil && a.st.cancelled() {
			break
		}
		if int64(p.Off+p.Filesz) > a.size {
			continue
		}
		buf := make([]byte, p.Filesz)
		if _, err := a.rsrc.ReadAt(buf, int64(p.Off)); err != nil {
			continue
		}
		execBytes += int64(len(buf))

		hits := ScanOpcodeSets(buf)
		if direct := filterKinds(hits, "syscall", "sysenter", "int-2e", "int-80", "svc-aarch64", "svc-arm", "direct-syscall-stub"); len(direct) > 0 {
			findings = append(findings, syscallFinding(fmt.Sprintf("PT_LOAD %#x", p.Vaddr), int64(p.Off), direct))
		}
		if pairs := filterKinds(hits, "rdtsc-pair"); len(pairs) > 0 {
			findings = append(findings, rdtscFinding(fmt.Sprintf("PT_LOAD %#x", p.Vaddr), int64(p.Off), pairs))
		}
		if h := ScanAPIHashConstants(buf); len(h) > 0 {
			findings = append(findings, apiHashFinding(fmt.Sprintf("PT_LOAD %#x", p.Vaddr), int64(p.Off), h))
		}
	}

	rep.Stats.BytesScanned = execBytes
	return findings
}

// markerFindings scans non-executable sections for hypervisor and sandbox
// markers. On ELF, VM and sandbox artefacts are typically in .rodata or .data.
func (a *elfAnalysis) markerFindings() []Finding {
	var findings []Finding
	for _, s := range a.file.Sections {
		if s.Type == elf.SHT_NOBITS || s.FileSize == 0 {
			continue
		}
		if a.st != nil && a.st.cancelled() {
			break
		}
		if s.Flags&elf.SHF_EXECINSTR != 0 {
			continue
		}
		if int64(s.Offset+s.FileSize) > a.size {
			continue
		}
		buf := make([]byte, s.FileSize)
		if _, err := a.rsrc.ReadAt(buf, int64(s.Offset)); err != nil {
			continue
		}
		if f, _ := ScanVMMarkers(buf); len(f) > 0 {
			findings = append(findings, f...)
		}
	}
	return findings
}
