package main

import (
	"os"
	"testing"
)

func TestDebugPolicy(t *testing.T) {
	cases := map[string]string{
		"exit":     "exit",
		"off":      "off",
		"0":        "off",
		"false":    "off",
		"no":       "off",
		"":         "warn",
		"warn":     "warn",
		"WARN":     "warn",
		"paranoid": "warn",
	}
	for in, want := range cases {
		t.Setenv("HIVEMIND_HARDEN", in)
		if got := debugPolicy(); got != want {
			t.Errorf("policy %q = %q, want %q", in, got, want)
		}
	}
	os.Unsetenv("HIVEMIND_HARDEN")
}

func TestTracerPid(t *testing.T) {
	if got := tracerPid("Name:\thivemind\nTracerPid:\t0\n"); got != 0 {
		t.Fatalf("clean status parsed as traced: %d", got)
	}
	if got := tracerPid("TracerPid:\t1234\n"); got != 1234 {
		t.Fatalf("tracer 1234 parsed as %d", got)
	}
	if got := tracerPid("garbage without colons"); got != 0 {
		t.Fatalf("garbage parsed as %d", got)
	}
	if got := tracerPid("TracerPid:\tnot-a-number\n"); got != 0 {
		t.Fatalf("non-numeric parsed as %d", got)
	}
}

func TestScanInjectorEnv(t *testing.T) {
	t.Setenv("LD_PRELOAD", "/tmp/evil.so")
	t.Setenv("LD_LIBRARY_PATH", "")
	if got := scanInjectorEnv(); len(got) != 1 {
		t.Fatalf("want exactly the set vector, got %v", got)
	}
	t.Setenv("LD_PRELOAD", "")
	if got := scanInjectorEnv(); len(got) != 0 {
		t.Fatalf("clean env flagged: %v", got)
	}
}

// The detectors must agree in the one direction that matters: a
// /proc-visible tracer implies TRACEME refusal. The reverse (refusal
// with a clean status) happens in ptrace-blocking sandboxes and is
// allowed — refusal is evidence, never proof.
func TestDetectorsAgree(t *testing.T) {
	tracedByProc := currentTracerPid() != 0
	refused := selfTraceTest()
	if tracedByProc && !refused {
		t.Fatal("/proc shows a tracer that TRACEME did not feel")
	}
	t.Logf("proc-traced=%v traceme-refused=%v", tracedByProc, refused)
}
