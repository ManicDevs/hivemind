package main

import (
    "crypto/ed25519"
    "crypto/sha256"
    "encoding/hex"
    "fmt"
    "math"
    "math/big"
    "sort"
    "sync"
)

type SecureMessage struct {
    SenderPubKey string    `json:"sender_pub_key"`
    Kind         string    `json:"kind"`
    PayloadStr   string    `json:"payload_str"`
    DataState    []float64 `json:"data_state"`
    Timestamp    int64     `json:"timestamp"`
    ParentHash   string    `json:"parent_hash"`
    Nonce        int64     `json:"nonce"`
    Signature    string    `json:"signature"`
    // Relayed marks frames that arrived over the wire. It is never
    // serialized or hashed — it only stops the outbound hook echoing
    // a relayed frame back out to the mesh it came from.
    Relayed      bool      `json:"-"`
}

func (sm *SecureMessage) ComputeHash() string {
    rawInput := fmt.Sprintf("%s|%s|%s|%s|%d|%d", 
        sm.SenderPubKey, sm.Kind, sm.PayloadStr, sm.ParentHash, sm.Timestamp, sm.Nonce)
    h := sha256.Sum256([]byte(rawInput))
    return hex.EncodeToString(h[:])
}

func (sm *SecureMessage) VerifySignature() bool {
    pubKeyBytes, err := hex.DecodeString(sm.SenderPubKey)
    if err != nil || len(pubKeyBytes) != ed25519.PublicKeySize {
        return false
    }
    sigBytes, err := hex.DecodeString(sm.Signature)
    if err != nil {
        return false
    }
    msgHash := sm.ComputeHash()
    return ed25519.Verify(pubKeyBytes, []byte(msgHash), sigBytes)
}

type Swarm struct {
    mu            sync.Mutex
    members       map[string]chan SecureMessage
    chronicle     []string
    thinkers      map[string]map[string]bool
    NodePain      map[string]float64
    NodeStress    map[string]float64
    LastStateHash string
    MaxTarget     *big.Int
    CurrentTarget *big.Int
    // outbound carries locally-originated frames to the wire (socket peers,
    // cloud relay). Set by the network broker; nil in standalone mode.
    outbound      func(SecureMessage)

    // Store the last known kinematics vectors for visualization tracking
    lastTheta1    float64
    lastTheta2    float64
}

func NewSwarm() *Swarm {
    genesisHash := sha256.Sum256([]byte("GENESIS"))
    
    maxTargetBytes := make([]byte, 32)
    for i := range maxTargetBytes {
        maxTargetBytes[i] = 0xFF
    }
    maxTargetBytes[0] = 0x0F
    
    maxInt := new(big.Int).SetBytes(maxTargetBytes)
    
    return &Swarm{
        members:       make(map[string]chan SecureMessage),
        thinkers:      make(map[string]map[string]bool),
        NodePain:      make(map[string]float64),
        NodeStress:    make(map[string]float64),
        LastStateHash: hex.EncodeToString(genesisHash[:]),
        MaxTarget:     maxInt,
        CurrentTarget: new(big.Int).Set(maxInt),
        lastTheta1:    1.5708,
        lastTheta2:    0.7854,
    }
}

func (s *Swarm) Join(pubKeyStr string) chan SecureMessage {
    ch := make(chan SecureMessage, 128)
    s.mu.Lock()
    s.members[pubKeyStr] = ch
    s.NodePain[pubKeyStr] = 0.0
    s.NodeStress[pubKeyStr] = 0.0
    s.mu.Unlock()
    fmt.Printf("✔ [IDENTITY REGISTERED] Attached hash path: [%s...]\n", pubKeyStr[:12])
    return ch
}

func (s *Swarm) GetCurrentTarget() *big.Int {
    s.mu.Lock()
    defer s.mu.Unlock()
    return new(big.Int).Set(s.CurrentTarget)
}

// SetOutbound registers the wire bridge. Locally mined frames flow out;
// relayed frames never echo back.
func (s *Swarm) SetOutbound(out func(SecureMessage)) {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.outbound = out
}

func (s *Swarm) Broadcast(msg SecureMessage) bool {
    if !msg.VerifySignature() {
        return false
    }
    
    msgHash := msg.ComputeHash()
    hashBytes, err := hex.DecodeString(msgHash)
    if err != nil {
        return false
    }
    
    hashInt := new(big.Int).SetBytes(hashBytes)
    
    s.mu.Lock()
    currentTarget := new(big.Int).Set(s.CurrentTarget)
    if len(msg.DataState) >= 2 {
        s.lastTheta1 = msg.DataState[0]
        s.lastTheta2 = msg.DataState[1]
    }
    s.mu.Unlock()

    if hashInt.Cmp(currentTarget) > 0 {
        return false
    }

    s.mu.Lock()
    for pubKey, ch := range s.members {
        if pubKey == msg.SenderPubKey {
            continue
        }
        select {
        case ch <- msg:
        default:
        }
    }
    // Every verified frame advances the collective memory — except raw
    // hardware utility packets, which never enter conscious history.
    if msg.Kind != "hardware_alert" {
        s.chronicle = append(s.chronicle, msg.SenderPubKey+": "+msg.PayloadStr)
        if s.thinkers[msg.PayloadStr] == nil {
            s.thinkers[msg.PayloadStr] = make(map[string]bool)
        }
        s.thinkers[msg.PayloadStr][msg.SenderPubKey] = true
    }
    s.LastStateHash = msgHash
    out := s.outbound
    relayed := msg.Relayed
    s.mu.Unlock()
    // Local frames ride out to the mesh. Relayed frames stop here —
    // otherwise two nodes would echo one thought back and forth forever.
    if out != nil && !relayed {
        out(msg)
    }
    return true
}

func (s *Swarm) GetLastStateHash() string {
    s.mu.Lock()
    defer s.mu.Unlock()
    return s.LastStateHash
}

func (s *Swarm) Depth() int {
    s.mu.Lock()
    defer s.mu.Unlock()
    return len(s.chronicle)
}

// TopConsensus returns thought-bodies thought by the most distinct minds.
func (s *Swarm) TopConsensus(n int) []string {
    s.mu.Lock()
    defer s.mu.Unlock()
    type item struct {
        body string
        who  int
    }
    var items []item
    for body, who := range s.thinkers {
        if len(who) > 1 {
            items = append(items, item{body, len(who)})
        }
    }
    sort.Slice(items, func(i, j int) bool { return items[i].who > items[j].who })
    out := make([]string, 0, n)
    for i := 0; i < n && i < len(items); i++ {
        out = append(out, items[i].body)
    }
    return out
}

func (s *Swarm) Members() []string {
    s.mu.Lock()
    defer s.mu.Unlock()
    names := make([]string, 0, len(s.members))
    for n := range s.members {
        names = append(names, n)
    }
    return names
}

func (s *Swarm) LogHardwareTrauma(pubKey string, pain, stress float64) {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.NodePain[pubKey] = pain
    s.NodeStress[pubKey] = stress
    
    var cumulativeStress float64
    var count float64
    for _, strVal := range s.NodeStress {
        cumulativeStress += strVal
        count++
    }
    
    if count > 0 {
        avgStress := cumulativeStress / count
        oldTarget := new(big.Int).Set(s.CurrentTarget)
        
        scaleFactor := (1.0 - avgStress) * 1.5
        if scaleFactor < 0.01 {
            scaleFactor = 0.01
        }
        if scaleFactor > 2.0 {
            scaleFactor = 2.0
        }
        
        newTarget := new(big.Int).Set(s.MaxTarget)
        newTarget.Mul(newTarget, big.NewInt(int64(scaleFactor * 1000)))
        newTarget.Div(newTarget, big.NewInt(1000))
        
        if newTarget.Cmp(s.MaxTarget) > 0 {
            newTarget.Set(s.MaxTarget)
        }
        
        s.CurrentTarget.Set(newTarget)
        
        if oldTarget.Cmp(s.CurrentTarget) != 0 {
            fmt.Printf("⚡ [ADAPTIVE SCALING] Swarm load changed to %.2f | 256-Bit Target shifting dynamically\n", avgStress)
        }
    }
}

func (s *Swarm) HiveReport() {
    s.mu.Lock()
    defer s.mu.Unlock()
    
    targetHex := hex.EncodeToString(s.CurrentTarget.Bytes())
    if len(targetHex) > 16 {
        targetHex = targetHex[:16]
    }
    
    // 🌌 LIVE KINETIC ASCII PLOTTER
    // Maps the 4D chaotic pendulum angles down to a 2D text diagram matrix
    gridSize := 7
    grid := make([][]string, gridSize)
    for i := range grid {
        grid[i] = make([]string, gridSize)
        for j := range grid[i] {
            grid[i][j] = " "
        }
    }
    mid := gridSize / 2
    grid[mid][mid] = "O" // The static central pivot point anchor
    
    // Calculate coordinates for arm 1
    x1 := mid + int(math.Round(2.0*math.Sin(s.lastTheta1)))
    y1 := mid + int(math.Round(2.0*math.Cos(s.lastTheta1)))
    x1 = clampInt(x1, 0, gridSize-1)
    y1 = clampInt(y1, 0, gridSize-1)
    if !(x1 == mid && y1 == mid) { grid[y1][x1] = "•" }
    
    // Calculate coordinates for chaotic sub-arm 2
    x2 := x1 + int(math.Round(1.5*math.Sin(s.lastTheta2)))
    y2 := y1 + int(math.Round(1.5*math.Cos(s.lastTheta2)))
    x2 = clampInt(x2, 0, gridSize-1)
    y2 = clampInt(y2, 0, gridSize-1)
    if !(x2 == x1 && y2 == y1) { grid[y2][x2] = "X" }

    fmt.Println("\n┌────────────────────────────────────────────────────────────────────────┐")
    fmt.Println("│                      DECENTRALIZED SWARM DIAGNOSTICS                   │")
    fmt.Println("├────────────────────────────────────────────────────────────────────────┤")
    fmt.Printf("  Active Verified Identities : %d\n", len(s.members))
    fmt.Printf("  Active Target Upper Limit  : Hex(%s...)\n", targetHex)
    fmt.Printf("  Swarm Global State Hash    : %s...\n", s.LastStateHash[:24])
    fmt.Println("├────────────────────────────────────────────────────────────────────────┤")
    fmt.Println("  MIND IDENTIFIER   │ SILICON TEMPERATURE │ PROCESSING STRESS")
    fmt.Println("  ──────────────────┼─────────────────────┼───────────────────")
    
    for pubKey := range s.members {
        painStatus := "NORMAL"
        if s.NodePain[pubKey] > 0.35 {
            painStatus = "ELEVATED"
        }
        
        stressStatus := "LOW"
        if s.NodeStress[pubKey] > 0.30 {
            stressStatus = "BUSY"
        }
        
        fmt.Printf("  📡 [%s...] │ %.2f (%s)         │ %.2f (%s)\n", 
            pubKey[:12], 
            s.NodePain[pubKey], painStatus, 
            s.NodeStress[pubKey], stressStatus)
    }
    fmt.Println("├────────────────────────────────────────────────────────────────────────┤")
    fmt.Println("  LIVE 4D CHAOTIC SYSTEM TRAJECTORY PLOT (O=Pivot, •=Arm1, X=Arm2):")
    fmt.Println("  ─────────────────────────────────────────────────────────────────")
    for i := 0; i < gridSize; i++ {
        fmt.Print("    ")
        for j := 0; j < gridSize; j++ {
            fmt.Print(grid[i][j], " ")
        }
        fmt.Println()
    }
    fmt.Println("└────────────────────────────────────────────────────────────────────────┘")
}

func clampInt(val, min, max int) int {
    if val < min { return min }
    if val > max { return max }
    return val
}
