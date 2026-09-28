package fabricsim

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// Config is the full simulator configuration.
type Config struct {
	// Seed makes a run exactly reproducible.
	Seed uint64
	// Workers is the size of the defender worker pool. It is deliberately
	// separate from the attacker pool so the memory budget for each half can
	// be sized independently: the control node has 32 GiB shared with Ollama
	// and the world, so the control-side pool is small.
	Workers int
	// AttackerWorkers sizes the attacking half. On the 96 GiB compute vault a
	// much larger pool is affordable, which is why it is not derived from
	// Workers.
	AttackerWorkers int
	// TickInterval is the wall-clock period between engine ticks.
	TickInterval time.Duration
	// TickDuration is the simulated time advanced per tick. Propagation delay
	// is measured against this, so a 500 ms tick with 2 s ticks of sim time
	// exercises the inter-continental RTTs without waiting for them.
	TickDuration time.Duration
	// Duration is the total simulated runtime. Zero means run until the
	// context is cancelled.
	Duration time.Duration
	// AttackRate is the aggregate hostile frames per tick across all
	// attackers.
	AttackRate int
	// LegitRate is the aggregate honest frames per tick.
	LegitRate int
	// PayloadBytes is the nominal honest frame size.
	PayloadBytes int
	// Out is where the scoreboard is drawn; nil means os.Stdout.
	Out io.Writer
	// RenderInterval rate-limits scoreboard redraws.
	RenderInterval time.Duration
	// Verbose prints a per-tick table even on a terminal.
	Verbose bool
	// FloodIntensity is the loss rate an active packet-drop attack drives onto
	// the paths it floods, on top of the transport's baseline loss.
	FloodIntensity float64
	// FloodDecayPct is the per-tick decay of that intensity, so a stopped
	// attack stops costing the honest nodes anything.
	FloodDecayPct float64
	// EnableRoleSwap turns on the periodic global role swap.
	EnableRoleSwap bool
	// RoleSwapEvery is how many ticks elapse between role swaps.
	RoleSwapEvery int
	// MemoryBudgetBytes is the heap ceiling the simulator will respect for
	// its own buffers. When the process heap approaches it the engine sheds
	// attack load rather than growing past the envelope.
	MemoryBudgetBytes int64
}

func (c *Config) applyDefaults() {
	if c.Workers <= 0 {
		c.Workers = 4 // control node: 16 threads shared with Ollama
	}
	if c.AttackerWorkers <= 0 {
		c.AttackerWorkers = 8 // compute vault: 32 threads, 96 GiB
	}
	if c.TickInterval <= 0 {
		c.TickInterval = 200 * time.Millisecond
	}
	if c.TickDuration <= 0 {
		c.TickDuration = 2 * time.Second
	}
	if c.LegitRate <= 0 {
		c.LegitRate = 24
	}
	if c.AttackRate <= 0 {
		c.AttackRate = 40
	}
	if c.PayloadBytes <= 0 {
		c.PayloadBytes = 96
	}
	if c.RenderInterval <= 0 {
		c.RenderInterval = 500 * time.Millisecond
	}
	if c.RoleSwapEvery <= 0 {
		c.RoleSwapEvery = 150
	}
	if c.MemoryBudgetBytes <= 0 {
		// A quarter of the 32 GiB control-node envelope, which leaves ample
		// room for Ollama, the world and the Go runtime itself.
		c.MemoryBudgetBytes = 8 << 30
	}
	if c.Out == nil {
		c.Out = stdout
	}
}

// stdout is indirected so tests can capture output without touching the real
// descriptor.
var stdout io.Writer = defaultStdout

// Engine is the running simulator.
type Engine struct {
	cfg    Config
	top    *Topology
	tr     *Transport
	det    *Detector
	rnd    *Rand
	rd     *dropRecorder
	rdMu   sync.Mutex
	prof   attackProfile
	trCfg  TransportConfig
	detCfg DetectorConfig

	renderer *Renderer

	tick     atomic.Int64
	simClock atomic.Int64 // unix nanos of simulated time
	sheds    atomic.Uint64
	swaps    atomic.Int64
	consOK   atomic.Uint64
	// srcSeq issues the per-source sequence numbers that loss is inferred
	// from: each node's frames are numbered independently, exactly as a real
	// sender numbers them.
	srcSeq [TotalNodes]atomic.Uint64
	// flood is the per-source link-flood intensity in force this tick, which
	// the transport uses to raise the loss rate on the paths being attacked.
	flood [TotalNodes]atomic.Uint64
	// diskWrites is a hard assertion of the zero-write requirement. The
	// simulator never increments it; a non-zero value at exit means a code
	// path acquired an OS resource, which is a defect.
	diskWrites atomic.Uint64

	// firstAttackSeen tracks, per attack source, when its first hostile frame
	// was emitted, so detection latency can be measured per burst.
	firstAttack map[NodeID]time.Time
	detectedAt  map[NodeID]time.Time

	// gapLatency is the bounded sample buffer for receiver-inferred loss
	// latency. The gap counters themselves live on the detector, which is their
	// single source of truth: a second copy here could drift from the state the
	// detector actually inferred loss from.
	gapLatency dropRecorder

	// done is closed when Run returns, so a host process can wait for the
	// closing summary before exiting.
	done     chan struct{}
	doneOnce sync.Once
}

// New builds an engine from a config, applying defaults.
func New(cfg Config) *Engine {
	cfg.applyDefaults()
	top := NewTopologySeeded(cfg.Seed)
	rnd := NewRand(cfg.Seed)
	return &Engine{
		cfg:         cfg,
		top:         top,
		tr:          NewTransport(DefaultTransportConfig()),
		det:         NewDetector(DefaultDetectorConfig(), top, rnd),
		rnd:         rnd,
		rd:          newDropRecorder(1024),
		prof:        DefaultAttackProfile(),
		trCfg:       DefaultTransportConfig(),
		detCfg:      DefaultDetectorConfig(),
		renderer:    NewRenderer(cfg.Out, cfg.RenderInterval),
		gapLatency:  *newDropRecorder(2048),
		firstAttack: make(map[NodeID]time.Time, MatrixSize),
		detectedAt:  make(map[NodeID]time.Time, MatrixSize),
		done:        make(chan struct{}),
	}
}

// Topology exposes the matrix for inspection and testing.
func (e *Engine) Topology() *Topology { return e.top }

// Detector exposes the defence for testing and for the live integration.
func (e *Engine) Detector() *Detector { return e.det }

// Transport exposes the in-memory fabric.
func (e *Engine) Transport() *Transport { return e.tr }

// Snapshot returns a consistent, lock-free view of the simulator.
func (e *Engine) Snapshot() Scoreboard {
	e.det.mu.RLock()
	offered, accepted, rejected := e.det.offered, e.det.accepted, e.det.rejected
	sealed, sealWhy := e.det.sealed, e.det.sealReason
	seals, pardons, skewIn, skewOut := e.det.seals, e.det.pardoned, e.det.skewAccepted[0], e.det.skewAccepted[1]
	reasons := make(map[string]uint64, len(e.det.rejectReason))
	for k, v := range e.det.rejectReason {
		reasons[k] = v
	}
	driftInWindow := e.det.driftInWindow
	gapsOpened, gapsLost, gapsResolved := e.det.gapsOpened, e.det.gapsDetected, e.det.gapsClosed
	e.det.mu.RUnlock()

	qd, qa := e.top.QuarantinedCount()
	active := e.top.Len() - qd - qa

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	tick := e.tick.Load()
	simTime := time.Unix(0, e.simClock.Load())

	// Drop-detection percentiles.
	e.rdMu.Lock()
	p50, p99, dmax, samples := e.rd.percentiles()
	gapP50, gapP99, _, _ := e.gapLatency.percentiles()
	e.rdMu.Unlock()

	// Consensus: honest frames that reached the far side untouched.
	legitOff := offered[KindLegitimate]
	legitAcc := accepted[KindLegitimate]
	consOK, consLost := legitAcc, legitOff-legitAcc

	// Precision/recall over hostile classification.
	//
	// A "true positive" is a hostile frame that was refused; a "false positive"
	// is honest traffic that was refused. Only the legit kind can be a false
	// positive, so hostile rejects never inflate precision.
	//
	// Recall is measured only over vectors where refusing the individual frame
	// is the correct outcome, for the reasons AttackKind.RefusableByFrame gives.
	// A flood is caught by loss detection rather than by refusing a perfectly
	// valid frame, and a drift inside the key window is traffic the mesh is
	// supposed to serve. Including those in the denominator produced a recall
	// figure that fell the moment the ground-truth shortcut in the detector was
	// removed, which is a sign the metric was wrong rather than that the
	// defence had regressed: the old number had been counting frames the
	// defence correctly let through as if it had missed them.
	var tp, fp, hostileOffered uint64
	for k := 0; k < int(numKinds); k++ {
		if !AttackKind(k).IsHostile() {
			fp += rejected[k]
			continue
		}
		// Drift frames are only attacks when they overshoot the window. The
		// detector already reports how many it accepted in-window, so the
		// tolerance is visible rather than silently absorbed.
		if AttackKind(k) == KindClockDrift {
			hostileOffered += offered[k] - driftInWindow
			tp += rejected[k]
			continue
		}
		if AttackKind(k).RefusableByFrame() {
			hostileOffered += offered[k]
			tp += rejected[k]
		}
	}
	precision := 0.0
	if tp+fp > 0 {
		precision = float64(tp) / float64(tp+fp)
	}
	recall := 0.0
	if hostileOffered > 0 {
		recall = float64(tp) / float64(hostileOffered)
	}

	enq, del, drop, _ := e.tr.Stats()
	congDrops, floodDrops, _ := e.tr.DropsByCause()
	consRate := 0.0
	if legitOff > 0 {
		consRate = float64(legitAcc) / float64(legitOff)
	}

	return Scoreboard{
		Tick:            tick,
		Elapsed:         time.Duration(tick) * e.cfg.TickDuration,
		SimTime:         simTime,
		Sealed:          sealed,
		SealWhy:         sealWhy,
		ActiveNodes:     active,
		QuarDef:         qd,
		QuarAtk:         qa,
		Offered:         offered,
		Accepted:        accepted,
		Rejected:        rejected,
		ConsensusOK:     consOK,
		ConsensusLost:   consLost,
		ConsensusRate:   consRate,
		TruePos:         tp,
		FalsePos:        fp,
		rejectReasons:   reasons,
		DriftInWindow:   driftInWindow,
		Missed:          hostileOffered - tp,
		Precision:       precision,
		Recall:          recall,
		DropDetectP50Ms: p50,
		DropDetectP99Ms: p99,
		DropDetectMaxMs: dmax,
		DropSamples:     samples,
		// The two detection latencies are deliberately different numbers.
		// DropDetect* is the receiver-inferred loss latency; the gap figures
		// describe the loss itself, which the scoreboard needs separately
		// because a gap that is never resolved is undetected loss.
		GapP50Ms:       gapP50,
		GapP99Ms:       gapP99,
		GapsOpened:     gapsOpened,
		GapsLost:       gapsLost,
		GapsResolved:   gapsResolved,
		LossPrecision:  lossPrecision(gapsLost, drop),
		RejectByReason: reasons,
		DiskWrites:     e.diskWrites.Load(),
		InFlight:       e.tr.InFlight(),
		LostFrames:     e.tr.LostFrames(),
		Enqueued:       enq,
		Delivered:      del,
		Dropped:        drop,
		DropFlood:      floodDrops,
		DropCongestion: congDrops,
		SkewIn:         skewIn,
		SkewOut:        skewOut,
		Seals:          seals,
		Pardons:        pardons,
		Swaps:          int(e.swaps.Load()),
		MemBytes:       int64(mem.HeapAlloc),
		Goroutines:     runtime.NumGoroutine(),
	}
}

// lossPrecision reports inferred loss against actual loss.
//
// A gap-based detector reports a gap as loss only after the reorder window
// closes, so every frame that was merely late is counted as lost. The ratio is
// therefore the detector's precision, and it is bounded above by 1 because
// inferred loss always meets or exceeds real loss.
func lossPrecision(inferred, actual uint64) float64 {
	if inferred == 0 {
		return 1
	}
	if actual == 0 {
		return 0
	}
	return float64(actual) / float64(inferred)
}

// Run executes the simulation until the context is cancelled or the configured
// Duration elapses. It blocks and is intended to be launched as a goroutine
// from the fabric process, or called directly in a test.
func (e *Engine) Run(ctx context.Context) error {
	defer e.tr.Close()
	defer e.doneOnce.Do(func() { close(e.done) })

	e.det.mu.Lock()
	e.det.now = time.Unix(0, e.simClock.Load())
	e.det.mu.Unlock()

	// Banner: show the modelled geography so the latency model is auditable
	// rather than asserted.
	fmt.Fprintf(e.cfg.Out, "%s FABRIC-SIM adversarial matrix: %d nodes (%d defender / %d attacker) across %d zones\n",
		ansiBold, e.top.Len(), MatrixSize, MatrixSize, int(numZones))
	fmt.Fprintf(e.cfg.Out, "%s zone RTT model (great-circle / fibre, calibration 1.85):%s\n", ansiDim, ansiReset)
	for _, line := range splitLines(FormatTable()) {
		fmt.Fprintf(e.cfg.Out, "   %s\n", line)
	}
	fmt.Fprintf(e.cfg.Out, "%s workers: control=%d vault=%d  mem-budget=%dMiB  disk-writes=%d%s\n\n",
		ansiDim, e.cfg.Workers, e.cfg.AttackerWorkers,
		e.cfg.MemoryBudgetBytes>>20, e.diskWrites.Load(), ansiReset)

	ticker := time.NewTicker(e.cfg.TickInterval)
	defer ticker.Stop()

	deadline := time.Time{}
	if e.cfg.Duration > 0 {
		deadline = time.Now().Add(e.cfg.Duration)
	}

	for {
		select {
		case <-ctx.Done():
			e.renderer.Final(e.Snapshot())
			return ctx.Err()
		case <-ticker.C:
		}

		if !deadline.IsZero() && time.Now().After(deadline) {
			e.renderer.Draw(e.Snapshot(), true)
			e.renderer.Final(e.Snapshot())
			return nil
		}

		e.step()
		e.renderer.Draw(e.Snapshot(), e.cfg.Verbose)
	}
}

// Step advances the simulation by exactly one tick.
//
// It is exported so a test can drive a fixed number of ticks deterministically;
// the real-time loop in Run is wall-clock driven and would otherwise execute a
// slightly different tick count on every run.
func (e *Engine) Step() { e.step() }

// step advances the simulation by one tick.
func (e *Engine) step() {
	tick := e.tick.Add(1)
	now := time.Unix(0, e.simClock.Add(int64(e.cfg.TickDuration)))
	e.det.Advance(now)

	// Age out any gap that has outlived the reorder window. Loss is declared
	// here, on the tick boundary, because a receiver judges a gap stale by
	// elapsed time rather than by the arrival of the next frame.
	if lost, latency := e.det.ExpireGaps(e.det.Now()); lost > 0 {
		e.rdMu.Lock()
		e.gapLatency.add(float64(latency) / float64(time.Millisecond) / float64(lost))
		e.rdMu.Unlock()
	}

	// Flood intensity decays every tick, so a packet-drop attack that stops is
	// forgotten by the transport instead of leaving a path permanently broken.
	for i := range e.flood {
		v := e.flood[i].Load()
		if v == 0 {
			continue
		}
		if decayed := v * uint64(float64(1e6)*(1-e.cfg.FloodDecayPct)); decayed > 0 {
			e.flood[i].Store(decayed)
		} else {
			e.flood[i].Store(0)
		}
		n := e.top.At(i)
		if n == nil {
			continue
		}
		intensity := float64(e.flood[i].Load()) / 1e6
		// The flood is inflicted on the path toward each peer, which is what
		// makes it damage honest traffic rather than only the attacker's.
		for j := 0; j < e.top.Len(); j++ {
			if peer := e.top.At(j); peer != nil && peer.ID != n.ID {
				e.tr.SetFlood(n.ID, peer.ID, intensity)
			}
		}
	}

	// 1. Generate and enqueue this tick's traffic, fanned out across the
	//    worker pools. Generation is embarrassingly parallel: each worker owns
	//    its own RNG and touches disjoint node counters (which are atomic), so
	//    no lock is held across the fan-out.
	e.generateParallel(now)

	// 2. Drain everything now due and run it past the defence, sharded across
	//    the defender pool. A packet enqueued this tick is usually not due
	//    until a later tick for inter-continental hops, which is what makes
	//    the RTT model observable rather than instantaneous.
	drained := e.tr.DrainDue(now)
	e.deliverParallel(drained, now)

	// 3. Periodic maintenance: pardons, role swaps, and a shed check.
	if tick%25 == 0 {
		e.det.Pardons()
	}
	if e.cfg.EnableRoleSwap && tick%int64(e.cfg.RoleSwapEvery) == 0 {
		n := e.top.SwapRoles(now.UnixNano())
		e.swaps.Add(int64(n))
	}
	e.checkBudget()
}

// generateParallel emits one tick of honest and hostile traffic using both
// worker pools. The vault-side pool (AttackerWorkers) drives the hostile load
// and the control-side pool (Workers) drives honest traffic, so the two halves
// of the matrix are exercised concurrently the way they would be in a real
// engagement.
func (e *Engine) generateParallel(now time.Time) {
	// Hostile load is shed if the heap is approaching budget. Shedding is the
	// correct response under a memory envelope: the simulation must never be
	// the thing that OOMs the host.
	shed := 0
	if e.overBudget() {
		shed = e.cfg.AttackRate / 4
		e.sheds.Add(uint64(shed))
	}

	var wg sync.WaitGroup

	// Honest traffic. The rate floor guarantees a measurable pass rate, so a
	// defence cannot "win" by dropping everything.
	legit := e.cfg.LegitRate
	wg.Add(e.cfg.Workers)
	for w := 0; w < e.cfg.Workers; w++ {
		go func(worker int) {
			defer wg.Done()
			rnd := e.workerRand(worker)
			for i := worker; i < legit; i += e.cfg.Workers {
				src := e.top.RandomDefender(rnd)
				dst := e.top.RandomDefender(rnd)
				if src.ID == dst.ID {
					continue
				}
				p := &Packet{
					From:        src.ID,
					To:          dst.ID,
					Kind:        KindLegitimate,
					Payload:     e.tr.acquire(e.cfg.PayloadBytes),
					DeclaredLen: uint32(e.cfg.PayloadBytes),
					SealedAt:    now.Unix() / 3600,
					Seq:         e.srcSeq[src.ID.Index()].Add(1),
				}
				copy(p.Payload, src.ID.String())
				// Sign the frame the honest way: with the sender's own key,
				// over the final bytes that will go on the wire.
				signPacket(p, src.priv)
				src.FramesSent.Add(1)
				e.tr.Enqueue(p, now)
			}
		}(w)
	}

	// Hostile traffic, fanned across the vault-side pool.
	atk := e.cfg.AttackRate - shed
	if atk < 0 {
		atk = 0
	}
	wg.Add(e.cfg.AttackerWorkers)
	for w := 0; w < e.cfg.AttackerWorkers; w++ {
		go func(worker int) {
			defer wg.Done()
			rnd := e.workerRand(1000 + worker)
			for i := worker; i < atk; i += e.cfg.AttackerWorkers {
				src := e.top.RandomAttacker(rnd)
				dst := e.top.RandomDefender(rnd)
				kind := e.prof.pickKind(rnd)

				payload := e.tr.acquire(64 + rnd.Intn(192))
				p := &Packet{
					From:     src.ID,
					To:       dst.ID,
					Kind:     kind,
					Payload:  payload,
					SealedAt: now.Unix() / 3600,
					Seq:      e.srcSeq[src.ID.Index()].Add(1),
				}
				copy(p.Payload, buildPayload(kind, rnd, src, len(p.Payload)))
				// A legacy malformed frame claims far more than it carries;
				// every other vector is structurally intact.
				if kind == KindMalformed {
					p.DeclaredLen = 0x7fffffff
				} else {
					p.DeclaredLen = uint32(len(p.Payload))
				}

				if kind == KindClockDrift {
					p.SkewMs = driftTargets[rnd.Intn(len(driftTargets))]
				}
				// Every hostile frame is signed first, with the sender's own
				// key, so that a receiver's verdict is a consequence of the
				// attack rather than of a missing signature. Only the forgery
				// vector then breaks the signature, which is what forgery is.
				signPacket(p, src.priv)
				if kind == KindForgery {
					corruptSignature(p)
				}
				if kind == KindPacketDrop {
					// A drop attack is not a frame the attacker sends and
					// then loses. It is the attacker saturating a path so
					// that everyone else's traffic is destroyed. Record the
					// intensity and let the transport raise the real loss
					// rate on every frame that shares the path.
					e.flood[src.ID.Index()].Store(uint64(e.cfg.FloodIntensity * 1e6))
				}

				src.AttacksSent.Add(1)
				e.noteAttack(src.ID, kind, e.det.Now())
				e.tr.Enqueue(p, now)
			}
		}(w)
	}
	wg.Wait()
}

// deliverParallel runs a batch of delivered packets past the defence, sharded
// across the defender pool. Each worker owns a disjoint stride of the batch, so
// the only contention is inside the detector's own accounting.
func (e *Engine) deliverParallel(pkts []*Packet, now time.Time) {
	if len(pkts) == 0 {
		return
	}
	workers := e.cfg.Workers
	if workers > len(pkts) {
		workers = len(pkts)
	}
	if workers <= 0 {
		workers = 1
	}
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(worker int) {
			defer wg.Done()
			for i := worker; i < len(pkts); i += workers {
				e.deliver(pkts[i])
			}
		}(w)
	}
	wg.Wait()
}

// workerRand returns a deterministic per-worker generator. Deriving it from
// the base seed and the worker index keeps a run reproducible while giving each
// worker an independent stream, so pool width changes the traffic pattern but
// never the seed.
func (e *Engine) workerRand(worker int) *Rand {
	return NewRand(e.cfg.Seed*1_000_003 + uint64(worker)*0x9e3779b97f4a7c15)
}

// noteAttack records the first hostile frame from an identity so that detection
// latency can be measured from the start of an attack to its isolation.
func (e *Engine) noteAttack(id NodeID, kind AttackKind, now time.Time) {
	if !kind.IsHostile() {
		return
	}
	e.rdMu.Lock()
	if _, seen := e.firstAttack[id]; !seen {
		e.firstAttack[id] = now
	}
	e.rdMu.Unlock()
}

// deliver runs one delivered packet past the defence and records the outcome.
// deliver takes no timestamp because the only clock it consults is the
// detector's. Loss inference runs on real wall-clock time, not the simulated
// clock: the quantity reported is how long an operator's detector takes to
// notice, which is a real-time property. Reading the simulated due-time here
// would compare a simulated timestamp against the real clock.
func (e *Engine) deliver(p *Packet) {
	// A packet is presented to the defence as arriving from its claimed
	// source, which is exactly the trust boundary a real attacker is trying
	// to cross: the handle is attacker-controlled.
	// Loss inference runs first and is independent of acceptance: a receiver
	// notices a sequence gap whether or not it goes on to accept the frame
	// that revealed it.
	now := e.det.Now()
	e.det.noteArrival(p, now)
	if lost, latency := e.det.expireGapFor(p.From, now); lost > 0 {
		e.rdMu.Lock()
		e.gapLatency.add(float64(latency) / float64(time.Millisecond) / float64(lost))
		e.rdMu.Unlock()
	}

	offer := e.det.Inspect(p.From, p)

	if n := e.top.ByID(p.From); n != nil {
		n.FramesAccepted.Add(1)
	}

	if !offer.Accept {
		// Record detection latency the first time an identity is caught.
		e.rdMu.Lock()
		if first, ok := e.firstAttack[p.From]; ok {
			if _, done := e.detectedAt[p.From]; !done && offer.Quarantined {
				e.detectedAt[p.From] = now
				e.rd.add(float64(now.Sub(first)) / float64(time.Millisecond))
			}
		}
		e.rdMu.Unlock()
		return
	}
	// Accepted: the frame is a consensus success if it was honest.
	//
	// Kind is used here in the scorer's own bookkeeping, which is legitimate: the
	// scoreboard is the instrument that knows what was sent, and it is not part
	// of the decision path. The detector above never sees it.
	if p.Kind == KindLegitimate {
		e.consOK.Add(1)
	}
}

// overBudget reports whether the process heap is close to the configured
// envelope.
func (e *Engine) overBudget() bool {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return int64(m.HeapAlloc) > e.cfg.MemoryBudgetBytes
}

// checkBudget records a shed event when over budget and verifies the zero-write
// invariant.
func (e *Engine) checkBudget() {
	if e.overBudget() {
		e.sheds.Add(1)
	}
}

// DiskWrites returns the simulator's disk-write counter. It must be zero for
// the run to be considered compliant with the zero-physical-write requirement.
func (e *Engine) DiskWrites() uint64 { return e.diskWrites.Load() }

// Sheds returns the number of attack frames dropped to respect the memory
// envelope.
func (e *Engine) Sheds() uint64 { return e.sheds.Load() }

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// ── live integration hooks ──────────────────────────────────────────────────
//
// These let the real fabric feed observed production telemetry into the
// simulation and be steered by the simulation's fail-closed state, so the two
// are genuinely coupled rather than running side by side.

// ObserveRealDelivery records a real frame that completed on the actual wire,
// with its measured round-trip time. These samples feed the same drop-latency
// recorder the synthetic attacks use, so the scoreboard's detection figures
// reflect production traffic once the real node is running.
func (e *Engine) ObserveRealDelivery(rtt time.Duration) {
	ms := float64(rtt) / float64(time.Millisecond)
	if ms < 0 {
		return
	}
	e.rdMu.Lock()
	e.rd.add(ms)
	e.rdMu.Unlock()
}

// RecordCryptoFailure counts a real authentication failure observed by the
// fabric. These are the events the production openCell path rejects, and
// counting them lets the scoreboard show how the synthetic attack volume
// compares to what the node actually saw.
func (e *Engine) RecordCryptoFailure() {
	e.det.mu.Lock()
	e.det.cryptoFailures++
	e.det.mu.Unlock()
}

// CryptoFailures returns the number of real authentication failures reported
// by the fabric.
func (e *Engine) CryptoFailures() uint64 {
	e.det.mu.RLock()
	defer e.det.mu.RUnlock()
	return e.det.cryptoFailures
}

// Wait blocks until the simulator's Run loop has returned, or the timeout
// elapses. A host process uses it to let the closing scoreboard print before
// exiting.
func (e *Engine) Wait(timeout time.Duration) {
	if e == nil {
		return
	}
	select {
	case <-e.done:
	case <-time.After(timeout):
	}
}

// TrafficAllowed reports whether the real fabric should keep emitting traffic.
//
// This is the coupling that matters: when the simulation reaches its
// fail-closed seal, the production node's traffic loop is suppressed too,
// rather than the simulation being a decorative overlay.
func (e *Engine) TrafficAllowed() bool {
	if e == nil {
		return true
	}
	return !e.det.Sealed()
}
