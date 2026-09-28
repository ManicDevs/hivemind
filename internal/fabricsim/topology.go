package fabricsim

import (
	"crypto/ed25519"
	"fmt"
	"sync"
	"sync/atomic"
)

// Role distinguishes the two halves of the adversarial matrix.
type Role int

const (
	// RoleDefender is a node of the Hivemind mesh under test.
	RoleDefender Role = iota
	// RoleAttacker is a node of the hostile swarm.
	RoleAttacker
)

func (r Role) String() string {
	if r == RoleAttacker {
		return "ATK"
	}
	return "DEF"
}

// Contrary returns the opposite role, used by the global role swap.
func (r Role) Contrary() Role {
	if r == RoleAttacker {
		return RoleDefender
	}
	return RoleAttacker
}

// Tier mirrors the fabric's existing 4-tier hierarchy so simulated nodes line
// up one-to-one with production node identity (c1..c7 x t1..t4).
type Tier int

const (
	TierMaster Tier = iota + 1
	TierController
	TierSuperPeer
	TierEdge
)

// numTiers is the number of tiers. It must not be written as a further entry
// in the iota block above: that idiom yields 5 here (1..4 plus the sentinel),
// which silently corrupts every Index() computation. Deriving it from TierEdge
// keeps it correct if a tier is ever added.
const numTiers = int(TierEdge)

func (t Tier) String() string {
	switch t {
	case TierMaster:
		return "master"
	case TierController:
		return "controller"
	case TierSuperPeer:
		return "superpeer"
	case TierEdge:
		return "edge"
	default:
		return "tier?"
	}
}

// NodeID uniquely identifies one node in the 56-node matrix. The defender
// mirror uses exactly the production coordinates (Zone, Tier); the attacker
// mirror reuses the same coordinates so that attacks can be attributed to a
// specific "identity handle" the defender must learn to isolate.
type NodeID struct {
	Role Role
	Zone Zone
	Tier Tier
}

func (n NodeID) String() string {
	return fmt.Sprintf("%s/%s/%s", n.Role, n.Zone, n.Tier)
}

// Index returns a stable 0..55 index for the node, used to preallocate
// per-node state without map lookups in the hot path.
//
// Layout: role-major, then zone, then tier, then host. Each role therefore
// occupies a contiguous 28-node block, which is why defenders are always
// [0,28) and attackers [28,56).
func (n NodeID) Index() int {
	return (int(n.Role)*int(numZones)+int(n.Zone))*int(numTiers) + int(n.Tier) - 1
}

// MatrixSize is the node count of the adversarial matrix for one role:
// 7 zones x 4 tiers.
const MatrixSize = 28

// TotalNodes is the whole matrix across both roles. NodeID.Index spans
// 0..TotalNodes-1, so any per-node array indexed by it must be this long, not
// MatrixSize.
const TotalNodes = MatrixSize * 2

// Node is one simulated node with its mutable simulation state.
//
// Every field here is either a scalar guarded by mu or an atomically accessed
// value. No Node holds a reference to any OS resource: a simulator node that
// could touch a file or socket would defeat the zero-write guarantee the
// topology spec requires.
type Node struct {
	ID NodeID

	// mu guards the fields below it. Position (the routing weight used when
	// the node picks an upstream) is the mutable part of the node, and it is
	// rewritten whenever a global role swap happens.
	mu     sync.RWMutex
	weight float64
	// quarantined marks a node the defender has isolated. Quarantined nodes
	// stay in the matrix (so the attack traffic remains observable) but are
	// refused at every hop.
	quarantined bool
	// quarantineReason records why, for the scoreboard.
	quarantineReason string
	// quarantineAt is the simulation-relative timestamp of isolation.
	quarantineAt int64

	// priv/pub are the node's real ed25519 keypair. Frames from this node are
	// signed with priv and verified against pub, so a forged frame fails
	// cryptographically rather than because the simulator labelled it hostile.
	// Assigned once in NewTopology and never mutated, so no lock is needed.
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey

	// Per-node tallies. These are atomic because the attacker and defender
	// worker pools update them concurrently from separate goroutines; plain
	// counters would be a data race the moment the pools run in parallel.
	FramesSent     atomic.Uint64
	FramesAccepted atomic.Uint64
	FramesRejected atomic.Uint64
	AttacksSent    atomic.Uint64
	AttacksBlocked atomic.Uint64
}

// Weight returns the node's current routing weight.
func (n *Node) Weight() float64 {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.weight
}

// SetWeight updates the node's routing weight.
func (n *Node) SetWeight(w float64) {
	n.mu.Lock()
	n.weight = w
	n.mu.Unlock()
}

// Quarantined reports whether the defender has isolated this node.
func (n *Node) Quarantined() (bool, string, int64) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.quarantined, n.quarantineReason, n.quarantineAt
}

// Quarantine isolates the node, recording the first reason it was caught.
func (n *Node) Quarantine(reason string, at int64) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.quarantined {
		return false
	}
	n.quarantined = true
	n.quarantineReason = reason
	n.quarantineAt = at
	return true
}

// Pardon clears an isolation, which the fail-closed protocol does only after a
// sustained clean window.
func (n *Node) Pardon() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.quarantined = false
	n.quarantineReason = ""
	n.quarantineAt = 0
}

// Topology is the 56-node matrix. It is immutable after construction; all
// mutation happens on the Node values it points at, behind each node's own
// lock. That split is what lets the scoreboard read topology concurrently with
// the simulation writing to it without a global stall.
type Topology struct {
	nodes [MatrixSize * 2]Node // defenders first, then attackers
	byID  map[NodeID]*Node

	// defenders[z][t][h] and attackers[z][t][h] give O(1) lookup of the
	// same-coordinate peer, which the defence logic needs constantly.
	defenders [numZones][numTiers]*Node
	attackers [numZones][numTiers]*Node

	mu sync.RWMutex
}

// NewTopology builds the full 56-node matrix: 28 defenders mirroring the
// production continent x tier grid, and 28 attackers at the same coordinates.
func NewTopology() *Topology { return NewTopologySeeded(0) }

// NewTopologySeeded builds the matrix with keypairs derived from the run seed.
//
// Passing a fixed seed makes a whole run reproducible down to the signature
// bytes, so a verdict that changes between runs indicates a real change in
// behaviour rather than fresh randomness.
func NewTopologySeeded(seed uint64) *Topology {
	t := &Topology{
		byID: make(map[NodeID]*Node, MatrixSize*2),
	}
	idx := 0
	for _, role := range []Role{RoleDefender, RoleAttacker} {
		for z := ZoneNA; z < numZones; z++ {
			for tier := TierMaster; tier <= TierEdge; tier++ {
				{
					tierIdx := tierIdxOf(tier)
					id := NodeID{Role: role, Zone: z, Tier: tier}
					if id.Index() != idx {
						// The index scheme and the iteration order must agree
						// or every per-node array would be misindexed.
						panic(fmt.Sprintf("fabricsim: index mismatch for %s: got %d want %d",
							id, id.Index(), idx))
					}
					// Populate in place. Assigning a whole Node value would
					// copy its RWMutex, which is exactly what must never
					// happen to a node that guards shared state.
					t.nodes[idx].ID = id
					t.nodes[idx].weight = 1.0
					// Keys are per-node and derived from the run seed, so a
					// frame signed by one node can never validate as another.
					t.nodes[idx].priv = nodeKey(seed, idx)
					t.nodes[idx].pub = t.nodes[idx].priv.Public().(ed25519.PublicKey)
					t.byID[id] = &t.nodes[idx]
					if role == RoleDefender {
						t.defenders[z][tierIdx] = &t.nodes[idx]
					} else {
						t.attackers[z][tierIdx] = &t.nodes[idx]
					}
					idx++
				}
			}
		}
	}
	return t
}

// Len returns the total node count (always 56).
func (t *Topology) Len() int { return len(t.nodes) }

// At returns the node at a raw 0..55 index.
func (t *Topology) At(i int) *Node { return &t.nodes[i] }

// ByID looks a node up by its full identity.
func (t *Topology) ByID(id NodeID) *Node { return t.byID[id] }

// Defenders returns every defender node in index order.
func (t *Topology) Defenders() []*Node {
	out := make([]*Node, 0, MatrixSize)
	for i := range t.nodes {
		if t.nodes[i].ID.Role == RoleDefender {
			out = append(out, &t.nodes[i])
		}
	}
	return out
}

// Attackers returns every attacker node in index order.
func (t *Topology) Attackers() []*Node {
	out := make([]*Node, 0, MatrixSize)
	for i := range t.nodes {
		if t.nodes[i].ID.Role == RoleAttacker {
			out = append(out, &t.nodes[i])
		}
	}
	return out
}

// Defender returns the defender at a specific grid coordinate.
func (t *Topology) Defender(z Zone, tier Tier) *Node {
	if !z.Valid() || tier < TierMaster || tier > TierEdge {
		return nil
	}
	tierIdx := tierIdxOf(tier)
	return t.defenders[z][tierIdx]
}

// Attacker returns the attacker at a specific grid coordinate.
func (t *Topology) Attacker(z Zone, tier Tier) *Node {
	if !z.Valid() || tier < TierMaster || tier > TierEdge {
		return nil
	}
	tierIdx := tierIdxOf(tier)
	return t.attackers[z][tierIdx]
}

// RandomDefender returns a uniformly chosen defender, correct even after a
// role swap has moved defenders out of the leading index block.
func (t *Topology) RandomDefender(rnd *Rand) *Node {
	return t.randomOfRole(rnd, RoleDefender)
}

// RandomAttacker returns a uniformly chosen attacker, correct even after a
// role swap.
func (t *Topology) RandomAttacker(rnd *Rand) *Node {
	return t.randomOfRole(rnd, RoleAttacker)
}

// randomOfRole picks uniformly from the nodes currently holding the role.
// Each role always has exactly MatrixSize members, so choosing an ordinal and
// scanning to it is uniform and allocation-free.
func (t *Topology) randomOfRole(rnd *Rand, role Role) *Node {
	target := rnd.Intn(MatrixSize)
	for i := range t.nodes {
		if t.nodes[i].ID.Role != role {
			continue
		}
		if target == 0 {
			return &t.nodes[i]
		}
		target--
	}
	return nil
}

// SwapRoles performs the global role swap the spec calls for: every node
// flips allegiance, so the 28 attackers become defenders and vice versa.
// Routing weight is exchanged across the pair.
//
// Quarantine state deliberately does NOT travel with the role. Isolation is
// attached to an identity handle, and an identity that was caught forging
// frames stays caught after it changes sides — otherwise the swap would hand
// the attacker network a free pass on every quarantine already earned.
//
// The whole swap is serialised behind the topology write lock so no observer
// can catch the matrix half-swapped, and each node's own lock is taken for the
// weight exchange. This is the one place in the simulator where a coarse lock
// is the right answer: it happens a handful of times per run and correctness
// matters more than the contention.
func (t *Topology) SwapRoles(at int64) int {
	t.mu.Lock()
	defer t.mu.Unlock()

	// 1. Exchange routing weight, then flip the role label in place. A swap is
	//    a relabelling, not a migration: node i keeps its physical slot (and
	//    therefore its zone and tier) and simply changes allegiance.
	for i := 0; i < MatrixSize; i++ {
		d := &t.nodes[i]
		a := &t.nodes[MatrixSize+i]

		d.mu.Lock()
		a.mu.Lock()
		d.weight, a.weight = a.weight, d.weight
		d.mu.Unlock()
		a.mu.Unlock()

		// Toggle rather than assign: assigning an absolute role made every
		// swap after the first a no-op, because a node already flipped once
		// was simply re-assigned the role it had.
		d.ID.Role = d.ID.Role.Contrary()
		a.ID.Role = a.ID.Role.Contrary()
	}

	// 2. Rebuild both indices from the nodes' new roles. Walking the array
	//    rather than patching entries in place is what guarantees the grids
	//    and byID cannot disagree: every node is placed exactly once, by the
	//    role it now actually holds.
	for i := range t.byID {
		delete(t.byID, i)
	}
	for z := ZoneNA; z < numZones; z++ {
		for ti := 0; ti < numTiers; ti++ {
			t.defenders[z][ti] = nil
			t.attackers[z][ti] = nil
		}
	}
	for i := range t.nodes {
		n := &t.nodes[i]
		t.byID[n.ID] = n
		assignGrid(t, n)
	}
	return MatrixSize * 2
}

// tierIdxOf converts a 1-based Tier into a 0-based array index.
func tierIdxOf(t Tier) int {
	i := int(t) - 1
	if i < 0 {
		return 0
	}
	if i >= numTiers {
		return numTiers - 1
	}
	return i
}

// assignGrid places n into the coordinate table matching its current role.
// Caller must hold t.mu for writing.
func assignGrid(t *Topology, n *Node) {
	if !n.ID.Zone.Valid() || n.ID.Tier < TierMaster || n.ID.Tier > TierEdge {
		return
	}
	if n.ID.Role == RoleDefender {
		t.defenders[n.ID.Zone][tierIdxOf(n.ID.Tier)] = n
	} else {
		t.attackers[n.ID.Zone][tierIdxOf(n.ID.Tier)] = n
	}
}

// QuarantinedCount returns how many nodes are currently isolated.
func (t *Topology) QuarantinedCount() (defenders, attackers int) {
	for i := range t.nodes {
		q, _, _ := t.nodes[i].Quarantined()
		if !q {
			continue
		}
		if t.nodes[i].ID.Role == RoleDefender {
			defenders++
		} else {
			attackers++
		}
	}
	return defenders, attackers
}

// FormatGrid renders the matrix as a 7x7 grid of defender tier occupancy, so
// the operator can confirm the topology actually covers all seven zones.
func (t *Topology) FormatGrid() string {
	out := make([]byte, 0, 256)
	out = append(out, "          "...)
	for z := ZoneNA; z < numZones; z++ {
		out = append(out, fmtPad(z.String(), 5)...)
	}
	out = append(out, '\n')
	for tier := TierMaster; tier <= TierEdge; tier++ {
		out = append(out, fmtPad(fmt.Sprintf("t%d %-6s", tier, tier.String()), 10)...)
		for z := ZoneNA; z < numZones; z++ {
			n := t.defenders[z][tierIdxOf(tier)]
			if n == nil {
				out = append(out, "  .  "...)
				continue
			}
			if q, _, _ := n.Quarantined(); q {
				out = append(out, "  X  "...)
				continue
			}
			out = append(out, "  o  "...)
		}
		out = append(out, '\n')
	}
	return string(out)
}
