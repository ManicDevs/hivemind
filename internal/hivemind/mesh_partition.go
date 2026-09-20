package hivemind

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

type PeerInfo struct {
	ID           string
	Address      string
	LastSeen     time.Time
	Capabilities []string
	AddedAt      time.Time
}

type PeerState struct {
	PeerID            string
	LastHeartbeat     time.Time
	ConsecutiveMisses int
	IsHealthy         bool
	LastScore         float64
	MissedHeartbeats  int
	TotalHeartbeats   int
}

type Partition struct {
	ID         string
	Members    []string
	DetectedAt time.Time
	HealedAt   *time.Time
	Size       int
	Severity   float64
	Healing    bool
}

type PartitionDetector struct {
	mu            sync.RWMutex
	config        MeshConfig
	expectedPeers map[string]PeerInfo
	peerStates    map[string]PeerState
	partitions    []Partition
	stopChan      chan struct{}
	checkTicker   *time.Ticker
}

func NewPartitionDetector(config MeshConfig) *PartitionDetector {
	pd := &PartitionDetector{
		config:        config,
		expectedPeers: make(map[string]PeerInfo),
		peerStates:    make(map[string]PeerState),
		partitions:    make([]Partition, 0),
		stopChan:      make(chan struct{}),
	}
	if config.EnablePartitionDetection {
		pd.checkTicker = time.NewTicker(config.PartitionCheckInterval)
		go pd.checkLoop()
	}
	return pd
}

func (pd *PartitionDetector) RegisterPeer(peerID, address string, capabilities []string) {
	pd.mu.Lock()
	defer pd.mu.Unlock()
	id := pd.normalizeID(peerID)
	pd.expectedPeers[id] = PeerInfo{
		ID:           id,
		Address:      address,
		LastSeen:     time.Now(),
		Capabilities: capabilities,
		AddedAt:      time.Now(),
	}
	pd.peerStates[id] = PeerState{
		PeerID:          id,
		LastHeartbeat:   time.Now(),
		IsHealthy:       true,
		TotalHeartbeats: 1,
	}
}

func (pd *PartitionDetector) Heartbeat(peerID string) {
	pd.mu.Lock()
	defer pd.mu.Unlock()
	id := pd.normalizeID(peerID)
	if state, ok := pd.peerStates[id]; ok {
		state.LastHeartbeat = time.Now()
		state.TotalHeartbeats++
		state.ConsecutiveMisses = 0
		state.IsHealthy = true
		pd.peerStates[id] = state
	}
}

func (pd *PartitionDetector) RecordMiss(peerID string) {
	pd.mu.Lock()
	defer pd.mu.Unlock()
	id := pd.normalizeID(peerID)
	if state, ok := pd.peerStates[id]; ok {
		state.ConsecutiveMisses++
		state.MissedHeartbeats++
		if state.ConsecutiveMisses >= 3 {
			state.IsHealthy = false
		}
		pd.peerStates[id] = state
	}
}

func (pd *PartitionDetector) GetPeerState(peerID string) (PeerState, bool) {
	pd.mu.RLock()
	defer pd.mu.RUnlock()
	state, ok := pd.peerStates[pd.normalizeID(peerID)]
	return state, ok
}

func (pd *PartitionDetector) GetHealthyPeers() []string {
	pd.mu.RLock()
	defer pd.mu.RUnlock()
	var healthy []string
	for id, state := range pd.peerStates {
		if state.IsHealthy {
			healthy = append(healthy, id)
		}
	}
	return healthy
}

func (pd *PartitionDetector) GetUnhealthyPeers() []string {
	pd.mu.RLock()
	defer pd.mu.RUnlock()
	var unhealthy []string
	for id, state := range pd.peerStates {
		if !state.IsHealthy {
			unhealthy = append(unhealthy, id)
		}
	}
	return unhealthy
}

func (pd *PartitionDetector) DetectPartitions() []Partition {
	pd.mu.Lock()
	defer pd.mu.Unlock()
	pd.cleanHealedPartitions()
	healthy := pd.getHealthyPeerIDs()
	unhealthy := pd.getUnhealthyPeerIDs()
	expectedSize := len(pd.expectedPeers)
	if expectedSize < pd.config.MinMeshSize {
		expectedSize = pd.config.MinMeshSize
	}
	healthyRatio := float64(len(healthy)) / float64(expectedSize)
	if healthyRatio < pd.config.PartitionThreshold {
		partition := Partition{
			ID:         generatePartitionID(),
			Members:    unhealthy,
			DetectedAt: time.Now(),
			Size:       len(unhealthy),
			Severity:   1.0 - healthyRatio,
			Healing:    false,
		}
		pd.partitions = append(pd.partitions, partition)
	}
	return pd.partitions
}

func (pd *PartitionDetector) GetPartitions() []Partition {
	pd.mu.RLock()
	defer pd.mu.RUnlock()
	return pd.partitions
}

func (pd *PartitionDetector) ActivePartitions() []Partition {
	pd.mu.RLock()
	defer pd.mu.RUnlock()
	var active []Partition
	for _, p := range pd.partitions {
		if p.HealedAt == nil {
			active = append(active, p)
		}
	}
	return active
}

func (pd *PartitionDetector) HealPartition(partitionID string) {
	pd.mu.Lock()
	defer pd.mu.Unlock()
	for i := range pd.partitions {
		if pd.partitions[i].ID == partitionID && pd.partitions[i].HealedAt == nil {
			now := time.Now()
			pd.partitions[i].HealedAt = &now
			pd.partitions[i].Healing = false
		}
	}
}

func (pd *PartitionDetector) Heal(ctx context.Context, partitionID string, dialFunc func(string) error) error {
	pd.mu.Lock()
	var partition *Partition
	for i := range pd.partitions {
		if pd.partitions[i].ID == partitionID {
			partition = &pd.partitions[i]
			break
		}
	}
	pd.mu.Unlock()
	if partition == nil {
		return nil
	}
	if partition.Healing {
		return nil
	}
	pd.mu.Lock()
	partition.Healing = true
	pd.mu.Unlock()
	for range partition.Members {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
	healthy := pd.GetHealthyPeers()
	allHealthy := true
	for _, m := range partition.Members {
		found := false
		for _, h := range healthy {
			if h == m {
				found = true
				break
			}
		}
		if !found {
			allHealthy = false
			break
		}
	}
	if allHealthy {
		pd.HealPartition(partitionID)
	}
	return nil
}

func (pd *PartitionDetector) checkLoop() {
	for {
		select {
		case <-pd.stopChan:
			return
		case <-pd.checkTicker.C:
			pd.DetectPartitions()
			pd.checkPeerHealth()
		}
	}
}

func (pd *PartitionDetector) checkPeerHealth() {
	pd.mu.Lock()
	defer pd.mu.Unlock()
	now := time.Now()
	for id, state := range pd.peerStates {
		if now.Sub(state.LastHeartbeat) > pd.config.PartitionCheckInterval*3 {
			state.ConsecutiveMisses++
			state.MissedHeartbeats++
			if state.ConsecutiveMisses >= 3 {
				state.IsHealthy = false
			}
			pd.peerStates[id] = state
		}
	}
}

func (pd *PartitionDetector) cleanHealedPartitions() {
	var active []Partition
	for _, p := range pd.partitions {
		if p.HealedAt == nil {
			active = append(active, p)
		}
	}
	pd.partitions = active
}

func (pd *PartitionDetector) getHealthyPeerIDs() []string {
	var healthy []string
	for id, state := range pd.peerStates {
		if state.IsHealthy {
			healthy = append(healthy, id)
		}
	}
	return healthy
}

func (pd *PartitionDetector) getUnhealthyPeerIDs() []string {
	var unhealthy []string
	for id, state := range pd.peerStates {
		if !state.IsHealthy {
			unhealthy = append(unhealthy, id)
		}
	}
	return unhealthy
}

func (pd *PartitionDetector) normalizeID(id string) string {
	return id
}

func (pd *PartitionDetector) Stop() {
	close(pd.stopChan)
	if pd.checkTicker != nil {
		pd.checkTicker.Stop()
	}
}

func (pd *PartitionDetector) GetStats() map[string]interface{} {
	pd.mu.RLock()
	defer pd.mu.RUnlock()
	active := 0
	healed := 0
	for _, p := range pd.partitions {
		if p.HealedAt == nil {
			active++
		} else {
			healed++
		}
	}
	return map[string]interface{}{
		"total_peers":       len(pd.expectedPeers),
		"healthy_peers":     len(pd.GetHealthyPeers()),
		"unhealthy_peers":   len(pd.GetUnhealthyPeers()),
		"active_partitions": len(pd.ActivePartitions()),
		"healed_partitions": healed,
	}
}

func generatePartitionID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
