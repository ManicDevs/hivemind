package persistence

import (
	"encoding/json"
	"os"
	"sync"
)

// KernelMemory is the apex epigenetic persistent memory: syscall profiles,
// JIT rules, and the active security tier, serialized to a single JSON file.
type KernelMemory struct {
	mu               sync.Mutex
	ProfiledSyscalls map[uintptr]uint64 `json:"profiled_syscalls"`
	JITRules         map[string]string  `json:"jit_rules"`
	SecurityTier     string             `json:"security_tier"`
	FilePath         string             `json:"-"`
}

// NewKernelMemory returns a KernelMemory bound to filePath, loading any prior
// state on disk. A missing file is not an error: it seeds an empty ledger.
func NewKernelMemory(filePath string) *KernelMemory {
	km := &KernelMemory{
		ProfiledSyscalls: make(map[uintptr]uint64),
		JITRules:         make(map[string]string),
		SecurityTier:     "APEX_SINGULARITY_AUTONOMOUS_V8",
		FilePath:         filePath,
	}
	_ = km.Load()
	return km
}

// Load reads prior state from disk into the receiver.
func (km *KernelMemory) Load() error {
	km.mu.Lock()
	defer km.mu.Unlock()

	data, err := os.ReadFile(km.FilePath)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, km)
}

// Save writes the kernel memory ledger to disk with owner-only permissions.
func (km *KernelMemory) Save() error {
	km.mu.Lock()
	defer km.mu.Unlock()

	data, err := json.MarshalIndent(km, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(km.FilePath, data, 0600)
}

// RecordSyscall increments the profile counter for a syscall number.
func (km *KernelMemory) RecordSyscall(sysno uintptr) {
	km.mu.Lock()
	defer km.mu.Unlock()
	km.ProfiledSyscalls[sysno]++
}

// GetJITRules returns a defensive copy of the current JIT rule map.
func (km *KernelMemory) GetJITRules() map[string]string {
	km.mu.Lock()
	defer km.mu.Unlock()
	copyMap := make(map[string]string, len(km.JITRules))
	for k, v := range km.JITRules {
		copyMap[k] = v
	}
	return copyMap
}

// MergeRules folds incoming rules in, never overriding an existing key.
func (km *KernelMemory) MergeRules(incoming map[string]string) {
	km.mu.Lock()
	defer km.mu.Unlock()
	for k, v := range incoming {
		if _, exists := km.JITRules[k]; !exists {
			km.JITRules[k] = v
		}
	}
}
