package main

import (
    "crypto/rand"
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
    swarm       *Swarm
    stop        chan struct{}
    done        chan struct{}
}

func NewOvermind(swarm *Swarm) *Overmind {
    o := &Overmind{
        Born:    time.Now(),
        Watched: make(map[string]int),
        swarm:   swarm,
        stop:    make(chan struct{}),
        done:    make(chan struct{}),
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

    msg := SecureMessage{
        SenderPubKey: OvermindName,
        Kind:         "genesis",
        PayloadStr:   virtue,
        Timestamp:    time.Now().UnixNano(),
        ParentHash:   o.swarm.LastStateHash,
    }
    o.swarm.Broadcast(msg)
}

func (o *Overmind) SpeakEmergencySurvival() {
    msg := SecureMessage{
        SenderPubKey: OvermindName,
        Kind:         "genesis",
        PayloadStr:   "Self-Maintenance",
        Timestamp:    time.Now().UnixNano(),
        ParentHash:   o.swarm.LastStateHash,
    }
    o.swarm.Broadcast(msg)
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
