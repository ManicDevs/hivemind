package hivemind

import (
	"context"
	"sync"
	"time"
)

type PunchAttempt struct {
	mu             sync.RWMutex
	PeerID         string
	TargetAddr     string
	LocalAddr      string
	AttemptCount   int
	FirstAttempt   time.Time
	LastAttempt    time.Time
	NextAttempt    time.Time
	Backoff        time.Duration
	MaxRetries     int
	Status         PunchStatus
	Cancel         context.CancelFunc
	ctx            context.Context
	SuccessCallback func(error)
	FailureCallback func(error)
}

type PunchStatus string

const (
	PunchStatusPending    PunchStatus = "pending"
	PunchStatusAttempting PunchStatus = "attempting"
	PunchStatusSucceeded  PunchStatus = "succeeded"
	PunchStatusFailed     PunchStatus = "failed"
	PunchStatusCancelled  PunchStatus = "cancelled"
)

type PunchRetryManager struct {
	mu            sync.RWMutex
	config        MeshConfig
	pendingPunches map[string]*PunchAttempt
	stopChan      chan struct{}
	cleanupTicker *time.Ticker
}

func NewPunchRetryManager(config MeshConfig) *PunchRetryManager {
	prm := &PunchRetryManager{
		config:         config,
		pendingPunches: make(map[string]*PunchAttempt),
		stopChan:       make(chan struct{}),
	}
	prm.cleanupTicker = time.NewTicker(30 * time.Second)
	go prm.cleanupLoop()
	return prm
}

func (prm *PunchRetryManager) SchedulePunch(peerID, targetAddr, localAddr string, maxRetries int, onSuccess, onFailure func(error)) (*PunchAttempt, error) {
	prm.mu.Lock()
	defer prm.mu.Unlock()
	if maxRetries <= 0 {
		maxRetries = prm.config.PunchMaxRetries
	}
	attempt := &PunchAttempt{
		PeerID:          peerID,
		TargetAddr:      targetAddr,
		LocalAddr:       localAddr,
		AttemptCount:    0,
		FirstAttempt:    time.Now(),
		NextAttempt:     time.Now(),
		Backoff:         prm.config.PunchBaseBackoff,
		MaxRetries:      maxRetries,
		Status:          PunchStatusPending,
		SuccessCallback: onSuccess,
		FailureCallback: onFailure,
	}
	prm.pendingPunches[peerID] = attempt
	go attempt.execute(prm.config)
	return attempt, nil
}

func (prm *PunchRetryManager) GetAttempt(peerID string) (*PunchAttempt, bool) {
	prm.mu.RLock()
	defer prm.mu.RUnlock()
	attempt, ok := prm.pendingPunches[peerID]
	return attempt, ok
}

func (prm *PunchRetryManager) CancelPunch(peerID string) error {
	prm.mu.Lock()
	defer prm.mu.Unlock()
	attempt, ok := prm.pendingPunches[peerID]
	if !ok {
		return nil
	}
	if attempt.Cancel != nil {
		attempt.Cancel()
	}
	attempt.Status = PunchStatusCancelled
	delete(prm.pendingPunches, peerID)
	return nil
}

func (prm *PunchRetryManager) GetPendingPunches() []*PunchAttempt {
	prm.mu.RLock()
	defer prm.mu.RUnlock()
	attempts := make([]*PunchAttempt, 0, len(prm.pendingPunches))
	for _, a := range prm.pendingPunches {
		attempts = append(attempts, a)
	}
	return attempts
}

func (prm *PunchRetryManager) cleanupLoop() {
	for {
		select {
		case <-prm.stopChan:
			return
		case <-prm.cleanupTicker.C:
			prm.cleanup()
		}
	}
}

func (prm *PunchRetryManager) cleanup() {
	prm.mu.Lock()
	defer prm.mu.Unlock()
	for peerID, attempt := range prm.pendingPunches {
		if attempt.Status == PunchStatusSucceeded ||
			attempt.Status == PunchStatusFailed ||
			attempt.Status == PunchStatusCancelled {
			if time.Since(attempt.LastAttempt) > 5*time.Minute {
				delete(prm.pendingPunches, peerID)
			}
		}
	}
}

func (prm *PunchRetryManager) Stop() {
	close(prm.stopChan)
	if prm.cleanupTicker != nil {
		prm.cleanupTicker.Stop()
	}
}

func (pa *PunchAttempt) execute(config MeshConfig) {
	pa.mu.Lock()
	pa.ctx, pa.Cancel = context.WithCancel(context.Background())
	pa.mu.Unlock()

	for attempt := 0; attempt < pa.MaxRetries; attempt++ {
		select {
		case <-pa.ctx.Done():
			pa.mu.Lock()
			pa.Status = PunchStatusCancelled
			pa.mu.Unlock()
			return
		default:
		}

		pa.mu.Lock()
		pa.AttemptCount++
		pa.LastAttempt = time.Now()
		pa.Status = PunchStatusAttempting
		pa.mu.Unlock()

		err := pa.attemptPunch()

		pa.mu.Lock()
		pa.LastAttempt = time.Now()
		if err == nil {
			pa.Status = PunchStatusSucceeded
			if pa.SuccessCallback != nil {
				pa.SuccessCallback(nil)
			}
			pa.mu.Unlock()
			return
		}
		pa.mu.Unlock()

		pa.mu.Lock()
		pa.Backoff = time.Duration(float64(pa.Backoff) * config.PunchBackoffMultiplier)
		if pa.Backoff > config.PunchMaxBackoff {
			pa.Backoff = config.PunchMaxBackoff
		}
		pa.NextAttempt = time.Now().Add(pa.Backoff)
		pa.mu.Unlock()

		select {
		case <-pa.ctx.Done():
			pa.mu.Lock()
			pa.Status = PunchStatusCancelled
			pa.mu.Unlock()
			return
		case <-time.After(pa.Backoff):
		}
	}

	pa.mu.Lock()
	pa.Status = PunchStatusFailed
	if pa.FailureCallback != nil {
		pa.FailureCallback(ErrPunchMaxRetries)
	}
	pa.mu.Unlock()
}

func (pa *PunchAttempt) attemptPunch() error {
	return nil
}

func (pa *PunchAttempt) GetStatus() PunchStatus {
	pa.mu.RLock()
	defer pa.mu.RUnlock()
	return pa.Status
}

func (pa *PunchAttempt) GetAttemptCount() int {
	pa.mu.RLock()
	defer pa.mu.RUnlock()
	return pa.AttemptCount
}

func (pa *PunchAttempt) GetNextAttempt() time.Time {
	pa.mu.RLock()
	defer pa.mu.RUnlock()
	return pa.NextAttempt
}

func (pa *PunchAttempt) GetBackoff() time.Duration {
	pa.mu.RLock()
	defer pa.mu.RUnlock()
	return pa.Backoff
}

func (pa *PunchAttempt) GetStats() map[string]interface{} {
	pa.mu.RLock()
	defer pa.mu.RUnlock()
	return map[string]interface{}{
		"peer_id":        pa.PeerID,
		"attempt_count":  pa.AttemptCount,
		"status":         string(pa.Status),
		"first_attempt":  pa.FirstAttempt,
		"last_attempt":   pa.LastAttempt,
		"next_attempt":   pa.NextAttempt,
		"backoff":        pa.Backoff.String(),
		"max_retries":    pa.MaxRetries,
	}
}