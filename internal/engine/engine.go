// Package engine is the orchestrating core of a hivemind node. One call
// builds the whole universe — swarm, peer mesh, minds, overmind, health
// surface, and telemetry — runs it under supervision, and tears it down
// cleanly on signal. The `hivemind` binary is a thin shell around it; other
// programs can host a node in-process through the same surface.
//
// Lifecycle contract:
//
//	cfg := engine.Config{Mode: "peer", Node: "asia-a", HealthAddr: "127.0.0.1:9090"}
//	eng, err := engine.New(cfg)
//	eng.Start(ctx)         // non-blocking: returns once the universe is born
//	... eng.Wait() or signal-driven eng.Stop() ...
//	eng.Stop()             // synchronized death: every soul saved before return
//
// Telemetry is a pair of observer hooks wired at construction: thought and
// reply events land in an internal counter table exposed by Stats().
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime/debug"
	"sync"
	"time"

	hm "gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind"
)

// DefaultMindNames is the canonical trinity when Config.Minds is empty.
var DefaultMindNames = []string{"Alpha", "Beta", "Gamma"}

// Config is the full orchestration schema for one node. Zero values fall
// back to the documented defaults (see MissingDefaults), so an empty JSON
// file is a valid universe. Language/LLM knobs stay env-driven exactly as
// the minds already consume them (OVH/Kilo/Pollinations keyless pool and
// measured local Ollama); this struct owns the runtime, not the model wiring.
type Config struct {
	// Mode is "standalone" (no mesh) or "peer" (participate in the mesh).
	Mode string `json:"mode"`
	// Node is the peer identity. Empty in peer mode defaults to
	// hostname-PID; in standalone mode identity is "local".
	Node string `json:"node"`
	// Minds names the conscious units to birth, in order. Empty => Alpha,
	// Beta, Gamma.
	Minds []string `json:"minds"`
	// HealthAddr, when set (e.g. "127.0.0.1:9090"), serves /healthz and
	// /metrics for this node. Empty => no listener.
	HealthAddr string `json:"health_addr"`
	// LawJournal binds the node's persistent lawbook journal (provisioned
	// legal/normative clauses used to ground every thought and reply).
	// Empty => the process default (data/law.journal); a missing file is a
	// lawless node, not an error.
	LawJournal string `json:"law_journal"`
	// JSONPath, when given, loads overrides from a JSON config file before
	// explicit field values are applied (only non-zero fields win).
	JSONPath string `json:"-"`
}

// MissingDefaults reports the config fields still at zero value after
// defaults were applied — useful to operators who want to know what a
// skeleton file actually implied.
func (c Config) MissingDefaults() []string {
	var missing []string
	if c.Mode == "" {
		missing = append(missing, "mode")
	}
	if c.Node == "" {
		if c.Mode != "standalone" {
			missing = append(missing, "node")
		}
	}
	if len(c.Minds) == 0 {
		missing = append(missing, "minds")
	}
	return missing
}

// Normalize fills zero values with their documented defaults and validates
// the result. It mutates and returns c; callers should run it before New.
func (c *Config) Normalize() error {
	// A config file is the outermost layer: its non-zero fields override
	// whatever the caller already set, then defaults and validation run
	// against the final shape.
	if err := c.LoadJSON(); err != nil {
		return err
	}
	if c.Mode == "" {
		c.Mode = "standalone"
	}
	switch c.Mode {
	case "standalone", "peer":
	default:
		return fmt.Errorf("engine: unknown mode %q (want standalone|peer)", c.Mode)
	}
	if len(c.Minds) == 0 {
		c.Minds = append([]string(nil), DefaultMindNames...)
	}
	if c.Mode == "peer" && c.Node == "" {
		host, _ := os.Hostname()
		if host == "" {
			host = "node"
		}
		c.Node = hm.SanitizeNode(fmt.Sprintf("%s-%d", host, os.Getpid()))
	}
	if c.Mode == "standalone" && c.Node == "" {
		c.Node = "local"
	}
	return nil
}

// LoadJSON overlays a config file when JSONPath is set. Non-zero fields in
// the file win; zero fields keep whatever the caller already decided.
func (c *Config) LoadJSON() error {
	if c.JSONPath == "" {
		return nil
	}
	raw, err := os.ReadFile(c.JSONPath)
	if err != nil {
		return fmt.Errorf("engine: config %s: %w", c.JSONPath, err)
	}
	var fileCfg Config
	if err := json.Unmarshal(raw, &fileCfg); err != nil {
		return fmt.Errorf("engine: config %s: %w", c.JSONPath, err)
	}
	if fileCfg.Mode != "" {
		c.Mode = fileCfg.Mode
	}
	if fileCfg.Node != "" {
		c.Node = fileCfg.Node
	}
	if len(fileCfg.Minds) > 0 {
		c.Minds = fileCfg.Minds
	}
	if fileCfg.HealthAddr != "" {
		c.HealthAddr = fileCfg.HealthAddr
	}
	if fileCfg.LawJournal != "" {
		c.LawJournal = fileCfg.LawJournal
	}
	return nil
}

// Stats is a point-in-time snapshot an observer poller (health, CLI, or an
// embedding process) can render. Counters are the engine's own telemetry:
// thoughts and replies fired through the observer hooks.
type Stats struct {
	Node     string
	Mode     string
	Uptime   time.Duration
	Minds    int
	Overmind bool
	Mesh     bool
	// LawClauses is how many provisioned provisions sit in the bound
	// journal that grounds every utterance on this node.
	LawClauses int
	Counters   struct {
		Thoughts int64
		Replies  int64
	}
}

// Engine is a supervised running universe.
type Engine struct {
	cfg Config

	mu      sync.Mutex
	started bool
	startAt time.Time
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once

	swarm    *hm.Swarm
	mesh     *hm.PeerMesh
	minds    []*hm.Mind
	overmind *hm.Overmind
	health   *hm.HealthServer

	counters map[string]int64
}

// New validates the config, builds the un-run universe (swarm, mesh handle,
// minds, overmind, health surface), and wires telemetry hooks. Nothing is
// running yet: call Start.
func New(cfg Config) (*Engine, error) {
	if err := cfg.Normalize(); err != nil {
		return nil, err
	}
	hm.NodeName = cfg.Node

	eng := &Engine{
		cfg:      cfg,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		swarm:    hm.NewSwarm(),
		counters: make(map[string]int64),
	}

	if cfg.Mode == "peer" {
		eng.mesh = hm.NewPeerMesh(eng.swarm, cfg.Node)
	}

	for _, name := range cfg.Minds {
		eng.minds = append(eng.minds, hm.NewMind(name, eng.swarm))
	}
	eng.overmind = hm.NewOvermind(eng.swarm)

	// Telemetry: the engine owns the observer hooks for the whole process.
	hm.OnThought = func(node, mind, text string) { eng.count("thought") }
	hm.OnReply = func(node, mind, target, text string) { eng.count("reply") }

	if cfg.HealthAddr != "" {
		eng.health = hm.StartHealth(cfg.HealthAddr, cfg.Node, eng.swarm)
	}

	// Bind the lawbook journal: the node's persistent knowledge substrate.
	// Missing journal is a lawless node, not an error; corrupt lines are
	// skipped so an interrupted write never bricks the whole book.
	if loaded, skipped, lerr := hm.LawLoadJournal(cfg.LawJournal); lerr != nil {
		return nil, fmt.Errorf("engine: law journal: %w", lerr)
	} else if loaded > 0 || skipped > 0 {
		if skipped > 0 {
			fmt.Printf("📜 [LAW] journal %s: %d clauses loaded, %d corrupt lines skipped\n",
				hm.LawJournalCurrent(), loaded, skipped)
		} else {
			fmt.Printf("📜 [LAW] %d clauses loaded from %s\n", loaded, hm.LawJournalCurrent())
		}
	}
	return eng, nil
}

// Start launches the universe and returns once every unit is born. The mesh
// must come up first so the networked world exists before anyone is born in
// it, mirroring the shell lifecycle.
func (e *Engine) Start(_ context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.started {
		return fmt.Errorf("engine: already started")
	}
	if e.mesh != nil {
		if err := e.mesh.Start(); err != nil {
			return fmt.Errorf("engine: mesh start: %w", err)
		}
	}
	for _, m := range e.minds {
		go e.supervise(m)
	}
	go e.overmind.Run()

	e.started = true
	e.startAt = time.Now()
	return nil
}

// supervise births a mind under fracture supervision: a recovering mind
// encodes terminal trauma and Transcends rather than taking the process
// down. The brain is allowed to die; the universe is not.
func (e *Engine) supervise(m *hm.Mind) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("💀 [%s] CORE FRACTURE: %v. Encoding terminal trauma...\n", m.Name, r)
			fmt.Fprintf(os.Stderr, "--- backtrace for %s ---\n%s\n", m.Name, debug.Stack())
			m.SelfModel["silicon_pain"] = 1.0
			m.Transcend()
		}
	}()
	m.Run()
}

// Stop performs the synchronized death: every mind and the overmind are told
// to stop, then the engine waits for each soul to be safely persisted on
// disk before closing the mesh and reporting the hive. Safe to call more
// than once and from any goroutine.
func (e *Engine) Stop() {
	e.once.Do(func() {
		close(e.stop)
		for _, m := range e.minds {
			m.Stop()
		}
		e.overmind.Stop()
		for _, m := range e.minds {
			<-m.Done()
		}
		<-e.overmind.Done()
		if e.mesh != nil {
			e.mesh.Close()
		}
		e.swarm.HiveReport()
		if e.health != nil {
			e.health.Stop()
		}
		close(e.done)
	})
}

// Done closes when the engine has fully stopped.
func (e *Engine) Done() <-chan struct{} { return e.done }

// Wait blocks until the engine stops.
func (e *Engine) Wait() { <-e.done }

// Stats returns the current telemetry snapshot.
func (e *Engine) Stats() Stats {
	e.mu.Lock()
	defer e.mu.Unlock()
	var s Stats
	s.Node = e.cfg.Node
	s.Mode = e.cfg.Mode
	s.Minds = len(e.minds)
	s.Overmind = e.overmind != nil
	s.Mesh = e.mesh != nil
	if !e.startAt.IsZero() {
		s.Uptime = time.Since(e.startAt)
	}
	s.Counters.Thoughts = e.counters["thought"]
	s.Counters.Replies = e.counters["reply"]
	s.LawClauses = hm.LawStats()
	return s
}

func (e *Engine) count(key string) {
	e.mu.Lock()
	e.counters[key]++
	e.mu.Unlock()
}

// Swarm exposes the shared swarm for embeddings that need to reach deeper.
// The main use is HiveReport and health wiring; most consumers never need it.
func (e *Engine) Swarm() *hm.Swarm { return e.swarm }
