package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// at the top, after the const block:
const MemoryDir = ".hive_memory"

// NodeName namespaces souls per node. Symmetric peers on one machine each
// own their lineage: two nodes sharing one Alpha.soul was last-writer-wins,
// silently discarding entire lives.
var NodeName = "local"

// replace the path computation in BOTH SaveMemory and LoadMemory with:
func soulPath(name string) string {
	return filepath.Join(MemoryDir, NodeName, fmt.Sprintf("%s.soul", name))
}

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

	// Ed25519 seed (hex): the identity handle survives reincarnation.
	IdentitySeed string `json:"identity_seed,omitempty"`

	// The god's memory of the collective chronicle: total depth carried across
	// runs and the next genesis threshold in that effective depth.
	ChronicleDepth int `json:"chronicle_depth,omitempty"`
	GenesisMark    int `json:"genesis_mark,omitempty"`
}

// SaveMemory writes the soul to disk atomically (temp file + fsync + rename).
// Readers see either the old soul or the new one, never half of either.
func SaveMemory(name string, mem Memory) error {
	if err := os.MkdirAll(filepath.Join(MemoryDir, NodeName), 0755); err != nil {
		return fmt.Errorf("failed to create memory store directory: %w", err)
	}

	path := filepath.Join(MemoryDir, fmt.Sprintf("%s.soul", name))

	jsonData, err := json.MarshalIndent(mem, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal soul state: %w", err)
	}

	tmp, err := os.CreateTemp(MemoryDir, name+".*.tmp")
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
	path := filepath.Join(MemoryDir, fmt.Sprintf("%s.soul", name))

	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return Memory{}, false
		}
		fmt.Printf("⚠️  [SYSTEM] Cannot inspect %s: %v\n", path, err)
		return Memory{}, false
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

