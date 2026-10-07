package bininspect

import (
	"sort"
	"strings"
)

// Signature is one named detection rule.
//
// A signature is data, not code, so the taxonomy can be extended or trimmed
// without touching detection logic. Case: Casing controls whether matching is
// case-insensitive (true for API and library names).
type Signature struct {
	// Name is the API, library, or marker being matched.
	Name string
	// Technique explains why this import or marker matters.
	Technique string
	// Confidence is the likelihood that mere presence indicates deliberate
	// anti-analysis rather than ordinary functionality.
	Confidence float64
	// Casing is true for names where case should be ignored.
	Casing bool
}

// signatureSet is an immutable, ordered collection of signatures.
//
// The tables below are package-level and are never written after init, so
// concurrent reads are safe without synchronisation.
type signatureSet struct {
	byName map[string]Signature
	names  []string
}

func newSignatureSet(sigs []Signature) signatureSet {
	m := make(map[string]Signature, len(sigs))
	names := make([]string, 0, len(sigs))
	for _, s := range sigs {
		if s.Casing {
			s.Name = strings.ToLower(s.Name)
		}
		if _, dup := m[s.Name]; dup {
			continue
		}
		m[s.Name] = s
		names = append(names, s.Name)
	}
	sort.Strings(names)
	return signatureSet{byName: m, names: names}
}

// lookup resolves a symbol to a signature, applying the casing rule per entry.
func (s signatureSet) lookup(symbol string) (Signature, bool) {
	if sig, ok := s.byName[symbol]; ok {
		return sig, true
	}
	lower := strings.ToLower(symbol)
	if sig, ok := s.byName[lower]; ok {
		return sig, true
	}
	return Signature{}, false
}

// antiDebugImports covers debugger and analyst-environment detection.
var antiDebugImports = newSignatureSet([]Signature{
	{Name: "IsDebuggerPresent", Technique: "PEB.BeingDebugged via KERNEL32", Confidence: 0.95},
	{Name: "CheckRemoteDebuggerPresent", Technique: "debugger presence query", Confidence: 0.95},
	{Name: "NtQueryInformationProcess", Technique: "ProcessDebugPort / ProcessDebugObjectHandle", Confidence: 0.85},
	{Name: "ZwQueryInformationProcess", Technique: "ProcessDebugPort / ProcessDebugObjectHandle", Confidence: 0.85},
	{Name: "NtQuerySystemInformation", Technique: "debugger and VM enumeration via SystemKernelDebuggerInformation", Confidence: 0.80},
	{Name: "ZwQuerySystemInformation", Technique: "debugger and VM enumeration", Confidence: 0.80},
	{Name: "NtSetInformationThread", Technique: "ThreadHideFromDebugger", Confidence: 0.90},
	{Name: "ZwSetInformationThread", Technique: "ThreadHideFromDebugger", Confidence: 0.90},
	{Name: "OutputDebugStringA", Technique: "debugger presence via SetLastError side effect", Confidence: 0.70, Casing: true},
	{Name: "OutputDebugStringW", Technique: "debugger presence via SetLastError side effect", Confidence: 0.70, Casing: true},
	{Name: "UnhandledExceptionFilter", Technique: "overwrites the filter under a debugger to hide crashes", Confidence: 0.65},
	{Name: "SetUnhandledExceptionFilter", Technique: "overwrites the filter under a debugger to hide crashes", Confidence: 0.65},
	{Name: "RaiseException", Technique: "structured-exception control-flow obfuscation", Confidence: 0.55},
	{Name: "NtClose", Technique: "invalid-handle anti-debug probe", Confidence: 0.40},
	{Name: "BlockInput", Technique: "blocks analyst input during execution", Confidence: 0.75},
	{Name: "SuspendThread", Technique: "anti-analysis thread suspension", Confidence: 0.60},
	{Name: "NtQueryObject", Technique: "debug object enumeration", Confidence: 0.70},
	{Name: "DebugActiveProcess", Technique: "debugger attaches to a child process", Confidence: 0.60},
})

// injectionImports covers process injection and process hollowing.
var injectionImports = newSignatureSet([]Signature{
	{Name: "VirtualAllocEx", Technique: "remote memory allocation for injection", Confidence: 0.95},
	{Name: "VirtualAlloc", Technique: "executable memory allocation", Confidence: 0.75},
	{Name: "VirtualProtectEx", Technique: "remote page permission change", Confidence: 0.90},
	{Name: "VirtualProtect", Technique: "page permission change, often to make data executable", Confidence: 0.70},
	{Name: "WriteProcessMemory", Technique: "remote process memory write", Confidence: 0.95},
	{Name: "ReadProcessMemory", Technique: "remote process memory read", Confidence: 0.90},
	{Name: "NtWriteVirtualMemory", Technique: "native remote memory write", Confidence: 0.95},
	{Name: "NtReadVirtualMemory", Technique: "native remote memory read", Confidence: 0.90},
	{Name: "CreateRemoteThread", Technique: "remote thread creation", Confidence: 0.95},
	{Name: "CreateRemoteThreadEx", Technique: "remote thread creation", Confidence: 0.95},
	{Name: "NtCreateThreadEx", Technique: "native remote thread creation", Confidence: 0.95},
	{Name: "RtlCreateUserThread", Technique: "native remote thread creation", Confidence: 0.95},
	{Name: "QueueUserAPC", Technique: "APC injection into a remote thread", Confidence: 0.90},
	{Name: "NtQueueApcThread", Technique: "APC injection", Confidence: 0.90},
	{Name: "SetThreadContext", Technique: "process hollowing: redirects the entry point", Confidence: 0.95},
	{Name: "GetThreadContext", Technique: "process hollowing: reads the entry point to redirect", Confidence: 0.90},
	{Name: "ResumeThread", Technique: "process hollowing: resumes the hollowed image", Confidence: 0.80},
	{Name: "NtResumeThread", Technique: "process hollowing", Confidence: 0.80},
	{Name: "NtMapViewOfSection", Technique: "section mapping into a remote process", Confidence: 0.90},
	{Name: "NtUnmapViewOfSection", Technique: "hollowing: unmaps the original image", Confidence: 0.90},
	{Name: "NtCreateSection", Technique: "section object for remote mapping", Confidence: 0.85},
	{Name: "ZwAllocateVirtualMemory", Technique: "native remote allocation", Confidence: 0.90},
	{Name: "CreateProcessA", Technique: "spawns a suspended process for hollowing", Confidence: 0.70, Casing: true},
	{Name: "CreateProcessW", Technique: "spawns a suspended process for hollowing", Confidence: 0.70, Casing: true},
	{Name: "OpenProcess", Technique: "obtains a handle for remote manipulation", Confidence: 0.60},
	{Name: "NtOpenProcess", Technique: "obtains a handle for remote manipulation", Confidence: 0.60},
})

// timingImports covers elapsed-time measurement used for anti-debug checks.
var timingImports = newSignatureSet([]Signature{
	{Name: "GetTickCount", Technique: "elapsed-time check for single-stepping", Confidence: 0.80},
	{Name: "GetTickCount64", Technique: "elapsed-time check for single-stepping", Confidence: 0.80},
	{Name: "QueryPerformanceCounter", Technique: "high-resolution elapsed-time check", Confidence: 0.80},
	{Name: "QueryPerformanceFrequency", Technique: "high-resolution elapsed-time check", Confidence: 0.70},
	{Name: "GetSystemTimeAsFileTime", Technique: "wall-clock check", Confidence: 0.65},
	{Name: "GetSystemTime", Technique: "wall-clock check", Confidence: 0.65},
	{Name: "NtQueryPerformanceCounter", Technique: "high-resolution elapsed-time check", Confidence: 0.80},
	{Name: "NtQuerySystemTime", Technique: "wall-clock check", Confidence: 0.65},
	{Name: "timeGetTime", Technique: "elapsed-time check", Confidence: 0.75},
	{Name: "GetSystemMetrics", Technique: "SM_REMOTESESSION and display-size sandbox probes", Confidence: 0.55},
	{Name: "GetSystemDirectoryA", Technique: "sandbox path probe", Confidence: 0.40, Casing: true},
	{Name: "GetSystemDirectoryW", Technique: "sandbox path probe", Confidence: 0.40, Casing: true},
})

// sleepImports covers deliberate dormancy.
var sleepImports = newSignatureSet([]Signature{
	{Name: "Sleep", Technique: "execution delay to outlast a sandbox timeout", Confidence: 0.70, Casing: true},
	{Name: "SleepEx", Technique: "execution delay to outlast a sandbox timeout", Confidence: 0.75},
	{Name: "NtDelayExecution", Technique: "native execution delay", Confidence: 0.80},
	{Name: "ZwDelayExecution", Technique: "native execution delay", Confidence: 0.80},
	{Name: "WaitForSingleObject", Technique: "stalls until an object signals", Confidence: 0.50},
	{Name: "WaitForSingleObjectEx", Technique: "stalls until an object signals", Confidence: 0.50},
	{Name: "WaitForMultipleObjects", Technique: "stalls until an object signals", Confidence: 0.45},
	{Name: "MsgWaitForMultipleObjects", Technique: "message-loop stall", Confidence: 0.45},
	{Name: "SetTimer", Technique: "timer-based execution gate", Confidence: 0.40},
	{Name: "SetWaitableTimer", Technique: "timer-based execution gate", Confidence: 0.55},
	{Name: "NtSetTimerResolution", Technique: "manipulates the system timer, often to skew timing checks", Confidence: 0.60},
})

// environmentImports covers VM, hypervisor, and sandbox fingerprinting APIs.
var environmentImports = newSignatureSet([]Signature{
	{Name: "EnumSystemFirmwareTables", Technique: "enumerates firmware tables (SMBIOS) for a VM vendor", Confidence: 0.80},
	{Name: "GetSystemFirmwareTable", Technique: "reads the SMBIOS firmware table", Confidence: 0.75},
	{Name: "GetAdaptersInfo", Technique: "enumerates adapters; VMware/VirtualBox MAC OUI is a giveaway", Confidence: 0.60},
	{Name: "GetAdaptersAddresses", Technique: "enumerates adapters for VM MAC prefixes", Confidence: 0.60},
	{Name: "RegOpenKeyExA", Technique: "registry probe for VM or sandbox artefacts", Confidence: 0.35, Casing: true},
	{Name: "RegOpenKeyExW", Technique: "registry probe for VM or sandbox artefacts", Confidence: 0.35, Casing: true},
	{Name: "RegQueryValueExA", Technique: "registry probe for VM or sandbox artefacts", Confidence: 0.35, Casing: true},
	{Name: "RegQueryValueExW", Technique: "registry probe for VM or sandbox artefacts", Confidence: 0.35, Casing: true},
	{Name: "FindWindowA", Technique: "window-class probe for a hypervisor console", Confidence: 0.60, Casing: true},
	{Name: "FindWindowW", Technique: "window-class probe for a hypervisor console", Confidence: 0.60, Casing: true},
	{Name: "FindWindowExA", Technique: "window-class probe for a hypervisor console", Confidence: 0.55, Casing: true},
	{Name: "FindWindowExW", Technique: "window-class probe for a hypervisor console", Confidence: 0.55, Casing: true},
	{Name: "EnumWindows", Technique: "enumerates windows looking for a hypervisor or sandbox UI", Confidence: 0.50},
	{Name: "DeviceIoControl", Technique: "driver communication, often to a VM guest agent", Confidence: 0.45},
	{Name: "SetupDiGetClassDevsA", Technique: "device enumeration for VM hardware", Confidence: 0.45, Casing: true},
	{Name: "SetupDiGetClassDevsW", Technique: "device enumeration for VM hardware", Confidence: 0.45, Casing: true},
	{Name: "GetModuleHandleA", Technique: "checks for sandbox or injected DLLs by name", Confidence: 0.30, Casing: true},
	{Name: "GetModuleHandleW", Technique: "checks for sandbox or injected DLLs by name", Confidence: 0.30, Casing: true},
})

// humanInteractionImports covers probes that distinguish a person from a
// scripted sandbox.
var humanInteractionImports = newSignatureSet([]Signature{
	{Name: "GetCursorPos", Technique: "cursor-position probe; unmoved cursor suggests automation", Confidence: 0.70},
	{Name: "GetAsyncKeyState", Technique: "keystroke-state probe; no input suggests a sandbox", Confidence: 0.75},
	{Name: "GetKeyState", Technique: "keystroke-state probe", Confidence: 0.65},
	{Name: "GetKeyboardState", Technique: "keystroke-state probe", Confidence: 0.70},
	{Name: "GetLastInputInfo", Technique: "idle-time probe; fresh boot or no input suggests a sandbox", Confidence: 0.80},
	{Name: "GetForegroundWindow", Technique: "foreground-window probe for an interactive desktop", Confidence: 0.50},
	{Name: "GetWindowTextA", Technique: "window-title probe for an interactive session", Confidence: 0.40, Casing: true},
	{Name: "GetWindowTextW", Technique: "window-title probe for an interactive session", Confidence: 0.40, Casing: true},
	{Name: "GetDesktopWindow", Technique: "desktop probe for an interactive session", Confidence: 0.45},
	{Name: "mouse_event", Technique: "synthetic input, used both to simulate and to detect input", Confidence: 0.50},
	{Name: "SendInput", Technique: "synthetic input", Confidence: 0.55},
	{Name: "SetWindowsHookExA", Technique: "input hook, often used to detect analyst keystrokes", Confidence: 0.60, Casing: true},
	{Name: "SetWindowsHookExW", Technique: "input hook, often used to detect analyst keystrokes", Confidence: 0.60, Casing: true},
	{Name: "GetUserNameA", Technique: "username probe; sandbox accounts are distinctive", Confidence: 0.45, Casing: true},
	{Name: "GetUserNameW", Technique: "username probe", Confidence: 0.45, Casing: true},
	{Name: "GetComputerNameA", Technique: "hostname probe; sandbox hostnames are distinctive", Confidence: 0.40, Casing: true},
	{Name: "GetComputerNameW", Technique: "hostname probe", Confidence: 0.40, Casing: true},
	{Name: "GetDiskFreeSpaceExA", Technique: "disk-size probe; sandboxes are often small", Confidence: 0.45, Casing: true},
	{Name: "GlobalMemoryStatusEx", Technique: "memory-size probe; sandboxes are often under-provisioned", Confidence: 0.55},
})

// antiDebugLibraries are modules whose mere presence is a strong signal,
// because a legitimate program rarely links them directly.
var antiDebugLibraries = newSignatureSet([]Signature{
	{Name: "SbieDll.dll", Technique: "Sandboxie dynamic-link interception", Confidence: 0.95},
	{Name: "dbghelp.dll", Technique: "debug helper library", Confidence: 0.40, Casing: true},
	{Name: "api_log.dll", Technique: "Cuckoo Sandbox API logging", Confidence: 0.95},
	{Name: "dir_watch.dll", Technique: "Cuckoo Sandbox file-system monitor", Confidence: 0.95},
	{Name: "pstorec.dll", Technique: "Cuckoo Sandbox persistence monitor", Confidence: 0.95},
	{Name: "vmcheck.dll", Technique: "VirtualBox guest additions detection helper", Confidence: 0.90},
	{Name: "wpespy.dll", Technique: "Wine Proton detection", Confidence: 0.80},
	{Name: "detoured.dll", Technique: "Microsoft Detours hooking library", Confidence: 0.85},
	{Name: "sbiedll.dll", Technique: "Sandboxie", Confidence: 0.95},
})

// vmMarkers are raw strings that identify a hypervisor, VM, or sandbox when
// present in the image. These are matched case-insensitively against printable
// runs in the data sections.
//
// The list is deliberately long and specific: a marker like "vmwaretray.exe"
// is strong evidence, whereas a generic word like "virtual" is not, so the
// generic terms are absent.
var vmMarkers = []string{
	// VMware
	"vmware", "vmwaretray", "vmwareuser", "vmtoolsd", "vmware-vmx",
	"vmx_svga", "vm3dmp", "vmmouse", "vmhgfs", "vmci", "vmrawdsk",
	// VirtualBox
	"vboxservice", "vboxtray", "vboxguest", "vboxmouse", "vboxvideo",
	"vboxsf", "vboxsharedfolders", "oracle virtualbox", "virtualbox",
	// QEMU / KVM
	"qemu", "bochs", "kvmkvmkvm", "vgabios", "seabios", "virtio",
	"redhat", "qxl", "virtio-net",
	// Hyper-V / Xen / KVM
	"microsoft corporation.*virtual machine", "hyper-v", "hypervisorpresent",
	"xenproject", "xenvm",
	// Parallels
	"parallels", "prl-tools", "prl_cc", "prl_memdev",
	// Other emulators and sandboxes
	"anubis", "joebox", "cwsandbox", "cuckoo", "threatexpert", "ddbox",
	"gfi sandbox", "gfi", "wine_get_unix_file_name", "wine_get_version",
	"cuckoo_monkey",
	// Analysis instrumentation
	"wireshark", "procmon", "ollydbg", "x64dbg", "x32dbg", "ida ",
	"immunity debugger", "windbg",
	// VM hardware identifiers
	"in\\dev\\vmci", "systembiosversion",
}

// vmRegistryKeys are registry paths whose presence indicates a VM or a
// sandbox has been installed on the host.
var vmRegistryKeys = []string{
	`hkey_local_machine\system\currentcontrolset\services\vmmouse`,
	`hkey_local_machine\system\currentcontrolset\services\vmhgfs`,
	`hkey_local_machine\system\currentcontrolset\services\vboxguest`,
	`hkey_local_machine\system\currentcontrolset\services\vboxsf`,
	`hkey_local_machine\system\currentcontrolset\services\vmbus`,
	`hkey_local_machine\system\currentcontrolset\control\class\{4d36e972-e325-11ce-bfc1-08002be10318}`,
	`hkey_local_machine\software\oracle\virtualbox guest additions`,
	`hkey_local_machine\software\microsoft\windows\currentversion\uninstall\vboxservice`,
	`hkey_local_machine\software\vmware, inc.\vmware tools`,
	`hkey_local_machine\system\currentcontrolset\enum\ide`,
	`hkey_local_machine\software\cucko sandbox`,
	`hkey_local_machine\software\sandboxie`,
	`hkey_local_machine\system\currentcontrolset\services\sbiedll`,
	`hkey_local_machine\hardware\devicemap\scsi\scsi port 0`,
	`system\currentcontrolset\control\computername\computername`,
}

// packerSectionNames are section names that packers use for their virtual
// decompression stub. Their presence is strong evidence of packing.
var packerSectionNames = map[string]string{
	"upx0":      "UPX",
	"upx1":      "UPX",
	"upx2":      "UPX",
	".aspack":   "ASPack",
	".adata":    "ASPack",
	"aspack":    "ASPack",
	".nsp0":     "NsPack",
	".nsp1":     "NsPack",
	".nsp2":     "NsPack",
	"pecompact": "PECompact",
	".petite":   "Petite",
	".yP":       "Y0da Protector",
	".themida":  "Themida",
	".vmp0":     "VMProtect",
	".vmp1":     "VMProtect",
	".vmp2":     "VMProtect",
	".enigma1":  "Enigma Protector",
	".enigma2":  "Enigma Protector",
	".packed":   "generic packer",
	".perplex":  "Perplex",
	".sforce":   "StarForce",
	".svkp":     "SVKP",
	".ccg":      "CCG packer",
	".maskpe":   "MaskPE",
	"pec2":      "PECompact v2",
	".neolit":   "NeoLite",
	".packed1":  "generic packer",
}

// apiHashConstants are well-known API-hashing magic constants. Their presence
// in a code section is a strong hint that imports are resolved at runtime by
// hash, which defeats static import analysis.
//
// The values are the standard ROR-13 hashes of the module names.
var apiHashConstants = []struct {
	Value uint32
	Name  string
}{
	{0x6A4ABC5B, "ntdll.dll (ROR-13)"},
	{0x0726774C, "kernel32.dll (ROR-13)"},
	{0x6174A256, "ws2_32.dll (ROR-13)"},
	{0x0C917432, "ntdll.dll (ADD-ROR13)"},
	{0x6B8029, "wininet.dll (ROR-13)"},
	{0x4FDAF6DA, "kernel32.dll (CRC32)"},
	{0x9DBD95A6, "GetProcAddress (ROR-13)"},
	{0x7C0DFCAA, "LoadLibraryA (ROR-13)"},
	{0xEC0E4E8E, "LoadLibraryA (ADD-ROR13)"},
}

// signatureTables is the aggregate view used by the detectors.
type signatureTables struct {
	antiDebug     signatureSet
	injection     signatureSet
	timing        signatureSet
	sleep         signatureSet
	environment   signatureSet
	humanInteract signatureSet
	libraries     signatureSet
}

var tables = signatureTables{
	antiDebug:     antiDebugImports,
	injection:     injectionImports,
	timing:        timingImports,
	sleep:         sleepImports,
	environment:   environmentImports,
	humanInteract: humanInteractionImports,
	libraries:     antiDebugLibraries,
}

// matchImports scans imported symbols against a signature table and returns
// one finding per distinct signature technique that matched.
//
// Findings are grouped by technique rather than emitted per symbol: a binary
// that imports both NtWriteVirtualMemory and WriteProcessMemory has one
// remote-write capability, not two, and the risk model deduplicates by ID.
func matchImports(fset signatureSet, symbols []ImportedSymbol, idPrefix string) []Finding {
	// Group hits by technique so one capability yields one finding.
	type hit struct {
		sig Signature
		ev  []Evidence
	}
	grouped := make(map[string]*hit)

	for _, sym := range symbols {
		key := sym.Name
		if sym.ByOrdinal {
			continue // an ordinal import carries no name to match
		}
		sig, ok := fset.lookup(key)
		if !ok {
			sig, ok = fset.lookup(normalizeSymbolName(key))
		}
		if !ok {
			continue
		}
		g := grouped[sig.Technique]
		if g == nil {
			g = &hit{sig: sig}
			grouped[sig.Technique] = g
		}
		g.ev = append(g.ev, Evidence{
			Kind:   "import",
			Detail: sym.Library + "!" + sym.Name + " — " + sig.Technique,
		})
	}

	techniques := make([]string, 0, len(grouped))
	for tech := range grouped {
		techniques = append(techniques, tech)
	}
	sort.Strings(techniques) // deterministic output

	out := make([]Finding, 0, len(techniques))
	for _, tech := range techniques {
		g := grouped[tech]
		names := make([]string, 0, len(g.ev))
		for _, e := range g.ev {
			names = append(names, strings.SplitN(e.Detail, " ", 2)[0])
		}
		out = append(out, Finding{
			ID:          idPrefix + "-" + slug(tech),
			Category:    CategoryStatic,
			Vector:      vectorForIDPrefix(idPrefix),
			Severity:    severityForConfidence(g.sig.Confidence),
			Confidence:  g.sig.Confidence,
			Title:       "Imported API set: " + tech,
			Description: "The image imports " + strings.Join(uniqueStrings(names), ", ") + ", which provides: " + tech + ".",
			Remediation: "Confirm the capability is required by the product's stated function; unexpected injection or anti-debug APIs in a non-security product warrant deeper triage.",
			Evidence:    dedupeEvidence(g.ev),
		})
	}
	return out
}

// severityForConfidence maps a signature's confidence onto a severity band.
func severityForConfidence(c float64) Severity {
	switch {
	case c >= 0.90:
		return SeverityCritical
	case c >= 0.75:
		return SeverityHigh
	case c >= 0.55:
		return SeverityMedium
	case c >= 0.35:
		return SeverityLow
	default:
		return SeverityInfo
	}
}

func vectorForIDPrefix(prefix string) Vector {
	switch prefix {
	case "TRI-AD":
		return VectorAntiDebugImport
	case "TRI-INJ":
		return VectorInjectionImport
	case "TRI-TIM":
		return VectorTimingCheck
	case "TRI-SLP":
		return VectorSleepStall
	case "TRI-ENV":
		return VectorEnvironmentProbe
	case "TRI-HUM":
		return VectorHumanInteraction
	}
	return VectorHeaderAnomalies
}

// slug converts a technique phrase into a stable identifier fragment.
func slug(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := b.String()
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	return strings.Trim(out, "-")
}

// uniqueStrings returns the input with duplicates removed, order preserved.
func uniqueStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
