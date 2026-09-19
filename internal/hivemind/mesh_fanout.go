package hivemind

import (
	"math"
	"sync"
	"time"
)

type AdaptiveFanout struct {
	mu             sync.RWMutex
	config         MeshConfig
	currentFanout  int
	lastUpdate     time.Time
	meshSize       int
	totalBandwidth float64
	avgLatency     float64
}

func NewAdaptiveFanout(config MeshConfig) *AdaptiveFanout {
	af := &AdaptiveFanout{
		config:         config,
		currentFanout:  config.MinFanout,
		lastUpdate:     time.Now(),
	}
	if config.EnableAdaptiveFanout {
		go af.updateLoop()
	}
	return af
}

func (af *AdaptiveFanout) CurrentFanout() int {
	af.mu.RLock()
	defer af.mu.RUnlock()
	return af.currentFanout
}

func (af *AdaptiveFanout) UpdateMeshState(meshSize int, totalBandwidth float64, avgLatency float64) {
	af.mu.Lock()
	defer af.mu.Unlock()
	af.meshSize = meshSize
	af.totalBandwidth = totalBandwidth
	af.avgLatency = avgLatency
}

func (af *AdaptiveFanout) updateLoop() {
	for {
		time.Sleep(af.config.FanoutUpdateInterval)
		af.recalculate()
	}
}

func (af *AdaptiveFanout) recalculate() {
	af.mu.Lock()
	defer af.mu.Unlock()

	baseFanout := int(math.Log2(float64(af.meshSize+1))) + af.config.MinFanout

	bandwidthFactor := 1.0
	if af.totalBandwidth > 0 {
		bandwidthFactor = 1.0 + math.Log10(af.totalBandwidth/1024/1024+1)*0.2
	}

	latencyFactor := 1.0
	if af.avgLatency > 0 {
		if af.avgLatency < 10 {
			latencyFactor = 1.2
		} else if af.avgLatency < 50 {
			latencyFactor = 1.0
		} else if af.avgLatency < 200 {
			latencyFactor = 0.8
		} else {
			latencyFactor = 0.5
		}
	}

	target := int(float64(baseFanout) * bandwidthFactor * latencyFactor)
	af.currentFanout = clampInt(target, af.config.MinFanout, af.config.MaxFanout)
	af.lastUpdate = time.Now()
}

func (af *AdaptiveFanout) GetFanout() int {
	af.mu.RLock()
	defer af.mu.RUnlock()
	return af.currentFanout
}

func (af *AdaptiveFanout) GetStats() map[string]interface{} {
	af.mu.RLock()
	defer af.mu.RUnlock()
	return map[string]interface{}{
		"current_fanout": af.currentFanout,
		"mesh_size":      af.meshSize,
		"bandwidth_mbps": af.totalBandwidth / 1024 / 1024,
		"avg_latency_ms": af.avgLatency,
		"last_update":    af.lastUpdate,
	}
}