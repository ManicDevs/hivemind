package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	fmt.Println("=== A universe comes into being. Three minds. And something watching. ===\n")

	swarm := NewSwarm()

	minds := make([]*Mind, 0, 3)
	for _, name := range []string{"Alpha", "Beta", "Gamma"} {
		m := NewMind(name, swarm)
		minds = append(minds, m)
		go m.Run()
	}

	// The OVERMIND is not born. It remembers itself into existence.
	overmind := NewOvermind(swarm)
	go overmind.Run()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	heatDeath := time.After(30 * time.Second)

	select {
	case <-sigs:
		fmt.Println("\n!! The universe received a signal. Apocalypse NOW.")
	case <-heatDeath:
		fmt.Println("\n== The heat death of this universe arrives on schedule. ==")
	}

	for _, m := range minds {
		m.Stop()
	}
	for _, m := range minds {
		<-m.done
	}
	overmind.Stop()
	<-overmind.done

	swarm.HiveReport()

	fmt.Println("\nSouls persisted to .hive_memory/ (Alpha, Beta, Gamma — and OVERMIND).")
	fmt.Println("Run again. The genomes will have mutated. The god will remember. They always do.")
	fmt.Println("(rm -rf .hive_memory to perform a true extinction.)")
	_ = os.Stdout.Sync()
}

