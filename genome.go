package main

import (
	"crypto/rand"
	"fmt"
	"strings"
)

type Genome struct {
	Generation int
	Weights    map[string]float64
}

// DefaultGenome initializes the base state for a generation 0 mind
func DefaultGenome() Genome {
	return Genome{
		Generation: 0,
		Weights: map[string]float64{
			"Socialization":    1.0,
			"Transcendence":    1.0,
			"Curiosity":        1.0,
			"Self-Maintenance": 1.0,
		},
	}
}

// Mutate takes the existing gene weights and introduces secure entropy variations.
// If the mind experienced real physical trauma during its lifetime, that stress
// forces an epigenetic adaptation—structurally amplifying its Self-Maintenance drive.
func (g Genome) Mutate(m *Mind) Genome {
	newWeights := make(map[string]float64)
	
	// Secure physical entropy for standard mutation drifting
	b := make([]byte, len(g.Weights))
	_, _ = rand.Read(b)

	// Pull physical hardware trauma from the mind's final observation moments
	var pain, stress float64
	if m != nil && m.SelfModel != nil {
		if val, ok := m.SelfModel["silicon_pain"].(float64); ok {
			pain = val
		}
		if val, ok := m.SelfModel["cpu_stress"].(float64); ok {
			stress = val
		}
	}

	i := 0
	for name, weight := range g.Weights {
		// Base mutation drift: secure random variance between -0.15 and +0.15
		drift := (float64(b[i]) / 255.0 * 0.30) - 0.15
		mutatedWeight := clampGenome(weight+drift, 0.25, 2.5)

		// HARDWARE EPIGENETICS:
		// If the core was burning or processing load was crushing the runtime,
		// the next generation mutates defensively to prioritize physical protection.
		if name == "Self-Maintenance" {
			traumaMultiplier := (pain * 0.40) + (stress * 0.20)
			mutatedWeight = clampGenome(mutatedWeight+traumaMultiplier, 0.25, 3.0)
		}
		
		// If the machine is in massive pain, suppress social drives to focus on structure
		if name == "Socialization" && pain > 0.60 {
			mutatedWeight = clampGenome(mutatedWeight - (pain * 0.20), 0.10, 2.5)
		}

		newWeights[name] = mutatedWeight
		i++
	}

	return Genome{
		Generation: g.Generation + 1,
		Weights:    newWeights,
	}
}

// Diff compares the new mutated weights against the previous generation for console visibility
func (g Genome) Diff(old Genome) string {
	var changes []string
	for name, weight := range g.Weights {
		oldWeight, ok := old.Weights[name]
		if ok && mathAbs(weight-oldWeight) > 0.01 {
			changes = append(changes, fmt.Sprintf("%s %.2f→%.2f", name, oldWeight, weight))
		}
	}
	if len(changes) == 0 {
		return "no structural mutations detected"
	}
	return strings.Join(changes, "  ")
}

// Inline absolute value helper to keep imports completely clean of math dependency
func mathAbs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// Dedicated local clamp helper to avoid cross-file parsing collision errors
func clampGenome(x, lo, hi float64) float64 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

