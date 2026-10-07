package bininspect

import (
	"context"
	"debug/pe"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// PE section characteristic flags. debug/pe does not export these, so they are
// defined here from the PE specification.
//
// The dangerous combination is MEM_WRITE|MEM_EXECUTE (0xA0000000): a region
// that is both writable and executable is self-modifying code by definition,
// and is the single most common indicator of packed or injected payloads.
const (
	scnCntCode              = 0x00000020
	scnCntInitializedData   = 0x00000040
	scnCntUninitializedData = 0x00000080
	scnMemExecute           = 0x20000000
	scnMemRead              = 0x40000000
	scnMemWrite             = 0x80000000
)

// PE file-header characteristic flags used for anomaly detection.
const (
	imageFileRelocsStripped = 0x0001
	imageFileExecutable     = 0x0002
	imageFileLargeAddrAware = 0x0020
	imageFileDebugStripped  = 0x0200
	imageFileSystem         = 0x1000
	imageFileDLL            = 0x2000
)

// PE subsystem values worth flagging.
const (
	subsystemNative  = 1 // runs without the Windows loader
	subsystemGUI     = 2
	subsystemWinCE   = 9
	subsystemEFIApp  = 10
	subsystemEFIBoot = 11
	subsystemEFIROM  = 12
	subsystemXBOX    = 14
	subsystemWinRT   = 16
)

// maxPESections is the section count above which an image is treated as
// packed. Real compilers emit a handful; packers emit dozens.
const maxPESections = 96

// maxTruncatedPESections bounds the loop that walks imported descriptors, so a
// malformed import directory cannot spin.
const maxTruncatedPESections = 4096

// peAnalysis carries the shared reader so each helper can re-read the file
// without re-opening it.
type peAnalysis struct {
	file *pe.File
	rsrc io.ReaderAt
	size int64
	warn func(format string, args ...any)
	// st carries cancellation and warning state. A pointer so both survive
	// across the whole analysis without being threaded through every helper.
	st *internalState
}

// rvaToOffset maps a relative virtual address to a file offset using the
// section table.
//
// RVA-to-offset is the crux of import parsing: the import table stores RVAs,
// but the file is laid out by section. A virtual size larger than the raw size
// is normal (BSS), and an RVA beyond every section is a malformed image.
func (p *peAnalysis) rvaToOffset(rva uint32) (int64, bool) {
	for _, s := range p.file.Sections {
		va := s.VirtualAddress
		// Prefer the virtual extent, then fall back to raw size, so an RVA in
		// the raw part of a section still resolves.
		extent := s.VirtualSize
		if extent < s.Size {
			extent = s.Size
		}
		if extent == 0 {
			extent = s.Size
		}
		if rva >= va && rva < va+uint32(extent) {
			return int64(s.Offset) + int64(rva-va), true
		}
	}
	return 0, false
}

// readAt reads len(buf) bytes at off, reporting short reads as an error.
func (p *peAnalysis) readAt(off int64, buf []byte) error {
	n, err := p.rsrc.ReadAt(buf, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if n < len(buf) {
		return io.ErrUnexpectedEOF
	}
	return nil
}

// analyzePE parses and audits a PE image.
//
// The returned report always contains the identity, section entropy analysis,
// and header anomaly checks. Import-derived findings depend on the import
// directory resolving; when it does not, the reason is recorded in Stats.Warnings
// rather than silently omitted.
func analyzePE(ctx context.Context, rsrc io.ReaderAt, size int64, name string, modTime time.Time, st *internalState) (*Report, error) {
	f, err := pe.NewFile(rsrc)
	if err != nil {
		return nil, fmt.Errorf("pe: %w", err)
	}
	defer f.Close()

	id, err := identify(name, io.NewSectionReader(rsrc, 0, size), modTime)
	if err != nil {
		return nil, fmt.Errorf("pe identity: %w", err)
	}

	a := &peAnalysis{file: f, rsrc: rsrc, size: size, st: st}
	a.warn = func(format string, args ...any) {
		st.warnf("pe: "+format, args...)
	}

	rep := &Report{
		Format:    "pe",
		Identity:  id,
		IsLibrary: f.FileHeader.Characteristics&imageFileDLL != 0,
		Stats:     AnalysisStats{BytesHashed: size},
	}

	rep.Sections, rep.Stats.SectionsParsed = a.sections()

	// Entry point: read from the optional header for both PE32 and PE32+.
	if f.OptionalHeader != nil {
		switch oh := f.OptionalHeader.(type) {
		case *pe.OptionalHeader32:
			rep.EntryPoint = uint64(oh.AddressOfEntryPoint)
			rep.Arch = "x86"
		case *pe.OptionalHeader64:
			rep.EntryPoint = uint64(oh.AddressOfEntryPoint)
			rep.Arch = "x86-64"
		default:
			rep.EntryPoint = uint64(a.entryPointRVA())
		}
	}

	rep.Libraries, rep.Imports, rep.Stats.ImportsParsed, rep.Stats.Truncated = a.imports()
	rep.Findings = a.findings(rep, st.opts)
	rep.Risk = buildRisk(rep.Findings)
	rep.Stats.Duration = st.elapsed()
	return rep, nil
}

// entryPointRVA reads the entry-point RVA without a type switch, tolerating an
// optional header type this build does not recognise.
func (p *peAnalysis) entryPointRVA() uint32 {
	switch oh := p.file.OptionalHeader.(type) {
	case *pe.OptionalHeader32:
		return oh.AddressOfEntryPoint
	case *pe.OptionalHeader64:
		return oh.AddressOfEntryPoint
	}
	return 0
}

// sections builds a SectionReport per section with entropy and permissions.
func (p *peAnalysis) sections() ([]SectionReport, int) {
	out := make([]SectionReport, 0, len(p.file.Sections))
	parsed := 0
	for _, s := range p.file.Sections {
		// Poll cancellation at section granularity: frequent enough to bound a
		// scan, cheap enough to be free.
		if p.st != nil && p.st.cancelled() {
			break
		}
		flags := s.Characteristics
		sec := SectionReport{
			Name:        s.Name,
			Offset:      int64(s.Offset),
			Size:        int64(s.Size),
			VirtualSize: int64(s.VirtualSize),
			Read:        flags&scnMemRead != 0,
			Write:       flags&scnMemWrite != 0,
			Execute:     flags&scnMemExecute != 0,
		}
		sec.WritableAndExecutable = sec.Write && sec.Execute

		// Entropy is computed over the bytes actually present in the file,
		// capped at the raw size so a large virtual size (BSS) is not treated
		// as readable data.
		if s.Size > 0 && int64(s.Offset+s.Size) <= p.size {
			if buf := make([]byte, s.Size); len(buf) > 0 {
				if err := p.readAt(int64(s.Offset), buf); err == nil {
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

// imports walks the import directory and recovers named symbols.
//
// debug/pe exposes imported library names but not the function names, which
// are the half that matters for anti-analysis auditing. The Import Name Table
// is therefore walked by hand: each descriptor points at an array of thunks,
// and each non-ordinal thunk points at an IMAGE_IMPORT_BY_NAME.
func (p *peAnalysis) imports() (libs []ImportedLibrary, syms []ImportedSymbol, count int, truncated bool) {
	is64 := false
	switch oh := p.file.OptionalHeader.(type) {
	case *pe.OptionalHeader64:
		is64 = true
		_ = oh
	}

	importRVA, importSize, ok := p.importDirectory()
	if !ok || importRVA == 0 {
		return nil, nil, 0, false
	}

	descOff, ok := p.rvaToOffset(importRVA)
	if !ok {
		p.warn("import directory RVA %#x does not map to any section", importRVA)
		return nil, nil, 0, false
	}

	thunkSize := uint32(4)
	if is64 {
		thunkSize = 8
	}

	seenLibs := make(map[string]int)
	// Each descriptor is 20 bytes; the table is NUL-terminated.
	maxDescs := int(importSize/thunkSize) + 8
	if maxDescs > maxTruncatedPESections {
		maxDescs = maxTruncatedPESections
	}

	for i := 0; i < maxDescs; i++ {
		off := descOff + int64(i)*20
		var desc [20]byte
		if err := p.readAt(off, desc[:]); err != nil {
			// Past the end of file: treat as the end of the table, and say so.
			truncated = true
			break
		}
		// An all-zero descriptor terminates the list.
		if desc[0] == 0 && desc[1] == 0 && desc[2] == 0 && desc[3] == 0 &&
			desc[4] == 0 && desc[5] == 0 && desc[6] == 0 && desc[7] == 0 &&
			desc[8] == 0 && desc[9] == 0 && desc[10] == 0 && desc[11] == 0 &&
			desc[12] == 0 && desc[13] == 0 && desc[14] == 0 && desc[15] == 0 &&
			desc[16] == 0 && desc[17] == 0 && desc[18] == 0 && desc[19] == 0 {
			break
		}

		nameRVA := binary.LittleEndian.Uint32(desc[12:16])
		thunkRVA := binary.LittleEndian.Uint32(desc[0:4]) // OriginalFirstThunk (INT)
		if thunkRVA == 0 {
			// No INT is legal; fall back to FirstThunk (IAT).
			thunkRVA = binary.LittleEndian.Uint32(desc[16:20])
		}

		libName := p.readStringAtRVA(nameRVA, 256)
		if libName == "" {
			libName = "<unnamed>"
		}
		thunkOff, ok := p.rvaToOffset(thunkRVA)
		if !ok {
			continue
		}

		// Walk this library's thunk array.
		for t := 0; t < maxTruncatedPESections; t++ {
			toff := thunkOff + int64(t)*int64(thunkSize)
			var raw uint64
			var tb [8]byte
			if err := p.readAt(toff, tb[:thunkSize]); err != nil {
				truncated = true
				break
			}
			if is64 {
				raw = binary.LittleEndian.Uint64(tb[:8])
			} else {
				raw = uint64(binary.LittleEndian.Uint32(tb[:4]))
			}
			if raw == 0 {
				break // NUL terminator ends this library's list
			}

			// The high bit marks an import by ordinal rather than by name.
			ordinalBit := uint64(1) << 63
			if is64 {
				ordinalBit = uint64(1) << 63
			} else {
				ordinalBit = uint64(1) << 31
			}
			if raw&ordinalBit != 0 {
				syms = append(syms, ImportedSymbol{
					Library:   libName,
					Name:      "",
					Ordinal:   uint16(raw & 0xFFFF),
					ByOrdinal: true,
				})
				count++
				continue
			}

			hintRVA := uint32(raw & 0x7FFFFFFF)
			name := p.readImportName(hintRVA)
			if name == "" {
				continue
			}
			syms = append(syms, ImportedSymbol{Library: libName, Name: name})
			count++
			seenLibs[libName]++
		}
	}

	libs = make([]ImportedLibrary, 0, len(seenLibs))
	for n, c := range seenLibs {
		libs = append(libs, ImportedLibrary{Name: n, SymbolCount: c})
	}
	sort.Slice(libs, func(i, j int) bool { return libs[i].Name < libs[j].Name })
	return libs, syms, count, truncated
}

// importDirectory returns the import table RVA and size from the optional
// header's data directory (index 1).
func (p *peAnalysis) importDirectory() (rva, size uint32, ok bool) {
	if p.file.OptionalHeader == nil {
		return 0, 0, false
	}
	const importDirIndex = 1
	switch oh := p.file.OptionalHeader.(type) {
	case *pe.OptionalHeader32:
		if int(oh.NumberOfRvaAndSizes) <= importDirIndex {
			return 0, 0, false
		}
		dd := oh.DataDirectory[importDirIndex]
		return dd.VirtualAddress, dd.Size, true
	case *pe.OptionalHeader64:
		if int(oh.NumberOfRvaAndSizes) <= importDirIndex {
			return 0, 0, false
		}
		dd := oh.DataDirectory[importDirIndex]
		return dd.VirtualAddress, dd.Size, true
	}
	return 0, 0, false
}

// readImportName reads an IMAGE_IMPORT_BY_NAME: a 2-byte hint then a NUL-
// terminated name.
func (p *peAnalysis) readImportName(rva uint32) string {
	off, ok := p.rvaToOffset(rva)
	if !ok {
		return ""
	}
	// Skip the 2-byte hint.
	return p.readStringAt(off+2, 256)
}

// readStringAtRVA reads a NUL-terminated string located at an RVA.
func (p *peAnalysis) readStringAtRVA(rva uint32, max int) string {
	off, ok := p.rvaToOffset(rva)
	if !ok {
		return ""
	}
	return p.readStringAt(off, max)
}

// readStringAt reads up to max bytes from off and returns the NUL-terminated
// prefix. Non-printable bytes end the string, which keeps a garbage offset from
// producing a megabyte of junk in the report.
func (p *peAnalysis) readStringAt(off int64, max int) string {
	if off < 0 || off >= p.size {
		return ""
	}
	if avail := p.size - off; int64(max) > avail {
		max = int(avail)
	}
	if max <= 1 {
		return ""
	}
	buf := make([]byte, max)
	if err := p.readAt(off, buf); err != nil {
		return ""
	}
	for i, c := range buf {
		if c == 0 {
			return string(buf[:i])
		}
		if c < 0x20 || c > 0x7E {
			return ""
		}
	}
	return string(buf)
}

// findings assembles all PE findings: section permissions, entropy,
// header anomalies, and import-derived detections.
func (a *peAnalysis) findings(rep *Report, opts Options) []Finding {
	var out []Finding

	out = append(out, a.sectionPermissionFindings(rep)...)
	out = append(out, a.entropyFindings(rep)...)
	out = append(out, a.headerFindings(rep)...)
	if !opts.DisableByteScan {
		out = append(out, a.byteScanFindings(rep)...)
	}

	// Import-derived findings: static and dynamic classes.
	out = append(out, matchImports(tables.antiDebug, rep.Imports, "TRI-AD")...)
	out = append(out, matchImports(tables.injection, rep.Imports, "TRI-INJ")...)
	out = append(out, matchImports(tables.environment, rep.Imports, "TRI-ENV")...)
	out = append(out, matchImports(tables.humanInteract, rep.Imports, "TRI-HUM")...)
	out = append(out, matchImports(tables.timing, rep.Imports, "TRI-TIM")...)
	out = append(out, matchImports(tables.sleep, rep.Imports, "TRI-SLP")...)

	// VM/sandbox markers: only the data-bearing sections are worth scanning.
	if !opts.DisableByteScan {
		out = append(out, a.markerFindings()...)
	}

	// Whole-image marker sweep catches PE images where a marker sits outside a
	// recognisable data section.
	if opts.FullStringScan {
		// The whole-image sweep is the most expensive stage, so it is guarded
		// rather than assumed to finish. A cancelled scan still returns the
		// findings gathered so far, with a warning marking them partial — a
		// partial report beats an error for a triage pipeline.
		switch {
		case a.st != nil && a.st.cancelled():
			a.warn("analysis cancelled during scan; findings are partial")
		default:
			if buf := readRange(a.rsrc, 0, min64(a.size, opts.MaxScanBytes)); len(buf) > 0 {
				f, _ := ScanVMMarkers(buf)
				out = append(out, f...)
			}
		}
	}

	return out
}

// sectionPermissionFindings flags writable-and-executable regions.
func (a *peAnalysis) sectionPermissionFindings(rep *Report) []Finding {
	var evidence []Evidence
	var names []string
	for _, s := range rep.Sections {
		if s.WritableAndExecutable {
			evidence = append(evidence, Evidence{
				Kind:    "section",
				Detail:  "section " + s.Name + " is writable and executable (IMAGE_SCN_MEM_WRITE|IMAGE_SCN_MEM_EXECUTE)",
				Offset:  s.Offset,
				Section: s.Name,
			})
			names = append(names, s.Name)
		}
	}
	if len(evidence) == 0 {
		return nil
	}
	return []Finding{{
		ID:          "TRI-SEC-WX",
		Category:    CategoryStatic,
		Vector:      VectorSectionPermissions,
		Severity:    SeverityHigh,
		Confidence:  0.92,
		Title:       "Writable+executable section: " + strings.Join(uniqueStrings(names), ", "),
		Description: "The image requests memory that is both writable and executable. Legitimate code rarely needs this; it is required for self-modifying code, runtime unpacking, and in-process payload injection.",
		Remediation: "Enforce a W^X policy in the analysis environment; treat the section contents as untrusted until unpacked under instrumentation.",
		Evidence:    dedupeEvidence(evidence),
	}}
}

// entropyFindings flags packed or encrypted sections.
func (a *peAnalysis) entropyFindings(rep *Report) []Finding {
	var evidence []Evidence
	var names []string
	for _, s := range rep.Sections {
		// A section too small to measure is not evidence.
		if s.Size < minPackedSectionBytes {
			continue
		}
		class := packingClass(s.Entropy, s.Size)
		if class != EntropyCompressed && class != EntropyEncrypted {
			continue
		}
		_, verdict := entropyVerdict(s.Name, s.Entropy, s.Size)
		evidence = append(evidence, Evidence{
			Kind:    "section",
			Detail:  s.Name + ": " + verdict,
			Offset:  s.Offset,
			Section: s.Name,
		})
		names = append(names, s.Name+" ("+formatFloat(s.Entropy)+" over "+humanBytes(s.Size)+")")
	}
	if len(evidence) == 0 {
		return nil
	}
	conf := 0.70
	for _, s := range rep.Sections {
		if s.Size < minPackedSectionBytes {
			continue
		}
		if packingClass(s.Entropy, s.Size) == EntropyEncrypted {
			conf = 0.90
			break
		}
		if s.Size >= largePackedSectionBytes && conf < 0.90 {
			conf = 0.90
		} else if s.Size >= mediumPackedSectionBytes && conf < 0.80 {
			conf = 0.80
		}
	}
	return []Finding{{
		ID:          "TRI-ENT-HIGH",
		Category:    CategoryStatic,
		Vector:      VectorPackedOrEncrypted,
		Severity:    severityForConfidence(conf),
		Confidence:  conf,
		Title:       "High-entropy section suggests packing or encryption: " + strings.Join(names, ", "),
		Description: "One or more sections have byte entropy consistent with compressed or encrypted content rather than compiled code or plain data.",
		Remediation: "Unpack in a disposable sandbox before triage; static analysis of a packed image sees only the packer stub, not the payload.",
		Evidence:    dedupeEvidence(evidence),
	}}
}

// headerFindings checks object-file header fields for anti-analysis anomalies.
func (a *peAnalysis) headerFindings(rep *Report) []Finding {
	var evidence []Evidence
	fh := a.file.FileHeader

	if n := len(a.file.Sections); n > maxPESections {
		evidence = append(evidence, Evidence{
			Kind:   "header",
			Detail: fmt.Sprintf("NumberOfSections=%d exceeds the %d-section practical maximum, a known packer trait", n, maxPESections),
		})
	}

	// A DLL with relocations stripped cannot be relocated, which defeats ASLR
	// and is a common anti-analysis trick.
	if fh.Characteristics&imageFileDLL != 0 && fh.Characteristics&imageFileRelocsStripped != 0 {
		evidence = append(evidence, Evidence{
			Kind:   "header",
			Detail: "IMAGE_FILE_DLL with IMAGE_FILE_RELOCS_STRIPPED: the image cannot be relocated, defeating ASLR",
		})
	}

	// A timestamp of zero is anti-forensic; a far-future one is usually forged.
	if fh.TimeDateStamp == 0 {
		evidence = append(evidence, Evidence{
			Kind:   "header",
			Detail: "TimeDateStamp=0 (zeroed to frustrate timeline analysis)",
		})
	} else if t := time.Unix(int64(fh.TimeDateStamp), 0); t.Year() > time.Now().Year()+1 {
		evidence = append(evidence, Evidence{
			Kind:   "header",
			Detail: "TimeDateStamp is in the future (" + t.UTC().Format("2006-01-02") + "), suggesting a forged or corrupted header",
		})
	}

	// Packer section names are a direct fingerprint.
	for _, s := range a.file.Sections {
		name := strings.ToLower(strings.TrimRight(s.Name, "\x00"))
		if packer, ok := packerSectionNames[name]; ok {
			evidence = append(evidence, Evidence{
				Kind:    "section",
				Detail:  "section name " + clip(s.Name, 16) + " is characteristic of " + packer,
				Offset:  int64(s.Offset),
				Section: s.Name,
			})
		}
	}

	if oh := a.file.OptionalHeader; oh != nil {
		switch h := oh.(type) {
		case *pe.OptionalHeader32:
			a.optionalHeaderEvidence(&evidence, h.Subsystem, uint64(h.ImageBase), h.DllCharacteristics, h.AddressOfEntryPoint)
		case *pe.OptionalHeader64:
			a.optionalHeaderEvidence(&evidence, h.Subsystem, uint64(h.ImageBase), h.DllCharacteristics, h.AddressOfEntryPoint)
		}
	}

	if len(evidence) == 0 {
		return nil
	}
	return []Finding{{
		ID:          "TRI-HDR-ANOM",
		Category:    CategoryStatic,
		Vector:      VectorHeaderAnomalies,
		Severity:    SeverityMedium,
		Confidence:  0.60,
		Title:       "Object-file header anomalies",
		Description: "One or more object-file header fields are unusual enough to indicate anti-analysis intent or heavy packing.",
		Remediation: "Treat the header as unreliable metadata; establish behaviour from observed execution, not from declared fields.",
		Evidence:    dedupeEvidence(evidence),
	}}
}

// optionalHeaderEvidence inspects subsystem, image base, and DLL characteristics.
func (a *peAnalysis) optionalHeaderEvidence(ev *[]Evidence, subsystem uint16, imageBase uint64, dllChars uint16, entryRVA uint32) {
	// Native subsystem binaries run without the Windows loader, a rootkit trait.
	if subsystem == subsystemNative {
		*ev = append(*ev, Evidence{
			Kind:   "header",
			Detail: "Subsystem=NATIVE: runs without the Windows loader",
		})
	}
	switch subsystem {
	case subsystemEFIApp, subsystemEFIBoot, subsystemEFIROM:
		*ev = append(*ev, Evidence{
			Kind:   "header",
			Detail: fmt.Sprintf("Subsystem=%d (EFI): unusual for a general-purpose Windows image", subsystem),
		})
	case subsystemXBOX, subsystemWinCE, subsystemWinRT:
		*ev = append(*ev, Evidence{
			Kind:   "header",
			Detail: fmt.Sprintf("Subsystem=%d (non-desktop): atypical target platform", subsystem),
		})
	}

	if imageBase == 0 {
		*ev = append(*ev, Evidence{
			Kind:   "header",
			Detail: "ImageBase=0: degenerate load address",
		})
	}

	// ASLR not opted into (DYNAMIC_BASE clear) on an executable that is
	// relocatable is a hardening gap worth noting.
	const dynamicBase = 0x0040
	if dllChars&dynamicBase == 0 {
		*ev = append(*ev, Evidence{
			Kind:   "header",
			Detail: "DllCharacteristics lacks DYNAMIC_BASE: ASLR is not enabled",
		})
	}

	// An entry point of zero is invalid for an executable image.
	if entryRVA == 0 {
		*ev = append(*ev, Evidence{
			Kind:   "header",
			Detail: "AddressOfEntryPoint=0: no valid entry point",
		})
	}
}

// byteScanFindings scans executable sections for direct syscalls and timing
// patterns, and for API-hash constants.
func (a *peAnalysis) byteScanFindings(rep *Report) []Finding {
	var findings []Finding
	var execBytes int64
	var scanned []Evidence

	for _, s := range rep.Sections {
		if !s.Execute || s.Size == 0 {
			continue
		}
		if a.st != nil && a.st.cancelled() {
			break
		}
		if s.Offset+s.Size > rep.Identity.Size {
			continue
		}
		buf := make([]byte, s.Size)
		if err := a.readAt(s.Offset, buf); err != nil {
			continue
		}
		execBytes += int64(len(buf))

		hits := ScanOpcodeSets(buf)
		if len(hits) > 0 {
			scanned = append(scanned, Evidence{
				Kind:    "opcode",
				Detail:  "section " + s.Name + ": " + itoa(len(hits)) + " syscall/timing instruction pattern(s)",
				Offset:  s.Offset,
				Section: s.Name,
			})
		}

		if direct := filterKinds(hits, "syscall", "sysenter", "int-2e", "int-80", "svc-aarch64", "svc-arm", "direct-syscall-stub"); len(direct) > 0 {
			findings = append(findings, syscallFinding(s.Name, s.Offset, direct))
		}
		if pairs := filterKinds(hits, "rdtsc-pair"); len(pairs) > 0 {
			findings = append(findings, rdtscFinding(s.Name, s.Offset, pairs))
		}

		if h := ScanAPIHashConstants(buf); len(h) > 0 {
			findings = append(findings, apiHashFinding(s.Name, s.Offset, h))
		}
	}

	rep.Stats.BytesScanned = execBytes
	_ = scanned
	return findings
}

// filterKinds selects hits whose Kind is in the given set.
func filterKinds(hits []OpcodeHit, kinds ...string) []OpcodeHit {
	want := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		want[k] = true
	}
	var out []OpcodeHit
	for _, h := range hits {
		if want[h.Kind] {
			out = append(out, h)
		}
	}
	return out
}

// syscallFinding reports direct system-call instructions.
func syscallFinding(section string, base int64, hits []OpcodeHit) Finding {
	var evidence []Evidence
	conf := 0.0
	for _, h := range hits {
		evidence = append(evidence, Evidence{
			Kind:    "opcode",
			Detail:  h.Detail,
			Offset:  base + int64(h.Offset),
			Section: section,
		})
		if h.Confidence > conf {
			conf = h.Confidence
		}
	}
	evidence = dedupeEvidence(evidence)
	return Finding{
		ID:          "TRI-SYSCALL",
		Category:    CategoryDynamic,
		Vector:      VectorDirectSyscall,
		Severity:    severityForConfidence(conf),
		Confidence:  conf,
		Title:       "Direct system-call instruction in code (bypasses user-mode API hooks)",
		Description: "The code section contains system-call instructions reached without going through the ntdll export table. This bypasses user-mode API monitoring, inline hooks, and IAT-based sandboxing, so the import table does not describe what the code actually does.",
		Remediation: "Instrument at the kernel boundary (ETW, syscall table, or emulation). User-mode hooks will not observe these calls.",
		Evidence:    evidence,
	}
}

// rdtscFinding reports RDTSC pairs indicative of a timing check.
func rdtscFinding(section string, base int64, hits []OpcodeHit) Finding {
	var evidence []Evidence
	conf := 0.0
	for _, h := range hits {
		evidence = append(evidence, Evidence{
			Kind:    "opcode",
			Detail:  h.Detail,
			Offset:  base + int64(h.Offset),
			Section: section,
		})
		if h.Confidence > conf {
			conf = h.Confidence
		}
	}
	return Finding{
		ID:          "TRI-RDTSC",
		Category:    CategoryDynamic,
		Vector:      VectorTimingCheck,
		Severity:    severityForConfidence(conf),
		Confidence:  conf,
		Title:       "RDTSC timing pair: elapsed-time anti-debug candidate",
		Description: "Two timestamp reads appear close together in the code section. This is the signature of an elapsed-time check used to detect single-stepping, breakpoints, or a slowed analysis environment.",
		Remediation: "If a branch depends on the difference, force both sides and compare behaviour; accelerate time so the threshold is not trivially crossed.",
		Evidence:    dedupeEvidence(evidence),
	}
}

// apiHashFinding reports well-known API-hash constants.
func apiHashFinding(section string, base int64, hits []OpcodeHit) Finding {
	var evidence []Evidence
	conf := 0.0
	for _, h := range hits {
		evidence = append(evidence, Evidence{
			Kind:    "opcode",
			Detail:  h.Detail,
			Offset:  base + int64(h.Offset),
			Section: section,
		})
		if h.Confidence > conf {
			conf = h.Confidence
		}
	}
	return Finding{
		ID:          "TRI-APIHASH",
		Category:    CategoryStatic,
		Vector:      VectorAPIHashing,
		Severity:    severityForConfidence(conf),
		Confidence:  conf,
		Title:       "API-hashing constant: imports are likely resolved at runtime",
		Description: "A magic constant matching a known module-name hash was found in the code. API hashing resolves functions by hash instead of name, which is why the import table may be sparse or misleading.",
		Remediation: "Treat the import table as incomplete; resolve APIs at runtime (dump) before drawing conclusions from static imports.",
		Evidence:    dedupeEvidence(evidence),
	}
}

// markerFindings scans PE data sections for VM/sandbox markers.
func (a *peAnalysis) markerFindings() []Finding {
	var findings []Finding
	for _, s := range a.file.Sections {
		if s.Size == 0 || int64(s.Offset+s.Size) > a.size {
			continue
		}
		if a.st != nil && a.st.cancelled() {
			break
		}
		// Skip executable sections; markers live in data.
		if s.Characteristics&scnMemExecute != 0 {
			continue
		}
		buf := make([]byte, s.Size)
		if err := a.readAt(int64(s.Offset), buf); err != nil {
			continue
		}
		if f, _ := ScanVMMarkers(buf); len(f) > 0 {
			for i := range f {
				f[i].Evidence = dedupeEvidence(f[i].Evidence)
			}
			findings = append(findings, f...)
		}
	}
	return findings
}
