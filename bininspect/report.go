package bininspect

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Category is the top-level class of an anti-analysis technique.
type Category string

const (
	// CategoryStatic covers anti-analysis visible in the file image alone:
	// packing, section permissions, header fields, and import tables.
	CategoryStatic Category = "static-anti-analysis"
	// CategoryDynamic covers techniques that only express themselves while the
	// binary runs: environment probing, stalling, and API-monitor bypass.
	CategoryDynamic Category = "dynamic-evasion"
)

// Vector is the specific technique a Finding describes.
type Vector string

const (
	// VectorPackedOrEncrypted — section entropy indicates compression or encryption.
	VectorPackedOrEncrypted Vector = "packed-or-encrypted"
	// VectorSectionPermissions — a memory region marked both writable and executable.
	VectorSectionPermissions Vector = "writable-executable-memory"
	// VectorHeaderAnomalies — object-file header fields that defeat or confuse analysis.
	VectorHeaderAnomalies Vector = "header-anomalies"
	// VectorAntiDebugImport — imports used to detect or evade a debugger.
	VectorAntiDebugImport Vector = "anti-debug-import"
	// VectorInjectionImport — imports used to inject into or hollow another process.
	VectorInjectionImport Vector = "process-injection-import"
	// VectorEnvironmentProbe — imports or markers that fingerprint VMs, hypervisors, or sandboxes.
	VectorEnvironmentProbe Vector = "environment-fingerprinting"
	// VectorHumanInteraction — probes that distinguish a human from an automated sandbox.
	VectorHumanInteraction Vector = "human-interaction-probe"
	// VectorTimingCheck — imports or opcode pairs used to measure elapsed time.
	VectorTimingCheck Vector = "timing-check"
	// VectorSleepStall — deliberate dormancy to outlast an analysis sandbox.
	VectorSleepStall Vector = "execution-delay"
	// VectorDirectSyscall — system call instructions reached without going through ntdll.
	VectorDirectSyscall Vector = "direct-system-call"
	// VectorAPIHashing — constants characteristic of import resolution by hash.
	VectorAPIHashing Vector = "api-hashing"
)

// Severity ranks how much a single finding matters on its own.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityLow      Severity = "low"
	SeverityMedium   Severity = "medium"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)

// severityWeight is the base contribution of a finding to the risk score,
// multiplied by the finding's confidence. Weights are deliberately
// non-linear: critical capabilities such as process injection or direct system
// calls dominate a 100-point budget, while an informational note barely moves it.
var severityWeight = map[Severity]float64{
	SeverityCritical: 34,
	SeverityHigh:     18,
	SeverityMedium:   8,
	SeverityLow:      3,
	SeverityInfo:     1,
}

// Rating is the coarse verdict derived from a risk score.
type Rating string

const (
	RatingNone     Rating = "none"
	RatingLow      Rating = "low"
	RatingModerate Rating = "moderate"
	RatingElevated Rating = "elevated"
	RatingHigh     Rating = "high"
	RatingSevere   Rating = "severe"
)

// RatingFor maps a 0..100 score onto a coarse verdict.
func RatingFor(score int) Rating {
	switch {
	case score >= 85:
		return RatingSevere
	case score >= 65:
		return RatingHigh
	case score >= 40:
		return RatingElevated
	case score >= 15:
		return RatingModerate
	case score > 0:
		return RatingLow
	default:
		return RatingNone
	}
}

// Evidence is one concrete artifact behind a Finding: a matched API name, an
// offset, a section, or a header field value. Evidence is what turns "this
// looks evasive" into something an analyst can verify.
type Evidence struct {
	// Kind names the evidence type, e.g. "import", "section", "opcode",
	// "header", "string", or "registry".
	Kind string `json:"kind"`
	// Detail is the human-readable description of the artifact.
	Detail string `json:"detail"`
	// Offset is the byte offset in the file when known, else -1.
	Offset int64 `json:"offset,omitempty"`
	// Section names the containing section when known.
	Section string `json:"section,omitempty"`
}

// Finding is a single detected anti-analysis technique.
type Finding struct {
	// ID is a stable machine-readable identifier, e.g. "TRI-SEC-WX".
	ID string `json:"id"`
	// Category and Vector locate the technique in the taxonomy.
	Category Category `json:"category"`
	Vector   Vector   `json:"vector"`
	Severity Severity `json:"severity"`
	// Confidence is the analyst-facing likelihood in [0,1] that the artifact
	// reflects deliberate anti-analysis rather than benign functionality.
	Confidence float64 `json:"confidence"`
	// Title is a one-line summary suitable for a table.
	Title string `json:"title"`
	// Description explains the technique and why it matters for triage.
	Description string `json:"description"`
	// Remediation states what an analyst should do next.
	Remediation string `json:"remediation"`
	// Evidence lists the artifacts that produced this finding.
	Evidence []Evidence `json:"evidence,omitempty"`
}

// SectionReport describes one section of the analysed image.
type SectionReport struct {
	Name string `json:"name"`
	// Offset and Size locate the section in the file.
	Offset int64 `json:"offset"`
	Size   int64 `json:"size"`
	// VirtualSize is the in-memory size where the format provides one.
	VirtualSize int64 `json:"virtual_size,omitempty"`
	// Read, Write, and Execute are the decoded permission bits.
	Read    bool `json:"read"`
	Write   bool `json:"write"`
	Execute bool `json:"execute"`
	// Entropy is Shannon entropy of the section's bytes, 0..8.
	Entropy float64 `json:"entropy"`
	// EntropyClass is the interpretation of Entropy.
	EntropyClass EntropyClass `json:"entropy_class"`
	// WritableAndExecutable is the dangerous combination, precomputed.
	WritableAndExecutable bool `json:"writable_and_executable"`
}

// ProgramSegmentReport describes one ELF program header (PT_LOAD etc).
type ProgramSegmentReport struct {
	Type   string `json:"type"`
	Flags  string `json:"flags"`
	Offset int64  `json:"offset"`
	Vaddr  uint64 `json:"vaddr"`
	Size   uint64 `json:"size"`
	// WritableAndExecutable is the dangerous PF_W|PF_X combination.
	WritableAndExecutable bool `json:"writable_and_executable"`
}

// FileIdentity carries the hashes and metadata that pin an exact artifact.
//
// MD5 is present only for compatibility with legacy intelligence pipelines and
// is not cryptographically sound; use SHA256 for any integrity decision.
type FileIdentity struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time,omitempty"`
	MD5     string    `json:"md5"`
	// MD5CryptographicallyBroken is always true. It exists so that a consumer
	// reading the JSON cannot mistake the MD5 field for a trust anchor.
	MD5CryptographicallyBroken bool   `json:"md5_cryptographically_broken"`
	SHA256                     string `json:"sha256"`
}

// AnalysisStats records what the analyser actually did, so a report can be
// interpreted honestly. A partially-read file yields partial conclusions, and
// that fact belongs in the output rather than in a footnote.
type AnalysisStats struct {
	// Duration is the wall time spent analysing.
	Duration time.Duration `json:"duration"`
	// BytesHashed is the number of bytes fed to the hash functions.
	BytesHashed int64 `json:"bytes_hashed"`
	// BytesScanned is the number of executable bytes opcode-scanned.
	BytesScanned int64 `json:"bytes_scanned"`
	// SectionsParsed counts sections/segments examined.
	SectionsParsed int `json:"sections_parsed"`
	// ImportsParsed counts imported symbols recovered.
	ImportsParsed int `json:"imports_parsed"`
	// Truncated is true if a structure extended past end-of-file and parsing
	// stopped early. Findings may be incomplete when it is set.
	Truncated bool `json:"truncated"`
	// Warnings holds non-fatal parse problems.
	Warnings []string `json:"warnings,omitempty"`
}

// RiskAssessment is the actionable summary of a Report.
type RiskAssessment struct {
	// Score is 0..100, confidence-weighted and capped.
	Score int `json:"score"`
	// Rating is the coarse verdict for Score.
	Rating Rating `json:"rating"`
	// Categories maps each category to its own contribution.
	Categories map[Category]int `json:"categories"`
	// Vectors maps the most significant techniques to their contribution.
	Vectors map[Vector]int `json:"vectors"`
	// TopFindings lists the highest-severity findings, most severe first.
	TopFindings []Finding `json:"top_findings"`
	// RecommendedActions is an ordered analyst checklist.
	RecommendedActions []string `json:"recommended_actions"`
}

// Report is the unified output of an analysis. See UnifiedReport for the alias.
type Report struct {
	// Format is "pe", "elf", or "unknown".
	Format string `json:"format"`
	// Arch is a human-readable architecture description.
	Arch string `json:"arch,omitempty"`
	// EntryPoint is the file offset of the entry point when located.
	EntryPoint uint64 `json:"entry_point,omitempty"`
	// IsLibrary reports DLL/shared-object intent.
	IsLibrary bool `json:"is_library"`
	// Stripped reports the absence of a symbol table.
	Stripped bool `json:"stripped"`
	// Identity pins the exact artifact.
	Identity FileIdentity `json:"identity"`
	// Sections and Segments describe layout.
	Sections  []SectionReport        `json:"sections,omitempty"`
	Segments  []ProgramSegmentReport `json:"segments,omitempty"`
	Imports   []ImportedSymbol       `json:"imports,omitempty"`
	Libraries []ImportedLibrary      `json:"libraries,omitempty"`
	Findings  []Finding              `json:"findings,omitempty"`
	Risk      RiskAssessment         `json:"risk"`
	Stats     AnalysisStats          `json:"stats"`
}

// UnifiedReport is an alias for Report, provided for callers that name their
// ingestion type after the payload. It carries identical fields and methods; a
// value of either type satisfies both names.
type UnifiedReport = Report

// ImportedLibrary is a resolved module name from the import table.
type ImportedLibrary struct {
	Name string `json:"name"`
	// SymbolCount is how many named symbols were recovered from it.
	SymbolCount int `json:"symbol_count"`
}

// ImportedSymbol is one resolved imported function.
type ImportedSymbol struct {
	Library string `json:"library"`
	Name    string `json:"name"`
	// Ordinal is set when the import is by ordinal rather than by name.
	Ordinal uint16 `json:"ordinal,omitempty"`
	// ByOrdinal distinguishes ordinal imports from named ones.
	ByOrdinal bool `json:"by_ordinal,omitempty"`
}

// Finding returns the findings belonging to a category.
func (r *Report) FindingsFor(c Category) []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if f.Category == c {
			out = append(out, f)
		}
	}
	return out
}

// HasVector reports whether any finding used the given vector.
func (r *Report) HasVector(v Vector) bool {
	for _, f := range r.Findings {
		if f.Vector == v {
			return true
		}
	}
	return false
}

// Summary renders a one-line human summary of the report.
func (r *Report) Summary() string {
	if r == nil {
		return "no report"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s risk %d/100 (%s), %d findings",
		r.Identity.Name, r.Format, r.Risk.Score, r.Risk.Rating, len(r.Findings))
	if len(r.Findings) > 0 {
		fmt.Fprintf(&b, "; top: %s", r.Findings[0].Title)
	}
	return b.String()
}

// RiskScore computes the 0..100 risk score for a set of findings.
//
// The score is the sum of severityWeight * confidence across findings, capped
// at 100. Confidence is what keeps a single weak heuristic from dominating:
// an opcode match at 0.45 confidence contributes less than a named
// process-injection import at 0.95.
//
// Deduplication is by finding ID: repeated detections of the same technique
// (for instance the same suspicious API appearing in two import tables) are
// counted once, so the score reflects distinct capability rather than count.
func RiskScore(findings []Finding) int {
	seen := make(map[string]bool, len(findings))
	total := 0.0
	for _, f := range findings {
		if seen[f.ID] {
			continue
		}
		seen[f.ID] = true
		weight, ok := severityWeight[f.Severity]
		if !ok {
			weight = severityWeight[SeverityInfo]
		}
		c := f.Confidence
		if c < 0 {
			c = 0
		}
		if c > 1 {
			c = 1
		}
		total += weight * c
	}
	if total < 0 {
		total = 0
	}
	if total > 100 {
		total = 100
	}
	return int(total + 0.5)
}

// buildRisk assembles the full risk assessment from findings.
func buildRisk(findings []Finding) RiskAssessment {
	ra := RiskAssessment{
		Score:      RiskScore(findings),
		Categories: make(map[Category]int),
		Vectors:    make(map[Vector]int),
	}
	ra.Rating = RatingFor(ra.Score)

	seen := make(map[string]bool, len(findings))
	for _, f := range findings {
		if seen[f.ID] {
			continue
		}
		seen[f.ID] = true
		weight := severityWeight[f.Severity]
		ra.Categories[f.Category] += int(weight*f.Confidence + 0.5)
		ra.Vectors[f.Vector] += int(weight*f.Confidence + 0.5)
	}

	ordered := append([]Finding(nil), findings...)
	sort.SliceStable(ordered, func(i, j int) bool {
		wi := severityWeight[ordered[i].Severity] * ordered[i].Confidence
		wj := severityWeight[ordered[j].Severity] * ordered[j].Confidence
		if wi != wj {
			return wi > wj
		}
		return ordered[i].ID < ordered[j].ID
	})
	if len(ordered) > 8 {
		ordered = ordered[:8]
	}
	ra.TopFindings = ordered
	ra.RecommendedActions = recommendActions(findings)
	return ra
}

// recommendActions derives an ordered analyst checklist from what was found.
// The order reflects triage value: understand what evades you before deciding
// whether to run it at all.
func recommendActions(findings []Finding) []string {
	has := make(map[Vector]bool)
	for _, f := range findings {
		has[f.Vector] = true
	}

	var actions []string
	if has[VectorPackedOrEncrypted] {
		actions = append(actions,
			"Treat the image as hostile input: unpack in a disposable sandbox, never on an analyst workstation.")
	}
	if has[VectorDirectSyscall] {
		actions = append(actions,
			"API-level sandboxing is insufficient — instrument at the kernel boundary or use emulation.")
	}
	if has[VectorInjectionImport] {
		actions = append(actions,
			"Expect process injection/hollowing: monitor process-creation and memory-write APIs across the whole fleet.")
	}
	if has[VectorAntiDebugImport] {
		actions = append(actions,
			"Do not single-step; prefer hardware breakpoints or full-system tracing.")
	}
	if has[VectorEnvironmentProbe] {
		actions = append(actions,
			"Dynamic analysis will misreport until the VM/sandbox artefacts it probes for are absent or spoofed.")
	}
	if has[VectorHumanInteraction] {
		actions = append(actions,
			"Require simulated human input; absence of it may be suppressing execution.")
	}
	if has[VectorSleepStall] {
		actions = append(actions,
			"Accelerate or skip sleeps during detonation, or analysis will time out with nothing observed.")
	}
	if has[VectorTimingCheck] {
		actions = append(actions,
			"Treat timing-derived branches as anti-analysis: force both sides and compare behaviour.")
	}
	if has[VectorSectionPermissions] {
		actions = append(actions,
			"Enforce W^X policy; the image requests writable-executable memory.")
	}
	if has[VectorAPIHashing] {
		actions = append(actions,
			"Imports are incomplete by design — resolve APIs by hash or dump at runtime before triaging further.")
	}
	if len(actions) == 0 {
		actions = append(actions,
			"No anti-analysis capability detected by static triage; dynamic behaviour may still differ.")
	}
	return actions
}
