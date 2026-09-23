package entropy

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// SensorReading holds one hardware telemetry reading.
// Dynamic fields change over time (thermal, freq, interrupts, load).
// Static fields identify the hardware but don't vary.
type SensorReading struct {
	Source  string
	Value   int64
	Dynamic bool
}

// Collect dynamically discovers all available hardware telemetry
// from sysfs and procfs.  Each candidate is validated before inclusion:
//
//   - Thermal zones must have a real temp reading (not 0, not unknown
//     0x7fffffff, and not emulated/software-sourced).
//   - Per-core CPU frequencies are included only when at least two
//     cores differ (if all identical, frequency is not a dynamic source).
//   - Interrupt counters are included only when non-zero.
//   - Static topology (model, cache, cores) is included as a fingerprint
//     but marked non-dynamic.
//   - Load averages are always dynamic.
//
// No kernel module or special privileges are required — everything is
// read from virtual filesystems the kernel exposes to userspace.
func Collect() ([]SensorReading, error) {
	var readings []SensorReading

	// Thermal zones: validate real, non-emulated readings.
	if zs, err := filepath.Glob("/sys/class/thermal/thermal_zone*/temp"); err == nil {
		for _, p := range zs {
			zone := filepath.Base(filepath.Dir(p))
			t, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			v, err := strconv.ParseInt(strings.TrimSpace(string(t)), 10, 64)
			if err != nil {
				continue
			}
			// 0 or 0x7fffffff means "unknown" on sysfs thermal.
			if v == 0 || v == 0x7fffffff {
				continue
			}
			// Skip emulated/software-sourced thermal zones.
			if typ, err := os.ReadFile(filepath.Dir(p) + "/type"); err == nil {
				if strings.TrimSpace(string(typ)) == "emul_temp" {
					continue
				}
			}
			readings = append(readings, SensorReading{
				Source:  zone + "/temp",
				Value:   v,
				Dynamic: true,
			})
		}
	}

	// Per-core CPU frequencies: only include if at least two cores differ.
	if fs, err := filepath.Glob("/sys/devices/system/cpu/cpu*/cpufreq/scaling_cur_freq"); err == nil {
		var coreFreqs []SensorReading
		for _, p := range fs {
			core := filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(p))))
			t, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			v, err := strconv.ParseInt(strings.TrimSpace(string(t)), 10, 64)
			if err != nil || v == 0 {
				continue
			}
			coreFreqs = append(coreFreqs, SensorReading{
				Source:  core + "/freq",
				Value:   v,
				Dynamic: true,
			})
		}
		if hasVariation(coreFreqs) {
			readings = append(readings, coreFreqs...)
		}
	}

	// Interrupt timing jitter from /proc/interrupts — non-zero counters only.
	if b, err := os.ReadFile("/proc/interrupts"); err == nil {
		sc := bufio.NewScanner(strings.NewReader(string(b)))
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "           ") || strings.HasPrefix(line, "CPU") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				if v, err := strconv.ParseInt(fields[0], 10, 64); err == nil && v != 0 {
					readings = append(readings, SensorReading{
						Source:  "irq:" + fields[1],
						Value:   v,
						Dynamic: true,
					})
				}
			}
		}
	}

	// CPU topology: static fingerprint (model, cache, cores).
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		sc := bufio.NewScanner(strings.NewReader(string(b)))
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "model name"):
				readings = append(readings, SensorReading{
					Source:  "cpu/model",
					Value:   hashString(line),
					Dynamic: false,
				})
			case strings.HasPrefix(line, "cpu cores"):
				if v, err := strconv.ParseInt(strings.Fields(line)[1], 10, 64); err == nil {
					readings = append(readings, SensorReading{
						Source:  "cpu/cores",
						Value:   v,
						Dynamic: false,
					})
				}
			case strings.HasPrefix(line, "cache size"):
				if v, err := strconv.ParseInt(strings.Fields(line)[1], 10, 64); err == nil {
					readings = append(readings, SensorReading{
						Source:  "cpu/cache",
						Value:   v,
						Dynamic: false,
					})
				}
			}
		}
	}

	// CPU load averages from /proc/loadavg — always dynamic.
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(b))
		for i, f := range fields[:3] {
			if v, err := strconv.ParseFloat(f, 64); err == nil {
				readings = append(readings, SensorReading{
					Source:  fmt.Sprintf("load/%d", i),
					Value:   int64(v * 1e6),
					Dynamic: true,
				})
			}
		}
	}

	// Deduplicate by source.
	seen := make(map[string]bool)
	var unique []SensorReading
	for _, r := range readings {
		if !seen[r.Source] {
			seen[r.Source] = true
			unique = append(unique, r)
		}
	}
	readings = unique

	return readings, nil
}

// hasVariation returns true if at least two sensor readings have
// different values (indicating a dynamic signal worth entropy).
func hasVariation(rs []SensorReading) bool {
	if len(rs) < 2 {
		return false
	}
	first := rs[0].Value
	for _, r := range rs[1:] {
		if r.Value != first {
			return true
		}
	}
	return false
}

// Dynamic returns only the readings that change over time.
// Static topology is excluded.
func Dynamic() ([]SensorReading, error) {
	all, err := Collect()
	if err != nil {
		return nil, err
	}
	var dyn []SensorReading
	for _, r := range all {
		if r.Dynamic {
			dyn = append(dyn, r)
		}
	}
	return dyn, nil
}

// Hash returns a hardware-entropy-derived byte slice of length n.
// It collects all sensor readings (dynamic + static fingerprint),
// hashes them with SHA-256 in a counter-mode chain, and returns
// the first n bytes.  Dynamic sources provide time-varying entropy;
// static sources bind the key to the specific hardware.
func Hash(n int) ([]byte, error) {
	readings, err := Collect()
	if err != nil {
		return nil, fmt.Errorf("entropy: collect failed: %w", err)
	}
	if len(readings) == 0 {
		return nil, fmt.Errorf("entropy: no hardware sensors found")
	}

	h := sha256.New()
	for _, r := range readings {
		h.Write([]byte(r.Source))
		var buf [8]byte
		putUint64(buf[:], uint64(r.Value))
		h.Write(buf[:])
		_ = h.Sum(nil)[:0] // reuse
	}
	sum := h.Sum(nil)

	out := make([]byte, n)
	copy(out, sum)
	for i := 1; len(out) > sha256.Size; i++ {
		h.Reset()
		h.Write(sum[:sha256.Size])
		var c [4]byte
		putUint32(c[:], uint32(i))
		h.Write(c[:])
		sum = h.Sum(nil)
		copy(out[sha256.Size:], sum[:])
		out = out[sha256.Size:]
	}

	return out[:n], nil
}

func putUint64(b []byte, v uint64) {
	for i := 0; i < 8; i++ {
		b[i] = byte(v >> (8 * i))
	}
}
func putUint32(b []byte, v uint32) {
	for i := 0; i < 4; i++ {
		b[i] = byte(v >> (8 * i))
	}
}

func hashString(s string) int64 {
	h := sha256.Sum256([]byte(s))
	var v int64
	for i := 0; i < 8; i++ {
		v |= int64(h[i]) << (8 * i)
	}
	return v
}
