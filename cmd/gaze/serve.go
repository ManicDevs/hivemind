package main

// serve raises a live SVG topology over HTTP. gaze joins the mesh as a
// silent, equal observer — no registry, no hub, no single point — and
// paints what the frames say. Read-only, like the terminal window.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	hm "gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind"
	"gitlab.torproject.org/cerberus-droid/hivemind/internal/infra"
	wm "gitlab.torproject.org/cerberus-droid/hivemind/internal/worldmap"
)

type frameEvent struct {
	At        time.Time `json:"at"`
	Kind      string    `json:"kind"`
	Sender    string    `json:"sender"`
	Payload   string    `json:"payload"`
	StateSize int       `json:"state_size"`
}

var replay = newReplayClock()

// soulz is the on-disk soul archive, refreshed in the background so a busy
// archive (hundreds of MB of thought windows) never stalls a dashboard poll.
// Handlers always get the last-good view instantly; it lags reality by at
// most refreshDelay.
var soulz soulArchive

const soulRefreshDelay = 15 * time.Second

type soulArchive struct {
	mu      sync.Mutex
	souls   []soul
	at      time.Time
	loading bool
}

func (c *soulArchive) get() []soul {
	c.mu.Lock()
	if c.souls != nil && time.Since(c.at) < soulRefreshDelay {
		s := c.souls
		c.mu.Unlock()
		return s
	}
	if !c.loading {
		c.loading = true
		go c.reload()
	}
	s := c.souls
	c.mu.Unlock()
	return s
}

func (c *soulArchive) reload() {
	loaded := loadSouls()
	c.mu.Lock()
	c.souls = loaded
	c.at = time.Now()
	c.loading = false
	c.mu.Unlock()
}

// observer is the live picture one gaze holds of the mesh.
type observer struct {
	mesh *hm.PeerMesh
	node string

	born   time.Time
	evMu   sync.Mutex
	events []frameEvent

	frMu   sync.Mutex
	total  int
	byKind map[string]int

	// Detached world supervisor(s) gaze has raised so the arcs have real
	// peers to draw: PID list + guard. Nothing here is invented — adminStart
	// appends, adminStop kills.
	spMu    sync.Mutex
	spawned []int

	// relay watches the relay's broker mesh so the map can draw it from
	// real telemetry instead of assuming a hub exists.
	relay *wm.RelayPoller
}

var continentCodes = []string{"eu", "as", "af", "na", "sa", "oc", "an"}

func continentOf(name string) string {
	low := strings.ToLower(name)
	for _, c := range continentCodes {
		if strings.HasPrefix(low, c+"-") || strings.HasPrefix(low, c+"_") {
			return c
		}
	}
	for _, c := range continentCodes {
		if strings.Contains(low, "-"+c) {
			return c
		}
	}
	return "?"
}

func isMaster(name string) bool {
	low := strings.ToLower(name)
	return strings.Contains(low, "master") || strings.Contains(low, "super")
}

func shortID(s string) string {
	if len(s) > 12 {
		return s[:12] + "…"
	}
	return s
}

func serve(addr, watch string) error {
	swarm := hm.NewSwarm()
	node := "gaze-" + hm.SanitizeNode(watch) + "-" + fmt.Sprintf("%d", time.Now().UnixNano()%100000)
	mesh := hm.NewPeerMesh(swarm, node)
	if err := mesh.Start(); err != nil {
		return fmt.Errorf("observer join failed: %w", err)
	}

	obs := &observer{mesh: mesh, node: node, born: time.Now(), byKind: map[string]int{}}
	obs.relay = wm.NewRelayPoller(relayURL())
	if obs.relay.Enabled() {
		relayStop := make(chan struct{})
		go obs.relay.Run(relayStop)
	}
	if watch != "" {
		_ = os.Setenv("GAZE_WATCH", watch)
	}

	// Witness everything the swarm accepts: local births and far links
	// both land here, so the picture is the whole collective, not a guess.
	inbox := swarm.Join(mesh.PubKey())
	go obs.observe(inbox)

	mux := http.NewServeMux()
	mux.HandleFunc("/", obs.page)
	mux.HandleFunc("/state", obs.stateJSON)
	mux.HandleFunc("/events", obs.eventsJSON)
	mux.HandleFunc("/command", obs.commandOK)
	mux.HandleFunc("/metrics", obs.metrics)
	mux.HandleFunc("/map.svg", obs.mapSVG)
	mux.HandleFunc("/admin/start", obs.adminStart)
	mux.HandleFunc("/admin/stop", obs.adminStop)
	mux.HandleFunc("/admin/restart", obs.adminRestart)
	mux.HandleFunc("/admin/derive", obs.adminDerive)
	mux.HandleFunc("/admin/keys", obs.adminKeys)
	mux.HandleFunc("/admin", obs.adminPage)

	fmt.Printf("  👁  [GAZE] live observer up at http://%s — watching mesh as %q\n", addr, node)
	return http.ListenAndServe(addr, mux)
}

func (o *observer) observe(inbox <-chan hm.SecureMessage) {
	for msg := range inbox {
		now := time.Now()

		o.frMu.Lock()
		o.total++
		o.byKind[msg.Kind]++
		o.frMu.Unlock()

		ev := frameEvent{At: now, Kind: msg.Kind, Sender: shortID(msg.SenderPubKey), Payload: msg.PayloadStr, StateSize: len(msg.DataState)}
		o.evMu.Lock()
		o.events = append(o.events, ev)
		if len(o.events) > 60 {
			o.events = o.events[len(o.events)-60:]
		}
		o.evMu.Unlock()
	}
}

type nodeView struct {
	Name      string  `json:"name"`
	Continent string  `json:"continent"`
	Role      string  `json:"role"`
	Alive     bool    `json:"alive"`
	Lives     int     `json:"lives"`
	Fitness   float64 `json:"fitness"`
	Pain      float64 `json:"pain"`
	NHearts   int     `json:"n_hearts"`
}

// buildNodes merges the live socket registry with the soul archive so the
// picture shows breathing continents and resting ones side by side.
func buildNodes(souls []soul) ([]nodeView, []string) {
	alive := map[string]bool{}
	var aliveList []string
	for _, s := range glob("/tmp/hivemind-*.sock") {
		name := strings.TrimPrefix(strings.TrimSuffix(filepath.Base(s), ".sock"), "hivemind-")
		alive[name] = true
		aliveList = append(aliveList, name)
	}
	sort.Strings(aliveList)

	merge := func(n string, isAlive bool, seen *map[string]bool, out *[]nodeView) {
		if (*seen)[n] {
			return
		}
		(*seen)[n] = true
		view := nodeView{Name: n, Continent: continentOf(n), Alive: isAlive, Role: "peer"}
		if isMaster(n) {
			view.Role = "master"
		}
		for _, s := range souls {
			if s.node != n || s.name == "OVERMIND" {
				continue
			}
			view.Lives += s.mem.LivesLived
			view.Fitness += s.mem.Fitness
			if s.mem.DeathPain > view.Pain {
				view.Pain = s.mem.DeathPain
			}
			view.NHearts++
		}
		*out = append(*out, view)
	}

	seen := map[string]bool{}
	var nodes []nodeView
	for _, n := range aliveList {
		merge(n, true, &seen, &nodes)
	}
	// Soul-only resting nodes, sorted, join the archive picture.
	var rest []string
	for _, s := range souls {
		if s.name != "OVERMIND" && !seen[s.node] {
			rest = append(rest, s.node)
		}
	}
	sort.Strings(rest)
	for _, n := range rest {
		merge(n, false, &seen, &nodes)
	}
	return nodes, aliveList
}

// snapshot assembles one render of the world without touching any mind.
// The dashboard always paints the full planet; an optional watch filter
// (query ?watch=eu) narrows the node list for continent-focused views.
// The gaze's own -watch flag does NOT hide other continents from /state —
// otherwise Americas/Eurasia/… vanish from the live map on :8090.
func (o *observer) snapshot(watch string) map[string]interface{} {
	souls := soulz.get()
	nodes, aliveList := buildNodes(souls)

	if watch != "" {
		var kept []nodeView
		for _, n := range nodes {
			if n.Continent == "?" || strings.HasPrefix(strings.ToLower(n.Name), strings.ToLower(watch)) || n.Continent == watch {
				kept = append(kept, n)
			}
		}
		nodes = kept
	}

	o.frMu.Lock()
	total := o.total
	byKind := make(map[string]int, len(o.byKind))
	for k, v := range o.byKind {
		byKind[k] = v
	}
	o.frMu.Unlock()

	o.evMu.Lock()
	events := make([]frameEvent, len(o.events))
	copy(events, o.events)
	o.evMu.Unlock()

	now := time.Now()
	tier := hm.CurrentKeyTier()
	return map[string]interface{}{
		"gaze": map[string]interface{}{
			"node":                o.node,
			"uptime":              time.Since(o.born).Round(time.Second).String(),
			"linked_peers":        o.mesh.LinkedPeers(),
			"link_names":          o.mesh.LinkedPeerNames(),
			"frames_total":        total,
			"by_kind":             byKind,
			"hour_key":            hm.CurrentHourKeyHex(),
			"day_root":            hm.CurrentDayRootHex(),
			"leaf_key":            hm.CurrentLeafKeyHex(),
			"host_key":            hm.HostKeyHex(),
			"key_tier":            tier.Tier,
			"leaf_window_minutes": tier.Window,
			"hour_rotates":        time.Until(now.Truncate(time.Hour).Add(time.Hour)).Round(time.Second).String(),
			"leaf_rotates":        time.Until(now.Truncate(time.Minute).Add(time.Minute)).Round(time.Second).String(),
			"relay_on":            relayOn(),
			"relay_url":           relayURL(),
			"brokers":             o.relay.Health().Brokers,
			"mesh":                o.relay.Health().Stats,
			"watch":               watch,
			"now":                 now.Format("15:04:05"),
		},
		"nodes":  nodes,
		"events": events,
		"alive":  aliveList,
	}
}

func (o *observer) stateJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	// Default: full planet (no watch). Only narrow when ?watch= is explicit.
	watch := r.URL.Query().Get("watch")
	switch r.URL.Query().Get("mode") {
	case "replay":
		_ = json.NewEncoder(w).Encode(replay.snapshot(watch))
		return
	}
	_ = json.NewEncoder(w).Encode(o.snapshot(watch))
}

// mapSVG renders the full world as static SVG, independent of the watch
// filter, so a complete planet snapshot can be saved from one gaze.
func (o *observer) mapSVG(w http.ResponseWriter, r *http.Request) {
	var nodes []nodeView
	var links []string
	gazeName := ""
	now := ""
	if r.URL.Query().Get("mode") == "replay" {
		ss := replay.snapshot("")
		g := ss["gaze"].(map[string]interface{})
		gazeName = g["node"].(string)
		now = g["now"].(string)
		if n, ok := ss["nodes"].([]nodeView); ok {
			nodes = n
		}
		if l, ok := ss["gaze"].(map[string]interface{})["link_names"].([]string); ok {
			links = l
		}
	} else {
		nodes, _ = buildNodes(soulz.get())
		links = o.mesh.LinkedPeerNames()
		g := o.snapshot("")["gaze"].(map[string]interface{})
		gazeName = g["node"].(string)
		now = g["now"].(string)
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	_, _ = io.WriteString(w, renderSVG(nodes, links, gazeName, now, relayURL(), o.relay.Health()))
}

// commandOK mines and broadcasts a signed command as this observer node:
// any master or peer is a full entry into the mesh, and the whole swarm
// receives the frame. Proof the network needs no registrar.
func (o *observer) commandOK(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var cmd struct {
		Kind    string    `json:"kind"`
		Payload string    `json:"payload"`
		State   []float64 `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if cmd.Kind == "" {
		cmd.Kind = "thought"
	}
	accepted := o.mesh.CommandMineAndBroadcast(cmd.Kind, cmd.Payload, cmd.State)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"accepted": accepted,
		"kind":     cmd.Kind,
		"payload":  cmd.Payload,
		"note":     "broadcast as " + o.node + " — serverless, every node an equal entry",
	})
}

// adminSTART raises a local peer mesh so the arcs have somebody to draw:
// it spawns N detached observer peers via the world supervisor (the same
// machinery `make demo` proves), then tells the mesh they exist. One click,
// real arcs — no second terminal.
func (o *observer) adminStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	n, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("nodes")))
	if n < 2 {
		n = 2
	}
	forSec := 60
	if fs := strings.TrimSpace(r.URL.Query().Get("for")); fs != "" {
		if v, err := strconv.Atoi(fs); err == nil && v > 0 {
			forSec = v
		}
	}
	started := o.spawnWorld(n, forSec)
	accepted := o.mesh.CommandMineAndBroadcast("world_start", "{}", nil)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"accepted": accepted,
		"started":  started,
		"command":  "world_start",
		"peers":    n,
		"for":      forSec,
		"note":     "spawned N local peers via bin/hivemind up — arcs now have real links to draw",
	})
}

// spawnWorld execs bin/hivemind up detached so the peers survive gaze's
// own lifecycle. Returns the supervisor's PID, or 0 if it couldn't rise.
func (o *observer) spawnWorld(n, forSec int) int {
	bin, err := filepath.Abs("bin/hivemind")
	if err != nil {
		return 0
	}
	args := []string{"up", "-nodes", strconv.Itoa(n), "-for", strconv.Itoa(forSec) + "s"}
	cmd := exec.Command(bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Env = append(os.Environ(),
		"HIVEMIND_TICK_MS=250", "HIVEMIND_RELAY_URL="+relayURL(),
	)
	if err := cmd.Start(); err != nil {
		return 0
	}
	// Detach: its own session, its own fate. We remember the PID so Stop
	// can lay it down gracefully instead of leaving orphans behind.
	o.spMu.Lock()
	o.spawned = append(o.spawned, cmd.Process.Pid)
	o.spMu.Unlock()
	return cmd.Process.Pid
}

// adminSTOP sends a signed "world_stop" command into the mesh:
func (o *observer) adminStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	accepted := o.mesh.CommandMineAndBroadcast("world_stop", "{}", nil)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"accepted": accepted,
		"command":  "world_stop",
		"note":     "broadcast to the mesh — world orchestrator shuts down",
	})
}

// adminRESTART sends world_stop then world_start:
func (o *observer) adminRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	o.mesh.CommandMineAndBroadcast("world_stop", "{}", nil)
	accepted := o.mesh.CommandMineAndBroadcast("world_start", "{}", nil)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"accepted": accepted,
		"command":  "world_restart",
		"note":     "stop then start broadcast to the mesh",
	})
}

// adminDERIVE re-derives hour_key / day_root via make derive:
func (o *observer) adminDerive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	accepted := o.mesh.CommandMineAndBroadcast("admin_derive", "{}", nil)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"accepted": accepted,
		"command":  "admin_derive",
		"note":     "triggers key derivation pipeline on the mesh",
	})
}

// adminKEYS returns the current relay key state:
func (o *observer) adminKeys(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"relay_on":  relayOn(),
		"relay_url": relayURL(),
		"hour_key":  hm.CurrentHourKeyHex(),
		"day_root":  hm.CurrentDayRootHex(),
		"node":      o.node,
		"uptime":    time.Since(o.born).Round(time.Second).String(),
	})
}

// adminPage serves a full admin HTML page with management controls:
func (o *observer) adminPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	html := strings.Replace(adminPageTmpl, "/*COAST_JSON*/[]", wm.LandRingsJSON(), 1)
	_, _ = io.WriteString(w, html)
}

// eventsJSON streams the last 60 raw frames with real kinds, senders, payloads.
func (o *observer) eventsJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	o.evMu.Lock()
	evs := make([]frameEvent, len(o.events))
	copy(evs, o.events)
	o.evMu.Unlock()
	_ = json.NewEncoder(w).Encode(evs)
}

func relayURL() string {
	if u := strings.TrimSpace(os.Getenv("HIVEMIND_RELAY_URL")); u != "" {
		return strings.TrimSuffix(u, "/")
	}
	if urls := infra.TCPBrokerURLs(); len(urls) > 0 {
		return urls[0]
	}
	return ""
}

func relayOn() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("HIVEMIND_RELAY"))) {
	case "0", "off", "false", "no":
		return false
	}
	return relayURL() != ""
}
