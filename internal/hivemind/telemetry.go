package hivemind

import (
	"math"
	"os"
	"path/filepath"
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

// psiSignals is pressure-stall truth: what fraction of tasks recently
// stalled on cpu, memory, io (some = at least one, full = all). The
// kernel's own suffering metric — more honest than loadavg, which counts
// the runnable and the waiting alike.
type psiSignals struct {
	cpuSome, cpuFull float64
	memSome, memFull float64
	ioSome, ioFull   float64
}

// parsePSI reads the avg10 from some/full lines:
// "some avg10=0.01 avg60=0.03 avg300=0.12 total=769870045".
// Missing lines read as absent, not zero: false means unfelt, not fine.
func parsePSI(data string) (some10, full10 float64, ok bool) {
	var some, full bool
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		for _, kv := range f[1:] {
			parts := strings.SplitN(kv, "=", 2)
			if len(parts) != 2 || parts[0] != "avg10" {
				continue
			}
			v, err := strconv.ParseFloat(parts[1], 64)
			if err != nil {
				continue
			}
			switch f[0] {
			case "some":
				some10, some = v, true
			case "full":
				full10, full = v, true
			}
		}
	}
	return some10, full10, some || full
}

// readPSI loads cpu, memory, and io stall signals. Absent files (old
// kernels, some containers) degrade silently — each signal independent.
func readPSI() psiSignals {
	var p psiSignals
	if raw, err := os.ReadFile("/proc/pressure/cpu"); err == nil {
		p.cpuSome, p.cpuFull, _ = parsePSI(string(raw))
	}
	if raw, err := os.ReadFile("/proc/pressure/memory"); err == nil {
		p.memSome, p.memFull, _ = parsePSI(string(raw))
	}
	if raw, err := os.ReadFile("/proc/pressure/io"); err == nil {
		p.ioSome, p.ioFull, _ = parsePSI(string(raw))
	}
	return p
}

// applyPressureSignals folds stall truth into the body's readings.
// Worst wins everywhere: different sufferings, same consequence.
// PSI avg10 is a percent — scaled to [0,1] like everything else.
func applyPressureSignals(cpuStress, ramFatigue, pain float64, p psiSignals) (float64, float64, float64) {
	cpuStress = math.Max(cpuStress, clamp(p.cpuSome/100, 0, 1))
	ramFatigue = math.Max(ramFatigue, clamp(p.memSome/100, 0, 1))
	pain = math.Max(pain, clamp(p.ioFull/100, 0, 1))
	pain = math.Max(pain, clamp(p.memFull/100, 0, 1))
	return cpuStress, ramFatigue, pain
}

// parseNetDev sums rx/tx bytes per interface. Loopback counts: local
// mesh chatter is real traffic, honestly included.
func parseNetDev(data string) map[string][2]uint64 {
	out := map[string][2]uint64{}
	for _, line := range strings.Split(data, "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		if name == "" || name == "Inter-" || strings.HasPrefix(name, "face") {
			continue
		}
		f := strings.Fields(parts[1])
		if len(f) < 9 {
			continue
		}
		rx, err1 := strconv.ParseUint(f[0], 10, 64)
		tx, err2 := strconv.ParseUint(f[8], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		out[name] = [2]uint64{rx, tx}
	}
	return out
}

// parseDiskStats sums sectors read and written across all block devices:
// the disk's side of the suffering.
func parseDiskStats(data string) (readSectors, writeSectors uint64) {
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) < 14 {
			continue
		}
		r, err1 := strconv.ParseUint(f[5], 10, 64)
		w, err2 := strconv.ParseUint(f[9], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		readSectors += r
		writeSectors += w
	}
	return readSectors, writeSectors
}

// readEntropyAvail reports the kernel entropy pool level: real randomness
// reserves, not a draw. Thin pools mean thin air for new souls.
func readEntropyAvail() (float64, bool) {
	raw, err := os.ReadFile("/proc/sys/kernel/random/entropy_avail")
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// readUptime reports seconds since boot: the age of the world.
func readUptime() (float64, bool) {
	raw, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, false
	}
	f := strings.Fields(string(raw))
	if len(f) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// parseLoadavg reads the 1-minute average plus the runnable/total task
// counts ("1.34 1.49 4.99 2/2155 90756"): the crowd behind the average.
func parseLoadavg(data string) (load1, running, total float64, ok bool) {
	f := strings.Fields(data)
	if len(f) < 4 {
		return 0, 0, 0, false
	}
	load1, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0, 0, 0, false
	}
	parts := strings.SplitN(f[3], "/", 2)
	if len(parts) != 2 {
		return load1, 0, 0, true
	}
	running, err1 := strconv.ParseFloat(parts[0], 64)
	total, err2 := strconv.ParseFloat(parts[1], 64)
	if err1 != nil || err2 != nil {
		return load1, 0, 0, true
	}
	return load1, running, total, true
}

// parseStatCounts extracts the kernel's nervous activity: total
// interrupts, context switches, and current runnable/blocked tasks.
// Counter rewinds (reboot, 32-bit wrap) report absent, never negative.
func parseStatCounts(data string) (intr, ctxt uint64, running, blocked uint64, ok bool) {
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			continue
		}
		switch f[0] {
		case "intr":
			intr, ok = v, true
		case "ctxt":
			ctxt = v
		case "procs_running":
			running = v
		case "procs_blocked":
			blocked = v
		}
	}
	return intr, ctxt, running, blocked, ok
}

// parseSockstat reads the network body's census: sockets used, TCP
// in-use/orphaned, UDP in-use. Orphans are connections nobody owns —
// the mesh feeling its own ghosts.
func parseSockstat(data string) (tcpInuse, tcpOrphan, udpInuse, socksUsed uint64) {
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		get := func(key string) uint64 {
			for i := 0; i+1 < len(f); i++ {
				if f[i] == key {
					v, _ := strconv.ParseUint(f[i+1], 10, 64)
					return v
				}
			}
			return 0
		}
		switch f[0] {
		case "sockets:":
			socksUsed = get("used")
		case "TCP:":
			tcpInuse, tcpOrphan = get("inuse"), get("orphan")
		case "UDP:":
			udpInuse = get("inuse")
		}
	}
	return tcpInuse, tcpOrphan, udpInuse, socksUsed
}

// parseFileNR reads allocated file handles ("13344 0 max").
// Only the first field matters: handles currently owned system-wide.
func parseFileNR(data string) (uint64, bool) {
	f := strings.Fields(data)
	if len(f) == 0 {
		return 0, false
	}
	v, err := strconv.ParseUint(f[0], 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// fdVelocity is handle-bleed rate: handles gained per second, floored at
// zero (releases are healing, not negative bleeding).
func fdVelocity(prev, cur uint64, elapsed float64) float64 {
	if elapsed <= 0 || cur <= prev {
		return 0
	}
	return float64(cur-prev) / elapsed
}

// thermalPainAll reads every thermal zone and returns the worst pain:
// one hot corner is enough. Returns the source zone name.
func thermalPainAll() (float64, string, bool) {
	zones, err := filepath.Glob("/sys/class/thermal/thermal_zone*/temp")
	if err != nil || len(zones) == 0 {
		return 0, "", false
	}
	worst, src, found := 0.0, "", false
	for _, zone := range zones {
		raw, err := os.ReadFile(zone)
		if err != nil {
			continue
		}
		milli, err := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
		if err != nil {
			continue
		}
		p := clamp((milli/1000.0-40.0)/45.0, 0, 1)
		if !found || p > worst {
			worst, src, found = p, "sensor:"+filepath.Base(filepath.Dir(zone)), true
		}
	}
	return worst, src, found
}

// throttleWorst returns the worst CPU throttling across all cores:
// one held-down core is shared suffering.
func throttleWorst() (float64, bool) {
	paths, err := filepath.Glob("/sys/devices/system/cpu/cpu[0-9]*/cpufreq/scaling_cur_freq")
	if err != nil || len(paths) == 0 {
		return 0, false
	}
	worst, found := 0.0, false
	for _, curPath := range paths {
		dir := filepath.Dir(curPath)
		rawCur, err := os.ReadFile(curPath)
		if err != nil {
			continue
		}
		rawMax, err := os.ReadFile(filepath.Join(dir, "cpuinfo_max_freq"))
		if err != nil {
			continue
		}
		cur, err1 := strconv.ParseFloat(strings.TrimSpace(string(rawCur)), 64)
		max, err2 := strconv.ParseFloat(strings.TrimSpace(string(rawMax)), 64)
		if err1 != nil || err2 != nil || max <= 0 {
			continue
		}
		if t := clamp(1.0-cur/max, 0, 1); t > worst || !found {
			worst, found = t, true
		}
	}
	return worst, found
}

// parseMounts lists unique mount points from /proc/mounts text
// ("device /path fstype opts ..."). Serverless discipline: never assume
// which filesystems exist — discover them, watch them all.
func parseMounts(data string) []string {
	seen := map[string]bool{}
	var paths []string
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		if !seen[f[1]] {
			seen[f[1]] = true
			paths = append(paths, f[1])
		}
	}
	return paths
}

// diskDread is existential: the fullest mounted filesystem as a
// fraction. Mounts discovered live (no hardcoded paths); unreadable
// mount tables fall back to the three places that always matter.
// Only dread past 90% counts — mapped by the caller, not here.
func diskDread() (float64, bool) {
	paths := []string{"/", "/tmp", "."}
	if raw, err := os.ReadFile("/proc/mounts"); err == nil {
		if found := parseMounts(string(raw)); len(found) > 0 {
			paths = found
		}
	}
	worst, found := 0.0, false
	checked := 0
	for _, path := range paths {
		if checked >= 64 {
			break // bound the syscall budget; 64 filesystems is plenty
		}
		checked++
		if f, ok := fsUseFraction(path); ok && (!found || f > worst) {
			worst, found = f, true
		}
	}
	return worst, found
}
