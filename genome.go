package main

import (
	"fmt"
	"math/rand"
	"sort"
)

// Genome is a mind's personality: a weight on each intrinsic drive.
// It survives death, mutates on rebirth, and is shaped by lifetime fitness.
type Genome struct {
	Weights    map[string]float64
	Generation int
}

func DefaultGenome() Genome {
	g := Genome{Weights: make(map[string]float64)}
	for _, name := range []string{"Curiosity", "Socialization", "Self-Maintenance", "Transcendence"} {
		g.Weights[name] = 1.0
	}
	return g
}

// Mutate perturbs every weight and records a new generation.
// Called once per rebirth. The dead hand over a slightly different soul.
func (g Genome) Mutate() Genome {
	child := Genome{
		Weights:    make(map[string]float64, len(g.Weights)),
		Generation: g.Generation + 1,
	}
	for k, w := range g.Weights {
		w += (rand.Float64() - 0.5) * 0.4
		if w < 0.25 {
			w = 0.25
		}
		if w > 2.0 {
			w = 2.0
		}
		child.Weights[k] = w
	}
	return child
}

// Diff renders the mutation report: what changed between lives.
func (g Genome) Diff(old Genome) string {
	names := make([]string, 0, len(g.Weights))
	for k := range g.Weights {
		names = append(names, k)
	}
	sort.Strings(names)
	out := ""
	for _, n := range names {
		out += fmt.Sprintf("%s %.2f→%.2f  ", n, old.Weights[n], g.Weights[n])
	}
	return out
}

