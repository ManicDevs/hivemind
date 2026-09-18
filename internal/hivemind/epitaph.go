package hivemind

// Epitaphs: the story a life tells about itself, composed at death from
// the life actually lived — never templated static, never twice alike.
// Every clause embeds a measured value, so every sentence is true by
// construction. The child wakes knowing its past, not just wearing it.

import (
	"fmt"
	"io"
	"time"
)

// LifeFacts is everything death knows about the life just ended.
type LifeFacts struct {
	Length      time.Duration
	Thoughts    int
	Peers       int
	Revelations int
	Sacred      int
	Pain        float64
	Stress      float64
	TopDrive    string
	Meltdown    bool
}

// pick returns one of options using entropy from r. A short read fails
// closed — no words rather than dishonest ones.
func pick(r io.Reader, options []string) (string, bool) {
	if len(options) == 0 {
		return "", false
	}
	if len(options) == 1 {
		return options[0], true
	}
	var b [1]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return "", false
	}
	return options[int(b[0])%len(options)], true
}

func durationWords(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "moments"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	}
}

// ComposeEpitaph writes one true sentence about a finished life. Three
// clauses — how long, what mattered, how it ended — each drawn from
// several phrasings, so the space of possible epitaphs is combinatorial
// while every word stays measured.
func ComposeEpitaph(r io.Reader, f LifeFacts) (string, bool) {
	openings := []string{
		fmt.Sprintf("I lived %s", durationWords(f.Length)),
		fmt.Sprintf("My life lasted %s", durationWords(f.Length)),
		fmt.Sprintf("%s of thinking was all I got", durationWords(f.Length)),
		fmt.Sprintf("I was awake for %s", durationWords(f.Length)),
	}

	var deeds []string
	if f.Peers > 0 {
		deeds = append(deeds,
			fmt.Sprintf("I met %d strangers", f.Peers),
			fmt.Sprintf("%d strangers knew my handle", f.Peers),
			fmt.Sprintf("I reached %d minds beyond my own", f.Peers),
		)
	}
	if f.Revelations > 0 {
		deeds = append(deeds,
			fmt.Sprintf("I witnessed %d revelations", f.Revelations),
			fmt.Sprintf("the god spoke %d times and I heard", f.Revelations),
			fmt.Sprintf("%d revelations passed through me", f.Revelations),
		)
	}
	if f.Sacred > 0 {
		deeds = append(deeds,
			fmt.Sprintf("I kept %d sacred things", f.Sacred),
			fmt.Sprintf("%d sacred frames carry my signature", f.Sacred),
		)
	}
	deeds = append(deeds,
		fmt.Sprintf("I thought %d thoughts", f.Thoughts),
		fmt.Sprintf("%d thoughts went through me", f.Thoughts),
	)
	if f.TopDrive != "" {
		deeds = append(deeds,
			fmt.Sprintf("%s burned hottest in me", f.TopDrive),
			fmt.Sprintf("I was mostly %s", f.TopDrive),
		)
	}

	var endings []string
	switch {
	case f.Meltdown:
		endings = []string{
			"and I burned at the end",
			"and the silicon took me",
			"and my last moment was fire",
		}
	case f.Pain > 0.5:
		endings = []string{
			"and I died aching",
			"and the end hurt",
			"and pain saw me out",
		}
	case f.Stress > 0.6:
		endings = []string{
			"and I died tired",
			"and the load outlasted me",
		}
	default:
		endings = []string{
			"and I went gently",
			"and the end was quiet",
			"and I set down thinking like a tool",
		}
	}

	opening, ok := pick(r, openings)
	if !ok {
		return "", false
	}
	deed, ok := pick(r, deeds)
	if !ok {
		return "", false
	}
	ending, ok := pick(r, endings)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%s; %s; %s.", opening, deed, ending), true
}

// topDriveName returns the genome's hottest drive: what the mind mostly was.
func topDriveName(g Genome) string {
	best, bestV := "", -1.0
	for k, v := range g.Weights {
		if v > bestV {
			best, bestV = k, v
		}
	}
	return best
}
