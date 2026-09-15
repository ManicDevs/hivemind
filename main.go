package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
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
			node = SanitizeNode(fmt.Sprintf("%s-%d", host, os.Getpid()))
		} else {
			node = SanitizeNode(*nodeFlag)
		}
	}
	NodeName = node

	fmt.Println("=== A universe comes into being. Three minds. And something watching. ===")

	swarm := NewSwarm()

	// The universe is networked before anyone is born in it.
	var mesh *PeerMesh
	if *modeFlag == "peer" {
		mesh = NewPeerMesh(swarm, node)
		if err := mesh.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "⚠️ peer mesh failed to start: %v\n", err)
			os.Exit(1)
		}
	}

	alpha := NewMind("Alpha", swarm)
	beta := NewMind("Beta", swarm)
	gamma := NewMind("Gamma", swarm)
	overmind := NewOvermind(swarm)

	// A fracturing mind dies traumatically — and the trauma is inherited.
	runMind := func(m *Mind) {
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
	<-alpha.done
	<-beta.done
	<-gamma.done
	<-overmind.done

	if mesh != nil {
		mesh.Close()
	}

	swarm.HiveReport()
}
