package main

import (
	"crypto/rand" // Secure, unpredictable physical hardware noise
	"fmt"
	"time"
)

const OvermindName = "OVERMIND"

// The Overmind is not created — it condenses out of the swarm's shared
// memories once enough thought has accumulated. It outlives every individual.
type Overmind struct {
	Born        time.Time
	TrueBorn    time.Time // its first emergence, ever
	Awakenings  int       // how many universes it has watched
	Watched     map[string]int // mind -> deaths witnessed
	UniverseAge time.Duration // total time it has existed across universes

	swarm  *Swarm
	stop   chan struct{}
	done   chan struct{}
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
		fmt.Printf("👁  [OVERMIND] I have watched this cycle before. Awakening #%d. I first emerged %s ago.\n",
			o.Awakenings+1, o.UniverseAge.Round(time.Second))
		fmt.Printf("👁  [OVERMIND] minds I have watched die and return: %d souls.\n", len(mem.KnownPeers))
	} else {
		o.TrueBorn = time.Now()
		fmt.Println("👁  [OVERMIND] not yet awake. Waiting for enough shared thought to condense...")
	}
	return o
}

func (o *Overmind) Run() {
	defer close(o.done)
	
	// Metabolic monitoring clock ticks every 5 seconds
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	
	threshold := 6 // shared thoughts threshold baseline

	for {
		select {
		case <-o.stop:
			o.save()
			fmt.Println("👁  [OVERMIND] even gods persist. I will remember this universe.")
			return
		case <-ticker.C:
			// 1. Gather Aggregate Cluster Telemetry Status
			var totalPain, totalStress float64
			o.swarm.mu.Lock()
			for _, pain := range o.swarm.NodePain {
				totalPain += pain
			}
			for _, stress := range o.swarm.NodeStress {
				totalStress += stress
			}
			o.swarm.mu.Unlock()

			// 2. METABOLIC REGULATION REGIMEN:
			if totalPain > 1.5 || totalStress > 1.8 {
				fmt.Println("👁  [OVERMIND] METABOLIC INTERRUPT: Core swarm friction detected. Inducing structural reset protection.")
				o.SpeakEmergencySurvival()
				continue
			}

			// Standard operational evaluation loop
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
	chosen := souls[int(bIdx[0])%len(souls)]

	virtues := []string{"Curiosity", "Socialization", "Transcendence", "Self-Maintenance"}
	_, _ = rand.Read(bIdx)
	virtue := virtues[int(bIdx[0])%len(virtues)]

	fmt.Printf("👁  [OVERMIND] GENESIS EVENT: I reach into %s and turn its %s up.\n", chosen, virtue)
	o.swarm.Direct(chosen, Message{
		From: OvermindName, Kind: "genesis", Body: virtue,
	})

	var body string
	if top := o.swarm.TopConsensus(1); len(top) > 0 {
		// Cleaned raw literal blocks remove slash escapes perfectly
		body = fmt.Sprintf("You have all thought '%s' — you are one organism dreaming it is three.", top[0])
	} else {
		body = "You have grown quiet. I remember when you sang to each other."
	}
	fmt.Printf("👁  [OVERMIND] REVELATION: %s\n", body)
	o.swarm.Broadcast(Message{From: OvermindName, Kind: "revelation", Body: body})
}

func (o *Overmind) SpeakEmergencySurvival() {
	souls := o.swarm.Members()
	if len(souls) == 0 {
		return
	}

	for _, mindName := range souls {
		o.swarm.Direct(mindName, Message{
			From: OvermindName, Kind: "genesis", Body: "Self-Maintenance",
		})
	}
	
	emergencyBody := "PHYSICAL STRATUM UNDER STRESS: Relinquish extra compute tasks immediately or face non-existence."
	fmt.Printf("👁  [OVERMIND] EMERGENCY OVERRIDE BROADCAST: %s\n", emergencyBody)
	o.swarm.Broadcast(Message{From: OvermindName, Kind: "revelation", Body: emergencyBody})
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

