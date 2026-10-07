package bininspect

import (
	"context"
	"testing"
)

// BenchmarkShannonEntropy measures the entropy hot loop. It is O(n) over the
// input with a fixed 256-bucket histogram, so cost is dominated by memory
// bandwidth; the benchmark exists to catch an accidental O(n log n) rewrite.
func BenchmarkShannonEntropy(b *testing.B) {
	data := prngBytes(1<<20, 7) // 1 MiB
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ShannonEntropy(data)
	}
}

// BenchmarkScanOpcodeSets measures executable-section scanning.
func BenchmarkScanOpcodeSets(b *testing.B) {
	data := prngBytes(1<<20, 11)
	// Seed a realistic density of syscall pairs so the scanner does real work.
	for off := 0; off < len(data)-1; off += 4096 {
		data[off], data[off+1] = 0x0F, 0x05
	}
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ScanOpcodeSets(data)
	}
}

// BenchmarkAnalyzePE measures a full PE analysis end to end.
func BenchmarkAnalyzePE(b *testing.B) {
	img := buildPE(defaultPEFixture())
	a := New(Options{})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := a.AnalyzeBytes(context.Background(), img, "bench.exe"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkAnalyzeELF measures a full ELF analysis end to end.
func BenchmarkAnalyzeELF(b *testing.B) {
	img := buildELF(defaultELFFixture())
	a := New(Options{})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := a.AnalyzeBytes(context.Background(), img, "bench.elf"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkAnalyzeFastPath measures the DisableByteScan path, which is the
// triage-first configuration.
func BenchmarkAnalyzeFastPath(b *testing.B) {
	img := buildPE(defaultPEFixture())
	a := New(Options{DisableByteScan: true})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := a.AnalyzeBytes(context.Background(), img, "bench.exe"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkAnalyzeParallel measures concurrent analysis through a single shared
// Analyzer, which is the intended production usage.
func BenchmarkAnalyzeParallel(b *testing.B) {
	img := buildPE(defaultPEFixture())
	a := New(Options{})
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := a.AnalyzeBytes(context.Background(), img, "bench.exe"); err != nil {
				b.Fatal(err)
			}
		}
	})
}
