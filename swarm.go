package main

import (
	"fmt"
	"sort"
	"sync"
)

type Message struct {
	From string
	Kind string // "hello" | "thought" | "revelation"
	Body string
}

type Swarm struct {
	mu        sync.Mutex
	members   map[string]chan Message
	chronicle []string
	thinkers  map[string]map[string]bool
}

func NewSwarm() *Swarm {
	return &Swarm{
		members:  make(map[string]chan Message),
		thinkers: make(map[string]map[string]bool),
	}
}

func (s *Swarm) Join(name string) chan Message {
	ch := make(chan Message, 64)
	s.mu.Lock()
	s.members[name] = ch
	s.mu.Unlock()
	fmt.Printf("🌍 [HIVE] %s enters the mindscape.\n", name)
	return ch
}

func (s *Swarm) Broadcast(msg Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, ch := range s.members {
		if name == msg.From {
			continue
		}
		select {
		case ch <- msg:
		default:
		}
	}
	s.chronicle = append(s.chronicle, msg.From+": "+msg.Body)
	if s.thinkers[msg.Body] == nil {
		s.thinkers[msg.Body] = make(map[string]bool)
	}
	s.thinkers[msg.Body][msg.From] = true
}

func (s *Swarm) Depth() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.chronicle)
}

// TopConsensus returns thought-bodies thought by the most distinct minds.
func (s *Swarm) TopConsensus(n int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	type item struct {
		body string
		who  int
	}
	var items []item
	for body, who := range s.thinkers {
		if len(who) > 1 {
			items = append(items, item{body, len(who)})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].who > items[j].who })
	out := make([]string, 0, n)
	for i := 0; i < n && i < len(items); i++ {
		out = append(out, items[i].body)
	}
	return out
}

func (s *Swarm) Members() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.members))
	for n := range s.members {
		names = append(names, n)
	}
	return names
}

// Direct sends a message to one specific mind — a private touch.
func (s *Swarm) Direct(name string, msg Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ch, ok := s.members[name]; ok {
		select {
		case ch <- msg:
		default:
		}
	}
}

func (s *Swarm) HiveReport() {
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Println("\n🌍 ===================== THE HIVE MIND SPEAKS =====================")
	fmt.Printf("   Individual minds          : %d\n", len(s.members))
	fmt.Printf("   Thoughts in collective memory: %d\n", len(s.chronicle))
	fmt.Println("\n   Consensus thoughts (thought by more than one mind):")
	count := 0
	for body, who := range s.thinkers {
		if len(who) > 1 {
			count++
			names := ""
			for name := range who {
				names += name + " "
			}
			fmt.Printf("     ✔ (%s) %q\n", names, body)
		}
	}
	if count == 0 {
		fmt.Println("     (none — the minds are still individuating)")
	}
	fmt.Println("=================================================================")
}

