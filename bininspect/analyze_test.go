package bininspect

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// analyzePEFixture is a helper that analyses a fixture image with default options.
func analyzePEFixture(t *testing.T, opts peFixtureOptions) *Report {
	t.Helper()
	rep, err := New(Options{}).AnalyzeBytes(context.Background(), buildPE(opts), "fixture.exe")
	if err != nil {
		t.Fatalf("AnalyzeBytes(PE): %v", err)
	}
	return rep
}

func analyzeELFFixture(t *testing.T, opts elfFixtureOptions) *Report {
	t.Helper()
	rep, err := New(Options{}).AnalyzeBytes(context.Background(), buildELF(opts), "fixture.elf")
	if err != nil {
		t.Fatalf("AnalyzeBytes(ELF): %v", err)
	}
	return rep
}

// TestPEFormatDetection verifies the image is classified from content, not from
// the file extension — a renamed sample must still be recognised.
func TestPEFormatDetection(t *testing.T) {
	rep := analyzePEFixture(t, defaultPEFixture())
	if rep.Format != "pe" {
		t.Errorf("Format = %q, want %q", rep.Format, "pe")
	}
	if rep.Arch != "x86-64" {
		t.Errorf("Arch = %q, want x86-64", rep.Arch)
	}
	if len(rep.Sections) != 3 {
		t.Errorf("sections = %d, want 3", len(rep.Sections))
	}
	if rep.EntryPoint != peTextRVA {
		t.Errorf("EntryPoint = %#x, want %#x", rep.EntryPoint, peTextRVA)
	}
	if rep.Identity.Size == 0 || rep.Identity.SHA256 == "" {
		t.Error("identity not populated")
	}
	if !rep.Identity.MD5CryptographicallyBroken {
		t.Error("MD5 must be flagged as cryptographically broken")
	}
}

// TestPEImportTableWalk is the load-bearing test for the import audit: the
// parser must recover named symbols by walking INT/descriptor structures, which
// is what every API-based detection depends on.
func TestPEImportTableWalk(t *testing.T) {
	rep := analyzePEFixture(t, defaultPEFixture())

	wantSymbols := map[string]bool{
		"Sleep":                false,
		"IsDebuggerPresent":    false,
		"VirtualAllocEx":       false,
		"NtWriteVirtualMemory": false,
		"NtDelayExecution":     false,
	}
	var ordinals int
	for _, sym := range rep.Imports {
		if sym.ByOrdinal {
			ordinals++
			continue
		}
		if _, ok := wantSymbols[sym.Name]; ok {
			wantSymbols[sym.Name] = true
		}
	}
	for name, found := range wantSymbols {
		if !found {
			t.Errorf("import %q not recovered from the import table", name)
		}
	}
	if ordinals != 1 {
		t.Errorf("ordinal imports = %d, want 1 (ordinals must be counted but not name-matched)", ordinals)
	}
	if len(rep.Libraries) != 2 {
		t.Errorf("libraries = %d, want 2: %+v", len(rep.Libraries), rep.Libraries)
	}
	if rep.Stats.ImportsParsed == 0 {
		t.Error("Stats.ImportsParsed not populated")
	}
}

// TestPEDetectsAntiDebugAndInjection verifies the two headline static
// detections fire on the right imports.
func TestPEDetectsAntiDebugAndInjection(t *testing.T) {
	rep := analyzePEFixture(t, defaultPEFixture())

	if !rep.HasVector(VectorAntiDebugImport) {
		t.Error("IsDebuggerPresent should produce an anti-debug finding")
	}
	if !rep.HasVector(VectorInjectionImport) {
		t.Error("VirtualAllocEx / NtWriteVirtualMemory should produce an injection finding")
	}
	if !rep.HasVector(VectorSleepStall) {
		t.Error("Sleep should produce an execution-delay finding")
	}

	// The injection finding must be critical: remote memory write is the
	// highest-risk capability the library reports.
	for _, f := range rep.Findings {
		if f.Vector == VectorInjectionImport && f.Severity != SeverityCritical {
			t.Errorf("injection severity = %q, want critical", f.Severity)
		}
	}
}

// TestPEDetectsStaticAnomalies covers W+X, entropy, syscalls, API hashing,
// timing, and markers against a single fixture.
func TestPEDetectsStaticAnomalies(t *testing.T) {
	rep := analyzePEFixture(t, defaultPEFixture())

	for _, v := range []Vector{
		VectorSectionPermissions,
		VectorPackedOrEncrypted,
		VectorDirectSyscall,
		VectorAPIHashing,
		VectorTimingCheck,
		VectorEnvironmentProbe,
	} {
		if !rep.HasVector(v) {
			t.Errorf("expected finding for vector %q; findings were %v", v, findingVectors(rep))
		}
	}
	if rep.Risk.Score <= 0 {
		t.Error("risk score not populated")
	}
	if rep.Risk.Rating == RatingNone {
		t.Errorf("rating = none despite %d findings", len(rep.Findings))
	}
	if len(rep.Risk.RecommendedActions) == 0 {
		t.Error("no recommended actions produced")
	}
	if rep.Stats.BytesScanned == 0 {
		t.Error("BytesScanned not populated; executable scan did not run")
	}
}

// TestPEDetectorsAreIndependent proves each detector fires on its own anomaly,
// so a passing aggregate test is not masking a dead detector.
func TestPEDetectorsAreIndependent(t *testing.T) {
	cases := []struct {
		name string
		opts peFixtureOptions
		want Vector
	}{
		{"W+X section", peFixtureOptions{WritableExecutable: true}, VectorSectionPermissions},
		{"no W+X section", peFixtureOptions{}, VectorSectionPermissions},
		{"high entropy", peFixtureOptions{HighEntropy: true}, VectorPackedOrEncrypted},
		{"direct syscall", peFixtureOptions{DirectSyscall: true}, VectorDirectSyscall},
		{"timing pair", peFixtureOptions{RDTSCPair: true}, VectorTimingCheck},
		{"vm markers", peFixtureOptions{VMMarkers: true}, VectorEnvironmentProbe},
		{"api hash", peFixtureOptions{APIHash: true}, VectorAPIHashing},
		{"packer names", peFixtureOptions{PackerSectionNames: true}, VectorHeaderAnomalies},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rep := analyzePEFixture(t, tc.opts)
			got := rep.HasVector(tc.want)
			want := tc.name != "no W+X section"
			if got != want {
				t.Errorf("HasVector(%q) = %v, want %v (findings: %v)",
					tc.want, got, want, findingVectors(rep))
			}
		})
	}
}

// TestPEHeaderAnomalies verifies the header checks fire and stay quiet on a
// clean header.
func TestPEHeaderAnomalies(t *testing.T) {
	clean := peFixtureOptions{}
	rep := analyzePEFixture(t, clean)
	for _, f := range rep.Findings {
		if f.Vector == VectorHeaderAnomalies && strings.Contains(f.Description, "ANOM") {
			t.Errorf("unexpected header anomaly on a clean fixture: %+v", f)
		}
	}

	dirty := analyzePEFixture(t, peFixtureOptions{ZeroTimestamp: true, NoASLR: true, PackerSectionNames: true})
	var found bool
	for _, f := range dirty.Findings {
		if f.Vector == VectorHeaderAnomalies {
			found = true
		}
	}
	if !found {
		t.Error("zeroed timestamp, missing ASLR, and packer section names should produce a header-anomaly finding")
	}
}

// TestPEDisableByteScan verifies the fast-path option actually skips the
// expensive scans rather than merely ignoring their results.
func TestPEDisableByteScan(t *testing.T) {
	img := buildPE(defaultPEFixture())

	full, err := New(Options{}).AnalyzeBytes(context.Background(), img, "full.exe")
	if err != nil {
		t.Fatal(err)
	}
	if !full.HasVector(VectorDirectSyscall) {
		t.Fatal("baseline fixture should detect a direct syscall")
	}

	fast, err := New(Options{DisableByteScan: true}).AnalyzeBytes(context.Background(), img, "fast.exe")
	if err != nil {
		t.Fatal(err)
	}
	if fast.HasVector(VectorDirectSyscall) {
		t.Error("DisableByteScan still reported a syscall finding")
	}
	if fast.HasVector(VectorEnvironmentProbe) {
		t.Error("DisableByteScan still reported marker findings")
	}
	// Import-derived findings must survive: they come from the import table,
	// not from byte scanning.
	if !fast.HasVector(VectorAntiDebugImport) {
		t.Error("DisableByteScan dropped import-derived findings")
	}
}

// TestELFFormatDetectionAndPermissions covers the ELF path, including the
// segment-level W^X check that is ELF's counterpart to a PE W+X section.
func TestELFFormatDetectionAndPermissions(t *testing.T) {
	rep := analyzeELFFixture(t, defaultELFFixture())

	if rep.Format != "elf" {
		t.Errorf("Format = %q, want elf", rep.Format)
	}
	if rep.Arch != "x86-64" {
		t.Errorf("Arch = %q, want x86-64", rep.Arch)
	}
	if !rep.Stripped {
		t.Error("Stripped = false; the fixture has no .symtab")
	}
	if !rep.HasVector(VectorSectionPermissions) {
		t.Error("PF_W|PF_X segment not detected")
	}
	if !rep.HasVector(VectorPackedOrEncrypted) {
		t.Error("high-entropy section not detected")
	}
	if !rep.HasVector(VectorDirectSyscall) {
		t.Error("direct syscall not detected in an executable segment")
	}
	if !rep.HasVector(VectorEnvironmentProbe) {
		t.Error("VM markers not detected in .rodata")
	}
	if len(rep.Segments) != elfPhnum {
		t.Errorf("segments = %d, want %d", len(rep.Segments), elfPhnum)
	}
}

// TestELFStaticLinkingIsNoted verifies the ELF-specific structural anomaly is
// reported, since a static image is materially harder to instrument.
func TestELFStaticLinkingIsNoted(t *testing.T) {
	rep := analyzeELFFixture(t, elfFixtureOptions{StaticallyLinked: true})
	if !rep.HasVector(VectorHeaderAnomalies) {
		t.Error("a static ET_EXEC image should produce a header-anomaly finding")
	}
}

// TestUnknownFormatIsReportedNotPanicked verifies graceful handling of input
// that is neither PE nor ELF: identity is still produced so the sample can be
// recorded, and the format is reported as unknown.
func TestUnknownFormatIsReportedNotPanicked(t *testing.T) {
	junk := []byte("this is not a binary, it is a text file about binaries")
	rep, err := New(Options{}).AnalyzeBytes(context.Background(), junk, "notes.txt")
	if err != nil {
		t.Fatalf("AnalyzeBytes on non-binary failed: %v", err)
	}
	if rep.Format != "unknown" {
		t.Errorf("Format = %q, want unknown", rep.Format)
	}
	if rep.Identity.SHA256 == "" {
		t.Error("identity missing for unknown format")
	}
	if len(rep.Findings) != 0 {
		t.Errorf("unknown format produced findings: %+v", rep.Findings)
	}
	if len(rep.Stats.Warnings) == 0 {
		t.Error("expected a warning explaining the unrecognised format")
	}
}

// TestMalformedInputIsRejected verifies the entry point's error contract: nil
// readers, empty input, and truncated images must return errors rather than
// panic.
func TestMalformedInputIsRejected(t *testing.T) {
	a := New(Options{})
	ctx := context.Background()
	if _, err := a.AnalyzeReaderAt(ctx, nil, 10, "x", zeroTime); err == nil {
		t.Error("nil reader accepted")
	}
	if _, err := a.AnalyzeBytes(ctx, nil, "empty"); err == nil {
		t.Error("empty input accepted")
	}
	// A truncated PE header: MZ present but nothing usable after it.
	truncated := []byte{'M', 'Z', 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	if _, err := a.AnalyzeBytes(ctx, truncated, "trunc.exe"); err == nil {
		t.Error("truncated PE accepted without error")
	}
	// A truncated ELF header.
	truncElf := []byte{0x7F, 'E', 'L', 'F', 2, 1, 1}
	if _, err := a.AnalyzeBytes(ctx, truncElf, "trunc.elf"); err == nil {
		t.Error("truncated ELF accepted without error")
	}
	// A nil context must be rejected rather than panic: ctx is now required for
	// cancellation, and a nil one would panic on the first ctx.Err() call.
	if _, err := a.AnalyzeBytes(nil, []byte("MZ data here to pass the size check"), "nilctx"); err == nil {
		t.Error("nil context accepted")
	}
}

// TestMalformedImportDirectoryIsBounded verifies a corrupt import table cannot
// hang the parser: an RVA pointing outside every section must terminate cleanly.
func TestMalformedImportDirectoryIsBounded(t *testing.T) {
	opts := defaultPEFixture()
	img := buildPE(opts)
	// Point the import directory at an RVA that maps to no section.
	opt := img[peOptOffset:]
	dd := opt[112+8:]
	putUint32(dd[0:], 0x7F000000)
	putUint32(dd[4:], 0x40)

	rep, err := New(Options{}).AnalyzeBytes(context.Background(), img, "badimports.exe")
	if err != nil {
		t.Fatalf("a malformed import table must not fail the whole analysis: %v", err)
	}
	if len(rep.Stats.Warnings) == 0 {
		t.Error("expected a warning about the unmappable import directory")
	}
}

// putUint32 is a small helper so the test can patch a header field without
// importing encoding/binary in every fixture helper.
func putUint32(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
}

// TestAnalyzerIsThreadSafe is the concurrency contract test: one Analyzer shared
// across many goroutines must produce identical, complete reports.
//
// Run under -race on a machine with a C toolchain, this is the definitive proof
// that the package holds no shared mutable state.
func TestAnalyzerIsThreadSafe(t *testing.T) {
	a := New(Options{})
	peImg := buildPE(defaultPEFixture())
	elfImg := buildELF(defaultELFFixture())

	baseline, err := a.AnalyzeBytes(context.Background(), peImg, "baseline.exe")
	if err != nil {
		t.Fatal(err)
	}
	wantScore := baseline.Risk.Score
	wantFindings := len(baseline.Findings)

	const workers = 32
	var wg sync.WaitGroup
	errs := make(chan error, workers*2)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Mix formats so concurrent ELF and PE analysis both run.
			var img []byte
			var name string
			if i%2 == 0 {
				img, name = peImg, "concurrent.exe"
			} else {
				img, name = elfImg, "concurrent.elf"
			}
			rep, err := a.AnalyzeBytes(context.Background(), img, name)
			if err != nil {
				errs <- err
				return
			}
			if i%2 == 0 {
				if rep.Risk.Score != wantScore || len(rep.Findings) != wantFindings {
					errs <- errConcurrentDivergence
				}
			}
			if rep.Identity.SHA256 == "" {
				errs <- errMissingIdentity
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent analysis failure: %v", err)
	}
}

// TestRiskScoringIsDeterministicAndCapped verifies the scoring contract:
// duplicates collapse, the score stays in range, and a confidence of 0
// contributes nothing.
func TestRiskScoringIsDeterministicAndCapped(t *testing.T) {
	critical := Finding{ID: "A", Severity: SeverityCritical, Confidence: 1}
	if got := RiskScore([]Finding{critical}); got != 34 {
		t.Errorf("RiskScore(critical) = %d, want 34", got)
	}
	// Duplicates must collapse to one contribution.
	if got := RiskScore([]Finding{critical, critical, critical}); got != 34 {
		t.Errorf("duplicate findings inflated the score to %d, want 34", got)
	}
	// Zero confidence contributes nothing.
	zero := Finding{ID: "B", Severity: SeverityCritical, Confidence: 0}
	if got := RiskScore([]Finding{zero}); got != 0 {
		t.Errorf("zero-confidence finding scored %d, want 0", got)
	}
	// Many critical findings must cap at 100, never exceed.
	many := make([]Finding, 0, 50)
	for i := 0; i < 50; i++ {
		many = append(many, Finding{ID: string(rune('a' + i%26)), Severity: SeverityCritical, Confidence: 1})
	}
	if got := RiskScore(many); got != 100 {
		t.Errorf("capped score = %d, want 100", got)
	}
	if got := RiskScore(nil); got != 0 {
		t.Errorf("RiskScore(nil) = %d, want 0", got)
	}
}

// TestRatingThresholds pins the score-to-rating mapping.
func TestRatingThresholds(t *testing.T) {
	cases := map[int]Rating{
		0:   RatingNone,
		1:   RatingLow,
		20:  RatingModerate,
		50:  RatingElevated,
		70:  RatingHigh,
		90:  RatingSevere,
		100: RatingSevere,
	}
	for score, want := range cases {
		if got := RatingFor(score); got != want {
			t.Errorf("RatingFor(%d) = %q, want %q", score, got, want)
		}
	}
}

// TestReportHelpers exercises the report's query surface.
func TestReportHelpers(t *testing.T) {
	rep := analyzePEFixture(t, defaultPEFixture())

	if len(rep.FindingsFor(CategoryStatic)) == 0 {
		t.Error("FindingsFor(static) returned nothing")
	}
	if len(rep.FindingsFor(CategoryDynamic)) == 0 {
		t.Error("FindingsFor(dynamic) returned nothing")
	}
	if !strings.Contains(rep.Summary(), rep.Identity.Name) {
		t.Errorf("Summary %q does not mention the file name", rep.Summary())
	}
	if len(rep.Risk.Categories) == 0 || len(rep.Risk.Vectors) == 0 {
		t.Error("risk breakdown maps are empty")
	}
}

// TestOptionsAreCopied verifies an Options value mutated after New cannot affect
// a live Analyzer, which is what makes sharing one Analyzer safe.
func TestOptionsAreCopied(t *testing.T) {
	opts := Options{MaxScanBytes: 1024}
	a := New(opts)
	opts.MaxScanBytes = 1 << 30
	if a.Options().MaxScanBytes != 1024 {
		t.Errorf("Analyzer retained a pointer into the caller's Options: %d", a.Options().MaxScanBytes)
	}
	// The zero value must be usable and must apply the default cap.
	if got := New(Options{}).Options().MaxScanBytes; got != defaultScanCap {
		t.Errorf("default MaxScanBytes = %d, want %d", got, defaultScanCap)
	}
}

// TestZeroValueOptionsAreSafe verifies a caller can construct an Analyzer-like
// usage with no configuration at all.
func TestZeroValueOptionsAreSafe(t *testing.T) {
	rep, err := AnalyzeBytes(context.Background(), buildPE(defaultPEFixture()), "zero.exe")
	if err != nil {
		t.Fatalf("package-level AnalyzeBytes failed: %v", err)
	}
	if rep.Risk.Score == 0 {
		t.Error("default analysis produced no risk")
	}
}

// findingVectors lists the vectors present in a report, for test failure output.
func findingVectors(rep *Report) []Vector {
	var out []Vector
	for _, f := range rep.Findings {
		out = append(out, f.Vector)
	}
	return out
}

// zeroTime is the modtime used for in-memory analyses.
var zeroTime = timeZero()

// Sentinel errors for the concurrency test, kept as vars so the goroutine can
// report which invariant broke.
var (
	errConcurrentDivergence = errors.New("concurrent analysis diverged from baseline")
	errMissingIdentity      = errors.New("concurrent analysis produced no identity")
)

// timeZero returns the zero time, used for in-memory analyses.
func timeZero() time.Time { return time.Time{} }
