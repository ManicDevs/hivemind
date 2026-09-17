package hivemind

import (
	"math"
	"os"
	"strconv"
	"strings"
)

// ── silicon telemetry: pure parsers + honest readers ──────────────────
// Every number the minds feel comes from here. Parsers take text and
// return numbers (unit-testable against fixture strings); readers touch
// /proc and /sys (best-effort, silent fallbacks, never fatal).

// cpuTimes is one /proc/stat snapshot: total and idle jiffies per cpu.
type cpuTimes struct {
	total uint64
	idle  uint64
}

// parseCPUStat reads aggregate + per-cpu lines. Malformed lines are
// skipped, not fatal: a partial reading beats a dead mind.
func parseCPUStat(data string) map[string]cpuTimes {
	out := make(map[string]cpuTimes)
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || !strings.HasPrefix(f[0], "cpu") {
			continue
		}
		var total, idle uint64
		// fields: cpu user nice system idle iowait irq softirq ...
		// idle = idle + iowait; total = everything counted.
		vals := make([]uint64, 0, len(f)-1)
		for _, tok := range f[1:] {
			v, err := strconv.ParseUint(tok, 10, 64)
			if err != nil {
				break
			}
			vals = append(vals, v)
		}
		if len(vals) < 4 {
			continue
		}
		for _, v := range vals {
			total += v
		}
		idle = vals[3]
		if len(vals) > 4 {
			idle += vals[4] // iowait is idle time wearing a work costume
		}
		out[f[0]] = cpuTimes{total: total, idle: idle}
	}
	return out
}

// cpuUsageFraction returns 0..1 utilization between two snapshots:
// 1 minus the idle fraction of elapsed jiffies. False when either side
// is empty or time ran backward (counters reset).
func cpuUsageFraction(prev, cur map[string]cpuTimes) (float64, bool) {
	var dt, di uint64
	var n int
	for name, c := range cur {
		p, ok := prev[name]
		if !ok || name == "cpu" {
			continue // aggregate double-counts; per-cpu only
		}
		if c.total < p.total || c.idle < p.idle {
			return 0, false
		}
		dt += c.total - p.total
		di += c.idle - p.idle
		n++
	}
	if n == 0 || dt == 0 {
		return 0, false
	}
	return math.Max(0, math.Min(1, 1-float64(di)/float64(dt))), true
}

// readMemPressure returns total, available, swap-total, swap-free kB.
// False when /proc/meminfo is missing or unparseable.
func readMemPressure() (total, avail, swapTotal, swapFree float64, ok bool) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, 0, 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v, err := strconv.ParseFloat(f[1], 64)
		if err != nil {
			continue
		}
		switch f[0] {
		case "MemTotal:":
			total = v
		case "MemAvailable:":
			avail = v
		case "SwapTotal:":
			swapTotal = v
		case "SwapFree:":
			swapFree = v
		}
	}
	if total <= 0 {
		return 0, 0, 0, 0, false
	}
	return total, avail, swapTotal, swapFree, true
}

// readCPUFreq returns current and max frequency (kHz) of cpu0 as a
// throttling signal. False where cpufreq is absent (VMs, some ARM).
func readCPUFreq() (cur, max float64, ok bool) {
	rawCur, err := os.ReadFile("/sys/devices/system/cpu/cpu0/cpufreq/scaling_cur_freq")
	if err != nil {
		return 0, 0, false
	}
	rawMax, err := os.ReadFile("/sys/devices/system/cpu/cpu0/cpufreq/cpuinfo_max_freq")
	if err != nil {
		return 0, 0, false
	}
	cur, err = strconv.ParseFloat(strings.TrimSpace(string(rawCur)), 64)
	if err != nil {
		return 0, 0, false
	}
	max, err = strconv.ParseFloat(strings.TrimSpace(string(rawMax)), 64)
	if err != nil || max <= 0 {
		return 0, 0, false
	}
	return cur, max, true
}

// cpuDelta computes this mind's CPU stress from jiffy deltas, stashing
// the snapshot for next time. First call always reports "no delta yet"
// so the caller falls back (loadavg), never a fabricated zero.
func (m *Mind) cpuDelta() (float64, bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, false
	}
	cur := parseCPUStat(string(data))
	prev := m.prevCPU
	m.prevCPU = cur
	if prev == nil {
		return 0, false
	}
	return cpuUsageFraction(prev, cur)
}
