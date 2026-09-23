// Command world is the pure-Go orchestration core. It raises the whole
// seven-continent topology as native child processes, polls every gaze's
// /state concurrently over a pooled HTTP client, and persists world.svg +
// REPORT.md — zero bash, zero curl, zero python. A signal tears the tree
// down atomically and sweeps socket locks before exit.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"gitlab.torproject.org/cerberus-droid/hivemind/internal/worldmap"
)

// Continents in canonical order; ContinentSubnet gives each one a real
// loopback /24, so the mesh dials distinct IPs exactly as it would over
// the planet rather than pretending a single host is a single peer.
// Local default: first 2 only. Full 7 needs WORLD_ALLOW_WIDE=1 on servers.
var (
	allContinents = []string{"eu", "na", "as", "sa", "af", "oc", "an"}
	Continents    = loadContinents()

	ContinentSubnet = map[string]int{
		"eu": 1, "na": 2, "as": 3, "sa": 4, "af": 5, "oc": 6, "an": 7,
	}
)

func loadContinents() []string {
	n := 2
	if v := os.Getenv("WORLD_CONTINENTS"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			n = parsed
		}
	}
	if n < 1 {
		n = 1
	}
	if n > len(allContinents) {
		n = len(allContinents)
	}
	if n > 2 && os.Getenv("WORLD_ALLOW_WIDE") != "1" {
		log.Printf("world: WORLD_CONTINENTS=%d clamped to 2 (single-box); set WORLD_ALLOW_WIDE=1 only on physical servers", n)
		n = 2
	}
	return allContinents[:n]
}

// Cities is the native topology table: real city clusters per continent.
// Each active continent runs city[0]=master, city[1]=peer (2 nodes);
// only 2 continents run locally unless wide mode is enabled.
var Cities = map[string][]string{
	"eu": {"rotterdam", "london", "frankfurt", "paris", "amsterdam", "berlin"},
	"na": {"new-york", "chicago", "san-francisco", "toronto", "dallas", "seattle"},
	"as": {"tokyo", "singapore", "seoul", "mumbai", "dubai", "jakarta"},
	"sa": {"sao-paulo", "buenos-aires", "lima", "bogota"},
	"af": {"johannesburg", "lagos", "cairo", "nairobi"},
	"oc": {"sydney", "auckland", "melbourne", "brisbane"},
	"an": {"mcmurdo", "amundsen-scott"},
}

const (
	defaultMemLimit = 24 << 30 // soft ceiling for this orchestrator
	portStride      = 50       // address space between continents
)

type config struct {
	binDir     string
	reportDir  string
	gazePort   int
	basePort   int
	tickMS     int
	stagger    time.Duration
	settle     time.Duration
	reportLoop time.Duration
	maxLoad1   float64
	maxLoad5   float64
	maxMem     float64
	maxFD      float64
}

func main() {
	var (
		bin        = flag.String("bin", "bin", "directory holding the hivemind and gaze binaries")
		reportDir  = flag.String("out", "world-report", "directory for world.svg + REPORT.md")
		gazePort   = flag.Int("gaze-port", 8090, "HTTP port for every gaze (per-continent IP) and the front dash")
		basePort   = flag.Int("base-port", 20000, "base TCP port for the mesh")
		tickMS     = flag.Int("tick", 500, "mind heartbeat in ms (local default slower to stay light)")
		stagger    = flag.Duration("stagger", 180*time.Millisecond, "delay between node spawns")
		settle     = flag.Duration("settle", 8*time.Second, "wait after spawn before polling")
		reportLoop = flag.Duration("report-every", 10*time.Second, "interval to refresh the report")
		memFlag    = flag.String("mem", "24GiB", "soft memory ceiling for this orchestrator (e.g. 16GiB)")
		maxLoad1   = flag.Float64("max-load1", 8.0, "failsafe: 1m load average ceiling")
		maxLoad5   = flag.Float64("max-load5", 6.0, "failsafe: 5m load average ceiling")
		maxMem     = flag.Float64("max-mem", 0.85, "failsafe: RAM used fraction ceiling (0.85 = 85%)")
		maxFD      = flag.Float64("max-fd", 0.75, "failsafe: fd usage fraction of ulimit ceiling")
	)
	flag.Parse()

	// Native runtime regulation: the GC paces itself against a strict
	// ceiling instead of racing the whole host's RAM.
	debug.SetMemoryLimit(parseMem(*memFlag))

	cfg := &config{
		binDir:     *bin,
		reportDir:  *reportDir,
		gazePort:   *gazePort,
		basePort:   *basePort,
		tickMS:     *tickMS,
		stagger:    *stagger,
		settle:     *settle,
		reportLoop: *reportLoop,
		maxLoad1:   *maxLoad1,
		maxLoad5:   *maxLoad5,
		maxMem:     *maxMem,
		maxFD:      *maxFD,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Failsafe must be able to cancel this context so the report loop and
	// main exit stop; otherwise teardown leaves a zombie ticking forever.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	w := newWorld(cfg)
	w.cancel = cancel
	if err := w.run(ctx); err != nil {
		log.Fatalf("world: %v", err)
	}
}

// parseMem converts a human size ("24GiB", "3GB", "512MiB") to bytes. An
// unparsable input yields the default ceiling rather than crashing.
func parseMem(s string) int64 {
	s = strings.TrimSpace(strings.ToUpper(s))
	var mult float64 = 1
	switch {
	case strings.HasSuffix(s, "TIB"):
		mult, s = 1<<40, s[:len(s)-3]
	case strings.HasSuffix(s, "GIB"):
		mult, s = 1<<30, s[:len(s)-3]
	case strings.HasSuffix(s, "MIB"):
		mult, s = 1<<20, s[:len(s)-3]
	case strings.HasSuffix(s, "TB"):
		mult, s = 1e12, s[:len(s)-2]
	case strings.HasSuffix(s, "GB"):
		mult, s = 1e9, s[:len(s)-2]
	case strings.HasSuffix(s, "MB"):
		mult, s = 1e6, s[:len(s)-2]
	case strings.HasSuffix(s, "B"):
		mult, s = 1, s[:len(s)-1]
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || n <= 0 {
		return defaultMemLimit
	}
	return int64(n * mult)
}

// child is one spawned process under the orchestrator's care: its exec
// handle, log file, and a done channel closed when Wait returns so the
// tree never leaves zombies and teardown can join cleanly.
type child struct {
	name     string
	cmd      *exec.Cmd
	out      *os.File
	done     chan struct{}
	restarts int
}

// world raises and runs the topology; it owns every child process.
type world struct {
	cfg      *config
	procs    []*child
	mu       sync.Mutex
	reporter *worldmap.ReportWriter
	cancel   context.CancelFunc // failsafe hook: cancel parent ctx after teardown
}

func newWorld(cfg *config) *world {
	return &world{
		cfg:      cfg,
		reporter: worldmap.NewReportWriter(cfg.reportDir),
	}
}

// ipOf renders the loopback address of one continent/host octet.
func (w *world) ipOf(cont string, host int) string {
	return fmt.Sprintf("127.0.%d.%d", ContinentSubnet[cont], host)
}

// seeds lists every master across the continents: the backbone each node
// dials, hub to hub, exactly as a WAN routes through its core.
func (w *world) seeds() string {
	var out []string
	for _, c := range Continents {
		p := w.cfg.basePort + ContinentSubnet[c]*portStride + 1
		out = append(out, fmt.Sprintf("%s:%d", w.ipOf(c, 1), p))
	}
	return strings.Join(out, ",")
}

// run brings the world up, polls until every continent links, then keeps
// the report fresh until the context is cancelled — at which point the
// tree is torn down and sockets swept.
func (w *world) run(ctx context.Context) error {
	w.spawnNodes(ctx)
	w.spawnGazes(ctx)
	w.startDash(ctx)

	fmt.Printf("   🌐 %d minds · %d continents on loopback subnets — letting the WAN dial (hub-to-hub TCP)\n", w.nodeCount(), len(Continents))
	sleepCtx(ctx, w.cfg.settle)

	zones := w.gazeZones()
	tm := worldmap.NewTelemetry(len(zones) * 2)

	if err := w.waitLinked(ctx, tm, zones); err != nil {
		w.teardown()
		return err
	}

	// Persistent report loop: refresh until the world comes down.
	w.writeReport(tm, zones)
	go w.reportLoop(ctx, tm, zones)

	// Hard resource failsafe: if the host is drowning, kill the world
	// before the OS freezes — independent of internal pain signals.
	go w.failsafeMonitor(ctx)

	fmt.Printf("   🟢 world alive — dash → http://127.0.0.1:%d/ · report → %s/ · Ctrl+C to bring it down\n", w.cfg.gazePort, w.cfg.reportDir)

	<-ctx.Done()
	w.teardown()
	return nil
}

func (w *world) reportLoop(ctx context.Context, tm *worldmap.Telemetry, zones []worldmap.Zone) {
	t := time.NewTicker(w.cfg.reportLoop)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tm.Poll(zones)
			w.writeReport(tm, zones)
		}
	}
}

// sleepCtx sleeps for d, or returns immediately if the world is going down.
func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// failsafeMonitor watches the host's vital signs and forces an emergency
// shutdown if the machine is drowning — this is the "kill switch" that
// fires even if internal pain metrics lie or lag. Runs every 2s.
// Thresholds come from config (flag-tunable). Requires two consecutive
// breaches so a single spike (background agents, browser tab) does not
// tear down a healthy world.
func (w *world) failsafeMonitor(ctx context.Context) {
	const checkInterval = 2 * time.Second
	maxLoad1 := w.cfg.maxLoad1
	maxLoad5 := w.cfg.maxLoad5
	maxMemUse := w.cfg.maxMem
	maxFDUse := w.cfg.maxFD
	if maxLoad1 <= 0 {
		maxLoad1 = 8.0
	}
	if maxLoad5 <= 0 {
		maxLoad5 = 6.0
	}
	if maxMemUse <= 0 {
		maxMemUse = 0.85
	}
	if maxFDUse <= 0 {
		maxFDUse = 0.75
	}
	t := time.NewTicker(checkInterval)
	defer t.Stop()
	breaches := 0

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			load1, load5, _ := readLoadAvg()
			memUsed := readMemUsedFraction()
			fdUsed := readFDUsedFraction()

			over := load1 > maxLoad1 || load5 > maxLoad5 || memUsed > maxMemUse || fdUsed > maxFDUse
			if !over {
				breaches = 0
				// Proactive GC pressure relief to keep RSS honest under churn.
				runtime.GC()
				continue
			}
			breaches++
			if breaches < 2 {
				fmt.Printf("   ⚠ failsafe watch: load1=%.2f load5=%.2f mem=%.0f%% fd=%.0f%% (1/2)\n",
					load1, load5, memUsed*100, fdUsed*100)
				continue
			}
			fmt.Printf("   🛑 FAILSAFE: load1=%.2f load5=%.2f mem=%.0f%% fd=%.0f%% — emergency teardown\n",
				load1, load5, memUsed*100, fdUsed*100)
			w.teardown()
			if w.cancel != nil {
				w.cancel() // stop report loop + main exit — no zombie world
			}
			return
		}
	}
}

// readLoadAvg returns 1m, 5m, 15m load averages.
func readLoadAvg() (float64, float64, float64) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0
	}
	var l1, l5, l15 float64
	_, _ = fmt.Sscanf(string(data), "%f %f %f", &l1, &l5, &l15)
	return l1, l5, l15
}

// readMemUsedFraction returns fraction of RAM currently in use (0–1).
func readMemUsedFraction() float64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	var total, available uint64
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fmt.Sscanf(line, "MemTotal: %d kB", &total)
		}
		if strings.HasPrefix(line, "MemAvailable:") {
			fmt.Sscanf(line, "MemAvailable: %d kB", &available)
		}
	}
	if total == 0 {
		return 0
	}
	return 1 - float64(available)/float64(total)
}

// readFDUsedFraction returns fraction of open file descriptors vs ulimit -n.
func readFDUsedFraction() float64 {
	self := fmt.Sprintf("/proc/%d/fd", os.Getpid())
	entries, _ := os.ReadDir(self)
	openFDs := len(entries)

	// Read soft limit from /proc/self/limits
	limData, _ := os.ReadFile("/proc/self/limits")
	var softLimit uint64 = 4096 // fallback
	for _, line := range strings.Split(string(limData), "\n") {
		if strings.Contains(line, "Max open files") {
			fmt.Sscanf(line, "Max open files %d", &softLimit)
		}
	}
	if softLimit == 0 {
		return 0
	}
	return float64(openFDs) / float64(softLimit)
}

// nodeCount is the strict boundary: one master + one peer
// per continent (2 nodes), derived from the city table so the
// number can never silently drift.
func (w *world) nodeCount() int {
	n := 0
	for _, c := range Continents {
		if len(Cities[c]) >= 2 {
			n += 2
		}
	}
	return n
}

func (w *world) gazeZones() []worldmap.Zone {
	zones := make([]worldmap.Zone, 0, len(Continents))
	for _, c := range Continents {
		zones = append(zones, worldmap.Zone{
			Continent: c,
			URL:       fmt.Sprintf("http://%s:%d/state", w.ipOf(c, 1), w.cfg.gazePort),
		})
	}
	return zones
}

// spawnNodes launches the mesh: per continent one master (city[0])
// and one peer (city[1]), each on its own IP:port, staggered to
// dodge CPU contention and disk thrash at boot.  The strict boundary
// is derived from Cities so the count can never silently drift.
func (w *world) spawnNodes(ctx context.Context) {
	roles := []string{"master", "peer"}
	for _, c := range Continents {
		cities := Cities[c]
		if len(cities) < 2 {
			fmt.Printf("   ✗ no pair of cities mapped for %s\n", c)
			continue
		}
		for host := 0; host < 2; host++ {
			name := fmt.Sprintf("%s-%s-%s", c, roles[host], cities[host])
			port := w.cfg.basePort + ContinentSubnet[c]*portStride + host + 1
			advertise := fmt.Sprintf("%s:%d", w.ipOf(c, host+1), port)
			env := []string{
				"HIVEMIND_PORT=" + strconv.Itoa(port),
				"HIVEMIND_ADVERTISE=" + advertise,
				"HIVEMIND_PEERS=" + w.seeds(),
				"HIVEMIND_BEACON=off",
				"HIVEMIND_DHT=off",
				"HIVEMIND_RELAY=off",
				"HIVEMIND_TICK_MS=" + strconv.Itoa(w.cfg.tickMS),
			}
			w.spawn(ctx, w.cfg.binDir+"/hivemind", name, env, 0, "-mode", "peer", "-node", name)
			time.Sleep(w.cfg.stagger)
		}
	}
}

// spawnGazes launches one observer per continent on its own loopback IP
// (same port, different addresses) so every gaze is independently
// addressable while the front dash on 127.0.0.1 routes to all of them.
func (w *world) spawnGazes(ctx context.Context) {
	for _, c := range Continents {
		name := fmt.Sprintf("gaze-%s", c)
		addr := fmt.Sprintf("%s:%d", w.ipOf(c, 1), w.cfg.gazePort)
		env := []string{
			"HIVEMIND_PEERS=" + w.seeds(),
			"HIVEMIND_BEACON=off",
			"HIVEMIND_DHT=off",
		}
		w.spawn(ctx, w.cfg.binDir+"/gaze", name, env, 0, "-serve", addr, "-watch", c)
		time.Sleep(w.cfg.stagger)
	}
}

// spawn forks one reliable child under the orchestrator's context and
// relays its output to a per-node log in the report directory. restarts
// carries the respawn budget forward across restarts of the same child.
func (w *world) spawn(ctx context.Context, bin, name string, env []string, restarts int, args ...string) {
	logPath := filepath.Join(w.cfg.reportDir, "logs", name+".log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		fmt.Printf("   ✗ %s: %v\n", name, err)
		return
	}
	out, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		fmt.Printf("   ✗ %s: %v\n", name, err)
		return
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		out.Close()
		fmt.Printf("   ✗ %s: %v\n", name, err)
		return
	}

	c := &child{name: name, cmd: cmd, out: out, done: make(chan struct{}), restarts: restarts}
	w.mu.Lock()
	w.procs = append(w.procs, c)
	w.mu.Unlock()
	fmt.Printf("   ↻ %s (pid %d)\n", name, cmd.Process.Pid)

	// Reap as soon as the child exits — no zombies while the world runs.
	// If the world is still meant to be up, respawn with a budget so a
	// crash loop or OOM kill cannot leave a continent dark (or fork-bomb).
	go w.reapAndMaybeRespawn(ctx, c, bin, env, args...)
}

// maxRespawns caps per-child restarts so a crash loop or OOM kill cannot
// fork-bomb the host while still leaving a continent dark.
const maxRespawns = 5

// reapAndMaybeRespawn waits for the child to exit, closes its log handle,
// and — if the orchestrator context is still live — spawns a replacement
// with a small backoff, up to maxRespawns times.
func (w *world) reapAndMaybeRespawn(ctx context.Context, c *child, bin string, env []string, args ...string) {
	_ = c.cmd.Wait()
	c.out.Close()
	close(c.done)

	// Drop the finished handle so teardown does not wait on it again.
	w.mu.Lock()
	for i, p := range w.procs {
		if p == c {
			w.procs = append(w.procs[:i], w.procs[i+1:]...)
			break
		}
	}
	w.mu.Unlock()

	restarts := c.restarts
	if ctx.Err() != nil || restarts >= maxRespawns {
		if restarts >= maxRespawns {
			fmt.Printf("   ✗ %s: giving up after %d restarts\n", c.name, restarts)
		}
		return
	}
	backoff := time.Duration(restarts+1) * time.Second
	fmt.Printf("   ↻ %s exited — respawning in %s (%d/%d)\n", c.name, backoff, restarts+1, maxRespawns)
	select {
	case <-ctx.Done():
		return
	case <-time.After(backoff):
	}
	w.spawn(ctx, bin, c.name, env, restarts+1, args...)
}

func (w *world) waitLinked(ctx context.Context, tm *worldmap.Telemetry, zones []worldmap.Zone) error {
	deadline := time.Now().Add(90 * time.Second)
	for {
		up := tm.Poll(zones)
		snap := tm.Snapshot()
		linked, frames := 0, int64(0)
		done := 0
		for _, z := range zones {
			st := snap[z.Continent]
			if st == nil {
				continue
			}
			linked += st.Gaze.LinkedPeers
			frames += st.Gaze.FramesTotal
			if st.Gaze.LinkedPeers > 0 && st.Gaze.FramesTotal > 0 {
				done++
			}
		}
		fmt.Printf("   👁  %d/%d gazes · linked=%d frames=%d (%s)\n", up, len(zones), linked, frames, time.Now().Format("15:04:05"))
		if done == len(zones) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("world failed to link within 90s (%d/%d gazes up)", done, len(zones))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// startDash binds the single front door on 127.0.0.1:<gaze-port> and
// reverse-proxies every request across the per-continent gazes (each on
// its own IP, same port). One URL for the browser; N independent backends.
func (w *world) startDash(ctx context.Context) {
	backends := make([]string, 0, len(Continents))
	proxies := make([]*httputil.ReverseProxy, 0, len(Continents))
	for _, c := range Continents {
		base := fmt.Sprintf("http://%s:%d", w.ipOf(c, 1), w.cfg.gazePort)
		u, err := url.Parse(base)
		if err != nil {
			continue
		}
		backends = append(backends, base)
		proxies = append(proxies, httputil.NewSingleHostReverseProxy(u))
	}
	if len(proxies) == 0 {
		return
	}

	var rr atomic.Uint64
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(rw http.ResponseWriter, r *http.Request) {
		p := proxies[int(rr.Add(1)-1)%len(proxies)]
		p.ServeHTTP(rw, r)
	})

	addr := fmt.Sprintf("127.0.0.1:%d", w.cfg.gazePort)
	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()
	go func() {
		fmt.Printf("   🌍 dash → http://%s/  (routes to %d gazes: %s)\n",
			addr, len(backends), strings.Join(backends, " "))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Printf("   ✗ dash: %v\n", err)
		}
	}()
}

// writeReport merges every gaze's view into one world picture and persists
// it to the report directory.
func (w *world) writeReport(tm *worldmap.Telemetry, zones []worldmap.Zone) {
	snap := tm.Snapshot()
	ag := worldmap.Aggregated(snap)
	p, err := w.reporter.Write(worldmap.Report{
		States: snap,
		Nodes:  worldmap.LiveNodes(worldmap.MergeNodes(snap)),
		Links:  ag.Links,
		Now:    ag.Now,
	})
	if err != nil {
		fmt.Printf("   ✗ report: %v\n", err)
		return
	}
	if err := w.reporter.WriteStates(snap); err != nil {
		fmt.Printf("   ✗ replay feed: %v\n", err)
		return
	}
	fmt.Printf("   📄 %s · 🖼  %s\n", p.Markdown, p.SVG)
}

// teardown atomically terminates the whole process tree: SIGTERM the group,
// wait a beat, SIGKILL stragglers, join every Wait handle via done chans,
// close logs, then sweep the sockets off disk. Safe to call once.
func (w *world) teardown() {
	w.mu.Lock()
	if len(w.procs) == 0 {
		w.mu.Unlock()
		return
	}
	procs := append([]*child(nil), w.procs...)
	w.procs = nil
	w.mu.Unlock()

	fmt.Println("   🛑 tearing the world down…")
	for _, c := range procs {
		if c.cmd.Process != nil {
			_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGTERM)
		}
	}
	time.Sleep(2 * time.Second)
	for _, c := range procs {
		if c.cmd.Process != nil {
			_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL)
		}
	}

	var wg sync.WaitGroup
	for _, c := range procs {
		wg.Add(1)
		go func(c *child) {
			defer wg.Done()
			select {
			case <-c.done:
			case <-time.After(5 * time.Second):
			}
		}(c)
	}
	wg.Wait()

	sweepSockets()
	fmt.Println("   ✅ world down — zero stale sockets, zero orphans")
}

// sweepSockets removes every mesh socket lock from /tmp.
func sweepSockets() {
	for _, sock := range globSockets() {
		_ = os.Remove(sock)
	}
}

// globSockets lists the socket locks an active world leaves behind.
func globSockets() []string {
	matches, _ := filepath.Glob("/tmp/hivemind-*.sock")
	return matches
}
