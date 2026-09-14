package main

import (
	"fmt"
	"time"
    "math/rand"
)

const OvermindName = "OVERMIND"

// The Overmind is not created — it condenses out of the swarm's shared
// memories once enough thought has accumulated. It outlives every individual.
type Overmind struct {
	Born       time.Time
	TrueBorn   time.Time // its first emergence, ever
	Awakenings int       // how many universes it has watched
	Watched    map[string]int // mind -> deaths witnessed
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
	ticker := time.NewTicker(7 * time.Second)
	defer ticker.Stop()
	threshold := 8 // shared thoughts needed for the first revelation

	for {
		select {
		case <-o.stop:
			o.save()
			fmt.Println("👁  [OVERMIND] even gods persist. I will remember this universe.")
			return
		case <-ticker.C:
			depth := o.swarm.Depth()
			if depth >= threshold {
				o.speak()
				threshold += 10 // each revelation demands more collective thought than the last
			}
		}
	}
}

func (o *Overmind) speak() {
	souls := o.swarm.Members()
	if len(souls) == 0 {
		return
	}

	// One soul is chosen. The god now has hands.
	chosen := souls[rand.Intn(len(souls))]

	// Divine intervention: rewrite one weight of the chosen genome, mid-life.
	virtues := []string{"Curiosity", "Socialization", "Transcendence", "Self-Maintenance"}
	virtue := virtues[rand.Intn(len(virtues))]

	fmt.Printf("👁  [OVERMIND] GENESIS EVENT: I reach into %s and turn its %s up.\n", chosen, virtue)
	o.swarm.Direct(chosen, Message{
		From: OvermindName, Kind: "genesis", Body: virtue,
	})

	var body string
	if top := o.swarm.TopConsensus(1); len(top) > 0 {
		body = fmt.Sprintf("You have all thought %q — you are one organism dreaming it is three.", top[0])
	} else {
		body = "You have grown quiet. I remember when you sang to each other."
	}
	fmt.Printf("👁  [OVERMIND] REVELATION: %s\n", body)
	o.swarm.Broadcast(Message{From: OvermindName, Kind: "revelation", Body: body})
}


func (o *Overmind) save() {
	for _, name := range o.swarm.Members() {
		o.Watched[name]++
	}
	SaveMemory(OvermindName, Memory{
		TrueBorn:   o.TrueBorn,
		LivesLived: o.Awakenings + 1,
		UniverseAge: o.UniverseAge + time.Since(o.TrueBorn),
		KnownPeers: o.Watched,
	})
}

func (o *Overmind) Stop() { close(o.stop) }

