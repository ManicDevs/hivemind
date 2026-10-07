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
	hm "gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind"
)

const usageText = `HIVEMIND — one universe, three minds, something watching.

Usage:
  hivemind [flags]                 run the engine (standalone by default)
  hivemind engine [flags]          run the engine (same flags, explicit)
  hivemind seed <corpus.json...>   provision the persistent lawbook journal
  hivemind up <program...>         supervise a child main instead of thinking
  hivemind version                 print build/runtime info
  hivemind help                    this screen

Engine flags:
  -mode  standalone|peer   operation profile (default "standalone")
  -node  <name>            peer identity (peer mode defaults hostname-PID)
  -minds A,B,C             mind names to birth (default Alpha,Beta,Gamma)
  -health <addr>           serve /healthz + /metrics (else $HIVEMIND_HEALTH)
  -law <journal>           bind the lawbook journal (default data/law.journal)
  -config <file.json>      overlay engine config from a JSON file

Seed:
  hivemind seed -journal data/law.journal data.json statute.md https://…/statute.txt
      Accepted sources: a JSON array of {"id","source","tag","clause"};
      markdown/plain text (# headings name a source, "Tag:" lines tag the
      next paragraph, blank-line paragraphs become clauses); or an http(s)
      URL of clause text (fetched, 30s budget). Every provisioned clause
      grounds every compiled mind on every node bound to the same journal;
      re-seeding is idempotent (identical clause text derives a stable id).

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
		case "seed":
			os.Exit(runSeed(os.Args[2:]))
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
	fs.StringVar(&cfg.LawJournal, "law", "", "bind the lawbook journal (default data/law.journal)")
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

	// Apex hyperkernel phase: epigenetic memory, hive mesh, autonomous JIT,
	// encrypted QUIC telemetry, and consensus quarantine — pure Go, sandboxed
	// under ./data/apex. Never fatal to the mesh.
	bootstrapApexKernel()

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
	fmt.Printf("📊 [ENGINE] node %s stood for %s: %d minds, %d thoughts, %d replies",
		stats.Node, stats.Uptime.Round(2_000_000_000), stats.Minds,
		stats.Counters.Thoughts, stats.Counters.Replies)
	if stats.LawClauses > 0 {
		fmt.Printf(", %d law clauses compiled", stats.LawClauses)
	}
	if stats.Gate != "" {
		fmt.Printf(", counsel gate: %s (%d/%d grounded)", stats.Gate, stats.GateGrounded, stats.GateTotal)
	}
	fmt.Println()
	return 0
}

// runSeed provisions clauses from one or more corpus JSON files into the
// lawbook journal. Idempotent across runs and nodes: identical clause text
// derives a stable id, so a rebroadcast fleet counts its law once.
func runSeed(args []string) int {
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	journal := fs.String("journal", "", "lawbook journal to bind (default data/law.journal)")
	_ = fs.Parse(args)
	files := fs.Args()
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "⚠️ seed needs at least one corpus file: a JSON array of {\"source\",\"tag\",\"clause\"}")
		return 2
	}
	loaded, skipped, err := hm.LawLoadJournal(*journal)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠️ law journal: %v\n", err)
		return 1
	}
	fmt.Printf("📜 [LAW] journal %s: %d clauses already present (%d corrupt lines skipped)\n",
		hm.LawJournalCurrent(), loaded, skipped)
	for _, p := range files {
		n, perr := seedOne(p)
		if perr != nil {
			fmt.Fprintf(os.Stderr, "⚠️ %s: %v\n", p, perr)
			return 1
		}
		fmt.Printf("📜 [LAW] %s: provisioned %d new clauses -> total %d on journal\n",
			p, n, hm.LawStats())
	}
	for _, e := range hm.LawList() {
		fmt.Printf("  [%-12s|%-16s] %s\n", e.ID, e.Source, clip(e.Clause, 72))
	}
	return 0
}

// seedOne pulls a single seed input (JSON array, markdown/text, or an
// http(s) URL of statute text) into the lawbook. Idempotent across runs and
// nodes: identical clause text derives a stable id, so a rebroadcast fleet
// counts its law once.
func seedOne(p string) (int, error) {
	entries, err := hm.LawParseFile(p, 0)
	if err != nil {
		return 0, err
	}
	if len(entries) == 0 {
		return 0, nil
	}
	return hm.ProvisionLawbook(entries)
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
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
