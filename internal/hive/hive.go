package hive

import (
	"context"
	"log/slog"
	"sync"

	"gitlab.torproject.org/cerberus-droid/hivemind/internal/persistence"
)

type SiblingNode struct {
	NodeID          string
	TargetOS        string
	EpigeneticRules map[string]string
	ActiveStatus    string
}

type HiveNetwork struct {
	mu       sync.Mutex
	nodeID   string
	siblings map[string]*SiblingNode
	memory   *persistence.KernelMemory
	log      *slog.Logger
}

func NewHiveNetwork(nodeID string, mem *persistence.KernelMemory) *HiveNetwork {
	return &HiveNetwork{
		nodeID:   nodeID,
		siblings: make(map[string]*SiblingNode),
		memory:   mem,
		log:      slog.Default(),
	}
}

// WithLogger binds a structured logger to the hive network.
func (h *HiveNetwork) WithLogger(lg *slog.Logger) *HiveNetwork {
	if lg != nil {
		h.log = lg
	}
	return h
}

func (h *HiveNetwork) RegisterSibling(siblingID string, targetOS string, rules map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.siblings[siblingID] = &SiblingNode{
		NodeID:          siblingID,
		TargetOS:        targetOS,
		EpigeneticRules: rules,
		ActiveStatus:    "MESH_SYNCHRONIZED",
	}
	h.log.InfoContext(context.Background(), "sibling linked",
		slog.String("sibling_id", siblingID),
		slog.String("target_os", targetOS))
}

func (h *HiveNetwork) AdoptSiblingIntelligence() {
	h.mu.Lock()
	defer h.mu.Unlock()

	adoptedCount := 0
	for _, sibling := range h.siblings {
		for ruleKey, ruleVal := range sibling.EpigeneticRules {
			h.memory.MergeRules(map[string]string{ruleKey: ruleVal})
			adoptedCount++
		}
	}
	_ = h.memory.Save()
	if adoptedCount > 0 {
		h.log.InfoContext(context.Background(), "sibling intelligence adopted",
			slog.Int("rules_adopted", adoptedCount),
			slog.Int("siblings", len(h.siblings)))
	}
}
