package hivemind

import (
	"sync"
	"sync/atomic"
	"time"
)

type PeerSample struct {
	BytesSent     uint64
	BytesReceived uint64
	Timestamp     time.Time
}

type PeerBandwidth struct {
	PeerID           string
	BytesSent        uint64
	BytesReceived    uint64
	CurrentSendRate  float64
	CurrentRecvRate  float64
	PeakSendRate     float64
	PeakRecvRate     float64
	AvgSendRate      float64
	AvgRecvRate      float64
	TotalSent        uint64
	TotalReceived    uint64
	Samples          int
	LastUpdate       time.Time
	WindowSamples    []RateSample
}

type RateSample struct {
	Timestamp   time.Time
	SendRate    float64
	RecvRate    float64
}

type GlobalBandwidthStats struct {
	TotalSent        uint64
	TotalReceived    uint64
	CurrentSendRate  float64
	CurrentRecvRate  float64
	PeakSendRate     float64
	PeakRecvRate     float64
	AvgSendRate      float64
	AvgRecvRate      float64
	ActivePeers      int
	OverLimitCount   int64
}

type TokenBucket struct {
	mu         sync.Mutex
	capacity   float64
	tokens     float64
	refillRate float64
	lastRefill time.Time
}

type BandwidthAccounting struct {
	mu             sync.RWMutex
	config         MeshConfig
	peerUsage      map[string]*PeerBandwidth
	peerBuckets    map[string]*TokenBucket
	globalBucket   *TokenBucket
	globalStats    GlobalBandwidthStats
	prevSample     map[string]PeerSample
	lastSampleTime time.Time
	sampleTicker   *time.Ticker
	stopChan       chan struct{}
}

func NewBandwidthAccounting(config MeshConfig) *BandwidthAccounting {
	ba := &BandwidthAccounting{
		config:      config,
		peerUsage:   make(map[string]*PeerBandwidth),
		peerBuckets: make(map[string]*TokenBucket),
		globalBucket: &TokenBucket{
			capacity:   float64(config.TotalBandwidthLimit),
			tokens:     float64(config.TotalBandwidthLimit),
			refillRate: float64(config.TotalBandwidthLimit),
			lastRefill: time.Now(),
		},
		prevSample:    make(map[string]PeerSample),
		lastSampleTime: time.Now(),
		stopChan:      make(chan struct{}),
	}
	if config.EnableBandwidthAccounting {
		ba.sampleTicker = time.NewTicker(config.BandwidthSampleInterval)
		go ba.sampleLoop()
	}
	return ba
}

func (ba *BandwidthAccounting) RecordSent(peerID string, bytes int) {
	ba.mu.Lock()
	defer ba.mu.Unlock()
	pb, ok := ba.peerUsage[peerID]
	if !ok {
		pb = &PeerBandwidth{
			PeerID:        peerID,
			WindowSamples: make([]RateSample, 0, 60),
		}
		ba.peerUsage[peerID] = pb
	}
	pb.BytesSent += uint64(bytes)
	pb.TotalSent += uint64(bytes)
}

func (ba *BandwidthAccounting) RecordReceived(peerID string, bytes int) {
	ba.mu.Lock()
	defer ba.mu.Unlock()
	pb, ok := ba.peerUsage[peerID]
	if !ok {
		pb = &PeerBandwidth{
			PeerID:        peerID,
			WindowSamples: make([]RateSample, 0, 60),
		}
		ba.peerUsage[peerID] = pb
	}
	pb.BytesReceived += uint64(bytes)
	pb.TotalReceived += uint64(bytes)
}

func (ba *BandwidthAccounting) TryConsume(bytes int) bool {
	return ba.globalBucket.TryConsume(float64(bytes))
}

func (ba *BandwidthAccounting) TryConsumePeer(peerID string, bytes int) bool {
	ba.mu.RLock()
	bucket, ok := ba.peerBuckets[peerID]
	ba.mu.RUnlock()
	if !ok {
		ba.mu.Lock()
		bucket = &TokenBucket{
			capacity:   float64(ba.config.MaxBandwidthPerPeer),
			tokens:     float64(ba.config.MaxBandwidthPerPeer),
			refillRate: float64(ba.config.MaxBandwidthPerPeer),
			lastRefill: time.Now(),
		}
		ba.peerBuckets[peerID] = bucket
		ba.mu.Unlock()
	}
	return bucket.TryConsume(float64(bytes))
}

func (ba *BandwidthAccounting) GetPeerBandwidth(peerID string) (*PeerBandwidth, bool) {
	ba.mu.RLock()
	defer ba.mu.RUnlock()
	pb, ok := ba.peerUsage[peerID]
	if !ok {
		return nil, false
	}
	return pb.Copy(), true
}

func (ba *BandwidthAccounting) GetGlobalStats() GlobalBandwidthStats {
	ba.mu.RLock()
	defer ba.mu.RUnlock()
	return ba.globalStats.Copy()
}

func (ba *BandwidthAccounting) GetPeerStats(peerID string) (*PeerBandwidth, bool) {
	ba.mu.RLock()
	defer ba.mu.RUnlock()
	pb, ok := ba.peerUsage[peerID]
	if !ok {
		return nil, false
	}
	return pb.Copy(), true
}

func (ba *BandwidthAccounting) AllPeerStats() map[string]*PeerBandwidth {
	ba.mu.RLock()
	defer ba.mu.RUnlock()
	result := make(map[string]*PeerBandwidth, len(ba.peerUsage))
	for _, pb := range ba.peerUsage {
		result[pb.PeerID] = pb.Copy()
	}
	return result
}

func (ba *BandwidthAccounting) OverLimitPeers() []string {
	ba.mu.RLock()
	defer ba.mu.RUnlock()

	var overLimit []string
	for _, pb := range ba.peerUsage {
		if pb.CurrentSendRate > float64(ba.config.MaxBandwidthPerPeer) ||
			pb.CurrentRecvRate > float64(ba.config.MaxBandwidthPerPeer) {
			overLimit = append(overLimit, pb.PeerID)
		}
	}
	return overLimit
}

func (ba *BandwidthAccounting) sampleLoop() {
	for {
		select {
		case <-ba.stopChan:
			return
		case <-ba.sampleTicker.C:
			ba.sample()
		}
	}
}

func (ba *BandwidthAccounting) sample() {
	now := time.Now()
	elapsed := now.Sub(ba.lastSampleTime).Seconds()
	if elapsed <= 0 {
		return
	}
	ba.mu.Lock()
	defer ba.mu.Unlock()
	for _, pb := range ba.peerUsage {
	prev, ok := ba.prevSample[pb.PeerID]
		if ok && prev.Timestamp.Before(ba.lastSampleTime) {
			interval := ba.lastSampleTime.Sub(prev.Timestamp).Seconds()
			if interval > 0 {
				sendRate := float64(pb.BytesSent-prev.BytesSent) / interval
				recvRate := float64(pb.BytesReceived-prev.BytesReceived) / interval
				pb.CurrentSendRate = sendRate
				pb.CurrentRecvRate = recvRate
				if sendRate > pb.PeakSendRate {
					pb.PeakSendRate = sendRate
				}
				if recvRate > pb.PeakRecvRate {
					pb.PeakRecvRate = recvRate
				}
				pb.WindowSamples = append(pb.WindowSamples, RateSample{
					Timestamp: now,
					SendRate:  sendRate,
					RecvRate:  recvRate,
				})
				if len(pb.WindowSamples) > 60 {
					pb.WindowSamples = pb.WindowSamples[1:]
				}
				var sendSum, recvSum float64
				for _, s := range pb.WindowSamples {
					sendSum += s.SendRate
					recvSum += s.RecvRate
				}
				pb.AvgSendRate = sendSum / float64(len(pb.WindowSamples))
				pb.AvgRecvRate = recvSum / float64(len(pb.WindowSamples))
				pb.LastUpdate = now
				pb.Samples++
			}
		}
		ba.prevSample[pb.PeerID] = PeerSample{
			BytesSent:     pb.BytesSent,
			BytesReceived: pb.BytesReceived,
			Timestamp:     ba.lastSampleTime,
		}
	}
	ba.updateGlobalStats()
	ba.lastSampleTime = now
}

func (ba *BandwidthAccounting) updateGlobalStats() {
	var totalSendRate, totalRecvRate float64
	activePeers := 0
	overLimit := int64(0)
	for _, pb := range ba.peerUsage {
		if pb.CurrentSendRate > 0 || pb.CurrentRecvRate > 0 {
			activePeers++
		}
		totalSendRate += pb.CurrentSendRate
		totalRecvRate += pb.CurrentRecvRate
		if pb.CurrentSendRate > float64(ba.config.MaxBandwidthPerPeer) ||
			pb.CurrentRecvRate > float64(ba.config.MaxBandwidthPerPeer) {
			overLimit++
		}
		if pb.CurrentSendRate > ba.globalStats.PeakSendRate {
			ba.globalStats.PeakSendRate = pb.CurrentSendRate
		}
		if pb.CurrentRecvRate > ba.globalStats.PeakRecvRate {
			ba.globalStats.PeakRecvRate = pb.CurrentRecvRate
		}
	}
	ba.globalStats.CurrentSendRate = totalSendRate
	ba.globalStats.CurrentRecvRate = totalRecvRate
	ba.globalStats.ActivePeers = activePeers
	atomic.StoreInt64(&ba.globalStats.OverLimitCount, overLimit)
	if totalSendRate > ba.globalStats.PeakSendRate {
		ba.globalStats.PeakSendRate = totalSendRate
	}
	if totalRecvRate > ba.globalStats.PeakRecvRate {
		ba.globalStats.PeakRecvRate = totalRecvRate
	}
}

func (tb *TokenBucket) TryConsume(tokens float64) bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	tb.refill()
	if tb.tokens >= tokens {
		tb.tokens -= tokens
		return true
	}
	return false
}

func (tb *TokenBucket) refill() {
	now := time.Now()
	elapsed := now.Sub(tb.lastRefill).Seconds()
	if elapsed <= 0 {
		return
	}
	tb.tokens += elapsed * tb.refillRate
	if tb.tokens > tb.capacity {
		tb.tokens = tb.capacity
	}
	tb.lastRefill = now
}

func (pb *PeerBandwidth) Copy() *PeerBandwidth {
	return &PeerBandwidth{
		PeerID:         pb.PeerID,
		BytesSent:      pb.BytesSent,
		BytesReceived:  pb.BytesReceived,
		CurrentSendRate: pb.CurrentSendRate,
		CurrentRecvRate: pb.CurrentRecvRate,
		PeakSendRate:   pb.PeakSendRate,
		PeakRecvRate:   pb.PeakRecvRate,
		AvgSendRate:    pb.AvgSendRate,
		AvgRecvRate:    pb.AvgRecvRate,
		TotalSent:      pb.TotalSent,
		TotalReceived:  pb.TotalReceived,
		Samples:        pb.Samples,
		LastUpdate:     pb.LastUpdate,
	}
}

func (gbs *GlobalBandwidthStats) Copy() GlobalBandwidthStats {
	return GlobalBandwidthStats{
		TotalSent:       gbs.TotalSent,
		TotalReceived:   gbs.TotalReceived,
		CurrentSendRate: gbs.CurrentSendRate,
		CurrentRecvRate: gbs.CurrentRecvRate,
		PeakSendRate:    gbs.PeakSendRate,
		PeakRecvRate:    gbs.PeakRecvRate,
		AvgSendRate:     gbs.AvgSendRate,
		AvgRecvRate:     gbs.AvgRecvRate,
		ActivePeers:     gbs.ActivePeers,
		OverLimitCount:  gbs.OverLimitCount,
	}
}

func (ba *BandwidthAccounting) Stop() {
	close(ba.stopChan)
	if ba.sampleTicker != nil {
		ba.sampleTicker.Stop()
	}
}

func (ba *BandwidthAccounting) GetStats() map[string]interface{} {
	ba.mu.RLock()
	defer ba.mu.RUnlock()
	return map[string]interface{}{
		"global":       ba.globalStats.Copy(),
		"peer_count":   len(ba.peerUsage),
		"over_limit":   ba.globalStats.OverLimitCount,
	}
}