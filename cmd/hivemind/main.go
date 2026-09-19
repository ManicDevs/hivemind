package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"

	hm "gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind"
)

func main() {
	// Encasement: `hivemind up` supervises child mains instead of thinking.
	if len(os.Args) > 1 && os.Args[1] == "up" {
		supervise(os.Args[2:])
		return
	}

	// Anti-debug gate first: a traced process decides its policy before
	// any key material exists in memory to protect.
	if !antiDebug() {
		fmt.Fprintln(os.Stderr, "🛡️  [HARDEN] Refusing to run under a tracer. Set HIVEMIND_HARDEN=warn to proceed watched, or off to disable.")
		os.Exit(3)
	}

	modeFlag := flag.String("mode", "standalone", "operation profile: standalone | peer")
	nodeFlag := flag.String("node", "", "peer node name (defaults to hostname-PID in peer mode)")
	flag.Parse()

	switch *modeFlag {
	case "standalone", "peer":
	default:
		fmt.Fprintf(os.Stderr, "unknown mode %q (want standalone|peer)\n", *modeFlag)
		os.Exit(2)
	}

	// Node identity: stable when given, unique when defaulted.
	node := "local"
	if *modeFlag == "peer" {
		if *nodeFlag == "" {
			host, _ := os.Hostname()
			if host == "" {
				host = "node"
			}
			node = hm.SanitizeNode(fmt.Sprintf("%s-%d", host, os.Getpid()))
		} else {
			node = hm.SanitizeNode(*nodeFlag)
		}
	}
	hm.NodeName = node

	fmt.Println("=== A universe comes into being. Three minds. And something watching. ===")

	swarm := hm.NewSwarm()

	// The universe is networked before anyone is born in it.
	var mesh *hm.PeerMesh
	if *modeFlag == "peer" {
		mesh = hm.NewPeerMesh(swarm, node)
		if err := mesh.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "⚠️ peer mesh failed to start: %v\n", err)
			os.Exit(1)
		}
	}

	alpha := hm.NewMind("Alpha", swarm)
	beta := hm.NewMind("Beta", swarm)
	gamma := hm.NewMind("Gamma", swarm)
	overmind := hm.NewOvermind(swarm)

	// A fracturing mind dies traumatically — and the trauma is inherited.
	// The stack goes to stderr so a fracture leaves a backtrace, not a rumor.
	runMind := func(m *hm.Mind) {
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

	go runMind(alpha)
	go runMind(beta)
	go runMind(gamma)
	go overmind.Run()

	// Observability is opt-in and local by default: set HIVEMIND_HEALTH
	// (e.g. 127.0.0.1:9090) to expose /healthz + Prometheus /metrics.
	// Empty (default) means no listener, no surface.
	health := hm.StartHealth(os.Getenv("HIVEMIND_HEALTH"), node, swarm)
	if health != nil {
		fmt.Printf("📊 [HEALTH] serving /healthz + /metrics\n")
		defer health.Stop()
	}

	// Live backtraces: SIGQUIT or SIGUSR1 dumps every goroutine's stack
	// to stderr without killing anything. (Registering SIGQUIT overrides
	// the runtime's default crash-dump — that is the point: inspect, don't die.)
	go traceLoop(node, *modeFlag)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	fmt.Println("\n!! The universe received a signal. Apocalypse NOW.")

	alpha.Stop()
	beta.Stop()
	gamma.Stop()
	overmind.Stop()

	// Death is synchronized, not hoped for: every soul is on disk
	// before anyone reports on them.
	<-alpha.Done()
	<-beta.Done()
	<-gamma.Done()
	<-overmind.Done()

	if mesh != nil {
		mesh.Close()
	}

	swarm.HiveReport()
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
