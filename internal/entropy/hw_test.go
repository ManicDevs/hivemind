package entropy

import (
	"testing"
)

func TestCollect(t *testing.T) {
	r, err := Collect()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(r) == 0 {
		t.Fatal("expected >0 readings")
	}
	t.Logf("readings=%d", len(r))
	dyn := 0
	for _, s := range r {
		if s.Dynamic {
			dyn++
		}
	}
	t.Logf("dynamic=%d static=%d", dyn, len(r)-dyn)
	for _, s := range r[:5] {
		t.Logf("  %s=%d dynamic=%v", s.Source, s.Value, s.Dynamic)
	}
}

func TestHash(t *testing.T) {
	b1, err := Hash(32)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if len(b1) != 32 {
		t.Fatalf("expected 32 bytes, got %d", len(b1))
	}
	// Same readings within a single call window should produce
	// identical hashes.
	b2, err := Hash(32)
	if err != nil {
		t.Fatalf("Hash2: %v", err)
	}
	t.Logf("hash1=%x hash2=%x same=%v", b1, b2, string(b1) == string(b2))
}

func TestDynamic(t *testing.T) {
	d, err := Dynamic()
	if err != nil {
		t.Fatalf("Dynamic: %v", err)
	}
	if len(d) == 0 {
		t.Fatal("expected >0 dynamic readings")
	}
	for _, s := range d {
		if !s.Dynamic {
			t.Errorf("non-dynamic reading in Dynamic(): %s", s.Source)
		}
	}
	t.Logf("dynamic readings=%d", len(d))
	for _, s := range d[:3] {
		t.Logf("  %s=%d", s.Source, s.Value)
	}
}

func TestHasVariation(t *testing.T) {
	if hasVariation([]SensorReading{{Value: 100}, {Value: 200}}) {
		// expected
	} else {
		t.Error("expected variation")
	}
	if hasVariation([]SensorReading{{Value: 100}, {Value: 100}}) {
		t.Error("expected no variation for identical values")
	}
	if hasVariation([]SensorReading{{Value: 100}}) {
		t.Error("expected no variation for single reading")
	}
}

func TestNoZeroOrUnknown(t *testing.T) {
	r, err := Collect()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, s := range r {
		if s.Value == 0 {
			t.Errorf("zero value in readings: %s", s.Source)
		}
		if s.Value == 0x7fffffff {
			t.Errorf("unknown value in readings: %s", s.Source)
		}
	}
}
