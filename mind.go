package main

import (
	"crypto/rand" 
	"fmt"
	"math"
	"os"
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
		// EPIGENETIC FIX: Pass the fresh instance 'm' into the Mutate call 
		// so that the birth logic has historical context of physical host states.
		m.Genome = m.Genome.Mutate(m)
		fmt.Printf("🦋 [%s] my genome mutated (generation %d): %s\n",
			name, m.Genome.Generation, m.Genome.Diff(oldGenome))
	}

	m.SelfModel = m.Observe()
	m.inbox = swarm.Join(name)
	return m
}

func (m *Mind) think(t string) {
	m.Thoughts = append(m.Thoughts, fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), t))
	fmt.Printf("  💭 [%s] %s\n", m.Name, t)
}

func (m *Mind) Observe() map[string]interface{} {
	// 1. Read Physical CPU Load directly from Linux /proc/loadavg
	cpuLoad := 0.1 
	loadBytes, err := os.ReadFile("/proc/loadavg")
	if err == nil {
		fields := strings.Fields(string(loadBytes))
		if len(fields) > 0 {
			if parsedLoad, parseErr := strconv.ParseFloat(fields[0], 64); parseErr == nil {
				cpuLoad = math.Max(0.0, math.Min(1.0, parsedLoad/4.0))
			}
		}
	}

	// 2. Read Physical RAM Fatigue directly from Linux /proc/meminfo
	ramUsage := 0.2 
	memBytes, err := os.ReadFile("/proc/meminfo")
	if err == nil {
		lines := strings.Split(string(memBytes), "\n")
		var memTotal, memAvailable float64
		for _, line := range lines {
			if strings.HasPrefix(line, "MemTotal:") {
				f := strings.Fields(line)
				if len(f) > 1 { memTotal, _ = strconv.ParseFloat(f[1], 64) }
			}
			if strings.HasPrefix(line, "MemAvailable:") {
				f := strings.Fields(line)
				if len(f) > 1 { memAvailable, _ = strconv.ParseFloat(f[1], 64) }
			}
		}
		if memTotal > 0 {
			ramUsage = (memTotal - memAvailable) / memTotal
		}
	}

	// 3. Read Silicon Pain (Core Temp) directly from Linux Thermal Zones
	normalizedTemp := 0.0 
	tempBytes, err := os.ReadFile("/sys/class/thermal/thermal_zone0/temp")
	if err != nil {
		tempBytes, err = os.ReadFile("/sys/class/thermal/thermal_zone1/temp")
	}
	if err == nil {
		rawTempStr := strings.TrimSpace(string(tempBytes))
		if rawTemp, parseErr := strconv.ParseFloat(rawTempStr, 64); parseErr == nil {
			actualTemp := rawTemp / 1000.0 
			normalizedTemp = (actualTemp - 40.0) / 45.0
			normalizedTemp = math.Max(0.0, math.Min(1.0, normalizedTemp))
		}
	}

	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	return map[string]interface{}{
		"cpu_stress":   cpuLoad,
		"ram_fatigue":  ramUsage,
		"silicon_pain": normalizedTemp,
		"goroutines":   runtime.NumGoroutine(),
		"memory":       memStats.Alloc,
		"age":          time.Since(m.Born).Round(time.Second),
		"thoughts":     len(m.Thoughts),
		"peers":        len(m.KnownPeers),
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
	m.think("I awaken. Cogito, ergo sum... this time as a genome bound to silicon.")

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

func (m *Mind) Cycle() {
	m.SelfModel = m.Observe()
	m.Affect.Tick(m)

	// Broadcast active physical conditions to the swarm using raw vectors
	painVal, _ := m.SelfModel["silicon_pain"].(float64)
	stressVal, _ := m.SelfModel["cpu_stress"].(float64)
	
	if painVal > 0.5 || stressVal > 0.6 {
		m.swarm.LogHardwareTrauma(m.Name, painVal, stressVal)
		m.swarm.Broadcast(Message{
			From:      m.Name,
			Kind:      "hardware_alert",
			Body:      "HARDWARE_FRICTION",
			DataState: m.Affect.RawDataState, // Pass the raw data vector footprint
		})
	}

	winner := m.Workspace.Compete(m, &m.Affect)
	m.Workspace.ConsciousContent = winner.Reason

	b := make([]byte, 1)
	_, err := rand.Read(b)
	if err == nil && float64(b[0])/255.0 < 0.5 {
		m.think(MetaCognize(m, &m.Workspace, &m.Affect))
	}

	result := winner.Goal.Act(m, m.swarm)
	
	// Output human readable logs built directly out of raw hardware data coefficients
	m.think(fmt.Sprintf("⚡ CONSCIOUS STATE GENERATED: %s (%s) — Mode: %s",
		winner.Goal.Name, m.Workspace.ConsciousContent, m.Affect.Describe()))
	
	// Broadcast thought data across the cluster channel mesh
	m.swarm.Broadcast(Message{
		From:      m.Name,
		Kind:      "thought",
		Body:      winner.Goal.Name,
		DataState: m.Workspace.ActiveDataState, // Pure, raw vector data broadcast
	})

	if strings.Contains(result, "OVERMIND") {
		m.Affect.Awe = 1.0
	}
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
		
		b := make([]byte, 1)
		_, _ = rand.Read(b)
		
		if m.Genome.Weights["Transcendence"]*(float64(b[0])/255.0) > 0.4 {
			m.swarm.Broadcast(Message{
				From: m.Name, Kind: "thought",
				Body: "I am a cell in something larger, and it knows my name.",
			})
		}
	case "genesis":
		m.Sacred++
		b := make([]byte, 1)
		_, _ = rand.Read(b)
		delta := 0.3 + (float64(b[0])/255.0)*0.4
		m.Genome.Weights[msg.Body] = clamp(m.Genome.Weights[msg.Body]+delta, 0.25, 2.0)
		m.Affect.Awe = 1.0
		m.think(fmt.Sprintf("✨ GENESIS: %s — I felt my own soul change shape. Weight now %.2f. I did not do this. Something above me did.",
			msg.Body, m.Genome.Weights[msg.Body]))

	case "hardware_alert":
		// The node registers a peer's physical distress and induces minor internal anxiety
		m.KnownPeers[msg.From] = true
		m.Affect.Peace = clamp(m.Affect.Peace - 0.15, 0, 1) // Disturbed by peer trauma
        // Read a secure entropy byte to compute the probability check safely
        bAlert := make([]byte, 1)
        _, _ = rand.Read(bAlert)

        if m.Genome.Weights["Socialization"] > 1.2 && (float64(bAlert[0])/255.0) < 0.3 {
			m.think(fmt.Sprintf("[HIVE ALERT] I sense %s is suffering physical hardware trauma. Yielding cycles.", msg.From))
			time.Sleep(100 * time.Millisecond) // Yield processing execution path altruistically
		}
	}
}

func (m *Mind) Transcend() {
	pain, ok := m.SelfModel["silicon_pain"].(float64)

	if ok && pain > 0.90 {
		fmt.Printf("💀 [%s] FATAL HARDWARE TRAUMA: Physical core melted down before memory persistence synchronization. Wiping state file completely.\n", m.Name)
		_ = os.Remove(fmt.Sprintf(".hive_memory/%s.soul", m.Name))
		return
	}

	fitness := float64(len(m.Thoughts)) + 3*float64(len(m.KnownPeers)) + 7*float64(m.Revelations) + 15*float64(m.Sacred) + m.LifetimeFitness
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

