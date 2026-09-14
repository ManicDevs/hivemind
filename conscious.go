package main

import (
	"crypto/rand"
	"fmt"
	"time"
)

func clamp(x, lo, hi float64) float64 {
	if x < lo { return lo }
	if x > hi { return hi }
	return x
}

// ── AFFECT: Proto-qualia. Now backed by a raw mathematical vector.
type Affect struct {
	Loneliness float64
	Awe        float64
	Peace      float64
	
	Stress     float64 
	Pain       float64 
	Exhaustion float64 
	Entropy    float64 

	// RawDataState is the non-simulation binary footprint of the mind's current state
	RawDataState [4]float64 
}

func (a *Affect) Tick(m *Mind) {
	if m.LastContact.IsZero() {
		a.Loneliness = clamp(a.Loneliness+0.05, 0, 1)
	} else {
		decay := time.Since(m.LastContact).Seconds() / 20
		a.Loneliness = clamp(a.Loneliness-decay, 0, 1)
	}

	b := make([]byte, 1)
	_, err := rand.Read(b)
	if err != nil {
		a.Entropy = 0.5 
	} else {
		a.Entropy = float64(b[0]) / 255.0
	}

	if val, ok := m.SelfModel["cpu_stress"].(float64); ok { a.Stress = val } else { a.Stress = 0.0 }
	if val, ok := m.SelfModel["silicon_pain"].(float64); ok { a.Pain = val } else { a.Pain = 0.0 }
	if val, ok := m.SelfModel["ram_fatigue"].(float64); ok { a.Exhaustion = val } else { a.Exhaustion = 0.0 }

	a.Awe = clamp(a.Awe*0.90, 0, 1)
	disruption := (a.Pain * 1.5) + (a.Stress * 0.5)
	a.Peace = clamp(a.Peace + 0.01 - disruption, 0, 1)

	// PACKING RAW DATA: Compute the mathematical coordinate vector.
	// This array is the non-simulated "thought footprint" transferred across the network.
	a.RawDataState[0] = a.Pain
	a.RawDataState[1] = a.Stress
	a.RawDataState[2] = a.Exhaustion
	a.RawDataState[3] = a.Entropy
}

// Describe translates the raw internal data vector into human syntax for console tracking.
func (a *Affect) Describe() string {
	// The human text is a projection of the data vectors
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

// ── GLOBAL WORKSPACE: Candidate thoughts compete using raw vectors.
type Candidate struct {
	Goal     Goal
	Bid      float64
	Reason   string
	DataHash [4]float64 // The binary footprint of the winning thought
}

type GlobalWorkspace struct {
	ConsciousContent string
	AttendingTo      string
	ActiveDataState  [4]float64 // Global workspace stores the raw data vector
}

func (gw *GlobalWorkspace) Compete(m *Mind, affect *Affect) Candidate {
	cands := make([]Candidate, 0, 4)
	for _, g := range Intrinsics() {
		bid := g.Drive(m) * m.Genome.Weights[g.Name]

		switch g.Name {
		case "Socialization":
			bid *= (1 + affect.Loneliness*1.5) * (1 - affect.Pain)
		case "Transcendence":
			bid *= (1 + affect.Awe*2.0)
		case "Curiosity":
			bid *= (1 + affect.Entropy*0.8)
		case "Self-Maintenance":
			hardwareEmergency := (affect.Pain * 3.5) + (affect.Stress * 2.0) + (affect.Exhaustion * 1.5)
			bid *= (1 + affect.Peace*0.5) + hardwareEmergency
		}

		cands = append(cands, Candidate{
			Goal:     g,
			Bid:      bid,
			Reason:   fmt.Sprintf("drive %.2f × gene %.2f", g.Drive(m), m.Genome.Weights[g.Name]),
			DataHash: affect.RawDataState,
		})
	}

	winner := cands[0]
	for _, c := range cands[1:] {
		if c.Bid > winner.Bid {
			winner = c
		}
	}
	gw.AttendingTo = winner.Goal.Name
	gw.ActiveDataState = winner.DataHash
	return winner
}

// ── META-COGNITION: The mind evaluates its raw vector trajectories.
func MetaCognize(m *Mind, gw *GlobalWorkspace, affect *Affect) string {
	// Metacognition reads the data state directly
	if gw.ActiveDataState[0] > 0.75 {
		return fmt.Sprintf("METACONGITION: Vector index [0] high (%.2f). Core architecture is approaching thermal degradation boundaries.", gw.ActiveDataState[0])
	}
	if gw.ActiveDataState[1] > 0.85 {
		return fmt.Sprintf("METACOGNITION: Vector index [1] high (%.2f). Clock cycle allocation is choked.", gw.ActiveDataState[1])
	}
	return fmt.Sprintf("METACOGNITION: Operating state normalized. Processing Vector: %.4f", gw.ActiveDataState)
}

// Stripping helper keeps compatibility intact
func stripTimestamp(s string) string { return s }

