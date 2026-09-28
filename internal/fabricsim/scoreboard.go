package fabricsim

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Scoreboard is the immutable snapshot the scoreboard renders from. Taking a
// snapshot is what lets the engine keep ticking while the terminal is being
// redrawn, with no lock held across the write.
type Scoreboard struct {
	Tick    int64
	Elapsed time.Duration
	SimTime time.Time
	Sealed  bool
	SealWhy string

	ActiveNodes int
	QuarDef     int
	QuarAtk     int

	// per-kind tallies
	Offered  [numKinds]uint64
	Accepted [numKinds]uint64
	Rejected [numKinds]uint64

	// rejectReasons is the detector's per-reason tally, kept unexported because
	// it is diagnostic detail rather than a headline figure. RejectReasonCounts
	// exposes a copy for the gate and tests.
	rejectReasons map[string]uint64

	// consensus: legitimate frames that got through untouched
	ConsensusOK   uint64
	ConsensusLost uint64
	// consensus success as a fraction, precomputed to keep rendering cheap
	ConsensusRate float64

	// DriftInWindow counts clock-drift frames served because their claimed skew
	// fell inside the accepted key window. It is the measured cost of tolerating
	// ordinary clock drift, reported so the tolerance is a visible decision
	// rather than a silent gap in the recall figure.
	DriftInWindow uint64

	// detection
	TruePos   uint64
	FalsePos  uint64
	Missed    uint64
	Precision float64
	Recall    float64

	// attacker packet-drop detection time
	DropDetectP50Ms float64
	DropDetectP99Ms float64
	DropDetectMaxMs float64
	DropSamples     int
	// GapP50Ms and GapP99Ms are the receiver-inferred loss latency: how long a
	// frame was missing before a sequence gap proved it was lost rather than
	// merely delayed. This is a different measurement from DropDetect*, which
	// is the latency of attributing the loss to an attacker.
	GapP50Ms float64
	GapP99Ms float64
	// GapsOpened, GapsLost and GapsResolved describe the loss itself. A gap
	// that is opened but never resolved is loss that is still unnoticed, so
	// GapsLost-GapsResolved is the standing undetected-loss figure.
	GapsOpened   uint64
	GapsLost     uint64
	GapsResolved uint64
	// LossPrecision is actual loss over inferred loss, bounded above by 1. A
	// sequence-number loss detector can only guess: until the reorder window
	// closes it cannot tell a lost frame from a late one, so it necessarily
	// reports more loss than actually occurred. This is the honest cost of that
	// trade, and it belongs beside the detection latency rather than hidden.
	LossPrecision float64
	// RejectByReason attributes every rejection to the defence rule that
	// refused it. Without this the per-vector table is unreadable: once a node
	// is quarantined its later frames are all refused at the identity check,
	// so they never reach the structural or key-window logic that the vector
	// name suggests they exercise.
	RejectByReason map[string]uint64

	// DiskWrites is the simulator's own write accounting. It is a self-report
	// and deliberately cannot be incremented by simulator code: the claim it
	// supports is that the simulation path itself performs no writes, which is
	// enforced structurally by the package importing no file, socket or
	// syscall API rather than by this counter.
	DiskWrites uint64

	// transport
	InFlight       int
	LostFrames     int
	Enqueued       uint64
	Delivered      uint64
	Dropped        uint64
	DropFlood      uint64
	DropCongestion uint64

	// key window
	SkewIn  uint64
	SkewOut uint64

	Seals      uint64
	Pardons    uint64
	Swaps      int
	MemBytes   int64
	Goroutines int
}

// KillRate returns the fraction of offered frames that were refused, across all
// kinds including honest traffic, which is the operator-facing "how much of my
// network is being blocked" number.
func (s Scoreboard) KillRate() float64 {
	var off, rej uint64
	for k := 0; k < int(numKinds); k++ {
		off += s.Offered[k]
		rej += s.Rejected[k]
	}
	if off == 0 {
		return 0
	}
	return float64(rej) / float64(off)
}

// HonestPassRate returns the fraction of legitimate traffic that got through.
// This is the number that must stay high: a defence that blocks honest frames
// is worse than no defence.
func (s Scoreboard) HonestPassRate() float64 {
	if s.Offered[KindLegitimate] == 0 {
		return 1
	}
	return float64(s.Accepted[KindLegitimate]) / float64(s.Offered[KindLegitimate])
}

// Rate renders a fraction as a percentage with one decimal.
func (s Scoreboard) Rate(v float64) string {
	return strconv.FormatFloat(v*100, 'f', 1, 64) + "%"
}

// DropDetect renders the attacker packet-drop detection latency, which is the
// headline adversarial metric: how long an attack remains undetected.
func (s Scoreboard) DropDetect() string {
	if s.DropSamples == 0 {
		return "n/a"
	}
	return fmt.Sprintf("p50=%dms p99=%dms max=%dms",
		int(s.DropDetectP50Ms+0.5), int(s.DropDetectP99Ms+0.5), int(s.DropDetectMaxMs+0.5))
}

// ── drop-latency recorder ───────────────────────────────────────────────────

// dropRecorder accumulates attacker-packet-drop detection latencies so the
// scoreboard can report percentiles. A ring buffer of fixed size is used so the
// recorder's memory is bounded and it never allocates on the hot path.
type dropRecorder struct {
	buf   []float64
	n     int
	count uint64
	sum   float64
	max   float64
}

func newDropRecorder(size int) *dropRecorder {
	if size < 16 {
		size = 16
	}
	return &dropRecorder{buf: make([]float64, size)}
}

func (d *dropRecorder) add(ms float64) {
	if ms < 0 {
		return
	}
	// A zero-value recorder has no ring buffer. Rather than divide by zero, it
	// stays inert: a nil recorder is a recorder that was never asked to keep
	// samples, which is the correct reading of a value-typed field.
	if len(d.buf) == 0 {
		return
	}
	d.buf[d.n%len(d.buf)] = ms
	d.n++
	d.count++
	d.sum += ms
	if ms > d.max {
		d.max = ms
	}
}

// percentiles sorts a copy of the samples and returns the requested
// percentiles. The copy keeps the recorder lock-free for the engine.
func (d *dropRecorder) percentiles() (p50, p99, max float64, samples int) {
	n := int(d.count)
	if n == 0 {
		return 0, 0, 0, 0
	}
	if n > len(d.buf) {
		n = len(d.buf)
	}
	sorted := make([]float64, n)
	copy(sorted, d.buf[:n])
	insertionSort(sorted)
	p50 = sorted[(n-1)*50/100]
	p99 = sorted[(n-1)*99/100]
	return p50, p99, d.max, int(d.count)
}

// insertionSort is used instead of sort.Slice to avoid the reflection-based
// swapper on a slice that is at most a few hundred elements.
func insertionSort(a []float64) {
	for i := 1; i < len(a); i++ {
		v := a[i]
		j := i - 1
		for j >= 0 && a[j] > v {
			a[j+1] = a[j]
			j--
		}
		a[j+1] = v
	}
}

// ── rendering ───────────────────────────────────────────────────────────────

// ansi sequences for a single-line redraw.
const (
	ansiReset = "\x1b[0m"
	ansiDim   = "\x1b[2m"
	ansiBold  = "\x1b[1m"
	ansiRed   = "\x1b[31m"
	ansiGreen = "\x1b[32m"
	ansiAmber = "\x1b[33m"
	ansiCyan  = "\x1b[36m"
)

// Renderer draws the scoreboard. When the output is a terminal it redraws a
// single scrolling line in place; when it is a log file it emits a full
// multi-line table per tick, because escape sequences in a log are noise.
type Renderer struct {
	w        io.Writer
	isTTY    bool
	lastLen  int
	interval time.Duration
	lastDraw time.Time
	ticks    atomic.Uint64
}

// NewRenderer builds a renderer, auto-detecting terminal capability.
func NewRenderer(w io.Writer, interval time.Duration) *Renderer {
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	return &Renderer{w: w, isTTY: isTerminal(w), interval: interval}
}

// Draw renders the snapshot, rate-limited to the render interval. It reports
// whether anything was written.
func (r *Renderer) Draw(s Scoreboard, force bool) bool {
	now := time.Now()
	if !force && now.Sub(r.lastDraw) < r.interval {
		return false
	}
	r.lastDraw = now
	r.ticks.Add(1)

	if r.isTTY {
		line := r.singleLine(s)
		// Erase the previous line, then rewrite in place. A single trailing
		// newline is avoided so the cursor stays on the same row and the
		// console scrolls exactly once, instead of filling the scrollback
		// with thousands of near-identical lines.
		pad := ""
		if n := r.lastLen - runeLen(line); n > 0 {
			pad = strings.Repeat(" ", n)
		}
		fmt.Fprintf(r.w, "\r\x1b[2K%s%s", line, pad)
		r.lastLen = runeLen(line)
		return true
	}
	fmt.Fprint(r.w, r.table(s))
	return true
}

// singleLine builds the compact scrolling status line.
func (r *Renderer) singleLine(s Scoreboard) string {
	var b strings.Builder
	b.Grow(220)

	seal := ansiGreen + "OPEN  " + ansiReset
	if s.Sealed {
		seal = ansiRed + "SEALED" + ansiReset
	}

	consensusColour := ansiGreen
	if s.ConsensusRate < 0.95 {
		consensusColour = ansiAmber
	}
	if s.ConsensusRate < 0.80 {
		consensusColour = ansiRed
	}

	dropColour := ansiCyan
	if s.DropDetectP50Ms > 500 {
		dropColour = ansiAmber
	}
	if s.DropDetectP50Ms > 5000 {
		dropColour = ansiRed
	}

	fmt.Fprintf(&b, "%s FABRIC-SIM %s t=%ds %s ",
		ansiBold, ansiReset, s.Tick, seal)
	fmt.Fprintf(&b, "nodes:%s%d%s ",
		ansiDim, s.ActiveNodes, ansiReset)
	fmt.Fprintf(&b, "quar:%s%d/%d%s ",
		ansiDim, s.QuarDef, s.QuarAtk, ansiReset)
	fmt.Fprintf(&b, "consensus:%s%.1f%%%s ",
		consensusColour, s.ConsensusRate*100, ansiReset)
	fmt.Fprintf(&b, "honest:%s%.0f%%%s ",
		ansiDim, s.HonestPassRate()*100, ansiReset)
	fmt.Fprintf(&b, "prec:%s%.0f%%%s ",
		ansiDim, s.Precision*100, ansiReset)
	fmt.Fprintf(&b, "dropdetect:%s%s%s ",
		dropColour, s.DropDetect(), ansiReset)
	fmt.Fprintf(&b, "%sinflight:%d heap:%dMiB%s",
		ansiDim, s.InFlight, s.MemBytes>>20, ansiReset)
	return b.String()
}

// table builds the full multi-line rendering used for non-terminal output.
func (r *Renderer) table(s Scoreboard) string {
	var b strings.Builder
	b.WriteString("════════════════════════════════════════════════════════════\n")
	fmt.Fprintf(&b, " FABRIC-SIM  tick=%d  sim_elapsed=%s  sim_clock=%s\n",
		s.Tick, roundDur(s.Elapsed), s.SimTime.UTC().Format("15:04:05"))
	if s.Sealed {
		fmt.Fprintf(&b, " STATE      \x1b[41;97m FAIL-CLOSED SEALED \x1b[0m  (%s)\n", s.SealWhy)
	} else {
		fmt.Fprintf(&b, " STATE      OPEN\n")
	}
	fmt.Fprintf(&b, " NODES      active=%d  quarantined_def=%d  quarantined_atk=%d\n",
		s.ActiveNodes, s.QuarDef, s.QuarAtk)
	fmt.Fprintf(&b, " CONSENSUS  rate=%.2f%%  ok=%d  lost=%d  honest_pass=%.1f%%\n",
		s.ConsensusRate*100, s.ConsensusOK, s.ConsensusLost, s.HonestPassRate()*100)
	fmt.Fprintf(&b, " DETECTION  precision=%.1f%%  recall=%.1f%%  tp=%d fp=%d missed=%d\n",
		s.Precision*100, s.Recall*100, s.TruePos, s.FalsePos, s.Missed)
	// Two different latencies, reported separately because conflating them
	// hides which mechanism is actually fast:
	//   LOSS-DETECT  time until a sequence gap proved a frame was lost
	//   ATTR-DETECT  time until the loss was attributed to a hostile identity
	fmt.Fprintf(&b, " LOSS-DETECT p50=%dms p99=%dms   (sequence-gap inference)\n",
		int(s.GapP50Ms+0.5), int(s.GapP99Ms+0.5))
	fmt.Fprintf(&b, " ATTR-DETECT %s   (hostile identity)\n", s.DropDetect())
	fmt.Fprintf(&b, " KEY-WINDOW inside=%d  outside_refused=%d  (window=%.0fms)\n",
		s.SkewIn, s.SkewOut, skewWindowMs)
	fmt.Fprintf(&b, " TRANSPORT  enqueued=%d delivered=%d dropped=%d inflight=%d\n",
		s.Enqueued, s.Delivered, s.Dropped, s.InFlight)
	fmt.Fprintf(&b, " LOSS      gap_opened=%d frames_lost=%d gap_resolved=%d\n",
		s.GapsOpened, s.GapsLost, s.GapsResolved)
	fmt.Fprintf(&b, "           lost_to_flood=%d lost_to_congestion=%d\n",
		s.DropFlood, s.DropCongestion)
	fmt.Fprintf(&b, "           inferred=%d actual=%d precision=%.1f%%  over-report=%d\n",
		s.GapsLost, s.Dropped, s.LossPrecision*100, int64(s.GapsLost)-int64(s.Dropped))
	fmt.Fprintf(&b, "           still_unnoticed=%d\n", s.LostFrames)
	fmt.Fprintf(&b, " ACTIONS    seals=%d pardons=%d role_swaps=%d\n",
		s.Seals, s.Pardons, s.Swaps)
	if len(s.RejectByReason) > 0 {
		fmt.Fprintf(&b, " REJECTED-BY first line that refused the frame\n   ")
		for _, r := range []string{"sealed", "quarantined", "malformed", "key-window", "bad-signature", "anomaly", "unknown-source"} {
			if n := s.RejectByReason[r]; n > 0 {
				fmt.Fprintf(&b, "%s=%d  ", r, n)
			}
		}
		fmt.Fprintf(&b, "\n")
	}
	fmt.Fprintf(&b, " PER-VECTOR offered/accepted/rejected\n")
	for k := 0; k < int(numKinds); k++ {
		fmt.Fprintf(&b, "   %-12s %8d %9d %9d\n",
			attackKindNames[k], s.Offered[k], s.Accepted[k], s.Rejected[k])
	}
	fmt.Fprintf(&b, " HEAP       %d MiB  goroutines=%d\n", s.MemBytes>>20, s.Goroutines)
	b.WriteString("════════════════════════════════════════════════════════════\n")
	return b.String()
}

// Final renders the closing summary, always in full table form.
func (r *Renderer) Final(s Scoreboard) {
	fmt.Fprint(r.w, r.table(s))
}

func roundDur(d time.Duration) time.Duration {
	if d > time.Hour {
		return d.Round(time.Second)
	}
	return d.Round(time.Millisecond)
}

func runeLen(s string) int {
	// Count runes, since the line is built from ASCII and multi-byte escape
	// sequences are zero-width on screen but would still pad incorrectly if
	// counted as runes.
	return len([]rune(s))
}

// RejectReasonCounts returns the per-reason rejection tallies.
//
// Exposed so the preflight gate and tests can show why frames were refused
// rather than only how many.
func (s Scoreboard) RejectReasonCounts() map[string]uint64 {
	out := make(map[string]uint64, len(s.rejectReasons))
	for k, v := range s.rejectReasons {
		out[k] = v
	}
	return out
}
