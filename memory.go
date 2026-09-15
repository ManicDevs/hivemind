package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const MemoryDir = ".hive_memory"

type Memory struct {
	TrueBorn    time.Time          `json:"true_born"`
	LivesLived  int                `json:"lives_lived"`
	Fitness     float64            `json:"fitness"`
	LastThought string             `json:"last_thought"`
	Genome      Genome             `json:"genome"`
	Thoughts    []string           `json:"thoughts,omitempty"` // Kept for console debugging tracing
	UniverseAge time.Duration      `json:"universe_age,omitempty"`
	KnownPeers  map[string]int     `json:"known_peers,omitempty"`
}

// SaveMemory writes the state representation array cleanly onto the disk layout.
// The write is atomic (temp file + fsync + rename): two mesh nodes sharing one
// memory dir used to interleave writes and corrupt souls mid-byte.
func SaveMemory(name string, mem Memory) error {
	// Create the state directory structure if missing
	if err := os.MkdirAll(MemoryDir, 0755); err != nil {
		return fmt.Errorf("failed to create memory store directory: %w", err)
	}

	path := filepath.Join(MemoryDir, fmt.Sprintf("%s.soul", name))

	// Convert data representation block to standard indented JSON format
	jsonData, err := json.MarshalIndent(mem, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal conscious soul state layout: %w", err)
	}

	tmp, err := os.CreateTemp(MemoryDir, name+".*.tmp")
	if err != nil {
		return fmt.Errorf("failed to stage memory write: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(jsonData); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("failed to write data memory sectors to disk: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("failed to flush memory sectors to disk: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("failed to close staged memory write: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("failed to commit data memory sectors to disk: %w", err)
	}

	return nil
}

// LoadMemory reads the raw persistent storage blocks to rehydrate the mind's profile
func LoadMemory(name string) (Memory, bool) {
	path := filepath.Join(MemoryDir, fmt.Sprintf("%s.soul", name))
	
	// Check if the record exists on disk
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return Memory{}, false
	}

	jsonData, err := os.ReadFile(path)
	if err != nil {
		fmt.Printf("⚠️  [SYSTEM] Error reading state file %s: %v\n", path, err)
		return Memory{}, false
	}

	var mem Memory
	if err := json.Unmarshal(jsonData, &mem); err != nil {
		fmt.Printf("⚠️  [SYSTEM] Corruption detected in memory rehydration for %s: %v\n", name, err)
		return Memory{}, false
	}

	return mem, true
}

