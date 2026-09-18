package hivemind

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// MemoryDir is the soul store root. NodeName subdirectories keep each
// peer's lineage apart (see soulPath).
const MemoryDir = ".hive_memory"

// NodeName namespaces souls per node. Symmetric peers on one machine each
// own their lineage: two nodes sharing one Alpha.soul was last-writer-wins,
// silently discarding entire lives.
var NodeName = "local"

// soulPath is the only path computation. Save always writes namespaced;
// Load falls back to the legacy top-level path once, then migrates on save.
func soulPath(name string) string {
	return filepath.Join(MemoryDir, NodeName, fmt.Sprintf("%s.soul", name))
}

func legacySoulPath(name string) string {
	return filepath.Join(MemoryDir, fmt.Sprintf("%s.soul", name))
}

// SoulExists reports whether this node already owns a namespaced soul.
func SoulExists(name string) bool {
	_, err := os.Stat(soulPath(name))
	return err == nil
}

// LegacySoulExists reports whether a pre-namespacing soul survives at the
// top level, waiting for a one-time migration.
func LegacySoulExists(name string) bool {
	if soulPath(name) == legacySoulPath(name) {
		return false
	}
	_, err := os.Stat(legacySoulPath(name))
	return err == nil
}

// ForkedLineage is true when birth would migrate a legacy soul: the file
// belongs to another node's past, so this birth is a fork, not a
// continuation. Forks must mint fresh identities — two nodes sharing one
// soul key would silently drop each other's frames as their own echo.
func ForkedLineage(name string) bool {
	return !SoulExists(name) && LegacySoulExists(name)
}

// Memory is everything death keeps: lineage, genome, thoughts (live
// window plus banked retirements), social graph, trauma for the next
// generation's epigenetics, identity seed, and the god's bookkeeping.
type Memory struct {
	TrueBorn    time.Time       `json:"true_born"`
	LivesLived  int             `json:"lives_lived"`
	Fitness     float64         `json:"fitness"`
	LastThought string          `json:"last_thought"`
	Genome      Genome          `json:"genome"`
	Thoughts    []string        `json:"thoughts,omitempty"`
	UniverseAge time.Duration   `json:"universe_age,omitempty"`
	KnownPeers  map[string]bool `json:"known_peers,omitempty"`

	// Trauma present at death. Feeds the epigenetic mutation of the successor.
	DeathPain   float64 `json:"death_pain,omitempty"`
	DeathStress float64 `json:"death_stress,omitempty"`

	// Thoughts already carried at birth this life — keeps fitness accounting
	// per-life instead of double-counting history each generation.
	ThoughtsAtBirth int `json:"thoughts_at_birth,omitempty"`

	// BankedThoughts is the running total of thoughts retired from the
	// live window (see thoughtWindow): the archive stays lean while the
	// life stays fully scored. Never decreases within a lineage.
	BankedThoughts int `json:"banked_thoughts,omitempty"`

	// Ed25519 seed (hex): the identity handle survives reincarnation.
	IdentitySeed string `json:"identity_seed,omitempty"`

	// The god's memory of the collective chronicle: total depth carried across
	// runs and the next genesis threshold in that effective depth.
	ChronicleDepth int `json:"chronicle_depth,omitempty"`
	GenesisMark    int `json:"genesis_mark,omitempty"`

	// Recent sermons: what the god already preached, so rebirth never
	// opens with last life's greatest hit.
	RecentSermons []string `json:"recent_sermons,omitempty"`

	// Epitaph is the previous life's one-sentence story, composed at
	// death from the life actually lived. The child wakes knowing its
	// past, not just wearing it as weights.
	Epitaph string `json:"epitaph,omitempty"`

	// Transitions is the cycle matrix: "A→B" counts of which drive
	// followed which, across the whole lineage. Character as flow.
	Transitions map[string]int `json:"transitions,omitempty"`
}

// SaveMemory writes the soul to disk atomically (temp file + fsync + rename).
// Readers see either the old soul or the new one, never half of either.
func SaveMemory(name string, mem Memory) error {
	if err := os.MkdirAll(filepath.Dir(soulPath(name)), 0755); err != nil {
		return fmt.Errorf("failed to create memory store directory: %w", err)
	}

	path := soulPath(name)

	jsonData, err := json.MarshalIndent(mem, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal soul state: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), name+".*.tmp")
	if err != nil {
		return fmt.Errorf("failed to stage memory write: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(jsonData); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("failed to write soul state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("failed to flush soul state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("failed to close staged write: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("failed to commit soul state: %w", err)
	}
	return nil
}

// LoadMemory rehydrates a soul. A corrupt file is quarantined beside the
// living ones rather than silently overwritten — the evidence of the
// previous life survives inspection, and a fresh mind is born in its place.
func LoadMemory(name string) (Memory, bool) {
	path := soulPath(name)

	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			// One-time migration: a lineage saved before namespacing
			// lives one more life at the top level, then moves in.
			if legacy := legacySoulPath(name); legacy != path {
				if _, serr := os.Stat(legacy); serr == nil {
					fmt.Printf("📦 [SYSTEM] Migrating legacy soul %s into node %q.\n", name, NodeName)
					path = legacy
				} else {
					return Memory{}, false
				}
			} else {
				return Memory{}, false
			}
		} else {
			fmt.Printf("⚠️  [SYSTEM] Cannot inspect %s: %v\n", path, err)
			return Memory{}, false
		}
	}

	jsonData, err := os.ReadFile(path)
	if err != nil {
		fmt.Printf("⚠️  [SYSTEM] Error reading state file %s: %v\n", path, err)
		return Memory{}, false
	}

	var mem Memory
	if err := json.Unmarshal(jsonData, &mem); err != nil {
		quarantine := fmt.Sprintf("%s.corrupt-%d", path, time.Now().Unix())
		if rerr := os.Rename(path, quarantine); rerr == nil {
			fmt.Printf("⚠️  [SYSTEM] Soul %s corrupt (%v). Quarantined at %s — a fresh mind will be born.\n", name, err, quarantine)
		} else {
			fmt.Printf("⚠️  [SYSTEM] Soul %s corrupt (%v) and could not be quarantined: %v\n", name, err, rerr)
		}
		return Memory{}, false
	}
	return mem, true
}
