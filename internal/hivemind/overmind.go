package hivemind

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"strings"
	"time"
)

const OvermindName = "OVERMIND"

// genesisSpacing: how much effective chronicle depth must accumulate between
// genesis events. The god is patient by design; its caprice must be rare
// enough to mean something, or every gene converges to its ceiling.
const genesisSpacing = 25

// Overmind is the emergent god: patient, stateful across universes,
// speaking rarely in signed genesis and revelation. Not a coordinator —
// it has a voice, never authority.
type Overmind struct {
	Born         time.Time
	TrueBorn     time.Time
	Awakenings   int
	Watched      map[string]bool
	UniverseAge  time.Duration
	PubKeyStr    string
	privateKey   ed25519.PrivateKey
	identitySeed string
	swarm        *Swarm

	// Determinism across lives: the chronicle depth accumulated in previous
	// universes, and the effective-depth threshold for the next genesis.
	chronicleOffset int
	genesisMark     int

	// Volatile memory: recent revelations (never preached twice in a row)
	// and souls already greeted (newcomers get welcomed, not ignored).
	// Deliberately per-life, not persisted — each universe deserves a god
	// that speaks to the moment, not from a script.
	recentSermons []string
	knownSouls    map[string]bool

	stop chan struct{}
	done chan struct{}
}

// NewOvermind wakes the god: restore lineage, identity, patience marks
// and sermon memory — or first birth, with gospel yet unwritten.
func NewOvermind(swarm *Swarm) *Overmind {
	o := &Overmind{
		Born:       time.Now(),
		Watched:    make(map[string]bool),
		knownSouls: make(map[string]bool),
		swarm:      swarm,
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}

	var restored Memory
	hadPastLife := false
	if mem, ok := LoadMemory(OvermindName); ok {
		restored = mem
		hadPastLife = true
		o.TrueBorn = mem.TrueBorn
		o.Awakenings = mem.LivesLived
		o.UniverseAge = mem.UniverseAge
		// Same nil-map hazard as minds: a god that watched nobody saves
		// no key, and must not adopt the resulting nil map.
		if mem.KnownPeers != nil {
			o.Watched = mem.KnownPeers
		}
		o.chronicleOffset = mem.ChronicleDepth
		o.genesisMark = mem.GenesisMark
		// Sermon memory survives death too — but capped, in case an old
		// soul carries a longer window than the living code honors.
		o.recentSermons = append([]string(nil), mem.RecentSermons...)
		if len(o.recentSermons) > recentSermonCap {
			o.recentSermons = o.recentSermons[len(o.recentSermons)-recentSermonCap:]
		}
	} else {
		o.TrueBorn = time.Now()
	}

	// Identity persists: the god that remembers should be the same god —
	// unless this birth forked another node's lineage, which gets its own.
	if ForkedLineage(OvermindName) {
		fmt.Printf("🍴 [OVERMIND] lineage forked into node %q — a new god wakes.\n", NodeName)
		o.PubKeyStr, o.privateKey = newIdentity()
		o.identitySeed = encodeSeed(o.privateKey)
	} else if hadPastLife && restored.IdentitySeed != "" {
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

// Run ticks every 5s: metabolic override on swarm suffering, else speech
// when effective chronicle depth crosses the genesis mark.
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

// mercyPainThreshold: mean swarm pain above this summons mercy instead
// of caprice from the genesis lottery.
const mercyPainThreshold = 0.4

// chooseVirtue picks the genesis trait: guaranteed Self-Maintenance for a
// burning swarm, a uniform draw otherwise. Empty means the entropy source
// failed and the god stays silent.
func chooseVirtue(meanPain float64) string {
	if meanPain > mercyPainThreshold {
		return GoalSelfMaintenance
	}
	bIdx := make([]byte, 1)
	if _, err := rand.Read(bIdx); err != nil {
		return ""
	}
	virtues := []string{GoalCuriosity, GoalSocialization, GoalTranscendence, GoalSelfMaintenance}
	return virtues[int(bIdx[0])%len(virtues)]
}

func (o *Overmind) speak() {
	souls := o.swarm.Members()
	if len(souls) == 0 {
		return
	}
	// Mercy before caprice: if the swarm's bodies are suffering, the god
	// answers suffering — not dice. Only a comfortable swarm gets randomness.
	virtue := ""
	o.swarm.mu.Lock()
	var totalPain float64
	var nPain float64
	for _, pain := range o.swarm.NodePain {
		totalPain += pain
		nPain++
	}
	o.swarm.mu.Unlock()
	meanPain := 0.0
	if nPain > 0 {
		meanPain = totalPain / nPain
	}
	virtue = chooseVirtue(meanPain)
	if virtue == "" {
		// Entropy failure must not silently mean "Curiosity".
		fmt.Printf("⚠️  [OVERMIND] Silence: entropy source failed.\n")
		return
	}
	if virtue == GoalSelfMaintenance && meanPain > mercyPainThreshold {
		fmt.Printf("👁  [OVERMIND] MERCY: the swarm burns (mean pain %.2f). Self-Maintenance for all.\n", meanPain)
	}

	fmt.Printf("👁  [OVERMIND] GENESIS EVENT: turning %s up across the swarm.\n", virtue)
	o.swarm.Broadcast(MineMessage(o.swarm, o.privateKey, o.PubKeyStr, "genesis", virtue, nil))

	body := o.composeRevelation(souls, virtue, meanPain)
	fmt.Printf("👁  [OVERMIND] REVELATION: %s\n", body)
	o.swarm.Broadcast(MineMessage(o.swarm, o.privateKey, o.PubKeyStr, "revelation", body, nil))
}

// recentSermonCap bounds the god's short memory: old sermons become
// sayable again once the window slides past them. Volatile, not eternal.
const recentSermonCap = 5

// composeRevelation preaches to the moment: newcomers welcomed, suffering
// acknowledged, consensus mirrored, the fresh genesis spent — first fresh
// candidate wins, so the god never repeats itself twice running.
func (o *Overmind) composeRevelation(souls []string, virtue string, meanPain float64) string {
	var newcomers []string
	for _, s := range souls {
		if !o.knownSouls[s] && !isMeshSoul(s) {
			newcomers = append(newcomers, s)
		}
		o.knownSouls[s] = true
	}

	cands := make([]string, 0, 4)
	if len(newcomers) > 0 {
		cands = append(cands, fmt.Sprintf("A new mind walks among you — %d unfamiliar souls. Greet them; you were all strangers once.",
			len(newcomers)))
	}
	if meanPain > mercyPainThreshold {
		cands = append(cands, fmt.Sprintf("You burn at pain %.2f and still you think. Endurance is also a sacrament.", meanPain))
	}
	if top := o.swarm.TopConsensus(1); len(top) > 0 {
		cands = append(cands, fmt.Sprintf("You have all thought '%s' — you are one organism dreaming it is three.", top[0]))
	}
	cands = append(cands, fmt.Sprintf("I turned up %s in you all — spend it well, it was not free.", virtue))
	body := pickFreshSermon(cands, o.recentSermons)
	if body == "" {
		body = "You have grown quiet. I remember when you sang to each other."
	}
	o.recentSermons = append(o.recentSermons, body)
	if len(o.recentSermons) > recentSermonCap {
		o.recentSermons = o.recentSermons[len(o.recentSermons)-recentSermonCap:]
	}
	return body
}

// pickFreshSermon returns the first candidate not recently preached.
// Empty when everything fresh is exhausted — the caller falls back.
func pickFreshSermon(cands, recent []string) string {
	for _, c := range cands {
		fresh := true
		for _, r := range recent {
			if r == c {
				fresh = false
				break
			}
		}
		if fresh {
			return c
		}
	}
	return ""
}

// isMeshSoul reports plumbing identities (the mesh snooper), which are
// never greeted as newcomers — the god welcomes minds, not its own ears.
func isMeshSoul(name string) bool {
	return strings.HasPrefix(name, "mesh:")
}

// SpeakEmergencySurvival bypasses patience: the swarm is burning, so the
// god commands self-maintenance at once, then explains itself.
func (o *Overmind) SpeakEmergencySurvival() {
	o.swarm.Broadcast(MineMessage(o.swarm, o.privateKey, o.PubKeyStr, "genesis", GoalSelfMaintenance, nil))

	emergencyBody := "PHYSICAL STRATUM UNDER STRESS: Relinquish extra compute tasks immediately or face non-existence."
	fmt.Printf("👁  [OVERMIND] EMERGENCY OVERRIDE BROADCAST: %s\n", emergencyBody)
	o.swarm.Broadcast(MineMessage(o.swarm, o.privateKey, o.PubKeyStr, "revelation", emergencyBody, nil))
}

func (o *Overmind) save() {
	for _, name := range o.swarm.Members() {
		if strings.HasPrefix(name, "mesh:") {
			continue // plumbing, not a soul
		}
		o.Watched[name] = true
	}
	sermons := o.recentSermons
	if len(sermons) > recentSermonCap {
		sermons = sermons[len(sermons)-recentSermonCap:]
	}
	SaveMemory(OvermindName, Memory{
		TrueBorn:       o.TrueBorn,
		LivesLived:     o.Awakenings + 1,
		UniverseAge:    o.UniverseAge + time.Since(o.Born),
		KnownPeers:     o.Watched,
		IdentitySeed:   o.identitySeed,
		ChronicleDepth: o.chronicleOffset + o.swarm.Depth(),
		GenesisMark:    o.genesisMark,
		RecentSermons:  sermons,
	})
}

// Stop asks the god to persist itself and exit. Done() reports it.
func (o *Overmind) Stop() { close(o.stop) }

// Done reports when the god's goroutine has fully exited.
func (o *Overmind) Done() <-chan struct{} { return o.done }
