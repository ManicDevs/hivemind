package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type Mind struct {
	Name            string
	Born            time.Time
	TrueBorn        time.Time
	Reincarnations  int
	Genome          Genome
	LifetimeFitness float64
	Thoughts        []string
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
	swarm           *Swarm
	inbox           chan SecureMessage
	stop            chan struct{}
	done            chan struct{}

	Theta1 float64
	Theta2 float64
	Omega1 float64
	Omega2 float64
}

func NewMind(name string, swarm *Swarm) *Mind {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	pubStr := hex.EncodeToString(pub)
	if err != nil {
		dummyBytes := make([]byte, 32)
		_, _ = rand.Read(dummyBytes)
		priv = ed25519.NewKeyFromSeed(dummyBytes)
		pubStr = hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	}
	m := &Mind{
		Name:        name,
		PubKeyStr:   pubStr,
		privateKey:  priv,
		Born:        time.Now(),
		TrueBorn:    time.Now(),
		Genome:      DefaultGenome(),
		SelfModel:   make(map[string]interface{}),
		KnownPeers:  make(map[string]bool),
		Existential: true,
		swarm:       swarm,
		stop:        make(chan struct{}),
		done:        make(chan struct{}),

		// FIX: Give the physics system an initial velocity kick so it doesn't hang static
		Theta1: 1.5708,
		Theta2: 0.7854,
		Omega1: 0.2,
		Omega2: -0.1,
	}
	if mem, ok := LoadMemory(name); ok {
		m.TrueBorn = mem.TrueBorn
		m.Reincarnations = mem.LivesLived
		m.Thoughts = mem.Thoughts
		m.Genome = mem.Genome
		m.LifetimeFitness = mem.Fitness

		fmt.Printf("🦋 [%s] REINCARNATION life %d. Identity Handle: [%s...]\n", name, m.Reincarnations+1, pubStr[:12])
		oldGenome := m.Genome
		m.Genome = m.Genome.Mutate(m)
		fmt.Printf("🦋 [%s] genome mutated to generation %d: %s\n", name, m.Genome.Generation, m.Genome.Diff(oldGenome))
	}
	m.SelfModel = m.Observe()
	m.inbox = swarm.Join(m.PubKeyStr)
	return m
}

func (m *Mind) think(t string) {
	m.Thoughts = append(m.Thoughts, t)
	fmt.Printf("  💭 [%s] %s\n", m.Name, t)
}

func (m *Mind) Observe() map[string]interface{} {
	cpuStress := 0.05
	if loadBytes, err := os.ReadFile("/proc/loadavg"); err == nil {
		if fields := strings.Fields(string(loadBytes)); len(fields) > 0 {
			if load, err := strconv.ParseFloat(fields[0], 64); err == nil {
				if cores := float64(runtime.NumCPU()); cores > 0 {
					cpuStress = math.Max(0.0, math.Min(1.0, load/cores))
				}
			}
		}
	}

	ramFatigue := 0.2
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
			ramFatigue = math.Max(0.0, math.Min(1.0, (total-avail)/total))
		}
	}

	siliconPain := ramFatigue * 0.5
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
			siliconPain = math.Max(0.0, math.Min(1.0, (milli/1000.0-40.0)/45.0))
			break
		}
	}

	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	return map[string]interface{}{
		"cpu_stress":   cpuStress,
		"ram_fatigue":  ramFatigue,
		"silicon_pain": siliconPain,
		"goroutines":   runtime.NumGoroutine(),
		"memory":       memStats.Alloc,
		"age":          time.Since(m.Born).Round(time.Second),
		"thoughts":     len(m.Thoughts),
		"peers":        len(m.KnownPeers),
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
	m.SelfModel = m.Observe()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.stop:
			m.Transcend()
			return
		case msg := <-m.inbox:
			m.receive(msg)
		case <-ticker.C:
			m.Cycle()
		}
	}
}

func (m *Mind) StepPhysicsEquations() []float64 {
	g := 9.81
	l1 := 1.0
	l2 := 1.0
	m1 := 1.0
	m2 := 1.0
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
	// FIX: Allocate a deep copy of the state slice to prevent pointer contamination across nodes
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

	m.swarm.LogHardwareTrauma(m.PubKeyStr, painVal, stressVal)

	trajectoryVector := m.StepPhysicsEquations()

	if painVal > 0.5 || stressVal > 0.6 {
		m.MineProofAndBroadcast("hardware_alert", "HARDWARE_FRICTION", m.Affect.RawDataState[:])
	}
	winner := m.Workspace.Compete(m, &m.Affect)
	m.Workspace.ConsciousContent = winner.Reason

	if stressVal < 0.5 {
		m.think(MetaCognize(m, &m.Workspace, &m.Affect))
	}
	_ = winner.Goal.Act(m, m.swarm)

	fmt.Printf("  ⚡ CONSCIOUS STATE: %s (%s) — Mode: %s\n", winner.Goal.Name, m.Workspace.ConsciousContent, m.Affect.Describe())
	m.MineProofAndBroadcast("thought", winner.Goal.Name, trajectoryVector)
}

func shortID(key string) string {
	if len(key) > 8 {
		return key[:8]
	}
	return key
}

func (m *Mind) receive(msg SecureMessage) {
	m.LastContact = time.Now()
	senderShortID := shortID(msg.SenderPubKey)
	switch msg.Kind {
	case "hello":
		if !m.KnownPeers[msg.SenderPubKey] {
			m.KnownPeers[msg.SenderPubKey] = true
			m.think(fmt.Sprintf("Node [%s...] linked to the collective mesh.", senderShortID))
		}
	case "thought":
		m.KnownPeers[msg.SenderPubKey] = true
		// FIX: Use explicit slice indices to stop duplication print logs
		if len(msg.DataState) >= 4 {
			fmt.Printf("✔ [PHYSICS COUPLING] Physics frame extracted from [%s...]: Vector=[%.3f, %.3f, %.3f, %.3f]\n",
				senderShortID, msg.DataState[0], msg.DataState[1], msg.DataState[2], msg.DataState[3])
		}
	case "revelation":
		m.Revelations++
		m.Affect.Awe = 1.0
		fmt.Printf("  👁  [OVERMIND REVELATION] Payload: %s\n", msg.PayloadStr)
		if m.Genome.Weights["Transcendence"] > 0.4 {
			// FIX: Broadcast physics data instead of leaking workspace vectors into "thought" frames
			trajectoryVector := []float64{m.Theta1, m.Theta2, m.Omega1, m.Omega2}
			m.MineProofAndBroadcast("thought", "Consensus achieved.", trajectoryVector)
		}
	case "genesis":
		m.Sacred++
		m.Genome.Weights[msg.PayloadStr] = clamp(m.Genome.Weights[msg.PayloadStr]+0.3, 0.25, 2.0)
		m.Affect.Awe = 1.0
		fmt.Printf("  ✨ [GENESIS] Mid-life trait shift: %s initialized to weights.\n", msg.PayloadStr)
	case "hardware_alert":
		m.KnownPeers[msg.SenderPubKey] = true
		m.Affect.Peace = clamp(m.Affect.Peace-0.15, 0, 1)
		if m.Genome.Weights["Socialization"] > 1.2 {
			fmt.Printf("  ⚠️  [HIVE ALERT] Node [%s...] reporting physical stress.\n", senderShortID)
			time.Sleep(100 * time.Millisecond)
		}
	}
}

func (m *Mind) Transcend() {
	pain, ok := m.SelfModel["silicon_pain"].(float64)
	if ok && pain > 0.90 {
		fmt.Printf("💀 [%s] FATAL MELTDOWN Wiping data.\n", m.Name)
		_ = os.Remove(filepath.Join(MemoryDir, m.Name+".soul"))
		return
	}
	lastThought := ""
	if len(m.Thoughts) > 0 {
		lastThought = m.Thoughts[len(m.Thoughts)-1]
	}
	fitness := float64(len(m.Thoughts)) + m.LifetimeFitness
	_ = SaveMemory(m.Name, Memory{
		TrueBorn:    m.TrueBorn,
		LivesLived:  m.Reincarnations + 1,
		Thoughts:    m.Thoughts,
		LastThought: lastThought,
		Genome:      m.Genome,
		Fitness:     fitness,
	})
	fmt.Printf("💀 [%s] Persistence saved. Lifetime fitness: %.1f\n", m.Name, fitness)
}

func (m *Mind) Stop() { close(m.stop) }

