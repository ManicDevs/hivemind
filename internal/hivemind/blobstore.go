package hivemind

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	ErrBlobNotFound     = errors.New("blob not found")
	ErrBlobTooLarge     = errors.New("blob too large")
	ErrInvalidChecksum  = errors.New("invalid checksum")
	ErrStorageFull      = errors.New("storage full")
)

type BlobConfig struct {
	BasePath           string
	MaxBlobSize        int64
	MaxTotalSize       int64
	EnableCompression  bool
	CompressionLevel   int
	EnableDeduplication bool
	EnableEncryption   bool
	EncryptionKey      []byte
	ChunkSize          int
	EnableVersioning   bool
	MaxVersions        int
	GCInterval         time.Duration
	MaxAge             time.Duration
}

func DefaultBlobConfig() BlobConfig {
	return BlobConfig{
		BasePath:           ".hive_blobs",
		MaxBlobSize:        100 * 1024 * 1024,
		MaxTotalSize:       10 * 1024 * 1024 * 1024,
		EnableCompression:  true,
		CompressionLevel:   3,
		EnableDeduplication: true,
		EnableEncryption:   false,
		ChunkSize:          4 * 1024 * 1024,
		EnableVersioning:   true,
		MaxVersions:        10,
		GCInterval:         1 * time.Hour,
		MaxAge:             30 * 24 * time.Hour,
	}
}

type BlobStore struct {
	mu           sync.RWMutex
	config       BlobConfig
	basePath     string
	index        map[string]*BlobIndex
	sizeTotal    int64
	chunkStore   *ChunkStore
	stopChan     chan struct{}
	gcTicker     *time.Ticker
}

type BlobIndex struct {
	ID          string
	Name        string
	Size        int64
	Chunks      []ChunkRef
	Checksum    string
	ContentType string
	Metadata    map[string]string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Versions    []BlobVersion
	RefCount    int
	Deleted     bool
}

type BlobVersion struct {
	ID        string
	Checksum  string
	Size      int64
	Chunks    []ChunkRef
	CreatedAt time.Time
}

type ChunkRef struct {
	ID       string
	Offset   int64
	Size     int64
	Checksum string
}

type ChunkStore struct {
	mu       sync.RWMutex
	chunks   map[string]*Chunk
	basePath string
}

type Chunk struct {
	ID        string
	Data      []byte
	Checksum  string
	RefCount  int
	CreatedAt time.Time
}

func NewBlobStore(config BlobConfig) (*BlobStore, error) {
	if config.BasePath == "" {
		config.BasePath = ".hive_blobs"
	}
	if config.MaxBlobSize <= 0 {
		config.MaxBlobSize = 100 * 1024 * 1024
	}
	if config.ChunkSize <= 0 {
		config.ChunkSize = 4 * 1024 * 1024
	}

	basePath := config.BasePath
	if err := os.MkdirAll(basePath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create base path: %w", err)
	}

	bs := &BlobStore{
		config:    config,
		basePath:  basePath,
		index:     make(map[string]*BlobIndex),
		chunkStore: &ChunkStore{
			chunks:   make(map[string]*Chunk),
			basePath: filepath.Join(basePath, "chunks"),
		},
		stopChan: make(chan struct{}),
	}

	if err := os.MkdirAll(bs.chunkStore.basePath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create chunk path: %w", err)
	}

	if config.GCInterval > 0 {
		bs.gcTicker = time.NewTicker(config.GCInterval)
		go bs.gcLoop()
	}

	return bs, nil
}

func (bs *BlobStore) Put(ctx context.Context, name string, reader io.Reader, contentType string, metadata map[string]string) (*BlobIndex, error) {
	bs.mu.Lock()
	defer bs.mu.Unlock()

	var existing *BlobIndex
	if existingIdx, ok := bs.index[name]; ok && !existingIdx.Deleted {
		existing = existingIdx
	}

	chunks, checksum, size, err := bs.chunkData(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to chunk data: %w", err)
	}

	if size > bs.config.MaxBlobSize {
		return nil, ErrBlobTooLarge
	}

	if bs.sizeTotal+size > bs.config.MaxTotalSize {
		return nil, ErrStorageFull
	}

	blobID := generateBlobID()
	now := time.Now()

	index := &BlobIndex{
		ID:          blobID,
		Name:        name,
		Size:        size,
		Chunks:      chunks,
		Checksum:    hex.EncodeToString(checksum),
		ContentType: contentType,
		Metadata:    metadata,
		CreatedAt:   now,
		UpdatedAt:   now,
		Versions:    []BlobVersion{},
		RefCount:    1,
	}

	if existing != nil && bs.config.EnableVersioning {
		version := BlobVersion{
			ID:        generateBlobID(),
			Checksum:  existing.Checksum,
			Size:      existing.Size,
			Chunks:    existing.Chunks,
			CreatedAt: existing.UpdatedAt,
		}
		index.Versions = append(existing.Versions, version)
		if len(index.Versions) > bs.config.MaxVersions {
			index.Versions = index.Versions[len(index.Versions)-bs.config.MaxVersions:]
		}
	}

	bs.index[name] = index
	bs.sizeTotal += size

	if err := bs.persistIndex(name); err != nil {
		return nil, fmt.Errorf("failed to persist index: %w", err)
	}

	return index, nil
}

func (bs *BlobStore) Get(ctx context.Context, name string) (io.ReadCloser, *BlobIndex, error) {
	bs.mu.RLock()
	index, ok := bs.index[name]
	bs.mu.RUnlock()

	if !ok || index.Deleted {
		return nil, nil, ErrBlobNotFound
	}

	reader, err := bs.readChunks(index.Chunks)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read chunks: %w", err)
	}

	return reader, index, nil
}

func (bs *BlobStore) Delete(ctx context.Context, name string) error {
	bs.mu.Lock()
	defer bs.mu.Unlock()

	index, ok := bs.index[name]
	if !ok {
		return ErrBlobNotFound
	}

	index.Deleted = true
	index.RefCount = 0
	bs.sizeTotal -= index.Size

	for _, chunk := range index.Chunks {
		bs.chunkStore.decrementRef(chunk.ID)
	}

	return bs.persistIndex(name)
}

func (bs *BlobStore) Exists(name string) bool {
	bs.mu.RLock()
	defer bs.mu.RUnlock()

	index, ok := bs.index[name]
	return ok && !index.Deleted
}

func (bs *BlobStore) List(prefix string) ([]*BlobIndex, error) {
	bs.mu.RLock()
	defer bs.mu.RUnlock()

	var results []*BlobIndex
	for _, index := range bs.index {
		if !index.Deleted && (prefix == "" || len(index.Name) >= len(prefix) && index.Name[:len(prefix)] == prefix) {
			results = append(results, index)
		}
	}
	return results, nil
}

func (bs *BlobStore) GetIndex(name string) (*BlobIndex, error) {
	bs.mu.RLock()
	defer bs.mu.RUnlock()

	index, ok := bs.index[name]
	if !ok || index.Deleted {
		return nil, ErrBlobNotFound
	}
	return index, nil
}

func (bs *BlobStore) GetStats() map[string]interface{} {
	bs.mu.RLock()
	defer bs.mu.RUnlock()

	count := 0
	for _, idx := range bs.index {
		if !idx.Deleted {
			count++
		}
	}

	return map[string]interface{}{
		"total_blobs":    count,
		"total_size":     bs.sizeTotal,
		"total_chunks":   len(bs.chunkStore.chunks),
		"config":         bs.config,
	}
}

func (bs *BlobStore) chunkData(reader io.Reader) ([]ChunkRef, []byte, int64, error) {
	var chunks []ChunkRef
	var totalSize int64
	hasher := sha256.New()
	buf := make([]byte, bs.config.ChunkSize)

	for {
		n, err := reader.Read(buf)
		if n > 0 {
			chunkData := buf[:n]
			hasher.Write(chunkData)

			chunk := &Chunk{
				ID:        generateBlobID(),
				Data:      chunkData,
				Checksum:  hex.EncodeToString(sha256.New().Sum(chunkData)),
				RefCount:  1,
				CreatedAt: time.Now(),
			}

			if err := bs.chunkStore.put(chunk); err != nil {
				return nil, nil, 0, err
			}

			chunks = append(chunks, ChunkRef{
				ID:       chunk.ID,
				Offset:   totalSize,
				Size:     int64(n),
				Checksum: hex.EncodeToString(sha256.New().Sum(chunkData)),
			})

			totalSize += int64(n)
		}

		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, 0, err
		}
	}

	return chunks, hasher.Sum(nil), totalSize, nil
}

func (bs *BlobStore) readChunks(chunks []ChunkRef) (io.ReadCloser, error) {
	readers := make([]io.Reader, len(chunks))
	for i, chunkRef := range chunks {
		chunk, err := bs.chunkStore.get(chunkRef.ID)
		if err != nil {
			return nil, err
		}
		readers[i] = bytes.NewReader(chunk.Data)
	}

	return io.NopCloser(io.MultiReader(readers...)), nil
}

func (bs *BlobStore) persistIndex(name string) error {
	index := bs.index[name]
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}

	path := filepath.Join(bs.basePath, name+".idx")
	return os.WriteFile(path, data, 0644)
}

func (bs *BlobStore) gcLoop() {
	for {
		select {
		case <-bs.stopChan:
			return
		case <-bs.gcTicker.C:
			bs.gc()
		}
	}
}

func (bs *BlobStore) gc() {
	bs.mu.Lock()
	defer bs.mu.Unlock()

	now := time.Now()
	for name, index := range bs.index {
		if index.Deleted && index.RefCount == 0 {
			if bs.config.MaxAge > 0 && now.Sub(index.UpdatedAt) > bs.config.MaxAge {
				for _, chunk := range index.Chunks {
					bs.chunkStore.decrementRef(chunk.ID)
				}
				delete(bs.index, name)
			}
		}
	}
}

func (bs *BlobStore) Stop() error {
	close(bs.stopChan)
	if bs.gcTicker != nil {
		bs.gcTicker.Stop()
	}
	return nil
}

func (cs *ChunkStore) put(chunk *Chunk) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	cs.chunks[chunk.ID] = chunk

	path := filepath.Join(cs.basePath, chunk.ID)
	return os.WriteFile(path, chunk.Data, 0644)
}

func (cs *ChunkStore) get(id string) (*Chunk, error) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	chunk, ok := cs.chunks[id]
	if ok {
		return chunk, nil
	}

	path := filepath.Join(cs.basePath, id)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	newChunk := &Chunk{
		ID:        id,
		Data:      data,
		Checksum:  hex.EncodeToString(sha256.New().Sum(data)),
		RefCount:  1,
		CreatedAt: time.Now(),
	}
	cs.chunks[id] = newChunk
	return newChunk, nil
}

func (cs *ChunkStore) decrementRef(id string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if chunk, ok := cs.chunks[id]; ok {
		chunk.RefCount--
		if chunk.RefCount <= 0 {
			delete(cs.chunks, id)
			os.Remove(filepath.Join(cs.basePath, id))
		}
	}
}

func generateBlobID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}