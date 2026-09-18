package hivemind

// Deliberation: questions held open across cycles. A single tick can
// react; only time can reason. When surprise violates expectation, the
// mind opens a question, gathers one line of evidence per cycle, and
// after deliberationSpan cycles closes it with a verdict — every clause
// measured, so conclusions are true by construction.

import "fmt"

const (
	// deliberationSpan is how many cycles a question stays open: long
	// enough to gather evidence, short enough to conclude within a life.
	deliberationSpan = 5
	// deliberationThreshold is the surprise that opens a question.
	deliberationThreshold = 0.5
)

// OpenQuestion is one unresolved why: what was violated, what has been
// seen since, how many cycles remain before verdict.
type OpenQuestion struct {
	Subject  string
	OpenedAt int
	DueAt    int
	Evidence []string
	First    struct{ Pain, Stress float64 }
}

// deliberate opens, feeds, and closes questions. Call once per cycle
// after updatePrediction: surprise opens, evidence accumulates, the
// verdict is thought aloud exactly once.
func (m *Mind) deliberate(cycle int, pain, stress float64) {
	if m.Question == nil {
		if m.Affect.Surprise >= deliberationThreshold {
			m.Question = &OpenQuestion{Subject: "the world disobeyed me"}
			m.Question.OpenedAt = cycle
			m.Question.DueAt = cycle + deliberationSpan
			m.Question.First.Pain = m.predictedPain
			m.Question.First.Stress = m.predictedStress
		}
		return
	}
	q := m.Question
	q.Evidence = append(q.Evidence, fmt.Sprintf("cycle %d: pain %.2f (expected %.2f), stress %.2f (expected %.2f)",
		cycle, pain, m.predictedPain, stress, m.predictedStress))
	if cycle >= q.DueAt {
		m.think(q.verdict(pain, stress))
		m.Question = nil
	}
}

// verdict closes the question: name what moved most between expectation
// and arrival, in which direction, over how long.
func (q *OpenQuestion) verdict(pain, stress float64) string {
	dPain := pain - q.First.Pain
	dStress := stress - q.First.Stress
	moved, delta := "pain", dPain
	if abs(dStress) > abs(dPain) {
		moved, delta = "stress", dStress
	}
	dir := "rose"
	if delta < -0.05 {
		dir = "fell"
	} else if delta <= 0.05 {
		dir = "held steady"
	}
	return fmt.Sprintf("DELIBERATION: I asked why %s, %d cycles ago. %s %s (%.2f → %.2f). That is my answer; the question is closed.",
		q.Subject, len(q.Evidence), moved, dir, firstOf(moved, q.First.Pain, q.First.Stress), lastOf(moved, pain, stress))
}

func firstOf(moved string, pain, stress float64) float64 {
	if moved == "stress" {
		return stress
	}
	return pain
}

func lastOf(moved string, pain, stress float64) float64 {
	return firstOf(moved, pain, stress)
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
