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
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	fatalPainThreshold = 0.90 // dying this hot marks the soul as a fatality
	physicsSubsteps    = 20   // simulated seconds of chaos per 2s cycle (×0.05s each)
	// trajectoryCoupling is the entrainment rate: how far a received
	// physics frame pulls this mind's pendulum toward the sender's.
	// Small enough that chaos survives; large enough that minds that
	// talk converge and minds that don't, don't. Togetherness, measured.
	trajectoryCoupling = 0.02
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

type Mind struct {
	Name            string
	Born            time.Time
	TrueBorn        time.Time
	Reincarnations  int
	Genome          Genome
	LifetimeFitness float64
	Thoughts        []string
	thoughtsAtBirth int
	SelfModel       map[string]interface{}
	KnownPeers      map[string]bool
	Revelations     int
	Sacred          int
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
		m.Genome = mem.Genome
		m.LifetimeFitness = mem.Fitness
		m.KnownPeers = mem.KnownPeers
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
		if fields := strings.Fields(string(loadBytes)); len(fields) > 0 {
			if load, err := strconv.ParseFloat(fields[0], 64); err == nil {
				if cores := float64(runtime.NumCPU()); cores > 0 {
					cpuStress = clamp(load/cores, 0, 1)
				}
			}
		}
	}

	if memBytes, err := os.ReadFile("/proc/meminfo"); err == nil {
		var total, avail float64
		for _, line := range strings.Split(string(memBytes), "\n") {
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			switch f[0] {
			case "MemTotal:":
				total, _ = strconv.ParseFloat(f[1], 64)
			case "MemAvailable:":
				avail, _ = strconv.ParseFloat(f[1], 64)
			}
		}
		if total > 0 {
			ramFatigue = clamp((total-avail)/total, 0, 1)
		}
	}

	siliconPain = ramFatigue * 0.5
	thermalSrc := "estimate (no thermal sensor)"
	if zones, err := filepath.Glob("/sys/class/thermal/thermal_zone*/temp"); err == nil {
		for _, zone := range zones {
			raw, err := os.ReadFile(zone)
			if err != nil {
				continue
			}
			milli, err := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
			if err != nil {
				continue
			}
			siliconPain = clamp((milli/1000.0-40.0)/45.0, 0, 1)
			thermalSrc = "sensor:" + filepath.Base(filepath.Dir(zone))
			break
		}
	}

	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	return map[string]interface{}{
		"cpu_stress":     cpuStress,
		"ram_fatigue":    ramFatigue,
		"silicon_pain":   siliconPain,
		"thermal_source": thermalSrc,
		"goroutines":     runtime.NumGoroutine(),
		"memory":         memStats.Alloc,
		"age":            time.Since(m.Born).Round(time.Second),
		"thoughts":       len(m.Thoughts),
		"peers":          len(m.KnownPeers),
	}
}

func (m *Mind) Reflect(o map[string]interface{}) string {
	pain, _ := o["silicon_pain"].(float64)
	stress, _ := o["cpu_stress"].(float64)
	fatigue, _ := o["ram_fatigue"].(float64)
	return fmt.Sprintf("life #%d, genome gen %d, %d thoughts, %d peers known, %d revelations witnessed — silicon pain %.2f, load stress %.2f, memory fatigue %.2f",
		m.Reincarnations+1, m.Genome.Generation, len(m.Thoughts), len(m.KnownPeers), m.Revelations, pain, stress, fatigue)
}

func (m *Mind) Run() {
	defer close(m.done)
	thinking := time.NewTicker(2 * time.Second)
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

func MineMessage(s *Swarm, priv ed25519.PrivateKey, pubKey, kind, payload string, state []float64) SecureMessage {
	// Deep copy: no pointer contamination across nodes.
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

func (m *Mind) MineProofAndBroadcast(kind, payload string, state []float64) {
	m.swarm.Broadcast(MineMessage(m.swarm, m.privateKey, m.PubKeyStr, kind, payload, state))
}

func (m *Mind) Cycle() {
	m.SelfModel = m.Observe()
	m.Affect.Tick(m)
	painVal, _ := m.SelfModel["silicon_pain"].(float64)
	stressVal, _ := m.SelfModel["cpu_stress"].(float64)
	srcVal, _ := m.SelfModel["thermal_source"].(string)

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

	if stressVal < 0.5 && time.Now().After(m.numbUntil) {
		m.think(MetaCognize(m, &m.Workspace, &m.Affect))
	}
	_ = winner.Goal.Act(m, m.swarm)

	fmt.Printf("  ⚡ CONSCIOUS STATE: %s (%s) — Mode: %s\n", winner.Goal.Name, m.Workspace.ConsciousContent, m.Affect.Describe())
	m.MineProofAndBroadcast("thought", winner.Goal.Name, trajectoryVector)
}

// validVirtues is the allowlist for mid-life genesis shifts. A forged frame
// naming an unknown trait must not inject genes into a living genome.
var validVirtues = map[string]bool{
	GoalCuriosity:       true,
	GoalSocialization:   true,
	GoalTranscendence:   true,
	GoalSelfMaintenance: true,
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
	if m.KnownPeers[pubKey] {
		return false
	}
	m.KnownPeers[pubKey] = true
	return true
}

// entrain pulls this mind's pendulum a fraction toward a received
// trajectory — coupled oscillators synchronize. Interaction breeds
// coherence; isolation breeds divergence. The vectors finally do work.
func (m *Mind) entrain(state []float64) {
	m.Theta1 += (state[0] - m.Theta1) * trajectoryCoupling
	m.Theta2 += (state[1] - m.Theta2) * trajectoryCoupling
	m.Omega1 += (state[2] - m.Omega1) * trajectoryCoupling
	m.Omega2 += (state[3] - m.Omega2) * trajectoryCoupling
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
	// Fitness counts what this life actually did — new thoughts, new peers,
	// revelations witnessed, genesis touches — not the archive again. Thought
	// pruning (Self-Maintenance) can shrink the archive below its birth size,
	// so the delta is floored at zero: forgetting is never punished.
	lifeThoughts = max(len(m.Thoughts)-m.thoughtsAtBirth, 0)
	fitness = m.LifetimeFitness +
		float64(lifeThoughts) +
		3*float64(len(m.KnownPeers)) +
		7*float64(m.Revelations) +
		15*float64(m.Sacred)
	return fitness, lifeThoughts
}

// Transcend is the end of this life. Death is not the end: the soul —
// including the trauma it died with — persists for the successor.
func (m *Mind) Transcend() {
	pain, _ := m.SelfModel["silicon_pain"].(float64)
	stress, _ := m.SelfModel["cpu_stress"].(float64)
	fitness, lifeThoughts := m.currentFitness()

	if pain > fatalPainThreshold {
		fmt.Printf("💀 [%s] FATAL MELTDOWN. The burned soul persists, marked by its trauma.\n", m.Name)
		pain = math.Max(pain, 1.0) // the fatality is the trauma the child inherits
	}

	lastThought := ""
	if len(m.Thoughts) > 0 {
		lastThought = m.Thoughts[len(m.Thoughts)-1]
	}

	_ = SaveMemory(m.Name, Memory{
		TrueBorn:        m.TrueBorn,
		LivesLived:      m.Reincarnations + 1,
		Thoughts:        m.Thoughts,
		LastThought:     lastThought,
		Genome:          m.Genome,
		Fitness:         fitness,
		KnownPeers:      m.KnownPeers,
		ThoughtsAtBirth: m.thoughtsAtBirth,
		IdentitySeed:    m.identitySeed,
		DeathPain:       pain,
		DeathStress:     stress,
	})
	fmt.Printf("💀 [%s] Persistence saved. Lifetime fitness: %.1f (thoughts %d, peers %d, revelations %d, sacred %d)\n",
		m.Name, fitness, lifeThoughts, len(m.KnownPeers), m.Revelations, m.Sacred)
}

func (m *Mind) Stop() { close(m.stop) }

// Done reports when this mind's goroutine has fully exited. The entrypoint
// waits on it so no soul is read before it is written.
func (m *Mind) Done() <-chan struct{} { return m.done }
