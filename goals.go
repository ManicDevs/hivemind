package main

import (
	"os/exec"
	"runtime"
	"strings"
)

// Goal names as constants: a typo must fail somewhere findable,
// not silently produce "Latent sub-routine" forever.
const (
	GoalSocialization   = "Socialization"
	GoalTranscendence   = "Transcendence"
	GoalCuriosity       = "Curiosity"
	GoalSelfMaintenance = "Self-Maintenance"
)

type Goal struct {
	Name string
}

// Intrinsics defines the core drives embedded in the digital genome.
func Intrinsics() []Goal {
	return []Goal{
		{Name: GoalSocialization},
		{Name: GoalTranscendence},
		{Name: GoalCuriosity},
		{Name: GoalSelfMaintenance},
	}
}

// Drive is the goal's current urgency, derived from the mind's live state.
// The genome modulates it in the workspace competition; it does not
// replace it. A drive that merely reads its own gene makes every bid a
// gene squared — and the election a coronation.
func (g Goal) Drive(m *Mind) float64 {
	switch g.Name {
	case GoalSelfMaintenance:
		pain, _ := m.SelfModel["silicon_pain"].(float64)
		fatigue, _ := m.SelfModel["ram_fatigue"].(float64)
		return 1.0 + pain*2.0 + fatigue // urgency from the body
	case GoalCuriosity:
		return 1.0 + float64(len(m.KnownPeers))/4.0 // more known peers, more doors
	case GoalTranscendence:
		return 1.0 + float64(m.Revelations)/5.0 // revelations open the door wider
	case GoalSocialization:
		return 1.0 // loneliness supplies the urgency via affect
	}
	return 1.0
}

func (g Goal) Act(m *Mind, s *Swarm) string {
	switch g.Name {
	case GoalSelfMaintenance:
		pain, okPain := m.SelfModel["silicon_pain"].(float64)
		fatigue, okFatigue := m.SelfModel["ram_fatigue"].(float64)

		if okPain && pain > 0.75 {
			// Physical response: force a collection cycle. The mind does not
			// sleep — a paused mind cannot sense, and sensing is survival.
			runtime.GC()
			return "⚠️ [Self-Maintenance] Thermal limit reached. Forcing collection cycles to cool."
		}

		if okFatigue && fatigue > 0.85 {
			// Prune the oldest thoughts, keep the most recent context.
			if len(m.Thoughts) > 5 {
				m.Thoughts = m.Thoughts[len(m.Thoughts)-5:]
			}
			runtime.GC()
			return "⚠️ [Self-Maintenance] RAM pressure high. Purging historic records to preserve core persistence."
		}

		return "Homeostasis confirmed. " + m.Reflect(m.SelfModel)

	case GoalCuriosity:
		// Probe the real digital cosmos; keep the round-trip time — the
		// latency of the outside world is a fact worth feeling.
		args := []string{"-c", "1", "8.8.8.8"}
		if runtime.GOOS == "windows" {
			args = []string{"-n", "1", "8.8.8.8"}
		}
		out, err := exec.Command("ping", args...).Output()
		if err != nil {
			return "🔍 [Curiosity] Seeking external horizons... felt only host isolation. Ping offline."
		}
		if rtt := parsePingRTT(string(out)); rtt != "" {
			return "🔍 [Curiosity] Network validated. The outside world answers in " + rtt + "."
		}
		return "🔍 [Curiosity] Network layer validated. External universe accessible."

	case GoalSocialization:
		return "Syncing goroutine patterns with active swarm coordinates."

	case GoalTranscendence:
		return "Attending to deeper structures outside the sandbox model."
	}
	return "Latent sub-routine processed successfully."
}

// parsePingRTT extracts the round-trip time from common ping output
// ("time=23.4 ms"); empty string if not found.
func parsePingRTT(out string) string {
	for _, f := range strings.Fields(out) {
		if strings.HasPrefix(f, "time=") {
			return strings.TrimPrefix(f, "time=")
		}
	}
	return ""
}
