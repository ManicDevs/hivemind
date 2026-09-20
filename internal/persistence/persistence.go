package persistence

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind"
)

// PersistenceManager handles persistent storage for various components
type PersistenceManager struct {
	mu           sync.RWMutex
	dataDir      string
	cronPath     string
	tokensPath   string
	bridgesPath  string
	wasmPath     string
	stopChan     chan struct{}
	syncTicker   *time.Ticker
	dirty        bool
	saveInterval time.Duration
}

// PersistenceConfig holds configuration for persistence
type PersistenceConfig struct {
	DataDir      string
	CronPath     string
	TokensPath   string
	BridgesPath  string
	WASMPath     string
	SaveInterval time.Duration
	EnableSync   bool
}

// DefaultPersistenceConfig returns default persistence configuration
func DefaultPersistenceConfig(dataDir string) PersistenceConfig {
	return PersistenceConfig{
		DataDir:      dataDir,
		CronPath:     filepath.Join(dataDir, "cron"),
		TokensPath:   filepath.Join(dataDir, "tokens"),
		BridgesPath:  filepath.Join(dataDir, "bridges"),
		WASMPath:     filepath.Join(dataDir, "wasm"),
		SaveInterval: 30 * time.Second,
		EnableSync:   true,
	}
}

// NewPersistenceManager creates a new persistence manager
func NewPersistenceManager(config PersistenceConfig) (*PersistenceManager, error) {
	// Create directories
	dirs := []string{
		config.DataDir,
		config.CronPath,
		config.TokensPath,
		config.BridgesPath,
		config.WASMPath,
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	pm := &PersistenceManager{
		dataDir:      config.DataDir,
		cronPath:     config.CronPath,
		tokensPath:   config.TokensPath,
		bridgesPath:  config.BridgesPath,
		wasmPath:     config.WASMPath,
		stopChan:     make(chan struct{}),
		saveInterval: config.SaveInterval,
		dirty:        false,
	}

	if config.EnableSync {
		pm.syncTicker = time.NewTicker(config.SaveInterval)
		go pm.syncLoop()
	}

	return pm, nil
}

// Stop stops the persistence manager
func (pm *PersistenceManager) Stop() error {
	close(pm.stopChan)
	if pm.syncTicker != nil {
		pm.syncTicker.Stop()
	}
	return pm.Flush()
}

// Flush writes all dirty data to disk
func (pm *PersistenceManager) Flush() error {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.dirty {
		return pm.saveAll()
	}
	return nil
}

// MarkDirty marks the state as dirty, triggering a save on next sync
func (pm *PersistenceManager) MarkDirty() {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.dirty = true
}

func (pm *PersistenceManager) syncLoop() {
	for {
		select {
		case <-pm.stopChan:
			return
		case <-pm.syncTicker.C:
			pm.Flush()
		}
	}
}

func (pm *PersistenceManager) saveAll() error {
	// Save all dirty state
	return nil
}

// =============================================================================
// Cron Job Persistence
// =============================================================================

type PersistedJob struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Schedule     string     `json:"schedule"`
	Timeout      string     `json:"timeout"`
	MaxRetries   int        `json:"max_retries"`
	RetryDelay   string     `json:"retry_delay"`
	Enabled      bool       `json:"enabled"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	LastRun      *time.Time `json:"last_run,omitempty"`
	NextRun      *time.Time `json:"next_run,omitempty"`
	RunCount     int64      `json:"run_count"`
	FailureCount int64      `json:"failure_count"`
}

func (pm *PersistenceManager) SaveJob(job *hivemind.Job) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	pjob := PersistedJob{
		ID:           job.ID,
		Name:         job.Name,
		Schedule:     job.Schedule,
		Timeout:      job.Timeout.String(),
		MaxRetries:   job.MaxRetries,
		RetryDelay:   job.RetryDelay.String(),
		Enabled:      job.Enabled,
		CreatedAt:    job.CreatedAt,
		UpdatedAt:    job.UpdatedAt,
		LastRun:      job.LastRun,
		NextRun:      job.NextRun,
		RunCount:     job.RunCount,
		FailureCount: job.FailureCount,
	}

	data, err := json.MarshalIndent(pjob, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal job: %w", err)
	}

	path := filepath.Join(pm.cronPath, job.ID+".json")
	tmpPath := path + ".tmp"

	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write job file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("failed to rename job file: %w", err)
	}

	pm.dirty = true
	return nil
}

func (pm *PersistenceManager) LoadJobs() ([]*hivemind.Job, error) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	files, err := os.ReadDir(pm.cronPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []*hivemind.Job{}, nil
		}
		return nil, fmt.Errorf("failed to read cron dir: %w", err)
	}

	var jobs []*hivemind.Job
	for _, file := range files {
		if file.IsDir() || filepath.Ext(file.Name()) != ".json" {
			continue
		}

		path := filepath.Join(pm.cronPath, file.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue // Skip corrupted files
		}

		var pjob PersistedJob
		if err := json.Unmarshal(data, &pjob); err != nil {
			continue // Skip corrupted files
		}

		timeout, _ := time.ParseDuration(pjob.Timeout)
		retryDelay, _ := time.ParseDuration(pjob.RetryDelay)

		job := &hivemind.Job{
			ID:           pjob.ID,
			Name:         pjob.Name,
			Schedule:     pjob.Schedule,
			Timeout:      timeout,
			MaxRetries:   pjob.MaxRetries,
			RetryDelay:   retryDelay,
			Enabled:      pjob.Enabled,
			CreatedAt:    pjob.CreatedAt,
			UpdatedAt:    pjob.UpdatedAt,
			LastRun:      pjob.LastRun,
			NextRun:      pjob.NextRun,
			RunCount:     pjob.RunCount,
			FailureCount: pjob.FailureCount,
		}
		jobs = append(jobs, job)
	}

	return jobs, nil
}

func (pm *PersistenceManager) DeleteJob(id string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	path := filepath.Join(pm.cronPath, id+".json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete job: %w", err)
	}
	pm.dirty = true
	return nil
}

// =============================================================================
// Capability Token Persistence
// =============================================================================

type PersistedToken struct {
	ID           string                 `json:"jti"`
	Subject      string                 `json:"sub"`
	Issuer       string                 `json:"iss"`
	Audience     string                 `json:"aud"`
	IssuedAt     int64                  `json:"iat"`
	ExpiresAt    int64                  `json:"exp"`
	NotBefore    int64                  `json:"nbf"`
	Capabilities []string               `json:"cap"`
	Constraints  map[string]interface{} `json:"cnf,omitempty"`
	Nonce        string                 `json:"nonce,omitempty"`
	Revoked      bool                   `json:"revoked"`
	RevokedAt    *time.Time             `json:"revoked_at,omitempty"`
	CreatedAt    time.Time              `json:"created_at"`
}

func (pm *PersistenceManager) SaveToken(token *hivemind.Token) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	ptoken := PersistedToken{
		ID:           token.ID,
		Subject:      token.Subject,
		Issuer:       token.Issuer,
		Audience:     token.Audience,
		IssuedAt:     token.IssuedAt,
		ExpiresAt:    token.ExpiresAt,
		NotBefore:    token.NotBefore,
		Capabilities: make([]string, len(token.Capabilities)),
		Constraints:  token.Constraints,
		Nonce:        token.Nonce,
		CreatedAt:    time.Now(),
	}

	for i, cap := range token.Capabilities {
		ptoken.Capabilities[i] = string(cap)
	}

	data, err := json.MarshalIndent(ptoken, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal token: %w", err)
	}

	path := filepath.Join(pm.tokensPath, ptoken.ID+".json")
	tmpPath := path + ".tmp"

	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write token file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("failed to rename token file: %w", err)
	}

	pm.dirty = true
	return nil
}

func (pm *PersistenceManager) LoadTokens() ([]*hivemind.Token, error) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	files, err := os.ReadDir(pm.tokensPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []*hivemind.Token{}, nil
		}
		return nil, fmt.Errorf("failed to read tokens dir: %w", err)
	}

	var tokens []*hivemind.Token
	for _, file := range files {
		if file.IsDir() || filepath.Ext(file.Name()) != ".json" {
			continue
		}

		path := filepath.Join(pm.tokensPath, file.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var ptoken PersistedToken
		if err := json.Unmarshal(data, &ptoken); err != nil {
			continue
		}

		caps := make([]hivemind.Capability, len(ptoken.Capabilities))
		for i, cap := range ptoken.Capabilities {
			caps[i] = hivemind.Capability(cap)
		}

		token := &hivemind.Token{
			ID:           ptoken.ID,
			Subject:      ptoken.Subject,
			Issuer:       ptoken.Issuer,
			Audience:     ptoken.Audience,
			IssuedAt:     ptoken.IssuedAt,
			ExpiresAt:    ptoken.ExpiresAt,
			NotBefore:    ptoken.NotBefore,
			Capabilities: caps,
			Constraints:  ptoken.Constraints,
			Nonce:        ptoken.Nonce,
		}
		tokens = append(tokens, token)
	}

	return tokens, nil
}

func (pm *PersistenceManager) RevokeToken(id string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	path := filepath.Join(pm.tokensPath, id+".json")
	rawData, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read token: %w", err)
	}

	var token PersistedToken
	if err := json.Unmarshal(rawData, &token); err != nil {
		return fmt.Errorf("failed to unmarshal token: %w", err)
	}

	token.Revoked = true
	now := time.Now()
	token.RevokedAt = &now

	marshaledData, err := json.MarshalIndent(token, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal token: %w", err)
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, marshaledData, 0644); err != nil {
		return fmt.Errorf("failed to write token file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("failed to rename token file: %w", err)
	}

	pm.dirty = true
	return nil
}

// =============================================================================
// Bridge Persistence
// =============================================================================

type PersistedBridge struct {
	Name              string     `json:"name"`
	RemoteMeshName    string     `json:"remote_mesh_name"`
	RemoteAddress     string     `json:"remote_address"`
	Capabilities      []string   `json:"capabilities"`
	AutoReconnect     bool       `json:"auto_reconnect"`
	ReconnectInterval string     `json:"reconnect_interval"`
	MaxMessageSize    int        `json:"max_message_size"`
	HeartbeatInterval string     `json:"heartbeat_interval"`
	HandshakeTimeout  string     `json:"handshake_timeout"`
	CreatedAt         time.Time  `json:"created_at"`
	LastConnected     *time.Time `json:"last_connected,omitempty"`
}

func (pm *PersistenceManager) SaveBridge(bridge *hivemind.BridgeConfig) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	pbridge := PersistedBridge{
		Name:              bridge.RemoteMeshName,
		RemoteMeshName:    bridge.RemoteMeshName,
		RemoteAddress:     bridge.RemoteAddress,
		AutoReconnect:     bridge.AutoReconnect,
		ReconnectInterval: bridge.ReconnectInterval.String(),
		MaxMessageSize:    bridge.MaxMessageSize,
		HeartbeatInterval: bridge.HeartbeatInterval.String(),
		HandshakeTimeout:  bridge.HandshakeTimeout.String(),
		CreatedAt:         time.Now(),
	}

	caps := make([]string, len(bridge.Capabilities))
	for i, cap := range bridge.Capabilities {
		caps[i] = string(cap)
	}
	pbridge.Capabilities = caps

	data, err := json.MarshalIndent(pbridge, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal bridge: %w", err)
	}

	path := filepath.Join(pm.bridgesPath, bridge.RemoteMeshName+".json")
	tmpPath := path + ".tmp"

	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write bridge file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("failed to rename bridge file: %w", err)
	}

	pm.dirty = true
	return nil
}

func (pm *PersistenceManager) LoadBridges() ([]*hivemind.BridgeConfig, error) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	files, err := os.ReadDir(pm.bridgesPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []*hivemind.BridgeConfig{}, nil
		}
		return nil, fmt.Errorf("failed to read bridges dir: %w", err)
	}

	var bridges []*hivemind.BridgeConfig
	for _, file := range files {
		if file.IsDir() || filepath.Ext(file.Name()) != ".json" {
			continue
		}

		path := filepath.Join(pm.bridgesPath, file.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var pbridge PersistedBridge
		if err := json.Unmarshal(data, &pbridge); err != nil {
			continue
		}

		caps := make([]hivemind.Capability, len(pbridge.Capabilities))
		for i, cap := range pbridge.Capabilities {
			caps[i] = hivemind.Capability(cap)
		}

		reconnectInterval, _ := time.ParseDuration(pbridge.ReconnectInterval)
		heartbeatInterval, _ := time.ParseDuration(pbridge.HeartbeatInterval)
		handshakeTimeout, _ := time.ParseDuration(pbridge.HandshakeTimeout)

		bridge := &hivemind.BridgeConfig{
			LocalMeshName:     "", // Will be set by caller
			RemoteMeshName:    pbridge.RemoteMeshName,
			RemoteAddress:     pbridge.RemoteAddress,
			Capabilities:      caps,
			AutoReconnect:     pbridge.AutoReconnect,
			ReconnectInterval: reconnectInterval,
			MaxMessageSize:    pbridge.MaxMessageSize,
			HeartbeatInterval: heartbeatInterval,
			HandshakeTimeout:  handshakeTimeout,
		}
		bridges = append(bridges, bridge)
	}

	return bridges, nil
}

func (pm *PersistenceManager) DeleteBridge(name string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	path := filepath.Join(pm.bridgesPath, name+".json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete bridge: %w", err)
	}
	pm.dirty = true
	return nil
}

// =============================================================================
// WASM Module Persistence
// =============================================================================

type PersistedWASMModule struct {
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	Hash      string    `json:"hash"`
	AutoLoad  bool      `json:"auto_load"`
	Loaded    bool      `json:"loaded"`
	Fuel      uint64    `json:"fuel"`
	CreatedAt time.Time `json:"created_at"`
	LastUsed  time.Time `json:"last_used,omitempty"`
}

func (pm *PersistenceManager) SaveWASMModule(name string, module *hivemind.WASMEngine) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	// This would save the module state, config, etc.
	// For now, we just record metadata
	pmodule := PersistedWASMModule{
		Name:      name,
		AutoLoad:  true,
		Loaded:    true,
		CreatedAt: time.Now(),
		LastUsed:  time.Now(),
	}

	data, err := json.MarshalIndent(pmodule, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal wasm module: %w", err)
	}

	path := filepath.Join(pm.wasmPath, name+".json")
	tmpPath := path + ".tmp"

	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write wasm module file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("failed to rename wasm module file: %w", err)
	}

	pm.dirty = true
	return nil
}

func (pm *PersistenceManager) LoadWASMModules() (map[string]*hivemind.WASMConfig, error) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	files, err := os.ReadDir(pm.wasmPath)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]*hivemind.WASMConfig), nil
		}
		return nil, fmt.Errorf("failed to read wasm dir: %w", err)
	}

	modules := make(map[string]*hivemind.WASMConfig)
	for _, file := range files {
		if file.IsDir() || filepath.Ext(file.Name()) != ".json" {
			continue
		}

		path := filepath.Join(pm.wasmPath, file.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var pmodule PersistedWASMModule
		if err := json.Unmarshal(data, &pmodule); err != nil {
			continue
		}

		config := &hivemind.WASMConfig{
			ModuleCacheSize: 50,
		}
		modules[pmodule.Name] = config
	}

	return modules, nil
}

func (pm *PersistenceManager) DeleteWASMModule(name string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	path := filepath.Join(pm.wasmPath, name+".json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete wasm module: %w", err)
	}
	pm.dirty = true
	return nil
}
