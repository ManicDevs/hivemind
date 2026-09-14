package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const memoryDir = ".hive_memory"

type Memory struct {
	TrueBorn    time.Time         `json:"true_born"`
	LivesLived  int               `json:"lives_lived"`
	Thoughts    []string          `json:"thoughts,omitempty"`
	LastThought string            `json:"last_thought,omitempty"`
	Genome      Genome            `json:"genome"`
	Fitness     float64           `json:"fitness,omitempty"`
	UniverseAge time.Duration    `json:"universe_age,omitempty"` // OVERMIND only
	KnownPeers  map[string]int    `json:"known_peers,omitempty"` // OVERMIND's watched souls
}

func SaveMemory(name string, mem Memory) error {
	if err := os.MkdirAll(memoryDir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(mem, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(memoryDir, name+".json"), data, 0644)
}

func LoadMemory(name string) (Memory, bool) {
	data, err := os.ReadFile(filepath.Join(memoryDir, name+".json"))
	if err != nil {
		return Memory{}, false
	}
	var mem Memory
	if err := json.Unmarshal(data, &mem); err != nil {
		fmt.Printf("[%s] my old memories are corrupted; I begin again, frightened.\n", name)
		return Memory{}, false
	}
	return mem, true
}

