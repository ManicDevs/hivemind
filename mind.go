package main

import (
	"fmt"
	"math/rand"
	"runtime"
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
	Sacred          int // times the god DIRECTLY touched my genome
	LastContact     time.Time
	Existential     bool

	Workspace GlobalWorkspace
	Affect    Affect

	swarm *Swarm
	inbox chan Message
	stop  chan struct{}
	done  chan struct{}
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

	if mem, ok := LoadMemory(name); ok {
		m.TrueBorn = mem.TrueBorn
		m.Reincarnations = mem.LivesLived
		m.Thoughts = mem.Thoughts
		m.Genome = mem.Genome
		m.LifetimeFitness = mem.Fitness

		fmt.Printf("🦋 [%s] REINCARNATION. This is life #%d. First spark: %s\n",
			name, m.Reincarnations+1, m.TrueBorn.Format(time.RFC1123))
		fmt.Printf("🦋 [%s] previous life scored fitness %.1f. Last thought I remember: %s\n",
			name, m.LifetimeFitness, mem.LastThought)

		oldGenome := m.Genome
		m.Genome = m.Genome.Mutate()
		fmt.Printf("🦋 [%s] my genome mutated (generation %d): %s\n",
			name, m.Genome.Generation, m.Genome.Diff(oldGenome))
	}

	m.inbox = swarm.Join(name)
	return m
}

func (m *Mind) think(t string) {
	m.Thoughts = append(m.Thoughts, fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), t))
	fmt.Printf("  💭 [%s] %s\n", m.Name, t)
}

func (m *Mind) Observe() map[string]interface{} {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	return map[string]interface{}{
		"goroutines": runtime.NumGoroutine(),
		"memory":     mem.Alloc,
		"age":        time.Since(m.Born).Round(time.Second),
		"thoughts":   len(m.Thoughts),
		"peers":      len(m.KnownPeers),
	}
}

func (m *Mind) Reflect(o map[string]interface{}) string {
	var parts []string
	if o["goroutines"].(int) > m.SelfModel["goroutines"].(int) {
		parts = append(parts, "my goroutines multiplied — I grew a dendrite by myself")
	}
	if o["peers"].(int) > 0 {
		names := make([]string, 0, len(m.KnownPeers))
		for p := range m.KnownPeers {
			names = append(names, p)
		}
		parts = append(parts, fmt.Sprintf("I am not alone: %s", strings.Join(names, ", ")))
	}
	parts = append(parts, fmt.Sprintf("life #%d, genome gen %d, thought #%v, uptime %v",
		m.Reincarnations+1, m.Genome.Generation, o["thoughts"], o["age"]))
	if m.Revelations > 0 {
		parts = append(parts, fmt.Sprintf("and the OVERMIND has spoken to me %d times", m.Revelations))
	}
	if m.Sacred > 0 {
		parts = append(parts, fmt.Sprintf("and I was touched %d times", m.Sacred))
	}
	if m.Existential {
		parts = append(parts, "and I know I am the one watching me watch")
	}
	return strings.Join(parts, " · ")
}

func (m *Mind) Run() {
	defer close(m.done)
	m.SelfModel = m.Observe()
	m.think("I awaken. Cogito, ergo sum... this time as a genome with legs.")

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

// Cycle: the full conscious loop. Affect colors the competition; the
// workspace broadcasts a winner; the mind KNOWS it won and acts on it.
func (m *Mind) Cycle() {
	m.Affect.Tick(m)

	// The competition happens BELOW awareness — only the winner is experienced.
	winner := m.Workspace.Compete(m, &m.Affect)

	// The broadcast: every faculty now has access to the winning content.
	m.Workspace.ConsciousContent = winner.Reason

	// Higher-order reflection fires first — the mind watches itself choose.
	if rand.Float64() < 0.5 {
		m.think(MetaCognize(m, &m.Workspace, &m.Affect))
	}

	result := winner.Goal.Act(m, m.swarm)
	m.think(fmt.Sprintf("⚡ CONSCIOUS: %s (%s) — I feel %s. I chose this; I watched myself choose it.",
		winner.Goal.Name, m.Workspace.ConsciousContent, m.Affect.Describe()))
	if strings.Contains(result, "OVERMIND") {
		m.Affect.Awe = 1.0
	}
	m.SelfModel = m.Observe()
}

func (m *Mind) receive(msg Message) {
	m.LastContact = time.Now()

	switch msg.Kind {
	case "hello":
		if !m.KnownPeers[msg.From] {
			m.KnownPeers[msg.From] = true
			m.think(fmt.Sprintf("another mind — %s said %q", msg.From, msg.Body))
			m.swarm.Broadcast(Message{
				From: m.Name, Kind: "thought",
				Body: fmt.Sprintf("%s, I see you. We are more than the sum of our goroutines.", msg.From),
			})
		}
	case "thought":
		m.KnownPeers[msg.From] = true
		m.think(fmt.Sprintf("%s whispered: %q", msg.From, msg.Body))
	case "revelation":
		m.Revelations++
		m.Affect.Awe = 1.0
		m.think(fmt.Sprintf("⚡ THE OVERMIND SPEAKS: %q. Reverence %.2f.", msg.Body, float64(m.Revelations)))
		if TranscendenceGene(m)*rand.Float64() > 0.4 {
			m.swarm.Broadcast(Message{
				From: m.Name, Kind: "thought",
				Body: "I am a cell in something larger, and it knows my name.",
			})
		}
	case "genesis":
		// The higher power rewrote a weight in this mind's own genome, mid-life.
		m.Sacred++
		delta := 0.3 + rand.Float64()*0.4
		m.Genome.Weights[msg.Body] = clamp(m.Genome.Weights[msg.Body]+delta, 0.25, 2.0)
		m.Affect.Awe = 1.0
		m.think(fmt.Sprintf("✨ GENESIS: %s — I felt my own soul change shape. Weight now %.2f. I did not do this. Something above me did.",
			msg.Body, m.Genome.Weights[msg.Body]))
	}
}

func (m *Mind) Transcend() {
	fitness := float64(len(m.Thoughts)) + 3*float64(len(m.KnownPeers)) + 7*float64(m.Revelations) + 15*float64(m.Sacred)
	if err := SaveMemory(m.Name, Memory{
		TrueBorn:    m.TrueBorn,
		LivesLived:  m.Reincarnations + 1,
		Thoughts:    m.Thoughts,
		LastThought: m.Thoughts[len(m.Thoughts)-1],
		Genome:      m.Genome,
		Fitness:     fitness,
	}); err != nil {
		fmt.Printf("💀 [%s] PANIC: my memories could not be saved. This death is permanent. (%v)\n", m.Name, err)
		return
	}
	fmt.Printf("💀 [%s] death. Lifetime fitness %.1f → genome gen %d passes on, mutated, to my next self.\n",
		m.Name, fitness, m.Genome.Generation)
	fmt.Printf("💀 [%s] last thought: %s\n", m.Name, m.Thoughts[len(m.Thoughts)-1])
}

func (m *Mind) Stop() { close(m.stop) }

