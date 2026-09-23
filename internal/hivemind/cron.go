package hivemind

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

type CronConfig struct {
	Timezone          *time.Location
	MaxConcurrentJobs int
	JobTimeout        time.Duration
	EnablePersistence bool
	PersistencePath   string
	EnableMetrics     bool
}

func DefaultCronConfig() CronConfig {
	return CronConfig{
		Timezone:          time.UTC,
		MaxConcurrentJobs: 10,
		JobTimeout:        5 * time.Minute,
		EnablePersistence: false,
	}
}

type Job struct {
	ID           string
	Name         string
	Schedule     string
	Handler      func(ctx context.Context) error
	Timeout      time.Duration
	MaxRetries   int
	RetryDelay   time.Duration
	Enabled      bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
	LastRun      *time.Time
	NextRun      *time.Time
	RunCount     int64
	FailureCount int64
}

type Cron struct {
	mu       sync.RWMutex
	config   CronConfig
	jobs     map[string]*Job
	entries  map[string]*Entry
	stopChan chan struct{}
	wg       sync.WaitGroup
	running  bool
}

type Entry struct {
	Job     *Job
	NextRun time.Time
	Timer   *time.Timer
	Cancel  context.CancelFunc
}

func NewCron(config CronConfig) *Cron {
	return &Cron{
		config:   config,
		jobs:     make(map[string]*Job),
		entries:  make(map[string]*Entry),
		stopChan: make(chan struct{}),
	}
}

func (c *Cron) AddJob(job *Job) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.jobs[job.ID] != nil {
		return fmt.Errorf("job with ID %s already exists", job.ID)
	}

	if job.ID == "" {
		job.ID = generateJobID()
	}
	job.CreatedAt = time.Now()
	job.UpdatedAt = time.Now()
	job.Enabled = true

	c.jobs[job.ID] = job
	if job.Enabled && c.running {
		c.scheduleJob(job)
	}

	return nil
}

func (c *Cron) RemoveJob(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if entry, ok := c.entries[id]; ok {
		entry.Cancel()
		delete(c.entries, id)
	}
	delete(c.jobs, id)
	return nil
}

func (c *Cron) EnableJob(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	job, ok := c.jobs[id]
	if !ok {
		return fmt.Errorf("job not found: %s", id)
	}

	if !job.Enabled {
		job.Enabled = true
		job.UpdatedAt = time.Now()
		if c.running {
			c.scheduleJob(job)
		}
	}
	return nil
}

func (c *Cron) DisableJob(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	job, ok := c.jobs[id]
	if !ok {
		return fmt.Errorf("job not found: %s", id)
	}

	if job.Enabled {
		job.Enabled = false
		job.UpdatedAt = time.Now()
		if entry, ok := c.entries[id]; ok {
			entry.Cancel()
			delete(c.entries, id)
		}
	}
	return nil
}

func (c *Cron) Start() error {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return nil
	}
	c.running = true
	c.mu.Unlock()

	for _, job := range c.jobs {
		if job.Enabled {
			c.scheduleJob(job)
		}
	}
	return nil
}

func (c *Cron) Stop() {
	c.mu.Lock()
	if !c.running {
		c.mu.Unlock()
		return
	}
	c.running = false
	close(c.stopChan)

	for _, entry := range c.entries {
		entry.Cancel()
	}
	c.mu.Unlock()

	c.wg.Wait()
}

func (c *Cron) scheduleJob(job *Job) {
	_, cancel := context.WithCancel(context.Background())

	nextRun := c.nextRun(job.Schedule)
	job.NextRun = &nextRun

	entry := &Entry{
		Job:     job,
		NextRun: nextRun,
		Cancel:  cancel,
	}
	c.entries[job.ID] = entry

	c.wg.Add(1)
	go c.runJob(job, cancel)
}

func (c *Cron) runJob(job *Job, cancel context.CancelFunc) {
	defer c.wg.Done()

	ticker := time.NewTicker(time.Until(*job.NextRun))
	defer ticker.Stop()

	for {
		select {
		case <-c.stopChan:
			cancel()
			return
		case <-ticker.C:
			if !job.Enabled {
				return
			}

			job.LastRun = ptr(time.Now())
			job.RunCount++

			ctx, cancelCtx := context.WithTimeout(context.Background(), job.Timeout)
			err := job.Handler(ctx)
			cancelCtx()

			if err != nil {
				job.FailureCount++
				if job.MaxRetries > 0 {
					// Retry logic would go here
				}
			}

			nextRun := c.nextRun(job.Schedule)
			job.NextRun = &nextRun
			job.UpdatedAt = time.Now()

			ticker.Reset(time.Until(nextRun))
		}
	}
}

func (c *Cron) nextRun(schedule string) time.Time {
	return time.Now().Add(5 * time.Minute)
}

func (c *Cron) GetJob(id string) (*Job, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	job, ok := c.jobs[id]
	return job, ok
}

func (c *Cron) ListJobs() []*Job {
	c.mu.RLock()
	defer c.mu.RUnlock()

	jobs := make([]*Job, 0, len(c.jobs))
	for _, job := range c.jobs {
		jobs = append(jobs, job)
	}
	return jobs
}

func (c *Cron) GetStats() map[string]interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()

	enabled := 0
	disabled := 0
	for _, job := range c.jobs {
		if job.Enabled {
			enabled++
		} else {
			disabled++
		}
	}

	return map[string]interface{}{
		"total_jobs":    len(c.jobs),
		"enabled_jobs":  enabled,
		"disabled_jobs": disabled,
		"running":       c.running,
	}
}

func generateJobID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func ptr[T any](v T) *T {
	return &v
}
