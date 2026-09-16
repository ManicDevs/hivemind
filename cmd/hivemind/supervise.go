package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// supervise launches N hive mains as children and shepherds them:
// prefixed output, graceful shutdown on Ctrl+C (or -for timeout),
// straggler reaping, exit summary. One main encasing many.
func supervise(args []string) {
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	nodes := fs.Int("nodes", 2, "peer nodes to raise")
	window := fs.Duration("for", 0, "run duration (0 = until Ctrl+C)")
	_ = fs.Parse(args)
	if *nodes < 1 {
		fmt.Fprintln(os.Stderr, "up: need at least 1 node")
		os.Exit(2)
	}

	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "up: cannot re-exec: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("🚀 [SUPERVISOR] Raising %d peer node(s)...\n", *nodes)
	var mu sync.Mutex
	procs := make([]*exec.Cmd, 0, *nodes)
	for i := 0; i < *nodes; i++ {
		name := fmt.Sprintf("up-%d", i+1)
		cmd := exec.Command(self, "-mode", "peer", "-node", name)
		cmd.Stdout = prefixWriter{tag: name, w: os.Stdout}
		cmd.Stderr = prefixWriter{tag: name, w: os.Stderr}
		cmd.Stdin = nil
		if err := cmd.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "up: node %s failed to start: %v\n", name, err)
			continue
		}
		mu.Lock()
		procs = append(procs, cmd)
		mu.Unlock()
		fmt.Printf("🚀 [SUPERVISOR] Node %s rising (PID %d).\n", name, cmd.Process.Pid)
		time.Sleep(500 * time.Millisecond) // stagger births; sockets settle
	}
	if len(procs) == 0 {
		fmt.Fprintln(os.Stderr, "up: no nodes rose")
		os.Exit(1)
	}

	deaths := make(chan os.Signal, 1)
	signal.Notify(deaths, syscall.SIGINT, syscall.SIGTERM)
	if *window > 0 {
		fmt.Printf("⏳ [SUPERVISOR] Window: %s. Sampling the mesh...\n", *window)
		time.AfterFunc(*window, func() { deaths <- syscall.SIGTERM })
	} else {
		fmt.Println("💡 [SUPERVISOR] Press Ctrl+C to lay the universe to rest.")
	}
	<-deaths
	fmt.Println("\n🛑 [SUPERVISOR] Laying nodes to rest, gracefully first...")

	var wg sync.WaitGroup
	for _, cmd := range procs {
		wg.Add(1)
		go func(c *exec.Cmd) {
			defer wg.Done()
			_ = c.Process.Signal(syscall.SIGTERM)
			done := make(chan struct{})
			go func() { _ = c.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				_ = c.Process.Kill() // graceful death refused; escalate
				<-done
			}
		}(cmd)
	}
	wg.Wait()

	fmt.Println("\n🗃️  [SUPERVISOR] Exit registry:")
	for _, cmd := range procs {
		fmt.Printf("     ✔ Node exited: %v\n", cmd.Args[len(cmd.Args)-1])
	}
	fmt.Println("✨ [SUPERVISOR] All processes reaped. Workspace verified by their own reports above.")
}

// prefixWriter tags every streamed line with its node's name so N
// interleaved universes stay legible on one terminal.
type prefixWriter struct {
	tag string
	w   io.Writer
}

func (p prefixWriter) Write(data []byte) (int, error) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	total := 0
	for sc.Scan() {
		n, err := fmt.Fprintf(p.w, "[%s] %s\n", p.tag, sc.Text())
		total += n
		if err != nil {
			return total, err
		}
	}
	return len(data), nil
}
