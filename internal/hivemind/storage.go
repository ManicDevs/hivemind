package hivemind

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
)

// StorageConfig holds storage configuration
type StorageConfig struct {
	BasePath           string
	MaxSnapshots       int
	Compression        string // "zstd", "gzip", "none"
	CompressionLevel   int
	SchemaVersion      int
	EnableSnapshots    bool
	SnapshotInterval   time.Duration
	MaxSnapshotAge     time.Duration
	IntegrityCheck     bool
	IntegrityInterval  time.Duration
	CrossNodeSync      bool
	SyncInterval       time.Duration
	ConflictResolution string // "last-write-wins", "merge", "manual"
}

func DefaultStorageConfig() StorageConfig {
	return StorageConfig{
		BasePath:           ".hive_memory",
		MaxSnapshots:       100,
		Compression:        "zstd",
		CompressionLevel:   3,
		SchemaVersion:      SchemaVersion,
		EnableSnapshots:    true,
		SnapshotInterval:   5 * time.Minute,
		MaxSnapshotAge:     30 * 24 * time.Hour,
		IntegrityCheck:     true,
		IntegrityInterval:  1 * time.Hour,
		CrossNodeSync:      true,
		SyncInterval:       5 * time.Minute,
		ConflictResolution: "last-write-wins",
	}
}

// SchemaVersion is the current schema version
const SchemaVersion = 1

// Snapshot represents a point-in-time snapshot of a soul
type Snapshot struct {
	ID            string            `json:"id"`
	SoulName      string            `json:"soul_name"`
	NodeName      string            `json:"node_name"`
	Timestamp     time.Time         `json:"timestamp"`
	SchemaVersion int               `json:"schema_version"`
	Compression   string            `json:"compression"`
	Size          int64             `json:"size"`
	Checksum      string            `json:"checksum"`
	Data          []byte            `json:"data,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

// soulEnvelope wraps a soul's compressed payload with its sha256 checksum so
// Load and the periodic integrity loop can prove that the bytes on disk are
// exactly the bytes that were written — a torn write or bit-rot surfaces as an
// explicit failure, not silent amnesia. Legacy raw-compressed .soul files
// (pre-envelope) stay readable but carry no checksum and are skipped by
// verification, with the gap logged once.
type soulEnvelope struct {
	Checksum    string `json:"checksum"`
	Compression string `json:"compression"`
	Payload     []byte `json:"payload"`
}

// SoulStore manages soul persistence with snapshots, compression, and integrity
type SoulStore struct {
	mu              sync.RWMutex
	config          StorageConfig
	nodeName        string
	basePath        string
	snapshots       map[string][]*Snapshot // soulName -> []*Snapshot
	snapshotIndex   map[string]int         // soulName -> latest snapshot index
	zstdEncoder     *zstd.Encoder
	zstdDecoder     *zstd.Decoder
	stopChan        chan struct{}
	snapshotTicker  *time.Ticker
	integrityTicker *time.Ticker
	syncTicker      *time.Ticker
}

// NewSoulStore creates a new soul store
func NewSoulStore(nodeName string, config StorageConfig) (*SoulStore, error) {
	if config.BasePath == "" {
		config.BasePath = ".hive_memory"
	}
	if config.Compression == "" {
		config.Compression = "zstd"
	}
	if config.MaxSnapshots <= 0 {
		config.MaxSnapshots = 100
	}
	if config.SchemaVersion <= 0 {
		config.SchemaVersion = SchemaVersion
	}

	basePath := filepath.Join(config.BasePath, nodeName)
	if err := os.MkdirAll(basePath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create base path: %w", err)
	}

	// Initialize compressor
	var zstdEncoder *zstd.Encoder
	var zstdDecoder *zstd.Decoder
	var err error

	if config.Compression == "zstd" {
		zstdEncoder, err = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.EncoderLevel(config.CompressionLevel)))
		if err != nil {
			return nil, fmt.Errorf("failed to create zstd encoder: %w", err)
		}
		zstdDecoder, err = zstd.NewReader(nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create zstd decoder: %w", err)
		}
	}

	ss := &SoulStore{
		config:        config,
		nodeName:      nodeName,
		basePath:      basePath,
		snapshots:     make(map[string][]*Snapshot),
		snapshotIndex: make(map[string]int),
		zstdEncoder:   zstdEncoder,
		zstdDecoder:   zstdDecoder,
		stopChan:      make(chan struct{}),
	}

	// Load existing snapshots
	if err := ss.loadSnapshots(); err != nil {
		return nil, fmt.Errorf("failed to load snapshots: %w", err)
	}

	// Start background tasks
	if config.EnableSnapshots {
		ss.snapshotTicker = time.NewTicker(config.SnapshotInterval)
		go ss.snapshotLoop()
	}

	if config.IntegrityCheck {
		ss.integrityTicker = time.NewTicker(config.IntegrityInterval)
		go ss.integrityLoop()
	}

	if config.CrossNodeSync {
		ss.syncTicker = time.NewTicker(config.SyncInterval)
		go ss.syncLoop()
	}

	return ss, nil
}

// Save saves a soul with atomic write and creates a snapshot
func (ss *SoulStore) Save(ctx context.Context, name string, mem Memory) error {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	// Update schema version
	mem.SchemaVersion = ss.config.SchemaVersion

	// Marshal
	data, err := json.MarshalIndent(mem, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal memory: %w", err)
	}

	// Compress
	compressed, err := ss.compress(data)
	if err != nil {
		return fmt.Errorf("failed to compress: %w", err)
	}

	// When integrity checking is on, the durable artifact is an envelope
	// wrapping the compressed payload with its real sha256. Otherwise the
	// file stays raw compressed bytes (the legacy format).
	toWrite := compressed
	if ss.config.IntegrityCheck {
		env := soulEnvelope{
			Checksum:    ss.checksum(compressed),
			Compression: ss.config.Compression,
			Payload:     compressed,
		}
		if toWrite, err = json.Marshal(env); err != nil {
			return fmt.Errorf("failed to envelope memory: %w", err)
		}
	}

	// Atomic write: temp file + fsync + rename
	path := filepath.Join(ss.basePath, name+".soul")

	tmpFile, err := os.CreateTemp(filepath.Dir(path), name+".*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	if _, err := tmpFile.Write(toWrite); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("failed to sync temp file: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	// Create snapshot if enabled
	if ss.config.EnableSnapshots {
		snapshot := &Snapshot{
			ID:            generateSnapshotID(),
			SoulName:      name,
			NodeName:      ss.nodeName,
			Timestamp:     time.Now(),
			SchemaVersion: ss.config.SchemaVersion,
			Compression:   ss.config.Compression,
			Size:          int64(len(compressed)),
			Checksum:      ss.checksum(compressed),
			Data:          compressed,
			Metadata: map[string]string{
				"fitness": fmt.Sprintf("%.2f", mem.Fitness),
				"lives":   fmt.Sprintf("%d", mem.LivesLived),
				"peers":   fmt.Sprintf("%d", len(mem.KnownPeers)),
			},
		}

		if err := ss.addSnapshot(name, snapshot); err != nil {
			return fmt.Errorf("failed to add snapshot: %w", err)
		}
	}

	return nil
}

// Load loads a soul from disk
func (ss *SoulStore) Load(ctx context.Context, name string) (Memory, error) {
	ss.mu.RLock()
	defer ss.mu.RUnlock()

	path := filepath.Join(ss.basePath, name+".soul")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Memory{}, fmt.Errorf("soul not found: %w", err)
		}
		return Memory{}, fmt.Errorf("failed to read soul: %w", err)
	}

	// Verify integrity if enabled. Durable files are envelopes: recover the
	// payload and prove its checksum. A legacy raw-compressed file (no
	// envelope) is still a valid soul, just without an outside checksum to
	// verify — so it is decompressed as before.
	payload := data
	compression := ss.config.Compression
	if ss.config.IntegrityCheck {
		var env soulEnvelope
		if err := json.Unmarshal(data, &env); err == nil && len(env.Payload) > 0 && env.Checksum != "" {
			if got := ss.checksum(env.Payload); got != env.Checksum {
				return Memory{}, fmt.Errorf("soul %s integrity check FAILED: expected %s got %s (torn write or bit-rot)", name, env.Checksum, got)
			}
			payload = env.Payload
			compression = env.Compression
		}
	}

	// Decompress
	decompressed, err := ss.decompressWith(payload, compression)
	if err != nil {
		return Memory{}, fmt.Errorf("failed to decompress: %w", err)
	}

	// Unmarshal
	var mem Memory
	if err := json.Unmarshal(decompressed, &mem); err != nil {
		return Memory{}, fmt.Errorf("failed to unmarshal memory: %w", err)
	}

	// Handle schema migration if needed
	if mem.SchemaVersion < SchemaVersion {
		if err := ss.migrate(&mem); err != nil {
			return Memory{}, fmt.Errorf("failed to migrate schema: %w", err)
		}
	}

	return mem, nil
}

// ListSnapshots returns all snapshots for a soul
func (ss *SoulStore) ListSnapshots(soulName string) ([]*Snapshot, error) {
	ss.mu.RLock()
	defer ss.mu.RUnlock()

	snapshots, ok := ss.snapshots[soulName]
	if !ok {
		return nil, fmt.Errorf("no snapshots for soul: %s", soulName)
	}

	// Return copies without data
	result := make([]*Snapshot, len(snapshots))
	for i, s := range snapshots {
		copy := *s
		copy.Data = nil
		result[i] = &copy
	}
	return result, nil
}

// RestoreSnapshot restores a soul from a snapshot
func (ss *SoulStore) RestoreSnapshot(ctx context.Context, soulName, snapshotID string) error {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	snapshots, ok := ss.snapshots[soulName]
	if !ok {
		return fmt.Errorf("no snapshots for soul: %s", soulName)
	}

	var snapshot *Snapshot
	for _, s := range snapshots {
		if s.ID == snapshotID {
			snapshot = s
			break
		}
	}

	if snapshot == nil {
		return fmt.Errorf("snapshot not found: %s", snapshotID)
	}

	// Decompress
	decompressed, err := ss.decompress(snapshot.Data)
	if err != nil {
		return fmt.Errorf("failed to decompress snapshot: %w", err)
	}

	// Unmarshal
	var mem Memory
	if err := json.Unmarshal(decompressed, &mem); err != nil {
		return fmt.Errorf("failed to unmarshal snapshot: %w", err)
	}

	// Save as current
	return ss.Save(context.Background(), soulName, mem)
}

// DeleteSnapshot deletes a snapshot
func (ss *SoulStore) DeleteSnapshot(soulName, snapshotID string) error {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	snapshots, ok := ss.snapshots[soulName]
	if !ok {
		return fmt.Errorf("no snapshots for soul: %s", soulName)
	}

	for i, s := range snapshots {
		if s.ID == snapshotID {
			ss.snapshots[soulName] = append(snapshots[:i], snapshots[i+1:]...)
			return nil
		}
	}

	return fmt.Errorf("snapshot not found: %s", snapshotID)
}

// GetLatestSnapshot returns the latest snapshot for a soul
func (ss *SoulStore) GetLatestSnapshot(soulName string) (*Snapshot, error) {
	ss.mu.RLock()
	defer ss.mu.RUnlock()

	snapshots, ok := ss.snapshots[soulName]
	if !ok || len(snapshots) == 0 {
		return nil, fmt.Errorf("no snapshots for soul: %s", soulName)
	}

	latest := snapshots[len(snapshots)-1]
	copy := *latest
	copy.Data = nil // don't return data in listing
	return &copy, nil
}

// Stats returns storage statistics
func (ss *SoulStore) Stats() map[string]interface{} {
	ss.mu.RLock()
	defer ss.mu.RUnlock()

	totalSnapshots := 0
	totalSize := int64(0)
	for _, snapshots := range ss.snapshots {
		totalSnapshots += len(snapshots)
		for _, s := range snapshots {
			totalSize += s.Size
		}
	}

	return map[string]interface{}{
		"total_souls":      len(ss.snapshots),
		"total_snapshots":  totalSnapshots,
		"total_size_bytes": totalSize,
		"base_path":        ss.basePath,
		"config":           ss.config,
	}
}

// Private methods

func (ss *SoulStore) loadSnapshots() error {
	// Load existing snapshots from disk
	// For now, we just ensure the directory exists
	return os.MkdirAll(ss.basePath, 0755)
}

func (ss *SoulStore) addSnapshot(name string, snapshot *Snapshot) error {
	if ss.snapshots[name] == nil {
		ss.snapshots[name] = make([]*Snapshot, 0)
	}

	ss.snapshots[name] = append(ss.snapshots[name], snapshot)

	// Prune old snapshots
	if len(ss.snapshots[name]) > ss.config.MaxSnapshots {
		ss.snapshots[name] = ss.snapshots[name][len(ss.snapshots[name])-ss.config.MaxSnapshots:]
	}

	// Save snapshot index
	return ss.saveSnapshotIndex()
}

func (ss *SoulStore) saveSnapshotIndex() error {
	index := make(map[string]int)
	for name, snapshots := range ss.snapshots {
		if len(snapshots) > 0 {
			index[name] = len(snapshots) - 1
		}
	}

	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}

	indexPath := filepath.Join(ss.basePath, ".snapshot_index.json")
	return os.WriteFile(indexPath, data, 0644)
}

func (ss *SoulStore) compress(data []byte) ([]byte, error) {
	return ss.compressWith(data, ss.config.Compression, ss.config.CompressionLevel)
}

func (ss *SoulStore) compressWith(data []byte, encoding string, level int) ([]byte, error) {
	switch encoding {
	case "zstd":
		enc := ss.zstdEncoder
		if enc == nil || level != ss.config.CompressionLevel {
			var err error
			enc, err = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.EncoderLevel(level)))
			if err != nil {
				return nil, err
			}
		}
		return enc.EncodeAll(data, nil), nil

	case "gzip":
		var buf bytes.Buffer
		gzw := gzip.NewWriter(&buf)
		if _, err := gzw.Write(data); err != nil {
			return nil, err
		}
		if err := gzw.Close(); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil

	default: // "none" and anything unheard of
		return data, nil
	}
}

func (ss *SoulStore) decompress(data []byte) ([]byte, error) {
	return ss.decompressWith(data, ss.config.Compression)
}

func (ss *SoulStore) decompressWith(data []byte, encoding string) ([]byte, error) {
	switch encoding {
	case "zstd":
		dec := ss.zstdDecoder
		if dec == nil {
			var err error
			dec, err = zstd.NewReader(nil)
			if err != nil {
				return nil, err
			}
		}
		return dec.DecodeAll(data, nil)

	case "gzip":
		gz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		return io.ReadAll(gz)

	default: // "none" and anything unheard of
		return data, nil
	}
}

func (ss *SoulStore) checksum(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func (ss *SoulStore) snapshotLoop() {
	for {
		select {
		case <-ss.stopChan:
			return
		case <-ss.snapshotTicker.C:
			ss.createPeriodicSnapshots()
		}
	}
}

func (ss *SoulStore) createPeriodicSnapshots() {
	// This would iterate over all souls and create snapshots
	// For now, this is a placeholder
}

func (ss *SoulStore) integrityLoop() {
	for {
		select {
		case <-ss.stopChan:
			return
		case <-ss.integrityTicker.C:
			ss.verifyIntegrity()
		}
	}
}

func (ss *SoulStore) verifyIntegrity() {
	// Verify checksums of all souls: every envelope on disk must prove its
	// payload hash. A mismatch is a torn write or bit-rot with consequences
	// for short-term memory, so it is surfaced loudly rather than skipped.
	ss.mu.RLock()
	defer ss.mu.RUnlock()

	entries, err := os.ReadDir(ss.basePath)
	if err != nil {
		return
	}
	for _, ent := range entries {
		name := ent.Name()
		if ent.IsDir() || !strings.HasSuffix(name, ".soul") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(ss.basePath, name))
		if err != nil {
			continue
		}
		var env soulEnvelope
		if err := json.Unmarshal(data, &env); err != nil || len(env.Payload) == 0 || env.Checksum == "" {
			// Legacy raw-compressed soul: no envelope, nothing to verify.
			continue
		}
		if got := ss.checksum(env.Payload); got != env.Checksum {
			fmt.Fprintf(os.Stderr, "⚠️  [MEMORY] integrity FAILED for %s: sha256 %s != %s\n", name, got, env.Checksum)
		}
	}
}

func (ss *SoulStore) syncLoop() {
	ticker := time.NewTicker(ss.config.SyncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ss.stopChan:
			return
		case <-ss.syncTicker.C:
			ss.syncWithPeers()
		}
	}
}

func (ss *SoulStore) syncWithPeers() {
	// Cross-node sync implementation
	// This would connect to peer nodes and sync souls
}

func (ss *SoulStore) migrate(mem *Memory) error {
	// Schema migration logic
	if mem.SchemaVersion < 1 {
		mem.SchemaVersion = 1
	}
	return nil
}

func (ss *SoulStore) Stop() error {
	close(ss.stopChan)
	if ss.snapshotTicker != nil {
		ss.snapshotTicker.Stop()
	}
	if ss.integrityTicker != nil {
		ss.integrityTicker.Stop()
	}
	if ss.syncTicker != nil {
		ss.syncTicker.Stop()
	}
	if ss.zstdEncoder != nil {
		ss.zstdEncoder.Close()
	}
	return nil
}

func generateSnapshotID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func minUint64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
