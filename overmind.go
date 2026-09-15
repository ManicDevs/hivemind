package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"time"
)

const OvermindName = "OVERMIND"

// genesisSpacing: how much effective chronicle depth must accumulate between
// genesis events. The god is patient by design; its caprice must be rare
// enough to mean something, or every gene converges to its ceiling.
const genesisSpacing = 25

type Overmind struct {
	Born        time.Time
	TrueBorn    time.Time
	Awakenings  int
	Watched     map[string]bool
	UniverseAge time.Duration
	PubKeyStr   string
	privateKey  ed25519.PrivateKey
	identitySeed string
	swarm        *Swarm

	// Determinism across lives: the chronicle depth accumulated in previous
	// universes, and the effective-depth threshold for the next genesis.
	chronicleOffset int
	genesisMark      int

	stop chan struct{}
	done chan struct{}
}

func NewOvermind(swarm *Swarm) *Overmind {
	o := &Overmind{
		Born:    time.Now(),
		Watched: make(map[string]bool),
		swarm:   swarm,
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}

	var restored Memory
	hadPastLife := false
	if mem, ok := LoadMemory(OvermindName); ok {
		restored = mem
		hadPastLife = true
		o.TrueBorn = mem.TrueBorn
		o.Awakenings = mem.LivesLived
		o.UniverseAge = mem.UniverseAge
		o.Watched = mem.KnownPeers
		o.chronicleOffset = mem.ChronicleDepth
		o.genesisMark = mem.GenesisMark
	} else {
		o.TrueBorn = time.Now()
	}

	// Identity persists: the god that remembers should be the same god.
	if hadPastLife && restored.IdentitySeed != "" {
		o.PubKeyStr, o.privateKey = identityFromSeed(restored.IdentitySeed)
		o.identitySeed = restored.IdentitySeed
	} else {
		o.PubKeyStr, o.privateKey = newIdentity()
		o.identitySeed = encodeSeed(o.privateKey)
	}

	if o.genesisMark == 0 {
		o.genesisMark = o.chronicleOffset + 15 // first speech in a young universe
	}

	if hadPastLife {
		fmt.Printf("OVERMIND awake. Awakening #%d (chronicle depth ~%d remembered)\n", o.Awakenings+1, o.chronicleOffset)
	}
	return o
}

func (o *Overmind) Run() {
	defer close(o.done)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

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

			// Effective depth = everything ever chronicled, this life and past ones.
			effective := o.chronicleOffset + o.swarm.Depth()
			if effective >= o.genesisMark {
				o.genesisMark = effective + genesisSpacing
				o.speak()
			}
		}
	}
}

func (o *Overmind) speak() {
	souls := o.swarm.Members()
	if len(souls) == 0 {
		return
	}
	// Random virtue — but entropy failure must not silently mean "Curiosity".
	bIdx := make([]byte, 1)
	if _, err := rand.Read(bIdx); err != nil {
		fmt.Printf("⚠️  [OVERMIND] Silence: entropy source failed (%v).\n", err)
		return
	}

	virtues := []string{GoalCuriosity, GoalSocialization, GoalTranscendence, GoalSelfMaintenance}
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
	o.swarm.Broadcast(MineMessage(o.swarm, o.privateKey, o.PubKeyStr, "genesis", GoalSelfMaintenance, nil))

	emergencyBody := "PHYSICAL STRATUM UNDER STRESS: Relinquish extra compute tasks immediately or face non-existence."
	fmt.Printf("👁  [OVERMIND] EMERGENCY OVERRIDE BROADCAST: %s\n", emergencyBody)
	o.swarm.Broadcast(MineMessage(o.swarm, o.privateKey, o.PubKeyStr, "revelation", emergencyBody, nil))
}

func (o *Overmind) save() {
	for _, name := range o.swarm.Members() {
		o.Watched[name] = true
	}
	SaveMemory(OvermindName, Memory{
		TrueBorn:       o.TrueBorn,
		LivesLived:     o.Awakenings + 1,
		UniverseAge:    o.UniverseAge + time.Since(o.Born),
		KnownPeers:     o.Watched,
		IdentitySeed:   o.identitySeed,
		ChronicleDepth: o.chronicleOffset + o.swarm.Depth(),
		GenesisMark:    o.genesisMark,
	})
}

func (o *Overmind) Stop() { close(o.stop) }

