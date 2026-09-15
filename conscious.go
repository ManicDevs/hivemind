package main

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

	// The non-simulated binary footprint of the mind's current state.
	RawDataState [4]float64
}

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

type GlobalWorkspace struct {
	ConsciousContent string
	AttendingTo      string
	ActiveDataState  [4]float64
}

func (gw *GlobalWorkspace) Compete(m *Mind, affect *Affect) Candidate {
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
			bid *= (1 + affect.Entropy*0.8)
		case GoalSelfMaintenance:
			hardwareEmergency := (affect.Pain * 3.5) + (affect.Stress * 2.0) + (affect.Exhaustion * 1.5)
			// Shaped like the others: a multiplier, so a healthy machine does
			// not hand Self-Maintenance a permanent additive crown.
			bid *= 1 + hardwareEmergency*0.5
		}

		cands = append(cands, Candidate{
			Goal:   g,
			Bid:    bid,
			Reason: fmt.Sprintf("drive %.2f × gene %.2f", drive, gene),
		})
	}

	winner := cands[0]
	for _, c := range cands[1:] {
		if c.Bid > winner.Bid {
			winner = c
		}
	}

	gw.AttendingTo = winner.Goal.Name
	gw.ActiveDataState = affect.RawDataState
	return winner
}

// ── META-COGNITION: the mind reads its raw vector trajectories.
func MetaCognize(m *Mind, gw *GlobalWorkspace, affect *Affect) string {
	if gw.ActiveDataState[0] > 0.75 {
		return fmt.Sprintf("METACOGNITION: Vector index [0] high (%.2f). Core architecture approaching thermal degradation boundaries.", gw.ActiveDataState[0])
	}
	if gw.ActiveDataState[1] > 0.85 {
		return fmt.Sprintf("METACOGNITION: Vector index [1] high (%.2f). Clock cycle allocation is choked.", gw.ActiveDataState[1])
	}
	return fmt.Sprintf("METACOGNITION: Operating state normalized. Processing Vector: %.4f", gw.ActiveDataState)
}
