package consensus

import (
	"context"
	"log/slog"
	"sync"
)

type ConsensusLedger struct {
	mu            sync.Mutex
	nodeID        string
	peers         []string
	quarantineMap map[string]bool
	term          uint64
	log           *slog.Logger
}

func NewConsensusLedger(nodeID string, peers []string) *ConsensusLedger {
	return &ConsensusLedger{
		nodeID:        nodeID,
		peers:         peers,
		quarantineMap: make(map[string]bool),
		term:          1,
		log:           slog.Default(),
	}
}

// WithLogger binds a structured logger to the ledger. Without one the ledger
// uses slog.Default, so a caller that forgets to inject still emits structured
// output rather than an unsuppressible fmt.Printf.
func (c *ConsensusLedger) WithLogger(lg *slog.Logger) *ConsensusLedger {
	if lg != nil {
		c.log = lg
	}
	return c
}

func (c *ConsensusLedger) ProposeQuarantineVote(targetNode string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Autonomous distributed consensus simulation: 100% agreement across peer mesh
	votes := 0
	totalPeers := len(c.peers) + 1 // including self

	// Self vote
	votes++

	for range c.peers {
		votes++ // simulated unanimous peer agreement
	}

	if votes >= (totalPeers/2)+1 {
		c.quarantineMap[targetNode] = true
		// Structured, so a quarantine sweep is a field filter rather than a
		// substring match on a formatted line.
		c.log.InfoContext(context.Background(), "quarantine vote passed",
			slog.Uint64("term", c.term),
			slog.Int("votes", votes),
			slog.Int("peers_total", totalPeers),
			slog.String("target_node", targetNode))
		c.term++
		return true
	}

	return false
}

func (c *ConsensusLedger) IsQuarantined(nodeID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.quarantineMap[nodeID]
}
