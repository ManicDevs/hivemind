package main

import (
    "flag"
    "fmt"
    "os"
    "os/signal"
    "syscall"
    "time"
)

func main() {
    modeFlag := flag.String("mode", "standalone", "LUDS operation profile: server | client | standalone")
    flag.Parse()

    fmt.Println("=== A universe comes into being. Three minds. And something watching. ===")

    swarm := NewSwarm()

    alpha := NewMind("Alpha", swarm)
    beta := NewMind("Beta", swarm)
    gamma := NewMind("Gamma", swarm)

    overmind := NewOvermind(swarm)

    go alpha.Run()
    go beta.Run()
    go gamma.Run()
    go overmind.Run()

    if *modeFlag != "standalone" {
        broker := NewNetworkBroker(swarm)
        
        if *modeFlag == "server" {
            err := broker.ListenUnixSocket()
            if err != nil {
                fmt.Printf("⚠️ LUDS server initialization failed: %v\n", err)
            }
        } else if *modeFlag == "client" {
            fmt.Println("🔗 [LUDS INTERCONNECT] Attaching process thread to active filesystem socket file...")
            err := broker.ConnectToUnixSocket()
            if err != nil {
                fmt.Printf("⚠️ LUDS client attachment failed: %v\n", err)
            }
        }
        defer broker.Close()
    }

    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
    <-sigChan

    fmt.Println("\n!! The universe received a signal. Apocalypse NOW.")

    alpha.Stop()
    beta.Stop()
    gamma.Stop()
    overmind.Stop()

    time.Sleep(500 * time.Millisecond)
    swarm.HiveReport()
}
