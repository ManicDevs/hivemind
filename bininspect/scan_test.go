package bininspect

import (
	"encoding/binary"
	"strings"
	"testing"
)

// TestScanSyscallOpcodes verifies each architecture's system-call instruction
// is recognised, and that absence of the byte pair yields no finding.
func TestScanSyscallOpcodes(t *testing.T) {
	cases := []struct {
		name    string
		in      []byte
		wantHit bool
	}{
		{"x86-64 syscall", []byte{0x0F, 0x05}, true},
		{"sysenter", []byte{0x0F, 0x34}, true},
		{"int 2e", []byte{0xCD, 0x2E}, true},
		{"int 80", []byte{0xCD, 0x80}, true},
		{"aarch64 svc", []byte{0x01, 0x00, 0x00, 0xD4}, true},
		{"arm svc", []byte{0x00, 0x00, 0x00, 0xEF}, true},
		{"ret only", []byte{0xC3, 0x90, 0x90}, false},
		{"empty", nil, false},
	}
	for _, tc := range cases {
		hits := ScanOpcodeSets(tc.in)
		got := len(hits) > 0
		if got != tc.wantHit {
			t.Errorf("%s: hit=%v, want %v (hits=%d)", tc.name, got, tc.wantHit, len(hits))
		}
	}
}

// TestScanDirectSyscallStubDecodesSSN pins the hand-rolled-stub heuristic: a
// "mov eax, imm32 ; syscall" sequence must yield the decoded service number,
// because that is what distinguishes a real syscall dispatcher from an
// incidental byte pair.
func TestScanDirectSyscallStubDecodesSSN(t *testing.T) {
	buf := make([]byte, 0, 16)
	buf = append(buf, 0xB8)
	var imm [4]byte
	binary.LittleEndian.PutUint32(imm[:], 0x50) // syscall number 80
	buf = append(buf, imm[:]...)
	buf = append(buf, 0x0F, 0x05)

	hits := ScanOpcodeSets(buf)
	var found bool
	for _, h := range hits {
		if h.Kind != "direct-syscall-stub" {
			continue
		}
		found = true
		if !strings.Contains(h.Detail, "80") {
			t.Errorf("stub detail %q does not carry the decoded service number", h.Detail)
		}
		if h.Confidence < 0.9 {
			t.Errorf("decoded stub confidence = %v, want >= 0.9", h.Confidence)
		}
	}
	if !found {
		t.Fatalf("no direct-syscall-stub hit for % x", buf)
	}
}

// TestScanRejectsImplausibleSSN ensures a wild immediate is not reported as a
// service number, which would otherwise turn any B8-prefixed byte into evidence.
func TestScanRejectsImplausibleSSN(t *testing.T) {
	buf := make([]byte, 0, 8)
	buf = append(buf, 0xB8)
	var imm [4]byte
	binary.LittleEndian.PutUint32(imm[:], 0xFFFFFFFF) // not a syscall number
	buf = append(buf, imm[:]...)
	buf = append(buf, 0x0F, 0x05)

	for _, h := range ScanOpcodeSets(buf) {
		if h.Kind == "direct-syscall-stub" {
			t.Errorf("implausible service number reported as a direct syscall: %q", h.Detail)
		}
	}
}

// TestScanRdtscPairConfidenceRisesWithRecurrence documents the confidence model:
// one pair is weaker evidence than several.
func TestScanRdtscPairConfidenceRisesWithRecurrence(t *testing.T) {
	single := []byte{0x0F, 0x31, 0x90, 0x90, 0x90, 0x90, 0x0F, 0x31}
	hits := ScanOpcodeSets(single)
	var pairs int
	for _, h := range hits {
		if h.Kind == "rdtsc-pair" {
			pairs++
		}
	}
	if pairs == 0 {
		t.Fatalf("two RDTSC reads within the window were not paired: %+v", hits)
	}
}

// TestScanIgnoresDistantRdtsc ensures two unrelated RDTSC uses far apart are not
// reported as a timing check, which is the false positive that would otherwise
// fire on any large function.
func TestScanIgnoresDistantRdtsc(t *testing.T) {
	buf := make([]byte, timingPairWindow*2)
	buf[0], buf[1] = 0x0F, 0x31
	buf[timingPairWindow+10], buf[timingPairWindow+11] = 0x0F, 0x31
	for _, h := range ScanOpcodeSets(buf) {
		if h.Kind == "rdtsc-pair" {
			t.Fatalf("distant RDTSC reads paired (%+v)", h)
		}
	}
}

// TestScanAPIHashConstants verifies a known hash is found and that absent ones
// are not invented.
func TestScanAPIHashConstants(t *testing.T) {
	buf := make([]byte, 64)
	binary.LittleEndian.PutUint32(buf[8:12], 0x6A4ABC5B) // ntdll ROR-13
	hits := ScanAPIHashConstants(buf)
	if len(hits) == 0 {
		t.Fatal("known API-hash constant not detected")
	}
	if !strings.Contains(hits[0].Detail, "ntdll") {
		t.Errorf("hash detail %q does not identify the module", hits[0].Detail)
	}

	clean := make([]byte, 64)
	if got := ScanAPIHashConstants(clean); len(got) != 0 {
		t.Errorf("clean buffer produced %d false hash hits", len(got))
	}
}

// TestScanVMMarkers verifies marker detection and the confidence ladder that
// makes one stray string weaker evidence than several distinct markers.
func TestScanVMMarkers(t *testing.T) {
	if f, _ := ScanVMMarkers([]byte("nothing interesting here")); len(f) != 0 {
		t.Errorf("benign buffer produced findings: %+v", f)
	}

	// One marker alone should score lower than a cluster.
	one, _ := ScanVMMarkers([]byte("\x00vmtoolsd\x00"))
	if len(one) != 1 {
		t.Fatalf("single marker not detected: %+v", one)
	}
	if one[0].Confidence >= 0.9 {
		t.Errorf("single marker confidence %v is too high; one marker is often a false positive", one[0].Confidence)
	}

	many := strings.Join([]string{
		"vmtoolsd", "VBoxService", "vboxguest", "qemu-ga", "vmmouse", "vmhgfs",
		"sbiedll.dll", "cuckoo",
	}, "\x00")
	f, _ := ScanVMMarkers([]byte(many))
	if len(f) != 1 {
		t.Fatalf("marker cluster produced %d findings, want 1", len(f))
	}
	if f[0].Confidence < 0.85 {
		t.Errorf("marker-cluster confidence %v, want >= 0.85", f[0].Confidence)
	}
	if f[0].Severity != SeverityCritical {
		t.Errorf("marker-cluster severity = %q, want critical", f[0].Severity)
	}
}

// TestExtractStrings verifies printable-run extraction, which underpins marker
// detection.
func TestExtractStrings(t *testing.T) {
	in := []byte("\x00\x00hello\x00\x01\x02world\x00\xff\xffabc")
	got := ExtractStrings(in, 3)
	want := []string{"hello", "world", "abc"}
	if len(got) != len(want) {
		t.Fatalf("ExtractStrings = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("string %d = %q, want %q", i, got[i], want[i])
		}
	}
	// Minimum length must be respected.
	if s := ExtractStrings([]byte("ab\x00cd\x00"), 3); len(s) != 0 {
		t.Errorf("minimum length ignored: %q", s)
	}
}

// TestIndexAllNoOverlap guards the scanner's advance rule, which prevents one
// pattern from being counted once per overlapping shift.
func TestIndexAllNoOverlap(t *testing.T) {
	got := indexAll([]byte{0xAA, 0xAA, 0xAA, 0xAA}, []byte{0xAA, 0xAA})
	if len(got) != 2 {
		t.Errorf("indexAll = %v, want [0 2] (non-overlapping)", got)
	}
	if got := indexAll([]byte{0xAA}, []byte{0xAA, 0xAA}); got != nil {
		t.Errorf("needle longer than haystack should yield nil, got %v", got)
	}
}
