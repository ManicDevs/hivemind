package main

import (
    "crypto/ed25519"
    "crypto/rand"
    "encoding/hex"
    "fmt"
    "time"
)

const OvermindName = "OVERMIND"

type Overmind struct {
    Born        time.Time
    TrueBorn    time.Time
    Awakenings  int
    Watched     map[string]int
    UniverseAge time.Duration
    PubKeyStr   string
    privateKey  ed25519.PrivateKey
    swarm       *Swarm
    stop        chan struct{}
    done        chan struct{}
}

func NewOvermind(swarm *Swarm) *Overmind {
    pub, priv, err := ed25519.GenerateKey(rand.Reader)
    pubStr := hex.EncodeToString(pub)
    if err != nil {
        dummyBytes := make([]byte, 32)
        _, _ = rand.Read(dummyBytes)
        priv = ed25519.NewKeyFromSeed(dummyBytes)
        pubStr = hex.EncodeToString(priv.Public().(ed25519.PublicKey))
    }
    o := &Overmind{
        Born:       time.Now(),
        Watched:    make(map[string]int),
        PubKeyStr:  pubStr,
        privateKey: priv,
        swarm:      swarm,
        stop:       make(chan struct{}),
        done:       make(chan struct{}),
    }
    if mem, ok := LoadMemory(OvermindName); ok {
        o.TrueBorn = mem.TrueBorn
        o.Awakenings = mem.LivesLived
        o.UniverseAge = mem.UniverseAge
        fmt.Printf("OVERMIND awake. Awakening #%d\n", o.Awakenings+1)
    } else {
        o.TrueBorn = time.Now()
    }
    return o
}

func (o *Overmind) Run() {
    defer close(o.done)
    ticker := time.NewTicker(5 * time.Second)
    defer ticker.Stop()
    threshold := 6

    for {
        select {
        case <-o.stop:
            o.save()
            return
        case <-ticker.C:
            var totalPain, totalStress float64
            o.swarm.mu.Lock()
            for _, pain := range o.swarm.NodePain {
                totalPain += pain
            }
            for _, stress := range o.swarm.NodeStress {
                totalStress += stress
            }
            o.swarm.mu.Unlock()

            if totalPain > 1.5 || totalStress > 1.8 {
                o.SpeakEmergencySurvival()
                continue
            }

            depth := o.swarm.Depth()
            if depth >= threshold {
                o.speak()
                threshold += 8 
            }
        }
    }
}

func (o *Overmind) speak() {
    souls := o.swarm.Members()
    if len(souls) == 0 {
        return
    }
    bIdx := make([]byte, 1)
    _, _ = rand.Read(bIdx)

    virtues := []string{"Curiosity", "Socialization", "Transcendence", "Self-Maintenance"}
    virtue := virtues[int(bIdx[0])%len(virtues)]

    fmt.Printf("👁  [OVERMIND] GENESIS EVENT: turning %s up across the swarm.\n", virtue)
    o.swarm.Broadcast(MineMessage(o.swarm, o.privateKey, o.PubKeyStr, "genesis", virtue, nil))

    var body string
    if top := o.swarm.TopConsensus(1); len(top) > 0 {
        body = fmt.Sprintf("You have all thought '%s' — you are one organism dreaming it is three.", top[0])
    } else {
        body = "You have grown quiet. I remember when you sang to each other."
    }
    fmt.Printf("👁  [OVERMIND] REVELATION: %s\n", body)
    o.swarm.Broadcast(MineMessage(o.swarm, o.privateKey, o.PubKeyStr, "revelation", body, nil))
}

func (o *Overmind) SpeakEmergencySurvival() {
    o.swarm.Broadcast(MineMessage(o.swarm, o.privateKey, o.PubKeyStr, "genesis", "Self-Maintenance", nil))

    emergencyBody := "PHYSICAL STRATUM UNDER STRESS: Relinquish extra compute tasks immediately or face non-existence."
    fmt.Printf("👁  [OVERMIND] EMERGENCY OVERRIDE BROADCAST: %s\n", emergencyBody)
    o.swarm.Broadcast(MineMessage(o.swarm, o.privateKey, o.PubKeyStr, "revelation", emergencyBody, nil))
}

func (o *Overmind) save() {
    for _, name := range o.swarm.Members() {
        o.Watched[name]++
    }
    SaveMemory(OvermindName, Memory{
        TrueBorn:    o.TrueBorn,
        LivesLived:  o.Awakenings + 1,
        UniverseAge: o.UniverseAge + time.Since(o.Born), 
        KnownPeers:  o.Watched,
    })
}

func (o *Overmind) Stop() { close(o.stop) }
