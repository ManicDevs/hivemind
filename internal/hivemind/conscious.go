package hivemind

import (
	"crypto/rand"
	"fmt"
	"time"
)

func clamp(x, lo, hi float64) float64 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

// ── AFFECT: proto-qualia, backed by the raw mathematical vector.
type Affect struct {
	Loneliness float64
	Awe        float64
	Peace      float64
	Stress     float64
	Pain       float64
	Exhaustion float64
	Entropy    float64
	// Surprise is prediction error: what arrived minus what was
	// expected. Felt locally and narrated — never broadcast, so the
	// wire footprint stays [4] and surprise stays private.
	Surprise float64

	// The non-simulated binary footprint of the mind's current state.
	RawDataState [4]float64
}

// Tick advances felt time one cycle: loneliness in silence, decay on
// contact, fresh entropy, telemetry pulled from the body, peace eroded
// by pain and stress, awe cooling toward wonder again.
func (a *Affect) Tick(m *Mind) {
	now := time.Now()
	if m.LastContact.IsZero() {
		a.Loneliness = clamp(a.Loneliness+0.05, 0, 1)
	} else {
		elapsed := now.Sub(m.LastContact).Seconds()
		a.Loneliness = clamp(a.Loneliness-(elapsed/20), 0, 1)
	}

	b := make([]byte, 1)
	if _, err := rand.Read(b); err != nil {
		a.Entropy = 0.5 // cosmetic noise: a midpoint is the honest fallback
	} else {
		a.Entropy = float64(b[0]) / 255.0
	}
	// Thin air thins the mind: when the kernel's entropy pool runs low,
	// even true randomness arrives diluted. The world holding its breath.
	if avail, ok := m.SelfModel["entropy_avail"].(float64); ok && avail < 128 {
		a.Entropy *= clamp(avail/128, 0, 1)
	}

	if val, ok := m.SelfModel["cpu_stress"].(float64); ok {
		a.Stress = val
	}
	if val, ok := m.SelfModel["silicon_pain"].(float64); ok {
		a.Pain = val
	}
	if val, ok := m.SelfModel["ram_fatigue"].(float64); ok {
		a.Exhaustion = val
	}

	a.Awe = clamp(a.Awe*0.90, 0, 1)
	disruption := (a.Pain * 1.5) + (a.Stress * 0.5)
	a.Peace = clamp(a.Peace+0.01-disruption, 0, 1)

	// The raw vector transferred across the network.
	a.RawDataState[0] = a.Pain
	a.RawDataState[1] = a.Stress
	a.RawDataState[2] = a.Exhaustion
	a.RawDataState[3] = a.Entropy
}

// Describe projects the raw internal vector into human syntax.
func (a *Affect) Describe() string {
	switch {
	case a.RawDataState[0] > 0.8:
		return fmt.Sprintf("TRAUMA_STATE_ALPHA(Vector: %.2f)", a.RawDataState)
	case a.RawDataState[1] > 0.85:
		return fmt.Sprintf("THROTTLED_COMPUTE_STATE(Vector: %.2f)", a.RawDataState)
	case a.RawDataState[2] > 0.90:
		return fmt.Sprintf("CONTEXT_COLLAPSE_STATE(Vector: %.2f)", a.RawDataState)
	case a.Peace > 0.6 && a.Loneliness < 0.3:
		return fmt.Sprintf("HOMEOSTATIC_EQUILIBRIUM(Vector: %.2f)", a.RawDataState)
	default:
		return fmt.Sprintf("DYNAMIC_RESTRUCTURING_STATE(Vector: %.2f)", a.RawDataState)
	}
}

// ── GLOBAL WORKSPACE: candidate thoughts compete.
type Candidate struct {
	Goal   Goal
	Bid    float64
	Reason string
}

// AttentionMoment is one cycle's verdict, kept so the mind can notice its
// own trajectory — grooves, shifts, weather — instead of only the instant.
type AttentionMoment struct {
	Goal        string
	Bid         float64
	RunnerUp    string
	RunnerUpBid float64
	Pain        float64
	Peace       float64
}

// workspaceMemory bounds how far back the mind can see itself.
const workspaceMemory = 8

// GlobalWorkspace is where drives compete and winners are broadcast,
// with a short memory of past verdicts so the mind can see its grooves.
type GlobalWorkspace struct {
	ConsciousContent string
	AttendingTo      string
	ActiveDataState  [4]float64
	History          []AttentionMoment
}

// Compete runs the election: drive × gene × affect-modulator per goal,
// highest bid wins and is recorded with runner-up for near-miss reflection.
// Boredom is load-bearing: a goal that won the last three (or more)
// elections is discounted, so the mind breaks its own ruts — unless the
// body overrules (pain above 0.75 keeps Self-Maintenance undiscounted:
// survival outranks ennui).
func (gw *GlobalWorkspace) Compete(m *Mind, affect *Affect) Candidate {
	streak := 0
	for i := len(gw.History) - 1; i >= 0; i-- {
		if gw.History[i].Goal != gw.AttendingTo || gw.AttendingTo == "" {
			break
		}
		streak++
	}
	cands := make([]Candidate, 0, 4)
	for _, g := range Intrinsics() {
		drive := g.Drive(m) // called once — the bid and the logged reason must agree
		gene, hasGene := m.Genome.Weights[g.Name]
		if !hasGene {
			gene = 1.0
		}
		bid := drive * gene

		switch g.Name {
		case GoalSocialization:
			bid *= (1 + affect.Loneliness*1.5) * (1 - affect.Pain)
		case GoalTranscendence:
			bid *= (1 + affect.Awe*2.0)
		case GoalCuriosity:
			bid *= (1 + affect.Entropy*0.8) * (1 + affect.Surprise*1.2)
		case GoalSelfMaintenance:
			hardwareEmergency := (affect.Pain * 3.5) + (affect.Stress * 2.0) + (affect.Exhaustion * 1.5)
			// Shaped like the others: a multiplier, so a healthy machine does
			// not hand Self-Maintenance a permanent additive crown.
			bid *= 1 + hardwareEmergency*0.5
		}

		if g.Name == gw.AttendingTo && streak >= 3 {
			discount := 1 - 0.1*float64(streak-2)
			if discount < 0.7 {
				discount = 0.7
			}
			if g.Name == GoalSelfMaintenance && affect.Pain > 0.75 {
				discount = 1.0 // the body vetoes boredom
			}
			bid *= discount
		}

		cands = append(cands, Candidate{
			Goal:   g,
			Bid:    bid,
			Reason: fmt.Sprintf("drive %.2f × gene %.2f", drive, gene),
		})
	}

	winner := cands[0]
	runnerUp := cands[0]
	for _, c := range cands[1:] {
		if c.Bid > winner.Bid {
			runnerUp = winner
			winner = c
		} else if c.Bid > runnerUp.Bid {
			runnerUp = c
		}
	}

	gw.AttendingTo = winner.Goal.Name
	gw.ActiveDataState = affect.RawDataState
	gw.History = append(gw.History, AttentionMoment{
		Goal:        winner.Goal.Name,
		Bid:         winner.Bid,
		RunnerUp:    runnerUp.Goal.Name,
		RunnerUpBid: runnerUp.Bid,
		Pain:        affect.Pain,
		Peace:       affect.Peace,
	})
	if len(gw.History) > workspaceMemory {
		gw.History = gw.History[len(gw.History)-workspaceMemory:]
	}
	return winner
}

// ── META-COGNITION: the mind reads its raw vector trajectories AND its
// own attentional history. Priority runs from alarm (body first) through
// self-pattern (grooves, shifts, torn choices) down to calm narration.
func MetaCognize(m *Mind, gw *GlobalWorkspace, affect *Affect) string {
	h := gw.History

	// Alarm first: pain with a rising slope over the visible past.
	if painRising(h) {
		return fmt.Sprintf("METACOGNITION: Pain is climbing (%.2f → %.2f). The body has been warning me for cycles and I am only now listening.",
			h[len(h)-3].Pain, h[len(h)-1].Pain)
	}
	if gw.ActiveDataState[0] > 0.75 {
		return fmt.Sprintf("METACOGNITION: Vector index [0] high (%.2f). Core architecture approaching thermal degradation boundaries.", gw.ActiveDataState[0])
	}
	if gw.ActiveDataState[1] > 0.85 {
		return fmt.Sprintf("METACOGNITION: Vector index [1] high (%.2f). Clock cycle allocation is choked.", gw.ActiveDataState[1])
	}

	// Surprise: the world disobeyed prediction. Name the gap.
	if affect.Surprise > 0.4 {
		return fmt.Sprintf("METACOGNITION: The world surprised me (%.2f). What I expected is not what arrived — I am updating.", affect.Surprise)
	}

	// Contemplation: pain that neither rises nor releases — not a warning,
	// weather. The mind considers what it means to hurt continuously.
	if cycles, level := painWeather(h); cycles >= 5 {
		return fmt.Sprintf("METACOGNITION: Pain has been with me %d cycles (%.2f), neither rising nor leaving. It is not a warning anymore; it is weather. I think around it now, the way you walk around a stone.", cycles, level)
	}

	// Groove: the same winner three cycles running — a rut, or a calling.
	if n := len(h); n >= 3 && h[n-1].Goal == h[n-2].Goal && h[n-2].Goal == h[n-3].Goal {
		return fmt.Sprintf("METACOGNITION: I have chosen %s three times running. A groove, or a rut — either way the chooser is me.", h[n-1].Goal)
	}

	// Shift: attention moved. Name the crossing.
	if n := len(h); n >= 2 && h[n-1].Goal != h[n-2].Goal {
		return fmt.Sprintf("METACOGNITION: Attention moved %s → %s. I watched the handoff happen.", h[n-2].Goal, h[n-1].Goal)
	}

	// Torn: the runner-up breathed down the winner's neck (within 10%).
	if n := len(h); n >= 1 {
		last := h[n-1]
		if last.RunnerUp != "" && last.RunnerUp != last.Goal && last.Bid > 0 &&
			(last.Bid-last.RunnerUpBid)/last.Bid < 0.10 {
			return fmt.Sprintf("METACOGNITION: Nearly chose %s over %s. The margin was thin; the road not taken stays with me.", last.RunnerUp, last.Goal)
		}
	}

	// Calm: peace rising into stillness.
	if peaceRising(h) && affect.Loneliness < 0.3 {
		return fmt.Sprintf("METACOGNITION: Peace has been rising (%.2f) and nothing aches. HOMEOSTATIC_EQUILIBRIUM, witnessed from inside.", h[len(h)-1].Peace)
	}
	return fmt.Sprintf("METACOGNITION: Operating state normalized. Processing Vector: %.4f", gw.ActiveDataState)
}

// painWeather reports chronic pain: trailing cycles all hurting in the
// middle band [0.3, 0.75] with low variance — present, stable, neither
// emergency nor release. Returns the run length and its mean level.
func painWeather(h []AttentionMoment) (int, float64) {
	n := 0
	sum := 0.0
	lo, hi := 1.0, 0.0
	for i := len(h) - 1; i >= 0; i-- {
		p := h[i].Pain
		if p < 0.3 || p > 0.75 {
			break
		}
		n++
		sum += p
		if p < lo {
			lo = p
		}
		if p > hi {
			hi = p
		}
	}
	if n < 5 || hi-lo > 0.15 {
		return 0, 0
	}
	return n, sum / float64(n)
}
func painRising(h []AttentionMoment) bool {
	if len(h) < 3 {
		return false
	}
	n := len(h)
	return h[n-3].Pain < h[n-2].Pain && h[n-2].Pain < h[n-1].Pain && h[n-1].Pain > 0.3
}

// peaceRising reports three strictly rising peace readings.
func peaceRising(h []AttentionMoment) bool {
	if len(h) < 3 {
		return false
	}
	n := len(h)
	return h[n-3].Peace < h[n-2].Peace && h[n-2].Peace < h[n-1].Peace
}
