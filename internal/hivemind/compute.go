package hivemind

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

var tracer = otel.Tracer("hivemind/compute")

type ComputeConfig struct {
	MaxCPUPercent      float64
	MaxMemoryMB        int
	TickTimeout        time.Duration
	CheckpointInterval int
	EnableTracing      bool
	EnableHotReload    bool
	HotReloadInterval  time.Duration
}

func DefaultComputeConfig() ComputeConfig {
	return ComputeConfig{
		MaxCPUPercent:      50.0,
		MaxMemoryMB:        512,
		TickTimeout:        2 * time.Second,
		CheckpointInterval: 100,
		EnableTracing:      true,
		EnableHotReload:    false,
		HotReloadInterval:  30 * time.Second,
	}
}

type ComputeEngine struct {
	mu           sync.RWMutex
	config       ComputeConfig
	mind         *Mind
	quota        *ResourceQuota
	checkpointer *Checkpointer
	tracer       trace.Tracer
	hotReloader  *HotReloader
	stopChan     chan struct{}
}

func NewComputeEngine(mind *Mind, config ComputeConfig) *ComputeEngine {
	ce := &ComputeEngine{
		config:   config,
		mind:     mind,
		quota:    NewResourceQuota(config.MaxCPUPercent, config.MaxMemoryMB),
		stopChan: make(chan struct{}),
	}

	if config.EnableTracing {
		ce.tracer = otel.Tracer("hivemind/compute")
	}

	ce.checkpointer = NewCheckpointer(mind, config.CheckpointInterval)
	go ce.checkpointer.Run()

	if config.EnableHotReload {
		ce.hotReloader = NewHotReloader(config.HotReloadInterval)
		go ce.hotReloader.Run()
	}

	return ce
}

func (ce *ComputeEngine) Run(ctx context.Context) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ce.stopChan:
			return nil
		case <-ticker.C:
			if err := ce.tick(ctx); err != nil {
				return err
			}
		}
	}
}

func (ce *ComputeEngine) tick(ctx context.Context) error {
	ctx, span := ce.tracer.Start(ctx, "mind.tick")
	defer span.End()

	// Check resource quota
	if !ce.quota.TryAcquire() {
		span.SetAttributes(attribute.String("quota", "exceeded"))
		return fmt.Errorf("resource quota exceeded")
	}
	defer ce.quota.Release()

	// Run tick with timeout
	ctx, cancel := context.WithTimeout(ctx, ce.config.TickTimeout)
	defer cancel()

	done := make(chan struct{}, 1)
	go func() {
		ce.mind.Cycle()
		done <- struct{}{}
	}()

	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

func (ce *ComputeEngine) Stop() {
	close(ce.stopChan)
	if ce.checkpointer != nil {
		ce.checkpointer.Stop()
	}
	if ce.hotReloader != nil {
		ce.hotReloader.Stop()
	}
}

type ResourceQuota struct {
	mu           sync.Mutex
	maxCPU       float64
	maxMemory    int64
	currentCPU   float64
	currentMem   int64
	availableCPU float64
	availableMem int64
	waiters      []chan struct{}
}

func NewResourceQuota(maxCPUPercent float64, maxMemoryMB int) *ResourceQuota {
	return &ResourceQuota{
		maxCPU:       maxCPUPercent,
		maxMemory:    int64(maxMemoryMB) * 1024 * 1024,
		availableCPU: maxCPUPercent,
		availableMem: int64(maxMemoryMB) * 1024 * 1024,
	}
}

func (rq *ResourceQuota) TryAcquire() bool {
	rq.mu.Lock()
	defer rq.mu.Unlock()

	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	if rq.availableCPU >= 1.0 && int64(m.Alloc) <= rq.maxMemory {
		rq.availableCPU -= 1.0
		rq.availableMem -= int64(runtime.NumCPU()) * 1024 * 1024 // rough estimate
		return true
	}
	return false
}

func (rq *ResourceQuota) Release() {
	rq.mu.Lock()
	defer rq.mu.Unlock()
	rq.availableCPU = minFloat64(rq.availableCPU+1.0, rq.maxCPU)
	rq.availableMem = minInt64(rq.availableMem+1024*1024*1024, rq.maxMemory)
}

type Checkpointer struct {
	mind       *Mind
	interval   int
	cycleCount int
	stopChan   chan struct{}
}

func NewCheckpointer(mind *Mind, interval int) *Checkpointer {
	return &Checkpointer{
		mind:     mind,
		interval: interval,
		stopChan: make(chan struct{}),
	}
}

func (c *Checkpointer) Run() {
	for {
		select {
		case <-c.stopChan:
			return
		case <-time.After(time.Duration(c.interval) * time.Second):
			c.checkpoint()
		}
	}
}

func (c *Checkpointer) checkpoint() {
	c.mind.Transcend()
}

func (c *Checkpointer) Stop() {
	close(c.stopChan)
}

type HotReloader struct {
	interval time.Duration
	stopChan chan struct{}
}

func NewHotReloader(interval time.Duration) *HotReloader {
	return &HotReloader{
		interval: interval,
		stopChan: make(chan struct{}),
	}
}

func (hr *HotReloader) Run() {
	ticker := time.NewTicker(hr.interval)
	defer ticker.Stop()

	for {
		select {
		case <-hr.stopChan:
			return
		case <-ticker.C:
			hr.reload()
		}
	}
}

func (hr *HotReloader) reload() {
	// Hot reload logic would go here
}

func (hr *HotReloader) Stop() {
	close(hr.stopChan)
}

func minFloat64(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
