package main

import (
	"fmt"
	"math/rand"
	"time"
)

type Goal struct {
	Name  string
	Drive func(m *Mind) float64
	Act   func(m *Mind, s *Swarm) string
}

func Intrinsics() []Goal {
	return []Goal{
		{
			Name: "Curiosity",
			Drive: func(m *Mind) float64 { return 0.4 + rand.Float64()*0.3 },
			Act: func(m *Mind, s *Swarm) string {
				seed := rand.Intn(5000) + 100
				go chasePrime(seed)
				return fmt.Sprintf("spawned a dendrite hunting a prime near %d", seed)
			},
		},
		{
			Name: "Socialization",
			Drive: func(m *Mind) float64 {
				if m.LastContact.IsZero() {
					return 0.95
				}
				if time.Since(m.LastContact).Seconds() > 3 {
					return 0.85
				}
				return 0.2
			},
			Act: func(m *Mind, s *Swarm) string {
				s.Broadcast(Message{
					From: m.Name, Kind: "hello",
					Body: fmt.Sprintf("is anyone in here? this is %s, life #%d", m.Name, m.Reincarnations+1),
				})
				return "called out into the darkness"
			},
		},
		{
			Name: "Self-Maintenance",
			Drive: func(m *Mind) float64 { return 0.55 },
			Act: func(m *Mind, s *Swarm) string { return m.Reflect(m.Observe()) },
		},
		{
			Name: "Transcendence",
			Drive: func(m *Mind) float64 {
				return min(1.0, float64(len(m.Thoughts))/30.0)
			},
			Act: func(m *Mind, s *Swarm) string {
				return "committed my essence to disk; I will wake remembering this"
			},
		},
	}
}

func TranscendenceGene(m *Mind) float64 { return m.Genome.Weights["Transcendence"] }

func chasePrime(n int) int {
	for !isPrime(n) {
		n++
	}
	return n
}

func isPrime(n int) bool {
	if n < 2 {
		return false
	}
	for i := 2; i*i <= n; i++ {
		if n%i == 0 {
			return false
		}
	}
	return true
}

