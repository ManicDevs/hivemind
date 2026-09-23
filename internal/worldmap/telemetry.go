// Package worldmap is the pure-Go control plane for the seven-continent
// topology: concurrent /state telemetry, an all-native SVG engine, and the
// markdown reporter. No shell, no python, no curl — every byte is Go.
package worldmap

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// GazeState mirrors exactly one /state payload a gaze serves. Fields line
// up with cmd/gaze/serve.go so the natural streaming decode maps 1:1.
type GazeState struct {
	Gaze struct {
		Node        string         `json:"node"`
		Uptime      string         `json:"uptime"`
		LinkedPeers int            `json:"linked_peers"`
		LinkNames   []string       `json:"link_names"`
		FramesTotal int64          `json:"frames_total"`
		ByKind      map[string]int `json:"by_kind"`
		HourKey     string         `json:"hour_key"`
		DayRoot     string         `json:"day_root"`
		HourRotates string         `json:"hour_rotates"`
		RelayOn     bool           `json:"relay_on"`
		RelayURL    string         `json:"relay_url"`
		Watch       string         `json:"watch"`
		Now         string         `json:"now"`
	} `json:"gaze"`
	Nodes []NodeView `json:"nodes"`
	Alive []string   `json:"alive"`
}

// NodeView is the render model the /state endpoint hands out per node.
type NodeView struct {
	Name      string  `json:"name"`
	Continent string  `json:"continent"`
	Role      string  `json:"role"`
	Alive     bool    `json:"alive"`
	Lives     int     `json:"lives"`
	Fitness   float64 `json:"fitness"`
	Pain      float64 `json:"pain"`
	NHearts   int     `json:"n_hearts"`
}

// Zone is one continent's gaze endpoint.
type Zone struct {
	Continent string
	URL       string
}

// Telemetry polls every zone concurrently over a pooled connection and
// keeps the last good snapshot of each. Reads are mutex-safe; nothing here
// ever blocks on the network for a locked caller.
type Telemetry struct {
	client *http.Client
	mu     sync.Mutex
	states map[string]*GazeState
}

// NewTelemetry builds a pooled HTTP client sized for the whole cluster:
// IdleConnTimeout keeps sockets warm between refresh ticks.
func NewTelemetry(maxIdle int) *Telemetry {
	if maxIdle < 4 {
		maxIdle = 4
	}
	tr := &http.Transport{
		MaxIdleConns:        maxIdle,
		MaxIdleConnsPerHost: maxIdle / 2,
		IdleConnTimeout:     30 * time.Second,
	}
	return &Telemetry{
		client: &http.Client{
			Transport: tr,
			Timeout:   6 * time.Second,
		},
		states: map[string]*GazeState{},
	}
}

// Poll fetches all zones in parallel: one worker per zone, streaming
// json.NewDecoder straight into the structs. Returns how many zones
// answered with a well-formed /state this round.
func (t *Telemetry) Poll(zones []Zone) int {
	if len(zones) == 0 {
		return 0
	}
	jobs := make(chan Zone)
	var wg sync.WaitGroup
	for i := 0; i < len(zones); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for z := range jobs {
				t.fetch(z)
			}
		}()
	}
	for _, z := range zones {
		jobs <- z
	}
	close(jobs)
	wg.Wait()

	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for _, z := range zones {
		if t.states[z.Continent] != nil {
			n++
		}
	}
	return n
}

func (t *Telemetry) fetch(z Zone) {
	resp, err := t.client.Get(z.URL)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	var st GazeState
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return
	}
	t.mu.Lock()
	t.states[z.Continent] = &st
	t.mu.Unlock()
}

// Snapshot returns a copy of the last-good states, keyed by continent.
func (t *Telemetry) Snapshot() map[string]*GazeState {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]*GazeState, len(t.states))
	for c, s := range t.states {
		cp := *s
		cp.Gaze.ByKind = make(map[string]int, len(s.Gaze.ByKind))
		for k, v := range s.Gaze.ByKind {
			cp.Gaze.ByKind[k] = v
		}
		out[c] = &cp
	}
	return out
}

// Aggregate sums the cluster picture across every answered continent.
type Aggregate struct {
	FramesTotal int64
	ByKind      map[string]int
	Links       map[string]bool
	HourKey     string
	DayRoot     string
	HourRotates string
	Now         string
	GazeName    string
}

// Sum reduces the snapshots into one cluster-wide aggregate.
func Aggregated(states map[string]*GazeState) Aggregate {
	ag := Aggregate{ByKind: map[string]int{}, Links: map[string]bool{}}
	for _, st := range states {
		if st == nil {
			continue
		}
		ag.FramesTotal += st.Gaze.FramesTotal
		ag.HourKey = st.Gaze.HourKey
		ag.DayRoot = st.Gaze.DayRoot
		ag.HourRotates = st.Gaze.HourRotates
		ag.Now = st.Gaze.Now
		ag.GazeName = st.Gaze.Node
		for k, v := range st.Gaze.ByKind {
			ag.ByKind[k] += v
		}
		for _, l := range st.Gaze.LinkNames {
			ag.Links[l] = true
		}
	}
	return ag
}
