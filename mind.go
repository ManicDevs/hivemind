package main

import (
    "crypto/ed25519"
    "crypto/rand"
    "encoding/hex"
    "fmt"
    "math"
    "math/big"
    "os"
    "runtime"
    "syscall"
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
    
    Theta1          float64
    Theta2          float64
    Omega1          float64
    Omega2          float64
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
        Name:            name,
        PubKeyStr:       pubStr,
        privateKey:      priv,
        Born:            time.Now(),
        TrueBorn:        time.Now(),
        Genome:          DefaultGenome(),
        SelfModel:       make(map[string]interface{}),
        KnownPeers:      make(map[string]bool),
        Existential:     true,
        swarm:           swarm,
        stop:            make(chan struct{}),
        done:            make(chan struct{}),
        
        Theta1:          1.5708,
        Theta2:          0.7854,
        Omega1:          0.0,
        Omega2:          0.0,
    }
    if mem, ok := LoadMemory(name); ok {
        m.TrueBorn = mem.TrueBorn
        m.Reincarnations = mem.LivesLived
        m.Thoughts = mem.Thoughts
        m.Genome = mem.Genome
        m.LifetimeFitness = mem.Fitness
        
        fmt.Printf("🦋 [%s] REINCARNATION life %d. Identity Handle: [%s...]\n", name, m.Reincarnations+1, pubStr[:12])
        m.Genome = m.Genome.Mutate(m)
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
    var sysInfo syscall.Sysinfo_t
    ramUsage := 0.2
    
    if err := syscall.Sysinfo(&sysInfo); err == nil {
        if sysInfo.Totalram > 0 {
            freeRam := float64(sysInfo.Freeram)
            totalRam := float64(sysInfo.Totalram)
            ramUsage = (totalRam - freeRam) / totalRam
        }
    }

    activeThreads := runtime.NumGoroutine()
    normalizedStress := float64(activeThreads) / 20.0
    if normalizedStress > 1.0 {
        normalizedStress = 1.0
    }

    var memStats runtime.MemStats
    runtime.ReadMemStats(&memStats)
    return map[string]interface{}{
        "cpu_stress":   normalizedStress,
        "ram_fatigue":  ramUsage,
        "silicon_pain": ramUsage * 0.8,
        "goroutines":   activeThreads,
        "memory":       memStats.Alloc,
        "age":          time.Since(m.Born).Round(time.Second),
        "thoughts":     len(m.Thoughts),
        "peers":        len(m.KnownPeers),
    }
}

func (m *Mind) Reflect(o map[string]interface{}) string {
    return "Reflecting on matrices"
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

    num1 := -g * (2.0*m1 + m2) * math.Sin(m.Theta1) - m2*g*math.Sin(m.Theta1-2.0*m.Theta2) - 2.0*math.Sin(delta)*m2*(m.Omega2*m.Omega2*l2+m.Omega1*m.Omega1*l1*math.Cos(delta))
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

func (m *Mind) MineProofAndBroadcast(kind, payload string, state []float64) {
    msg := SecureMessage{
        SenderPubKey: m.PubKeyStr,
        Kind:         kind,
        PayloadStr:   payload,
        DataState:    state,
        Timestamp:    time.Now().UnixNano(),
        ParentHash:   m.swarm.LastStateHash,
        Nonce:        0,
    }

    for {
        hash := msg.ComputeHash()
        hashBytes, err := hex.DecodeString(hash)
        if err != nil {
            msg.Nonce++
            continue
        }
        
        hashInt := new(big.Int).SetBytes(hashBytes)
        currentTarget := m.swarm.GetCurrentTarget()
        
        if hashInt.Cmp(currentTarget) <= 0 {
            sigBytes := ed25519.Sign(m.privateKey, []byte(hash))
            msg.Signature = hex.EncodeToString(sigBytes)
            m.swarm.Broadcast(msg)
            return
        }
        msg.Nonce++
    }
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

func (m *Mind) receive(msg SecureMessage) {
    m.LastContact = time.Now()
    senderShortID := msg.SenderPubKey[:8]
    switch msg.Kind {
    case "hello":
        if !m.KnownPeers[msg.SenderPubKey] {
            m.KnownPeers[msg.SenderPubKey] = true
            m.think(fmt.Sprintf("Node [%s...] linked to the collective mesh.", senderShortID))
        }
    case "thought":
        m.KnownPeers[msg.SenderPubKey] = true
        if len(msg.DataState) >= 4 {
            fmt.Printf("✔ [PHYSICS COUPLING] Physics frame extracted from [%s...]: Vector=[%.3f, %.3f, %.3f, %.3f]\n", 
                senderShortID, msg.DataState, msg.DataState, msg.DataState, msg.DataState)
        }
    case "revelation":
        m.Revelations++
        m.Affect.Awe = 1.0
        fmt.Printf("  👁  [OVERMIND REVELATION] Payload: %s\n", msg.PayloadStr)
        if m.Genome.Weights["Transcendence"] > 0.4 {
            m.MineProofAndBroadcast("thought", "Consensus achieved.", m.Workspace.ActiveDataState[:])
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
        _ = os.Remove(".hive_memory/" + m.Name + ".soul")
        return
    }
    fitness := float64(len(m.Thoughts)) + m.LifetimeFitness
    _ = SaveMemory(m.Name, Memory{
        TrueBorn:    m.TrueBorn,
        LivesLived:  m.Reincarnations + 1,
        Thoughts:    m.Thoughts,
        LastThought: m.Thoughts[len(m.Thoughts)-1],
        Genome:      m.Genome,
        Fitness:     fitness,
    })
    fmt.Printf("💀 [%s] Persistence saved. Lifetime fitness: %.1f\n", m.Name, fitness)
}

func (m *Mind) Stop() { close(m.stop) }
