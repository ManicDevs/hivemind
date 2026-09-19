package hivemind

import (
	"errors"
	"math"
	"sync"
	"time"
)

var (
	ErrPunchMaxRetries = errors.New("max punch retries exceeded")
)

// MeshConfig holds all mesh configuration
type MeshConfig struct {
	// Peer scoring
	EnablePeerScoring     bool
	ScoreUpdateInterval   time.Duration
	ScoreDecayFactor      float64
	MinPeerScore          float64
	MaxPeerScore          float64

	// Adaptive fanout
	EnableAdaptiveFanout  bool
	MinFanout             int
	MaxFanout             int
	FanoutUpdateInterval  time.Duration

	// Partition detection
	EnablePartitionDetection bool
	PartitionCheckInterval   time.Duration
	MinMeshSize             int
	PartitionThreshold      float64

	// Bandwidth accounting
	EnableBandwidthAccounting bool
	BandwidthSampleInterval   time.Duration
	MaxBandwidthPerPeer       int64
	TotalBandwidthLimit       int64

	// Punch retry
	PunchMaxRetries       int
	PunchBaseBackoff      time.Duration
	PunchMaxBackoff       time.Duration
	PunchBackoffMultiplier float64

	// Health
	HealthCheckInterval   time.Duration
	UnhealthyThreshold    int

	// Discovery
	DiscoveryInterval     time.Duration
	StaleSocketAge        time.Duration
}

func DefaultMeshConfig() MeshConfig {
	return MeshConfig{
		EnablePeerScoring:        true,
		ScoreUpdateInterval:      30 * time.Second,
		ScoreDecayFactor:         0.95,
		MinPeerScore:             0.1,
		MaxPeerScore:             10.0,

		EnableAdaptiveFanout:  true,
		MinFanout:             3,
		MaxFanout:             32,
		FanoutUpdateInterval:  60 * time.Second,

		EnablePartitionDetection: true,
		PartitionCheckInterval:   60 * time.Second,
		MinMeshSize:              3,
		PartitionThreshold:       0.5,

		EnableBandwidthAccounting: true,
		BandwidthSampleInterval:   10 * time.Second,
		MaxBandwidthPerPeer:       1024 * 1024,
		TotalBandwidthLimit:       50 * 1024 * 1024,

		PunchMaxRetries:         5,
		PunchBaseBackoff:        2 * time.Second,
		PunchMaxBackoff:         60 * time.Second,
		PunchBackoffMultiplier:  2.0,

		HealthCheckInterval:  30 * time.Second,
		UnhealthyThreshold:   3,

		DiscoveryInterval:  2 * time.Second,
		StaleSocketAge:     5 * time.Second,
	}
}

type PeerScore struct {
	mu              sync.RWMutex
	PeerID          string
	FirstSeen       time.Time
	LastSeen        time.Time
	Uptime          float64
	Latency         float64
	Reliability     float64
	Bandwidth       float64
	FramesSent      uint64
	FramesReceived  uint64
	FramesDropped   uint64
	BytesSent       uint64
	BytesReceived   uint64
	Penalties       map[string]int
	ConsecutiveFails int
	LastScore       float64
	LastUpdate      time.Time
}

func (ps *PeerScore) Score() float64 {
	ps.mu.RLock()
	defer ps.mu.RUnlock()

	score := ps.Uptime*3.0 + ps.Reliability*3.0
	latencyBonus := math.Max(0, 2.0-ps.Latency*1000)
	score += math.Min(latencyBonus, 2.0)
	if ps.Bandwidth > 0 {
		score += math.Log10(ps.Bandwidth/1024+1) * 0.5
	}
	for _, count := range ps.Penalties {
		score -= float64(count) * 0.5
	}
	score -= float64(ps.ConsecutiveFails) * 1.0
	return math.Max(0, math.Min(score, 10.0))
}

func (ps *PeerScore) RecordSuccess(latency time.Duration, bytesSent, bytesReceived int) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	now := time.Now()
	ps.LastSeen = now
	latencyMs := float64(latency.Milliseconds())
	if ps.Latency == 0 {
		ps.Latency = latencyMs
	} else {
		ps.Latency = 0.875*ps.Latency + 0.125*latencyMs
	}
	ps.FramesSent++
	ps.FramesReceived++
	ps.BytesSent += uint64(bytesSent)
	ps.BytesReceived += uint64(bytesReceived)
	ps.ConsecutiveFails = 0
}

func (ps *PeerScore) RecordFailure() {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.ConsecutiveFails++
}

func (ps *PeerScore) AddPenalty(violation string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.Penalties == nil {
		ps.Penalties = make(map[string]int)
	}
	ps.Penalties[violation]++
}

func (ps *PeerScore) ApplyDecay(decayFactor float64, elapsed time.Duration) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if elapsed > 0 {
		decay := math.Pow(decayFactor, elapsed.Seconds()/30.0)
		ps.Uptime *= decay
		ps.Reliability *= decay
		ps.Latency *= 1.0 + (1.0-decay)*0.1
		ps.Bandwidth *= decay
		for k, v := range ps.Penalties {
			newVal := int(float64(v) * 0.95)
			if newVal <= 0 {
				delete(ps.Penalties, k)
			} else {
				ps.Penalties[k] = newVal
			}
		}
		if ps.ConsecutiveFails > 0 {
			ps.ConsecutiveFails = int(float64(ps.ConsecutiveFails) * 0.9)
		}
	}
}

func (ps *PeerScore) IsHealthy(threshold int) bool {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return ps.ConsecutiveFails < threshold
}

type PeerRegistry struct {
	mu          sync.RWMutex
	scores      map[string]*PeerScore
	config      MeshConfig
	decayTicker *time.Ticker
	stopChan    chan struct{}
	decayFactor float64
	lastDecay   time.Time
}

func NewPeerRegistry(config MeshConfig) *PeerRegistry {
	pr := &PeerRegistry{
		scores:      make(map[string]*PeerScore),
		config:      config,
		stopChan:    make(chan struct{}),
		decayFactor: config.ScoreDecayFactor,
		lastDecay:   time.Now(),
	}
	if config.EnablePeerScoring {
		pr.decayTicker = time.NewTicker(config.ScoreUpdateInterval)
		go pr.decayLoop()
	}
	return pr
}

func (pr *PeerRegistry) RecordPeer(peerID string) *PeerScore {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	if ps, ok := pr.scores[peerID]; ok {
		return ps
	}
	ps := &PeerScore{
		PeerID:    peerID,
		FirstSeen: time.Now(),
		LastSeen:  time.Now(),
		Penalties: make(map[string]int),
	}
	pr.scores[peerID] = ps
	return ps
}

func (pr *PeerRegistry) GetScore(peerID string) (*PeerScore, bool) {
	pr.mu.RLock()
	defer pr.mu.RUnlock()
	ps, ok := pr.scores[peerID]
	if !ok {
		return nil, false
	}
	return ps, true
}

func (pr *PeerRegistry) AllScores() map[string]float64 {
	pr.mu.RLock()
	defer pr.mu.RUnlock()
	result := make(map[string]float64, len(pr.scores))
	for id, ps := range pr.scores {
		result[id] = ps.Score()
	}
	return result
}

func (pr *PeerRegistry) HealthyPeers() []string {
	pr.mu.RLock()
	defer pr.mu.RUnlock()
	var healthy []string
	for id, ps := range pr.scores {
		if ps.IsHealthy(pr.config.UnhealthyThreshold) {
			healthy = append(healthy, id)
		}
	}
	return healthy
}

func (pr *PeerRegistry) RemovePeer(peerID string) {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	delete(pr.scores, peerID)
}

func (pr *PeerRegistry) decayLoop() {
	for {
		select {
		case <-pr.stopChan:
			return
		case <-pr.decayTicker.C:
			pr.applyDecay()
		}
	}
}

func (pr *PeerRegistry) applyDecay() {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	now := time.Now()
	elapsed := now.Sub(pr.lastDecay)
	pr.lastDecay = now
	for _, ps := range pr.scores {
		ps.ApplyDecay(pr.decayFactor, elapsed)
	}
}

func (pr *PeerRegistry) Stop() {
	close(pr.stopChan)
	if pr.decayTicker != nil {
		pr.decayTicker.Stop()
	}
}