package bininspect

import (
	"math"
	"testing"
)

// TestShannonEntropyKnownValues pins the entropy function against values that
// can be computed by hand, so a refactor cannot silently change the number the
// whole packing heuristic depends on.
func TestShannonEntropyKnownValues(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want float64
	}{
		{"empty", nil, 0},
		{"single byte", []byte{0xAA}, 0},
		{"two distinct", []byte{0x00, 0xFF}, 1},
		{"four distinct", []byte{0, 1, 2, 3}, 2},
		{"uniform 256", uniform256(), 8},
		{"half/half", append(make([]byte, 128), byteSlice(128, 0xFF)...), 1},
		{"three symbols", []byte{0, 1, 2}, math.Log2(3)},
	}
	for _, tc := range cases {
		got := ShannonEntropy(tc.in)
		if math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("%s: ShannonEntropy = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// uniform256 returns one of each byte value.
func uniform256() []byte {
	b := make([]byte, 256)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

func byteSlice(n int, v byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = v
	}
	return b
}

// TestShannonEntropyIsBounded asserts the documented [0,8] range across a
// range of skewed inputs, which is what makes the thresholds meaningful.
func TestShannonEntropyIsBounded(t *testing.T) {
	inputs := [][]byte{
		nil,
		{0},
		{0, 0, 0, 1},
		uniform256(),
		byteSlice(1000, 0x41),
		uniform256()[:255],
	}
	for i, in := range inputs {
		h := ShannonEntropy(in)
		if h < 0 || h > MaxEntropy() {
			t.Errorf("input %d: entropy %v outside [0,%v]", i, h, MaxEntropy())
		}
	}
}

// TestClassifyEntropyBands pins the banding boundaries and their ordering,
// because the bands are what separate "compiled code" from "packed".
func TestClassifyEntropyBands(t *testing.T) {
	cases := []struct {
		h    float64
		want EntropyClass
	}{
		{0, EntropyEmpty},
		{0.01, EntropyStructured},
		{4.99, EntropyStructured},
		{EntropyNativeThreshold, EntropyNative},
		{6.5, EntropyNative},
		{EntropyCompressedThreshold, EntropyCompressed},
		{7.0, EntropyCompressed},
		{EntropyEncryptedThreshold, EntropyEncrypted},
		{8, EntropyEncrypted},
	}
	for _, tc := range cases {
		if got := ClassifyEntropy(tc.h); got != tc.want {
			t.Errorf("ClassifyEntropy(%v) = %q, want %q", tc.h, got, tc.want)
		}
	}
	if !(EntropyNativeThreshold < EntropyCompressedThreshold && EntropyCompressedThreshold < EntropyEncryptedThreshold) {
		t.Error("entropy thresholds are not strictly increasing; banding would be ambiguous")
	}
}

// TestShannonEntropyDistinguishesPackedFromPlain guards the heuristic's core
// promise: real text has low entropy, random data does not.
func TestShannonEntropyDistinguishesPackedFromPlain(t *testing.T) {
	plain := []byte("the quick brown fox jumps over the lazy dog, repeatedly, at length.")
	if h := ShannonEntropy(plain); h >= EntropyCompressedThreshold {
		t.Errorf("plain text entropy %v should be below the compressed threshold", h)
	}
	// A deterministic pseudo-random buffer stands in for encrypted content.
	if h := ShannonEntropy(prngBytes(4096, 12345)); h < EntropyEncryptedThreshold {
		t.Errorf("pseudo-random entropy %v should reach the encrypted band", h)
	}
}

// prngBytes returns n deterministic pseudo-random bytes (xorshift), so the
// test never depends on math/rand's global state or on a fixed random seed
// interacting with other tests.
func prngBytes(n int, seed uint64) []byte {
	b := make([]byte, n)
	x := seed | 1
	for i := range b {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		b[i] = byte(x)
	}
	return b
}

func TestMaxEntropy(t *testing.T) {
	if MaxEntropy() != 8 {
		t.Errorf("MaxEntropy() = %v, want 8", MaxEntropy())
	}
}
