package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"gitlab.torproject.org/cerberus-droid/hivemind/internal/engine"
)

const usageText = `HIVEMIND — one universe, three minds, something watching.

Usage:
  hivemind [flags]                 run the engine (standalone by default)
  hivemind engine [flags]          run the engine (same flags, explicit)
  hivemind up <program...>         supervise a child main instead of thinking
  hivemind version                 print build/runtime info
  hivemind help                    this screen

Engine flags:
  -mode  standalone|peer   operation profile (default "standalone")
  -node  <name>            peer identity (peer mode defaults hostname-PID)
  -minds A,B,C             mind names to birth (default Alpha,Beta,Gamma)
  -health <addr>           serve /healthz + /metrics (else $HIVEMIND_HEALTH)
  -config <file.json>      overlay engine config from a JSON file

LLM wiring stays env-driven: the keyless pool (OVH/Kilo/Pollinations) and the
measured local Ollama primary negotiate privileges at mind birth.
`

func main() {
	// Encasement: `hivemind up` supervises child mains instead of thinking.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "up":
			supervise(os.Args[2:])
			return
		case "version":
			fmt.Printf("hivemind %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
			return
		case "help", "-h", "--help":
			fmt.Print(usageText)
			return
		case "engine", "run":
			cfg := parseEngineFlags(flag.ExitOnError, os.Args[2:])
			os.Exit(runEngine(cfg))
			return
		}
	}

	// Anti-debug gate first: a traced process decides its policy before
	// any key material exists in memory to protect.
	if !antiDebug() {
		fmt.Fprintln(os.Stderr, "🛡️  [HARDEN] Refusing to run under a tracer. Set HIVEMIND_HARDEN=warn to proceed watched, or off to disable.")
		os.Exit(3)
	}

	// Backward-compatible default: `hivemind -mode peer -node asia-a`.
	cfg := parseEngineFlags(flag.ExitOnError, os.Args[1:])
	os.Exit(runEngine(cfg))
}

// parseEngineFlags reads the flat CLI surface into an engine.Config. Config
// file, health env, and per-flag order of precedence: flags > file > env.
func parseEngineFlags(eh flag.ErrorHandling, args []string) engine.Config {
	var cfg engine.Config
	var mindsList string
	fs := flag.NewFlagSet("hivemind", eh)
	fs.StringVar(&cfg.Mode, "mode", "standalone", "operation profile: standalone | peer")
	fs.StringVar(&cfg.Node, "node", "", "peer node name (defaults to hostname-PID in peer mode)")
	fs.StringVar(&mindsList, "minds", "", "comma-separated mind names (default Alpha,Beta,Gamma)")
	fs.StringVar(&cfg.HealthAddr, "health", "", "address to serve /healthz + /metrics")
	fs.StringVar(&cfg.JSONPath, "config", "", "overlay engine config from a JSON file")
	_ = fs.Parse(args)

	if cfg.HealthAddr == "" {
		cfg.HealthAddr = os.Getenv("HIVEMIND_HEALTH")
	}
	if mindsList != "" {
		cfg.Minds = splitMinds(mindsList)
	}
	return cfg
}

func splitMinds(list string) []string {
	var out []string
	cur := ""
	for _, r := range list {
		if r == ',' || r == ' ' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// runEngine is the whole universe in one call: build, birth, supervise until
// a signal, then synchronized death. Returns the process exit code.
func runEngine(cfg engine.Config) int {
	fmt.Println("=== A universe comes into being. Three minds. And something watching. ===")

	eng, err := engine.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠️ engine refused to build: %v\n", err)
		return 2
	}
	if err := eng.Start(nil); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️ engine failed to start: %v\n", err)
		return 1
	}
	if cfg.HealthAddr != "" {
		fmt.Printf("📊 [HEALTH] serving /healthz + /metrics on %s\n", cfg.HealthAddr)
	}

	// Live backtraces: SIGQUIT or SIGUSR1 dumps every goroutine's stack to
	// stderr without killing anything. (Registering SIGQUIT overrides the
	// runtime's default crash-dump — that is the point: inspect, don't die.)
	go traceLoop(cfg.Node, cfg.Mode)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	fmt.Println("\n!! The universe received a signal. Apocalypse NOW.")

	eng.Stop()
	eng.Wait()

	stats := eng.Stats()
	fmt.Printf("📊 [ENGINE] node %s stood for %s: %d minds, %d thoughts, %d replies\n",
		stats.Node, stats.Uptime.Round(2_000_000_000), stats.Minds,
		stats.Counters.Thoughts, stats.Counters.Replies)
	return 0
}

// traceLoop serves in-run backtraces: each trace signal writes the full
// goroutine dump (all minds, swarm, mesh, cloud loops) to stderr with a
// timestamp, and the universe keeps thinking. No signals on platforms
// without them (Windows parks here).
func traceLoop(node, mode string) {
	sigs := traceSignals()
	if len(sigs) == 0 {
		return
	}
	traceChan := make(chan os.Signal, 1)
	signal.Notify(traceChan, sigs...)
	for sig := range traceChan {
		dumpGoroutines(node, mode, sig)
	}
}

// dumpGoroutines captures every stack, growing the buffer until the
// runtime fits — a fixed 1MB would silently truncate a big mesh.
func dumpGoroutines(node, mode string, sig os.Signal) {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			fmt.Fprintf(os.Stderr, "\n--- goroutine backtrace (node %s, mode %s, signal %s, %s) ---\n%s\n",
				node, mode, sig, time.Now().Format(time.RFC3339), buf[:n])
			return
		}
		buf = make([]byte, 2*len(buf))
	}
}
