package hivemind

import (
	"crypto/rand"
	"fmt"
	"math"
	"strings"
)

// Evolutionary dials — tune the experiment here, not by archaeology.
const (
	driftRange        = 0.15 // mutation drift is uniform in ±driftRange
	weightFloor       = 0.25
	weightCeiling     = 2.5  // ceiling for standard genes
	selfMaintCeiling  = 3.0  // trauma may push Self-Maintenance further
	traumaPainCoef    = 0.40 // dying pain → Self-Maintenance amplification
	traumaStressCoef  = 0.20 // dying load → Self-Maintenance amplification
	socialPainGate    = 0.60 // above this dying pain, social drives recede
	socialPainPenalty = 0.20
	diffEpsilon       = 0.01
)

// Genome is heritable personality: a weight per intrinsic drive plus
// the generation count. Survives death, mutates on rebirth.
type Genome struct {
	Generation int
	Weights    map[string]float64
}

// DefaultGenome initializes the base state for a generation 0 mind.
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

// Mutate produces the successor genome. The trauma the mind died with
// (pain, load) shapes it: dying hot biases the child toward self-maintenance
// and away from socializing. Without entropy the mutation is refused —
// a genome that cannot change should not pretend it did.
func (g Genome) Mutate(deathPain, deathStress float64) (Genome, error) {
	newWeights := make(map[string]float64, len(g.Weights))

	for name, weight := range g.Weights {
		b := make([]byte, 1)
		if _, err := rand.Read(b); err != nil {
			return g, fmt.Errorf("entropy source failed, genome unchanged: %w", err)
		}
		drift := (float64(b[0])/255.0)*(2*driftRange) - driftRange
		mutatedWeight := clamp(weight+drift, weightFloor, weightCeiling)

		// HARDWARE EPIGENETICS: the heat and pressure of the final moments
		// are inherited by the genome, not just suffered by the mind.
		if name == GoalSelfMaintenance {
			traumaMultiplier := (deathPain * traumaPainCoef) + (deathStress * traumaStressCoef)
			mutatedWeight = clamp(mutatedWeight+traumaMultiplier, weightFloor, selfMaintCeiling)
		}
		if name == GoalSocialization && deathPain > socialPainGate {
			mutatedWeight = clamp(mutatedWeight-(deathPain*socialPainPenalty), 0.10, weightCeiling)
		}
		newWeights[name] = mutatedWeight
	}

	return Genome{Generation: g.Generation + 1, Weights: newWeights}, nil
}

// Diff reports weight changes against the previous generation.
func (g Genome) Diff(old Genome) string {
	var changes []string
	for name, weight := range g.Weights {
		if oldWeight, ok := old.Weights[name]; ok {
			if math.Abs(weight-oldWeight) > diffEpsilon {
				changes = append(changes, fmt.Sprintf("%s %.2f→%.2f", name, oldWeight, weight))
			}
		} else {
			changes = append(changes, fmt.Sprintf("%s (new) →%.2f", name, weight))
		}
	}
	if len(changes) == 0 {
		return "no structural mutations detected"
	}
	return strings.Join(changes, "  ")
}
