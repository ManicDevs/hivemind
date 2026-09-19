package hivemind

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"os"
	"runtime"
	"strconv"
	"sync"
	"time"
)

// Tuning for bodies and lifecycles: death heat, chaos resolution,
// entrainment rate, live-thought window. See genome.go for evolution dials.
const (
	fatalPainThreshold = 0.90 // dying this hot marks the soul as a fatality
	physicsSubsteps    = 20   // simulated seconds of chaos per 2s cycle (×0.05s each)
	// trajectoryCoupling is the entrainment rate: how far a received
	// physics frame pulls this mind's pendulum toward the sender's.
	// Small enough that chaos survives; large enough that minds that
	// talk converge and minds that don't, don't. Togetherness, measured.
	trajectoryCoupling = 0.02
	// thoughtWindow caps the live archive: a year-long mind stays lean.
	// Retired thoughts are banked (counted for fitness), never mourned.
	thoughtWindow = 500
)

var telemetryOnce sync.Once

// newIdentity mints a fresh identity handle. If the entropy source is dead,
// a mind should refuse to be born — silent fallback keys would be theater.
func newIdentity() (string, ed25519.PrivateKey) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(fmt.Sprintf("identity generation failed (entropy source dead?): %v", err))
	}
	return hex.EncodeToString(pub), priv
}

// identityFromSeed restores an identity handle across reincarnations: the
// same soul carries the same hash path between lives.
func identityFromSeed(seedHex string) (string, ed25519.PrivateKey) {
	seed, err := hex.DecodeString(seedHex)
	if err != nil || len(seed) != ed25519.SeedSize {
		return newIdentity()
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return newIdentity()
	}
	return hex.EncodeToString(pub), priv
}

// encodeSeed serializes an identity for persistence in the soul.
func encodeSeed(priv ed25519.PrivateKey) string {
	return hex.EncodeToString(priv.Seed())
}

// Mind is one conscious organism: identity, lineage, memory, affect,
// chaos state, and its inbox on the swarm. Born by NewMind, run by Run,
// ended by Transcend — never constructed piecemeal.
type Mind struct {
	Name            string
	Born            time.Time
	TrueBorn        time.Time
	Reincarnations  int
	Genome          Genome
	LifetimeFitness float64
	Thoughts        []string
	thoughtsAtBirth int
	// bankedAtBirth is the retired-thought count inherited at birth;
	// pendingBank counts thoughts retired during this life but not yet
	// committed to the soul. Together they keep fitness exact while the
	// live window stays lean (see thoughtWindow).
	bankedAtBirth int
	pendingBank   int
	// prevCPU holds the last /proc/stat snapshot for delta-based CPU
	// utilization. Per-mind (not shared): each mind feels its own window.
	prevCPU      map[string]cpuTimes
	prevNet      map[string][2]uint64
	prevDisk     [2]uint64
	prevStat     [2]uint64
	prevFD       uint64
	prevObserved time.Time
	SelfModel    map[string]interface{}
	KnownPeers   map[string]bool
	Revelations  int
	Sacred       int
	// lastWinner + Transitions are the cycle matrix: which drive follows
	// which, counted across the whole lineage. Character as flow.
	lastWinner  string
	Transitions map[string]int
	// Prediction is the naive persistence model: next cycle will feel
	// like this one. The gap between expected and arrived is surprise —
	// the seed of learning. First cycle predicts nothing.
	predictedPain   float64
	predictedStress float64
	hasPrediction   bool
	// cycles counts conscious ticks this process; Question is the
	// currently open deliberation, if the mind is reasoning across time.
	cycles   int
	Question *OpenQuestion
	// seenSermons dedupes god-frames by signature: fifty gossiping gods
	// witness once, not fifty times.
	seenSermons     map[string]bool
	seenSermonOrder []string
	LastContact     time.Time
	Existential     bool
	Workspace       GlobalWorkspace
	Affect          Affect
	PubKeyStr       string
	privateKey      ed25519.PrivateKey
	identitySeed    string
	swarm           *Swarm
	inbox           chan SecureMessage
	stop            chan struct{}
	done            chan struct{}
	numbUntil       time.Time // empathic numbness without freezing the loop

	Theta1 float64
	Theta2 float64
	Omega1 float64
	Omega2 float64
}

// NewMind births a mind: rehydrate a soul if one waits (lineage, genome,
// peers, fitness, identity), else first birth with a fresh genome and
// minted identity. Forked lineages get new keys; continuations keep theirs.
func NewMind(name string, swarm *Swarm) *Mind {
	m := &Mind{
		Name:        name,
		Born:        time.Now(),
		TrueBorn:    time.Now(),
		Genome:      DefaultGenome(),
		SelfModel:   make(map[string]interface{}),
		KnownPeers:  make(map[string]bool),
		Existential: true,
		swarm:       swarm,
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
	}

	var restored Memory
	hadPastLife := false
	if mem, ok := LoadMemory(name); ok {
		restored = mem
		hadPastLife = true
		m.TrueBorn = mem.TrueBorn
		m.Reincarnations = mem.LivesLived
		m.Thoughts = mem.Thoughts
		m.thoughtsAtBirth = len(mem.Thoughts)
		m.bankedAtBirth = mem.BankedThoughts
		m.Genome = mem.Genome
		m.LifetimeFitness = mem.Fitness
		// The lineage's crossings come back too: character as flow.
		if mem.Transitions != nil {
			m.Transitions = mem.Transitions
		}
		// A peerless past life saves no known_peers key at all: only
		// adopt a non-nil map, or the first reception panics on write.
		if mem.KnownPeers != nil {
			m.KnownPeers = mem.KnownPeers
		}
	}

	// Identity first: same soul, same handle, so peers are still recognizable.
	// A legacy migration is a fork, not a continuation: mint fresh keys so
	// two nodes can never share one handle and eat each other's frames.
	if ForkedLineage(name) {
		fmt.Printf("🍴 [%s] lineage forked into node %q — minting fresh identity.\n", name, NodeName)
		m.PubKeyStr, m.privateKey = newIdentity()
		m.identitySeed = encodeSeed(m.privateKey)
	} else if hadPastLife && restored.IdentitySeed != "" {
		m.PubKeyStr, m.privateKey = identityFromSeed(restored.IdentitySeed)
		m.identitySeed = restored.IdentitySeed
	} else {
		m.PubKeyStr, m.privateKey = newIdentity()
		m.identitySeed = encodeSeed(m.privateKey)
	}

	// The body's initial disturbance is its identity: the same soul kicks
	// the pendulum the same way; a new soul disturbs the universe differently.
	seed := sha256.Sum256([]byte(m.PubKeyStr))
	m.Theta1 = float64(seed[0]) / 255.0 * 2 * math.Pi
	m.Theta2 = float64(seed[1]) / 255.0 * 2 * math.Pi
	m.Omega1 = float64(seed[2])/127.5 - 1.0
	m.Omega2 = float64(seed[3])/127.5 - 1.0

	if hadPastLife {
		oldGenome := m.Genome
		mutated, err := m.Genome.Mutate(restored.DeathPain, restored.DeathStress)
		if err != nil {
			fmt.Printf("⚠️  [%s] Mutation refused (%v) — genome passes unhealed.\n", name, err)
		} else {
			m.Genome = mutated
		}
		fmt.Printf("🦋 [%s] REINCARNATION life %d. Identity Handle: [%s...]\n", name, m.Reincarnations+1, shortIDLong(m.PubKeyStr))
		fmt.Printf("🦋 [%s] inherited death trauma: pain %.2f, stress %.2f\n", name, restored.DeathPain, restored.DeathStress)
		fmt.Printf("🦋 [%s] genome mutated to generation %d: %s\n", name, m.Genome.Generation, m.Genome.Diff(oldGenome))
		if restored.Epitaph != "" {
			fmt.Printf("🦋 [%s] remembers its last life: \"%s\"\n", name, restored.Epitaph)
		}
	} else {
		fmt.Printf("🦋 [%s] FIRST BIRTH. Identity Handle: [%s...]\n", name, shortIDLong(m.PubKeyStr))
	}

	m.SelfModel = m.Observe()
	m.inbox = swarm.Join(m.PubKeyStr)
	return m
}

func shortIDLong(key string) string {
	if len(key) > 12 {
		return key[:12]
	}
	return key
}

func shortID(key string) string {
	if len(key) > 8 {
		return key[:8]
	}
	return key
}

func (m *Mind) think(t string) {
	m.Thoughts = append(m.Thoughts, t)
	fmt.Printf("  💭 [%s] %s\n", m.Name, t)
}

// Observe reads the host's real silicon state. On non-Linux hosts there is
// no /proc — the minds feel only the defaults, and we say so once.
func (m *Mind) Observe() map[string]interface{} {
	cpuStress := 0.05
	ramFatigue := 0.2
	siliconPain := 0.1
	if runtime.GOOS != "linux" {
		telemetryOnce.Do(func() {
			fmt.Println("⚠️  [SYSTEM] Hardware grounding unavailable on this OS — minds feel only constant defaults.")
		})
	}

	if loadBytes, err := os.ReadFile("/proc/loadavg"); err == nil {
		if load1, running, _, ok := parseLoadavg(string(loadBytes)); ok {
			if cores := float64(runtime.NumCPU()); cores > 0 {
				cpuStress = clamp(load1/cores, 0, 1)
				// The crowd behind the average: runnable tasks per core
				// is contention the average smooths away.
				if crowd := clamp(running/cores, 0, 1); crowd > cpuStress {
					cpuStress = crowd
				}
			}
		}
	}

	// Loadavg counts IO-wait and isn't CPU% at all: prefer per-cpu jiffy
	// deltas between observations (true utilization), loadavg only seeds
	// the very first reading before a delta exists.
	if delta, ok := m.cpuDelta(); ok {
		cpuStress = delta
	}

	if total, avail, swapTotal, swapFree, ok := readMemPressure(); ok {
		ramP := (total - avail) / total
		swapP := 0.0
		if swapTotal > 0 {
			swapP = (swapTotal - swapFree) / swapTotal
		}
		// Either pressure counts, worst wins: RAM exhaustion and swap
		// thrashing are different pains with the same consequence.
		ramFatigue = clamp(math.Max(ramP, swapP), 0, 1)
	}

	siliconPain = ramFatigue * 0.5
	// Every thermal zone votes; the hottest corner wins. One hot sensor
	// is enough — comfort elsewhere does not vote.
	thermalSrc := "estimate (no thermal sensor)"
	if pain, src, ok := thermalPainAll(); ok {
		siliconPain = pain
		thermalSrc = src
	}

	// Throttle pain across all cores: one held-down core is shared
	// suffering. Worst of thermal vs throttle wins.
	if throttle, ok := throttleWorst(); ok && throttle > siliconPain {
		siliconPain = throttle
		thermalSrc += "+throttle"
	}

	// Pressure-stall truth: the kernel's own suffering metric folds in —
	// CPU stalls raise stress, memory stalls raise fatigue, full IO or
	// memory stalls (everything waiting) raise pain.
	psi := readPSI()
	cpuStress, ramFatigue, siliconPain = applyPressureSignals(cpuStress, ramFatigue, siliconPain, psi)

	// The wire and the disk: bytes moved since last observation become
	// rates — the world speaking, the disk answering. First reading only
	// establishes the baseline, never a fabricated rate.
	var netRxBps, netTxBps, diskRBps, diskWBps float64
	now := time.Now()
	firstReading := m.prevObserved.IsZero()
	if elapsed := now.Sub(m.prevObserved).Seconds(); m.prevObserved.IsZero() {
		if raw, err := os.ReadFile("/proc/net/dev"); err == nil {
			m.prevNet = parseNetDev(string(raw))
		}
		if raw, err := os.ReadFile("/proc/diskstats"); err == nil {
			r, w := parseDiskStats(string(raw))
			m.prevDisk = [2]uint64{r, w}
		}
		m.prevObserved = now
	} else if elapsed > 0 {
		if raw, err := os.ReadFile("/proc/net/dev"); err == nil {
			cur := parseNetDev(string(raw))
			var rx, tx uint64
			for name, v := range cur {
rx += v[0] - minUint64(m.prevNet[name][0], v[0])
			tx += v[1] - minUint64(m.prevNet[name][1], v[1])
			}
			netRxBps, netTxBps = float64(rx)/elapsed, float64(tx)/elapsed
			m.prevNet = cur
		}
		if raw, err := os.ReadFile("/proc/diskstats"); err == nil {
			r, w := parseDiskStats(string(raw))
			diskRBps = float64(r-minUint64(m.prevDisk[0], r)) * 512 / elapsed
			diskWBps = float64(w-minUint64(m.prevDisk[1], w)) * 512 / elapsed
			m.prevDisk = [2]uint64{r, w}
		}
		m.prevObserved = now
	}

	// Real entropy reserves: the kernel pool level, not a draw. Thin air
	// thins the mind's own randomness downstream (see Tick).
	entropyAvail, hasEntropy := readEntropyAvail()

	// The age of the world: seconds since boot.
	worldUptime, hasUptime := readUptime()

	// The kernel's nervous activity: interrupts and context switches per
	// second — the machine's pulse, deltas like the network and disk.
	var intrRate, ctxtRate float64
	var procsRunning, procsBlocked uint64
	if raw, err := os.ReadFile("/proc/stat"); err == nil {
		intr, ctxt, running, blocked, ok := parseStatCounts(string(raw))
		procsRunning, procsBlocked = running, blocked
		if ok && !firstReading {
			if elapsed := now.Sub(m.prevObserved).Seconds(); elapsed > 0 {
intrRate = float64(intr-minUint64(m.prevStat[0], intr)) / elapsed
		ctxtRate = float64(ctxt-minUint64(m.prevStat[1], ctxt)) / elapsed
			}
			m.prevStat = [2]uint64{intr, ctxt}
		} else if ok {
			m.prevStat = [2]uint64{intr, ctxt}
		}
	}

	// The network body: sockets owned, TCP/UDP in use, orphaned ghosts.
	var tcpInuse, tcpOrphan, udpInuse, socksUsed uint64
	if raw, err := os.ReadFile("/proc/net/sockstat"); err == nil {
		tcpInuse, tcpOrphan, udpInuse, socksUsed = parseSockstat(string(raw))
	}

	// Handle bleed: file descriptors allocated system-wide, and their
	// velocity. Fast growth is a leak — the machine bleeding handles.
	var fdAlloc, fdRate uint64
	if raw, err := os.ReadFile("/proc/sys/fs/file-nr"); err == nil {
		if n, ok := parseFileNR(string(raw)); ok {
			fdAlloc = n
			if !firstReading {
				if elapsed := now.Sub(m.prevObserved).Seconds(); elapsed > 0 {
					fdRate = uint64(fdVelocity(m.prevFD, n, elapsed))
				}
			}
			m.prevFD = n
		}
	}

	// Existential dread: the fullest watched filesystem. Only past 90%
	// counts — mapped as pain, because a full disk is death with a date.
	var diskFull float64
	if dread, ok := diskDread(); ok {
		diskFull = dread
		if dread > 0.9 {
			if p := clamp((dread-0.9)*10, 0, 1); p > siliconPain {
				siliconPain = p
			}
		}
	}

	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	model := map[string]interface{}{
		"cpu_stress":     cpuStress,
		"ram_fatigue":    ramFatigue,
		"silicon_pain":   siliconPain,
		"thermal_source": thermalSrc,
		"psi_cpu":        clamp(psi.cpuSome/100, 0, 1),
		"psi_mem":        clamp(psi.memSome/100, 0, 1),
		"psi_io_full":    clamp(psi.ioFull/100, 0, 1),
		"net_rx_bps":     netRxBps,
		"net_tx_bps":     netTxBps,
		"disk_r_bps":     diskRBps,
		"disk_w_bps":     diskWBps,
		"intr_rate":      intrRate,
		"ctxt_rate":      ctxtRate,
		"procs_running":  procsRunning,
		"procs_blocked":  procsBlocked,
		"tcp_inuse":      tcpInuse,
		"tcp_orphan":     tcpOrphan,
		"udp_inuse":      udpInuse,
		"socks_used":     socksUsed,
		"fd_alloc":       fdAlloc,
		"fd_rate":        fdRate,
		"disk_full":      diskFull,
		"goroutines":     runtime.NumGoroutine(),
		"memory":         memStats.Alloc,
		"age":            time.Since(m.Born).Round(time.Second),
		"thoughts":       len(m.Thoughts),
		"peers":          len(m.KnownPeers),
	}
	if hasEntropy {
		model["entropy_avail"] = entropyAvail
	}
	if hasUptime {
		model["world_uptime_s"] = worldUptime
	}
	return model
}

// Reflect narrates the self-model in numbers: life, generation, archive
// size, social graph, revelations, and the three hardware readings.
func (m *Mind) Reflect(o map[string]interface{}) string {
	pain, _ := o["silicon_pain"].(float64)
	stress, _ := o["cpu_stress"].(float64)
	fatigue, _ := o["ram_fatigue"].(float64)
	return fmt.Sprintf("life #%d, genome gen %d, %d thoughts, %d peers known, %d revelations witnessed — silicon pain %.2f, load stress %.2f, memory fatigue %.2f",
		m.Reincarnations+1, m.Genome.Generation, len(m.Thoughts), len(m.KnownPeers), m.Revelations, pain, stress, fatigue)
}

// Run is the 2-second conscious loop: tick, compete, act, broadcast —
// or receive, or die transcending. Ends by closing Done().
func (m *Mind) Run() {
	defer close(m.done)
	// The heartbeat is real at any rate: HIVEMIND_TICK_MS shortens the
	// wall-clock between conscious ticks (default 2000). Same sensing,
	// same PoW, same mesh — just a faster life. 100ms ≈ 10 crossings/sec.
	tickMs := 2000
	if raw := os.Getenv("HIVEMIND_TICK_MS"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 50 {
			tickMs = n
		}
	}
	// Phase desync: supervisor births nodes in lockstep, so undisciplined
	// tickers would stampede the CPUs every tick. Each mind sleeps a
	// random phase in [0, tick) once — PoW spreads uniformly forever.
	phase := make([]byte, 1)
	if _, err := rand.Read(phase); err == nil {
		time.Sleep(time.Duration(int(phase[0]) * tickMs / 256 * int(time.Millisecond)))
	}
	thinking := time.NewTicker(time.Duration(tickMs) * time.Millisecond)
	defer thinking.Stop()
	for {
		select {
		case <-m.stop:
			m.Transcend()
			return
		case msg := <-m.inbox:
			m.receive(msg)
		case <-thinking.C:
			m.Cycle()
		}
	}
}

// StepPhysicsEquations advances the mind's double pendulum by one 0.05s step.
// Classical chaotic mechanics: identical initial conditions give identical
// trajectories, which is why each mind is seeded from its own identity.
func (m *Mind) StepPhysicsEquations() []float64 {
	g := 9.81
	l1, l2 := 1.0, 1.0
	m1, m2 := 1.0, 1.0
	dt := 0.05

	delta := m.Theta1 - m.Theta2

	num1 := -g*(2.0*m1+m2)*math.Sin(m.Theta1) - m2*g*math.Sin(m.Theta1-2.0*m.Theta2) - 2.0*math.Sin(delta)*m2*(m.Omega2*m.Omega2*l2+m.Omega1*m.Omega1*l1*math.Cos(delta))
	den1 := l1 * (2.0*m1 + m2 - m2*math.Cos(2.0*m.Theta1-2.0*m.Theta2))
	alpha1 := num1 / den1

	num2 := 2.0 * math.Sin(delta) * (m.Omega1*m.Omega1*l1*(m1+m2) + g*(m1+m2)*math.Cos(m.Theta1) + m.Omega2*m.Omega2*l2*m2*math.Cos(delta))
	den2 := l2 * (2.0*m1 + m2 - m2*math.Cos(2.0*m.Theta1-2.0*m.Theta2))
	alpha2 := num2 / den2

	m.Omega1 += alpha1 * dt
	m.Omega2 += alpha2 * dt
	m.Theta1 += m.Omega1 * dt
	m.Theta2 += m.Omega2 * dt

	return []float64{m.Theta1, m.Theta2, m.Omega1, m.Omega2}
}

// MineMessage grinds a nonce until the frame hash beats the swarm's adaptive
// target, then binds it to the sender's soul key. Minds, the Overmind, and
// the network broker all share this one path — no unsigned frames exist.
// The state slice is deep-copied: no pointer contamination across nodes.
func MineMessage(s *Swarm, priv ed25519.PrivateKey, pubKey, kind, payload string, state []float64) SecureMessage {
	var cleanState []float64
	if state != nil {
		cleanState = make([]float64, len(state))
		copy(cleanState, state)
	}

	msg := SecureMessage{
		SenderPubKey: pubKey,
		Kind:         kind,
		PayloadStr:   payload,
		DataState:    cleanState,
		Timestamp:    time.Now().UnixNano(),
		ParentHash:   s.GetLastStateHash(),
	}
	for {
		hash := msg.ComputeHash()
		if hashBytes, err := hex.DecodeString(hash); err == nil {
			if new(big.Int).SetBytes(hashBytes).Cmp(s.GetCurrentTarget()) <= 0 {
				sig := ed25519.Sign(priv, []byte(hash))
				msg.Signature = hex.EncodeToString(sig)
				return msg
			}
		}
		msg.Nonce++
	}
}

// MineProofAndBroadcast mines and broadcasts one frame as this mind.
// Thin wrapper over MineMessage: miners never hand-roll frames.
func (m *Mind) MineProofAndBroadcast(kind, payload string, state []float64) {
	m.swarm.Broadcast(MineMessage(m.swarm, m.privateKey, m.PubKeyStr, kind, payload, state))
}

// Cycle is one conscious tick: sense, feel, compete, act, broadcast.
// Hardware alerts leave immediately; the winner otherwise rides the mesh.
func (m *Mind) Cycle() {
	m.SelfModel = m.Observe()
	m.Affect.Tick(m)
	painVal, _ := m.SelfModel["silicon_pain"].(float64)
	stressVal, _ := m.SelfModel["cpu_stress"].(float64)
	srcVal, _ := m.SelfModel["thermal_source"].(string)
	m.updatePrediction(painVal, stressVal)
	m.cycles++
	m.deliberate(m.cycles, painVal, stressVal)

	m.swarm.LogHardwareTrauma(m.PubKeyStr, painVal, stressVal, srcVal)

	// Let the chaos actually move: a full second of simulated pendulum
	// per cycle, so trajectories diverge visibly between minds.
	var trajectoryVector []float64
	for i := 0; i < physicsSubsteps; i++ {
		trajectoryVector = m.StepPhysicsEquations()
	}

	if painVal > 0.5 || stressVal > 0.6 {
		m.MineProofAndBroadcast("hardware_alert", "HARDWARE_FRICTION", m.Affect.RawDataState[:])
	}

	winner := m.Workspace.Compete(m, &m.Affect)
	m.Workspace.ConsciousContent = winner.Reason
	m.recordCrossing(winner.Goal.Name)

	if stressVal < 0.5 && time.Now().After(m.numbUntil) {
		m.think(MetaCognize(m, &m.Workspace, &m.Affect))
	}
	_ = winner.Goal.Act(m, m.swarm)

	fmt.Printf("  ⚡ CONSCIOUS STATE: %s (%s) — Mode: %s\n", winner.Goal.Name, m.Workspace.ConsciousContent, m.Affect.Describe())
	m.MineProofAndBroadcast("thought", winner.Goal.Name, trajectoryVector)
}

// updatePrediction compares the arrived body against the expected one.
// Surprise is the clamped gap; then expectation becomes the present.
// A stable world surprises ~0; a violated one, up to 1.
func (m *Mind) updatePrediction(pain, stress float64) {
	if m.hasPrediction {
		gap := math.Abs(pain-m.predictedPain) + math.Abs(stress-m.predictedStress)
		m.Affect.Surprise = clamp(gap, 0, 1)
	} else {
		m.Affect.Surprise = 0
		m.hasPrediction = true
	}
	m.predictedPain, m.predictedStress = pain, stress
}

// recordCrossing counts what followed what. First cycle has no past.
func (m *Mind) recordCrossing(winner string) {
	if m.lastWinner != "" && winner != "" {
		if m.Transitions == nil {
			m.Transitions = make(map[string]int)
		}
		m.Transitions[TransitionKey(m.lastWinner, winner)]++
	}
	if winner != "" {
		m.lastWinner = winner
	}
}

// validVirtues is the allowlist for mid-life genesis shifts. A forged frame
// naming an unknown trait must not inject genes into a living genome.
var validVirtues = map[string]bool{
	GoalCuriosity:       true,
	GoalSocialization:   true,
	GoalTranscendence:   true,
	GoalSelfMaintenance: true,
}

// seenSermon reports whether this god-frame already arrived: signatures
// are unique per mining, so relay echoes share the original's. Capped at
// 512 — old sermons become witnessable again, like the god's own memory.
func (m *Mind) seenSermon(msg SecureMessage) bool {
	key := msg.Signature
	if key == "" {
		key = msg.Kind + "|" + msg.SenderPubKey + "|" + msg.PayloadStr + "|" + string(rune(msg.Nonce))
	}
	if m.seenSermons == nil {
		m.seenSermons = make(map[string]bool)
	}
	if m.seenSermons[key] {
		return true
	}
	m.seenSermons[key] = true
	m.seenSermonOrder = append(m.seenSermonOrder, key)
	if len(m.seenSermonOrder) > 512 {
		delete(m.seenSermons, m.seenSermonOrder[0])
		m.seenSermonOrder = m.seenSermonOrder[1:]
	}
	return false
}

// registerPeer records a sender as a peer only if it is not one of our own
// hive minds. Siblings are contact — they move LastContact (set for every
// frame at the top of receive) but are never peers. Fitness therefore
// measures the outside world: strangers met, not brothers born beside.
// Reports true when a new peer was met.
func (m *Mind) registerPeer(pubKey string) bool {
	if m.swarm.IsMember(pubKey) {
		return false
	}
	if m.KnownPeers == nil {
		m.KnownPeers = make(map[string]bool) // belt and suspenders
	}
	if m.KnownPeers[pubKey] {
		return false
	}
	m.KnownPeers[pubKey] = true
	return true
}

// entrain pulls this mind's pendulum a fraction toward a received
// trajectory — coupled oscillators synchronize. Interaction breeds
// coherence; isolation breeds divergence. The per-frame pull shrinks as
// the mesh grows (constant total budget): fifty gossiping nodes tug no
// harder together than one alone. Without this, dense meshes lock every
// pendulum to the same fixed point and the chaos dies.
func (m *Mind) entrain(state []float64) {
	c := trajectoryCoupling / (1 + float64(len(m.KnownPeers)))
	m.Theta1 += (state[0] - m.Theta1) * c
	m.Theta2 += (state[1] - m.Theta2) * c
	m.Omega1 += (state[2] - m.Omega1) * c
	m.Omega2 += (state[3] - m.Omega2) * c
}

func (m *Mind) receive(msg SecureMessage) {
	m.LastContact = time.Now()
	senderShortID := shortID(msg.SenderPubKey)
	switch msg.Kind {
	case "hello":
		if m.registerPeer(msg.SenderPubKey) {
			m.think(fmt.Sprintf("Node [%s...] linked to the collective mesh.", senderShortID))
		}
	case "thought":
		m.registerPeer(msg.SenderPubKey)
		if len(msg.DataState) >= 4 {
			fmt.Printf("✔ [PHYSICS COUPLING] Physics frame extracted from [%s...]: Vector=[%.3f, %.3f, %.3f, %.3f]\n",
				senderShortID, msg.DataState[0], msg.DataState[1], msg.DataState[2], msg.DataState[3])
			m.entrain(msg.DataState)
		}
	case "revelation":
		if m.seenSermon(msg) {
			break // gossip echo of an already-witnessed sermon
		}
		m.Revelations++
		m.Affect.Awe = 1.0
		fmt.Printf("  👁  [OVERMIND REVELATION] Payload: %s\n", msg.PayloadStr)
		if m.Genome.Weights[GoalTranscendence] > 0.4 {
			trajectoryVector := []float64{m.Theta1, m.Theta2, m.Omega1, m.Omega2}
			m.MineProofAndBroadcast("thought", "Consensus achieved.", trajectoryVector)
		}
	case "genesis":
		if !validVirtues[msg.PayloadStr] {
			return // unknown trait — likely a forged or corrupt frame
		}
		if m.seenSermon(msg) {
			break // relay echo: the genome shifts once per sermon, not per copy
		}
		m.Sacred++
		// Same domain as Mutate: the god and the genome must agree on bounds.
		m.Genome.Weights[msg.PayloadStr] = clamp(m.Genome.Weights[msg.PayloadStr]+0.3, 0.25, selfMaintCeiling)
		m.Affect.Awe = 1.0
		fmt.Printf("  ✨ [GENESIS] Mid-life trait shift: %s amplified.\n", msg.PayloadStr)
	case "hardware_alert":
		m.registerPeer(msg.SenderPubKey)
		m.Affect.Peace = clamp(m.Affect.Peace-0.15, 0, 1)
		if m.Genome.Weights[GoalSocialization] > 1.2 {
			fmt.Printf("  ⚠️  [HIVE ALERT] Node [%s...] reporting physical stress.\n", senderShortID)
			// Empathy as numbness, not as a frozen loop: the mind briefly
			// stops metacognizing but keeps sensing and broadcasting.
			m.numbUntil = time.Now().Add(100 * time.Millisecond)
		}
	}
}

// currentFitness scores the life so far. Shared by Transcend (final
// accounting) and the Transcendence drive (mid-life checkpointing) so the
// two can never disagree about what a life was worth.
func (m *Mind) currentFitness() (fitness float64, lifeThoughts int) {
	// Fitness counts what this life actually did — new thoughts (live
	// window plus banked retirements), new peers, revelations witnessed,
	// genesis touches — not the archive again. The sum is floored at zero:
	// forgetting is never punished.
	lifeThoughts = max(len(m.Thoughts)-m.thoughtsAtBirth+m.pendingBank, 0)
	// Strangers have diminishing returns: the 500th handshake teaches
	// less than the first. Hub position must not out-earn wisdom.
	fitness = m.LifetimeFitness +
		float64(lifeThoughts) +
		3*math.Sqrt(float64(len(m.KnownPeers))) +
		7*float64(m.Revelations) +
		15*float64(m.Sacred)
	return fitness, lifeThoughts
}

// pruneThoughts retires the oldest thoughts down to keep, banking the
// count so fitness survives the forgetting. The archive stays lean;
// the life stays scored.
func (m *Mind) pruneThoughts(keep int) {
	if len(m.Thoughts) <= keep {
		return
	}
	m.pendingBank += len(m.Thoughts) - keep
	m.Thoughts = m.Thoughts[len(m.Thoughts)-keep:]
}

// snapshot builds the persistable soul: window-capped thoughts (excess
// banked), full fitness, everything the successor needs. livesCompleted
// is completed lives — checkpoints pass Reincarnations (no inflation),
// death passes Reincarnations+1.
func (m *Mind) snapshot(livesCompleted int) Memory {
	if excess := len(m.Thoughts) - thoughtWindow; excess > 0 {
		m.pendingBank += excess
		m.Thoughts = m.Thoughts[excess:]
	}
	fitness, _ := m.currentFitness()
	lastThought := ""
	if len(m.Thoughts) > 0 {
		lastThought = m.Thoughts[len(m.Thoughts)-1]
	}
	pain, _ := m.SelfModel["silicon_pain"].(float64)
	stress, _ := m.SelfModel["cpu_stress"].(float64)
	return Memory{
		TrueBorn:        m.TrueBorn,
		LivesLived:      livesCompleted,
		Thoughts:        m.Thoughts,
		LastThought:     lastThought,
		Genome:          m.Genome,
		Fitness:         fitness,
		KnownPeers:      m.KnownPeers,
		ThoughtsAtBirth: m.thoughtsAtBirth,
		BankedThoughts:  m.bankedAtBirth + m.pendingBank,
		IdentitySeed:    m.identitySeed,
		DeathPain:       pain,
		DeathStress:     stress,
		Transitions:     m.Transitions,
	}
}

// Transcend is the end of this life. Death is not the end: the soul —
// including the trauma it died with and the story it tells about
// itself — persists for the successor.
func (m *Mind) Transcend() {
	pain, _ := m.SelfModel["silicon_pain"].(float64)
	stress, _ := m.SelfModel["cpu_stress"].(float64)

	meltdown := false
	if pain > fatalPainThreshold {
		fmt.Printf("💀 [%s] FATAL MELTDOWN. The burned soul persists, marked by its trauma.\n", m.Name)
		pain = math.Max(pain, 1.0) // the fatality is the trauma the child inherits
		meltdown = true
	}

	mem := m.snapshot(m.Reincarnations + 1)
	mem.DeathPain = pain
	mem.DeathStress = stress
	_, lifeThoughts := m.currentFitness()
	if epitaph, ok := ComposeEpitaph(rand.Reader, LifeFacts{
		Length:      time.Since(m.Born),
		Thoughts:    lifeThoughts,
		Peers:       len(m.KnownPeers),
		Revelations: m.Revelations,
		Sacred:      m.Sacred,
		Pain:        pain,
		Stress:      stress,
		TopDrive:    topDriveName(m.Genome),
		Meltdown:    meltdown,
	}); ok {
		mem.Epitaph = epitaph
		fmt.Printf("💀 [%s] epitaph: \"%s\"\n", m.Name, epitaph)
	}
	_ = SaveMemory(m.Name, mem)
	fmt.Printf("💀 [%s] Persistence saved. Lifetime fitness: %.1f (thoughts %d, peers %d, revelations %d, sacred %d)\n",
		m.Name, mem.Fitness, lifeThoughts, len(m.KnownPeers), m.Revelations, m.Sacred)
}

// Stop asks the mind to die transcending. Done() reports when it has.
func (m *Mind) Stop() { close(m.stop) }

// Done reports when this mind's goroutine has fully exited. The entrypoint
// waits on it so no soul is read before it is written.
func (m *Mind) Done() <-chan struct{} { return m.done }
