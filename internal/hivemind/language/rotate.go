package language

import (
	"context"
	"fmt"
	"sync"
)

// RotatingLLM fans the same Generate call out across a pool of remote
// endpoints, trimming dead or rate-limited members as it goes. This is what
// lets the swarm actually keep talking when an individual public endpoint
// (e.g. OVH at 2 req/min/IP/model) throttles back to HTTP 429.
type RotatingLLM struct {
	mu    sync.Mutex
	pool  []*RemoteLLM
	rr    int
	alias []string
}

// NewRotatingLLM builds a rotating model from the given remote models. All
// members must already have passed a liveness probe; this type only concerns
// itself with runtime failover.
func NewRotatingLLM(models ...*RemoteLLM) *RotatingLLM {
	r := &RotatingLLM{
		pool:  make([]*RemoteLLM, 0, len(models)),
		alias: make([]string, 0, len(models)),
	}
	for _, m := range models {
		if m == nil {
			continue
		}
		r.pool = append(r.pool, m)
		r.alias = append(r.alias, m.Name())
	}
	return r
}

func (r *RotatingLLM) Name() string {
	return fmt.Sprintf("rotate(%s)", r.alias)
}

// Generate tries each live pool member in round-robin order, returning the
// first successful completion. Members that fail during a call are skipped
// for the rest of that call only, so a provider that transiently 429s gets
// re-probed next call.
func (r *RotatingLLM) Generate(ctx context.Context, prompt string) (string, error) {
	r.mu.Lock()
	n := len(r.pool)
	if n == 0 {
		r.mu.Unlock()
		return "", fmt.Errorf("rotating pool empty")
	}
	start := r.rr % n
	var lastErr error
	for step := 0; step < n; step++ {
		idx := (start + step) % n
		out, err := r.pool[idx].Generate(ctx, prompt)
		if err == nil {
			r.rr = (idx + 1) % n
			r.mu.Unlock()
			return out, nil
		}
		lastErr = err
	}
	r.mu.Unlock()
	return "", lastErr
}

func (r *RotatingLLM) Close() error {
	var first error
	for _, m := range r.pool {
		if err := m.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
