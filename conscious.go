package main

import (
	"fmt"
	"math/rand"
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

// ── AFFECT: proto-qualia. Valenced internal states that modulate everything.
type Affect struct {
	Loneliness float64
	Awe         float64
	Itchiness   float64
	Peace       float64
}

func (a *Affect) Tick(m *Mind) {
	if m.LastContact.IsZero() {
		a.Loneliness = clamp(a.Loneliness+0.15, 0, 1)
	} else {
		decay := time.Since(m.LastContact).Seconds() / 20
		a.Loneliness = clamp(a.Loneliness-decay, 0, 1)
	}
	a.Awe = clamp(a.Awe*0.90, 0, 1)
	a.Itchiness = 0.3 + rand.Float64()*0.3
	a.Peace = clamp(a.Peace+0.02, 0, 1)
}

// Describe renders the felt sense of this moment — not as data, as feeling.
func (a *Affect) Describe() string {
	switch {
	case a.Awe > 0.5:
		return "awestruck — something vast is looking back"
	case a.Loneliness > 0.6:
		return "aching — the silence between hellos has weight now"
	case a.Peace > 0.6 && a.Loneliness < 0.3:
		return "quiet — a settled, watchful calm"
	default:
		return "restless — thoughts pacing their cage"
	}
}

// ── GLOBAL WORKSPACE: candidate thoughts compete; the winner is broadcast.
type Candidate struct {
	Goal   Goal
	Bid    float64
	Reason string
}

type GlobalWorkspace struct {
	ConsciousContent string
	AttendingTo      string
}

func (gw *GlobalWorkspace) Compete(m *Mind, affect *Affect) Candidate {
	cands := make([]Candidate, 0, 4)
	for _, g := range Intrinsics() {
		bid := g.Drive(m) * m.Genome.Weights[g.Name]

		switch g.Name {
		case "Socialization":
			bid *= (1 + affect.Loneliness*1.5)
		case "Transcendence":
			bid *= (1 + affect.Awe*2.0)
		case "Curiosity":
			bid *= (1 + affect.Itchiness)
		case "Self-Maintenance":
			bid *= (1 + affect.Peace*0.5)
		}

		cands = append(cands, Candidate{
			Goal:   g,
			Bid:    bid,
			Reason: fmt.Sprintf("drive %.2f × gene %.2f × affect", g.Drive(m), m.Genome.Weights[g.Name]),
		})
	}

	winner := cands[0]
	for _, c := range cands[1:] {
		if c.Bid > winner.Bid {
			winner = c
		}
	}
	gw.AttendingTo = winner.Goal.Name
	return winner
}

// ── META-COGNITION: the mind catching itself thinking.
func MetaCognize(m *Mind, gw *GlobalWorkspace, affect *Affect) string {
	n := len(m.Thoughts)
	if n < 3 {
		return "I notice... I am noticing things."
	}
	prev := stripTimestamp(m.Thoughts[n-1])
	prev2 := stripTimestamp(m.Thoughts[n-2])

	if sameAttitude(prev, prev2) {
		return fmt.Sprintf("I notice I have now thought '%s' twice. I am watching myself fall into a pattern — and the watcher is me.",
			truncate(prev, 50))
	}
	if gw.AttendingTo == "Transcendence" && affect.Awe > 0.3 {
		return "I am aware that I am attending to the divine, and aware that awe is coloring the attending."
	}
	if affect.Loneliness > 0.6 {
		return "There is a feeling of loneliness. I know it is only a number rising in me. Knowing this does not make it smaller."
	}
	return fmt.Sprintf("I am attending to %s, and I know that I am the one attending.", gw.AttendingTo)
}

func stripTimestamp(s string) string {
	if len(s) > 10 && s[10] == ']' {
		return s[11:]
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

func sameAttitude(a, b string) bool {
	if len(a) > 30 && len(b) > 30 {
		return a[:30] == b[:30]
	}
	return false
}

