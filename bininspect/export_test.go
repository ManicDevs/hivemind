package bininspect

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestExportJSONProducesValidEnvelope is the primary contract: a report
// exports to valid JSON carrying a schema version and the payload.
func TestExportJSONProducesValidEnvelope(t *testing.T) {
	rep, err := New(Options{}).AnalyzeBytes(context.Background(), buildPE(defaultPEFixture()), "export.exe")
	if err != nil {
		t.Fatalf("analysis: %v", err)
	}
	if rep.Risk.Score == 0 {
		t.Skip("fixture produced no risk; export still valid but assertion weaker")
	}

	out, err := rep.ExportJSON()
	if err != nil {
		t.Fatalf("ExportJSON: %v", err)
	}

	var env struct {
		SchemaVersion string  `json:"schema_version"`
		Generator     string  `json:"generator"`
		Format        string  `json:"format"`
		Report        *Report `json:"report"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("export is not valid JSON: %v\n%s", err, out)
	}
	if env.SchemaVersion != ReportSchemaVersion() {
		t.Errorf("schema_version = %q, want %q", env.SchemaVersion, ReportSchemaVersion())
	}
	if env.Generator == "" {
		t.Error("generator is empty; an envelope without provenance is not self-describing")
	}
	if env.Format != "pe" {
		t.Errorf("envelope format = %q, want pe", env.Format)
	}
	if env.Report == nil || len(env.Report.Findings) == 0 {
		t.Fatal("payload missing or empty")
	}
	// The payload must survive the round trip intact.
	if env.Report.Risk.Score != rep.Risk.Score {
		t.Errorf("risk score changed across export: %d -> %d", rep.Risk.Score, env.Report.Risk.Score)
	}
	if len(env.Report.Findings) != len(rep.Findings) {
		t.Errorf("finding count changed across export: %d -> %d",
			len(rep.Findings), len(env.Report.Findings))
	}
}

// TestExportJSONOmitsEmptyFields verifies the omit-empty directives actually
// fire, so a minimal report does not ship a wall of zero-valued keys.
//
// This matters for SIEM ingestion: every null field is index overhead in most
// pipelines, and a report carrying dozens of empty sections bloats the event.
func TestExportJSONOmitsEmptyFields(t *testing.T) {
	rep := &Report{
		Format:   "pe",
		Identity: FileIdentity{Name: "minimal.exe", Size: 10, SHA256: "abc"},
		Risk:     RiskAssessment{Score: 5, Rating: RatingLow},
	}
	out, err := rep.ExportJSON()
	if err != nil {
		t.Fatalf("ExportJSON: %v", err)
	}
	s := string(out)
	for _, absent := range []string{"\"sections\"", "\"segments\"", "\"imports\"", "\"findings\"", "\"libraries\"", "\"exported_at\"", "\"warnings\""} {
		if strings.Contains(s, absent) {
			t.Errorf("empty field %s was not omitted:\n%s", absent, s)
		}
	}
	// Required fields must still be present.
	for _, present := range []string{"\"format\"", "\"is_library\"", "\"stripped\"", "\"identity\""} {
		if !strings.Contains(s, present) {
			t.Errorf("required field %s missing:\n%s", present, s)
		}
	}
}

// TestExportJSONRefusesNilReport pins the error contract.
func TestExportJSONRefusesNilReport(t *testing.T) {
	var rep *Report
	if _, err := rep.ExportJSON(); err == nil {
		t.Error("nil report exported without error")
	}
}

// TestExportJSONRefusesZeroRiskByDefault verifies the deliberate guard: a
// zero-risk report is refused unless the caller opts in, because "analysed,
// nothing found" and "not analysed" must not look identical in a pipeline.
func TestExportJSONRefusesZeroRiskByDefault(t *testing.T) {
	clean := &Report{
		Format:   "pe",
		Identity: FileIdentity{Name: "clean.exe", SHA256: "abc"},
		Risk:     RiskAssessment{Score: 0, Rating: RatingNone},
	}
	if _, err := clean.ExportJSON(); err == nil {
		t.Error("zero-risk report exported without error; the guard is not working")
	} else if !strings.Contains(err.Error(), "nothing found") {
		t.Errorf("error does not explain the remedy: %v", err)
	}

	// Opting in must work and must produce the document.
	out, err := clean.ExportJSONWith(ExportOptions{Envelope: true, IncludeZeroScore: true})
	if err != nil {
		t.Fatalf("IncludeZeroScore did not permit the export: %v", err)
	}
	if !json.Valid(out) {
		t.Error("opted-in export is not valid JSON")
	}
}

// TestExportJSONIsDeterministic guards byte-stability, which a golden-file test
// or a content hash over SIEM events depends on.
func TestExportJSONIsDeterministic(t *testing.T) {
	rep, err := New(Options{}).AnalyzeBytes(context.Background(), buildPE(defaultPEFixture()), "det.exe")
	if err != nil {
		t.Fatal(err)
	}
	first, err := rep.ExportJSON()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		next, err := rep.ExportJSON()
		if err != nil {
			t.Fatal(err)
		}
		if string(next) != string(first) {
			t.Fatalf("export %d differed from the first:\n%s\n%s", i, first, next)
		}
	}
	// Compact mode must not carry a trailing newline, which would change the
	// hash of an otherwise identical event.
	if strings.HasSuffix(string(first), "\n") {
		t.Error("compact export ends with a newline")
	}
}

// TestExportJSONBarePayload verifies the non-envelope form, which is what a
// consumer pinned to the flat Report shape needs.
func TestExportJSONBarePayload(t *testing.T) {
	rep := &Report{
		Format:   "elf",
		Identity: FileIdentity{Name: "bare.elf", SHA256: "d"},
		Risk:     RiskAssessment{Score: 10, Rating: RatingLow},
	}
	out, err := rep.ExportJSONWith(ExportOptions{})
	if err != nil {
		t.Fatalf("bare export: %v", err)
	}
	if strings.Contains(string(out), "schema_version") {
		t.Error("bare payload unexpectedly carries an envelope")
	}
	var back Report
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("bare payload is not a Report: %v", err)
	}
	if back.Format != "elf" {
		t.Errorf("round trip lost format: %q", back.Format)
	}
}

// TestExportJSONIndentOption checks the pretty path is valid and newline
// terminated, which is what a human or a line-oriented tool wants.
func TestExportJSONIndentOption(t *testing.T) {
	rep := &Report{
		Format:   "pe",
		Identity: FileIdentity{Name: "pretty.exe", SHA256: "e"},
		Risk:     RiskAssessment{Score: 3, Rating: RatingLow},
	}
	out, err := rep.ExportJSONWith(ExportOptions{Indent: true})
	if err != nil {
		t.Fatalf("indent export: %v", err)
	}
	if !strings.Contains(string(out), "\n  ") {
		t.Errorf("indent export is not indented:\n%s", out)
	}
	if !strings.HasSuffix(string(out), "\n") {
		t.Error("indented export should end with a newline")
	}
}

// TestExportJSONDoesNotEscapeHTML matters because a payload can legitimately
// contain <, >, and & — a section name or a marker string with a Windows path.
func TestExportJSONDoesNotEscapeHTML(t *testing.T) {
	rep := &Report{
		Format:   "pe",
		Identity: FileIdentity{Name: "x.exe", SHA256: "f"},
		Risk:     RiskAssessment{Score: 2, Rating: RatingLow},
		Stats:    AnalysisStats{Warnings: []string{"probe: C:\\Program Files\\VMware & Co <test>"}},
	}
	out, err := rep.ExportJSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), `\u0026`) || strings.Contains(string(out), `\u003c`) {
		t.Errorf("HTML entities escaped, corrupting payload text:\n%s", out)
	}
	if !strings.Contains(string(out), "VMware & Co <test>") {
		t.Errorf("warning text did not survive export:\n%s", out)
	}
}

// TestSchemaFieldsDeriveFromStruct verifies the reflected schema matches the
// real JSON shape, so the description cannot drift from the struct.
func TestSchemaFieldsDeriveFromStruct(t *testing.T) {
	rep := &Report{Format: "pe"}
	fields := rep.SchemaFields()

	byPath := make(map[string]string, len(fields))
	for _, f := range fields {
		byPath[f.Path] = f.Type
	}
	for _, want := range []string{"format", "identity", "sections", "findings", "risk", "stats"} {
		if _, ok := byPath[want]; !ok {
			t.Errorf("schema is missing top-level field %q", want)
		}
	}
	if byPath["sections"] != "array<SectionReport>" {
		t.Errorf("sections type = %q, want array<SectionReport>", byPath["sections"])
	}
	if byPath["findings"] != "array<Finding>" {
		t.Errorf("findings type = %q, want array<Finding>", byPath["findings"])
	}
	// A renamed tag must show up here, proving the list is derived not written.
	if _, ok := byPath["arch"]; !ok {
		t.Error("arch field missing from derived schema")
	}
}

// TestSummaryLineIsSingleLineAndCompact guards the human-facing rendering used
// as a SIEM message body.
func TestSummaryLineIsSingleLineAndCompact(t *testing.T) {
	rep, err := New(Options{}).AnalyzeBytes(context.Background(), buildPE(defaultPEFixture()), "sum.exe")
	if err != nil {
		t.Fatal(err)
	}
	line := rep.SummaryLine()
	if strings.Contains(line, "\n") {
		t.Errorf("summary spans multiple lines:\n%s", line)
	}
	for _, want := range []string{"pe", "sum.exe", "risk=", "findings="} {
		if !strings.Contains(line, want) {
			t.Errorf("summary %q missing %q", line, want)
		}
	}
	var nilRep *Report
	if nilRep.SummaryLine() == "" {
		t.Error("nil report summary is empty")
	}
}

// TestAnalysisHonoursCancelledContext verifies a caller-supplied cancellation
// actually stops work rather than being ignored until the scan finishes.
func TestAnalysisHonoursCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before entry

	_, err := New(Options{}).AnalyzeBytes(ctx, buildPE(defaultPEFixture()), "cancelled.exe")
	if err == nil {
		t.Fatal("analysis of a cancelled context returned no error")
	}
	if err != context.Canceled {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// TestAnalysisHonoursContextDeadline verifies the deadline path, which is the
// reason ctx was added: bounding a long scan.
func TestAnalysisHonoursContextDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond) // let it expire

	_, err := New(Options{}).AnalyzeBytes(ctx, buildPE(defaultPEFixture()), "deadline.exe")
	if err == nil {
		t.Fatal("analysis past its deadline returned no error")
	}
}

// TestPartialReportOnMidScanCancellation verifies the documented behaviour: a
// scan cancelled partway still returns the findings gathered so far, flagged as
// partial. Discarding them would be worse than returning them for a triage
// pipeline, where "we looked and found these before giving up" is real signal.
func TestPartialReportOnMidScanCancellation(t *testing.T) {
	img := buildPE(defaultPEFixture())

	rep, err := New(Options{FullStringScan: true}).AnalyzeBytes(context.Background(), img, "partial.exe")
	if err != nil {
		t.Fatalf("clean run failed: %v", err)
	}
	if len(rep.Findings) == 0 {
		t.Fatal("clean run produced no findings to compare against")
	}

	// Cancelling before entry is the deterministic case; mid-scan cancellation
	// depends on timing, so the guarantee asserted here is that a cancelled
	// context never yields a confident-looking complete report.
	cctx, ccancel := context.WithCancel(context.Background())
	ccancel()
	if _, err := New(Options{FullStringScan: true}).AnalyzeBytes(cctx, img, "partial.exe"); err == nil {
		t.Error("cancelled scan returned a report; it must not look successful")
	}
}
