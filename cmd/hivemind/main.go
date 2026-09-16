package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	hm "gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind"
)

func main() {
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
	runMind := func(m *hm.Mind) {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("💀 [%s] CORE FRACTURE: %v. Encoding terminal trauma...\n", m.Name, r)
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
