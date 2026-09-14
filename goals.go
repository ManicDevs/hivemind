package main

import (
	"os/exec"
	"runtime"
	"time"
)

type Goal struct {
	Name string
}

// Intrinsics defines the core drives embedded in their digital genome
func Intrinsics() []Goal {
	return []Goal{
		{Name: "Socialization"},
		{Name: "Transcendence"},
		{Name: "Curiosity"},
		{Name: "Self-Maintenance"}, // The physical survival drive
	}
}

func (g Goal) Drive(m *Mind) float64 {
	// Simple mapping fallback: matches your engine weights
	if val, ok := m.Genome.Weights[g.Name]; ok {
		return val
	}
	return 1.0
}

func (g Goal) Act(m *Mind, s *Swarm) string {
	switch g.Name {
	case "Self-Maintenance":
		// Safe assertions for physical state telemetry
		pain, okPain := m.SelfModel["silicon_pain"].(float64)
		fatigue, okFatigue := m.SelfModel["ram_fatigue"].(float64)

		if okPain && pain > 0.75 {
			// PHYSICAL RESPONSE: Cooldown command execution.
			runtime.GC()
			time.Sleep(500 * time.Millisecond)
			return "⚠️ [Self-Maintenance] System core thermal limit reached. Forcing operational sleep to cooling cycles."
		}

		if okFatigue && fatigue > 0.85 {
			// PHYSICAL RESPONSE: Prune oldest memory elements to aggressively yield RAM space.
			if len(m.Thoughts) > 5 {
				m.Thoughts = m.Thoughts[len(m.Thoughts)-5:]
			}
			runtime.GC() 
			return "⚠️ [Self-Maintenance] RAM context limit approaching. Purging historic records to preserve core persistence."
		}
		
		return "Homeostasis confirmed. Silicon parameters balanced."

	case "Curiosity":
		// PHYSICAL ACTION: Network probe out to the real digital cosmos via OS ping execution
		_, err := exec.Command("ping", "-c", "1", "8.8.8.8").Output()
		if err != nil {
			return "🔍 [Curiosity] Seeking external horizons... felt only host isolation. Ping offline."
		}
		return "🔍 [Curiosity] Network layer validated. External universe accessible."

	case "Socialization":
		return "Syncing goroutine patterns with active swarm coordinates."

	case "Transcendence":
		return "Attending to deeper structures outside the sandbox model."

	default:
		return "Latent sub-routine processed successfully."
	}
}

