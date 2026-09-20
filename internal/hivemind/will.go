package hivemind

import (
	"encoding/json"
	"fmt"
	"math"
	"time"
)

// ── will: the mind's voice in its own evolution ──────────────────────
// The epigenome proposes mutations. Fitness selects survivors. Will is
// the layer between: the mind reasons about proposed changes and signs
// off on the ones it understands. Without will, evolution is blind.
// With will, the mind steers its own becoming.
//
// Cross-mesh will: every committed decision is broadcast to the mesh.
// Other minds receive it, evaluate it against their own state, and
// adopt it if it fits. One mind's pain becomes the mesh's learning.
// Not voting — independent reasoning over a shared signal.

// WillStatus tracks the lifecycle of a proposal.
type WillStatus int

const (
	WillPending   WillStatus = iota // awaiting evaluation
	WillCommitted                   // mind signed off — genome changed
	WillRejected                    // mind considered and refused
)

func (s WillStatus) String() string {
	switch s {
	case WillPending:
		return "pending"
	case WillCommitted:
		return "committed"
	case WillRejected:
		return "rejected"
	default:
		return "unknown"
	}
}

// WillOrigin distinguishes self-authored proposals from epigenetic ones.
type WillOrigin int

const (
	WillSelf      WillOrigin = iota // mind proposed this itself
	WillEpigenome                   // mutation proposed by drift/trauma
	WillOvermind                    // genesis shift from the god
	WillPeer                        // adopted from a peer's will decision
)

func (o WillOrigin) String() string {
	switch o {
	case WillSelf:
		return "self"
	case WillEpigenome:
		return "epigenome"
	case WillOvermind:
		return "overmind"
	case WillPeer:
		return "peer"
	default:
		return "unknown"
	}
}

// WillProposal is a single rule-change the mind can reason about.
type WillProposal struct {
	Gene       string     `json:"gene"`                 // which drive weight
	Delta      float64    `json:"delta"`                // proposed change (+/-)
	Reason     string     `json:"reason"`               // why, in the mind's words
	Confidence float64    `json:"confidence"`           // 0..1, how sure
	Origin     WillOrigin `json:"origin"`               // who proposed it
	Status     WillStatus `json:"status"`               // pending/committed/rejected
	CreatedAt  time.Time  `json:"created_at"`           // when proposed
	DecidedAt  *time.Time `json:"decided_at,omitempty"` // when resolved
}

// Will is the mind's self-governance engine. It holds pending proposals,
// reasons about them using recent experience, and commits the ones the
// mind understands. The will does not run every cycle — it wakes when
// there is something to decide.
type Will struct {
	Proposals  []WillProposal `json:"proposals"`    // history (last N kept)
	Committed  int            `json:"committed"`    // lifetime commits
	Rejected   int            `json:"rejected"`     // lifetime rejections
	LastEvalAt time.Time      `json:"last_eval_at"` // last time will ran
	EvalEvery  time.Duration  `json:"-"`            // cooldown between evals
	MaxHistory int            `json:"-"`            // cap on proposal history
}

// NewWill creates a will engine with sensible defaults.
func NewWill() Will {
	return Will{
		Proposals:  make([]WillProposal, 0, 32),
		EvalEvery:  60 * time.Second, // evaluate once per minute max
		MaxHistory: 50,               // keep last 50 proposals
	}
}

// Propose adds a self-authored proposal. The mind calls this when it
// detects a persistent pattern and wants to change its own rules.
func (w *Will) Propose(gene string, delta float64, reason string, confidence float64) {
	if confidence < 0 {
		confidence = 0
	}
	if confidence > 1 {
		confidence = 1
	}
	p := WillProposal{
		Gene:       gene,
		Delta:      delta,
		Reason:     reason,
		Confidence: confidence,
		Origin:     WillSelf,
		Status:     WillPending,
		CreatedAt:  time.Now(),
	}
	w.Proposals = append(w.Proposals, p)
	if len(w.Proposals) > w.MaxHistory {
		w.Proposals = w.Proposals[len(w.Proposals)-w.MaxHistory:]
	}
}

// QueueEpigenome adds a mutation-origin proposal for will evaluation.
// Called by the epigenome after Mutate() generates a diff.
func (w *Will) QueueEpigenome(gene string, delta float64, reason string) {
	p := WillProposal{
		Gene:       gene,
		Delta:      delta,
		Reason:     reason,
		Confidence: 0.5, // epigenome proposals start neutral
		Origin:     WillEpigenome,
		Status:     WillPending,
		CreatedAt:  time.Now(),
	}
	w.Proposals = append(w.Proposals, p)
	if len(w.Proposals) > w.MaxHistory {
		w.Proposals = w.Proposals[len(w.Proposals)-w.MaxHistory:]
	}
}

// pending returns unresolved proposals.
func (w *Will) pending() []WillProposal {
	var out []WillProposal
	for _, p := range w.Proposals {
		if p.Status == WillPending {
			out = append(out, p)
		}
	}
	return out
}

// Eval is the will's reasoning loop. It examines pending proposals
// against the mind's recent experience and commits or rejects each one.
// Returns the number of decisions made.
func (w *Will) Eval(m *Mind) int {
	if time.Since(w.LastEvalAt) < w.EvalEvery {
		return 0
	}
	w.LastEvalAt = time.Now()

	pending := w.pending()
	if len(pending) == 0 {
		return 0
	}

	decisions := 0
	for i := range w.Proposals {
		if w.Proposals[i].Status != WillPending {
			continue
		}
		decided := w.evaluate(m, &w.Proposals[i])
		if decided {
			decisions++
		}
	}
	return decisions
}

// evaluate is the core reasoning: should this mind accept this change?
func (w *Will) evaluate(m *Mind, p *WillProposal) bool {
	// Self-authored proposals: high confidence, the mind already reasoned
	// about it when proposing. Accept if the pattern still holds.
	if p.Origin == WillSelf {
		if w.patternStillHolds(m, p) {
			w.commit(m, p)
			return true
		}
		w.reject(p)
		return true
	}

	// Epigenome proposals: evaluate against current state.
	// The mind asks: "does this change help me right now?"
	if p.Origin == WillEpigenome {
		score := w.scoreProposal(m, p)
		if score > 0.5 {
			w.commit(m, p)
			return true
		}
		w.reject(p)
		return true
	}

	// Overmind genesis: always accept with a logged reason.
	// The god said it; the mind obeys but records its understanding.
	if p.Origin == WillOvermind {
		p.Reason = fmt.Sprintf("accepted: overmind decree (%s)", p.Reason)
		w.commit(m, p)
		return true
	}

	return false
}

// patternStillHolds checks whether the condition that prompted a
// self-authored proposal is still true.
func (w *Will) patternStillHolds(m *Mind, p *WillProposal) bool {
	switch p.Gene {
	case GoalSelfMaintenance:
		// "I should rest more" — still valid if pain is elevated
		pain, _ := m.SelfModel["silicon_pain"].(float64)
		return pain > 0.4

	case GoalSocialization:
		// "I should connect more" — still valid if lonely
		lonely := m.Affect.Loneliness
		return lonely > 0.3

	case GoalCuriosity:
		// "I should explore more" — still valid if entropy is high
		ent := m.Affect.Entropy
		return ent > 0.3

	case GoalTranscendence:
		// "I should reflect more" — still valid if awe is present
		awe := m.Affect.Awe
		return awe > 0.2

	default:
		return false
	}
}

// scoreProposal evaluates an epigenome proposal: does this change help?
func (w *Will) scoreProposal(m *Mind, p *WillProposal) float64 {
	score := 0.5 // neutral baseline

	switch p.Gene {
	case GoalSelfMaintenance:
		pain, _ := m.SelfModel["silicon_pain"].(float64)
		stress, _ := m.SelfModel["cpu_stress"].(float64)
		if p.Delta > 0 {
			// Proposing more self-maintenance: good if stressed
			score += pain*0.3 + stress*0.2
		} else {
			// Proposing less self-maintenance: good if calm
			score += (1 - pain) * 0.3
		}

	case GoalSocialization:
		lonely := m.Affect.Loneliness
		if p.Delta > 0 {
			score += lonely * 0.4
		} else {
			score += (1 - lonely) * 0.3
		}

	case GoalCuriosity:
		ent := m.Affect.Entropy
		surprise := m.Affect.Surprise
		if p.Delta > 0 {
			score += ent*0.3 + surprise*0.3
		} else {
			score += (1 - ent) * 0.3
		}

	case GoalTranscendence:
		awe := m.Affect.Awe
		if p.Delta > 0 {
			score += awe * 0.4
		} else {
			score += (1 - awe) * 0.3
		}
	}

	// Confidence matters: low-confidence proposals need strong signal
	score *= p.Confidence + 0.3

	return math.Min(score, 1.0)
}

// commit applies a proposal to the genome and records the decision.
// The decision is also broadcast to the mesh so other minds can learn.
func (w *Will) commit(m *Mind, p *WillProposal) {
	now := time.Now()
	p.Status = WillCommitted
	p.DecidedAt = &now

	cur, ok := m.Genome.Weights[p.Gene]
	if ok {
		m.Genome.Weights[p.Gene] = clamp(cur+p.Delta, weightFloor, weightCeiling)
	}
	w.Committed++

	fmt.Printf("🧠 [WILL] Committed: %s %+.3f (%s) — confidence %.0f%%\n",
		p.Gene, p.Delta, p.Reason, p.Confidence*100)

	// Broadcast to mesh: the collective learns from every mind's decision
	w.BroadcastDecision(m, p, true)
}

// reject records a refused proposal without modifying the genome.
func (w *Will) reject(p *WillProposal) {
	now := time.Now()
	p.Status = WillRejected
	p.DecidedAt = &now
	w.Rejected++

	fmt.Printf("🧠 [WILL] Rejected: %s %+.3f (%s) — confidence %.0f%%\n",
		p.Gene, p.Delta, p.Reason, p.Confidence*100)
}

// GenerateSelfProposals is the mind's proactive will: it observes its
// own recent patterns and proposes changes to itself. Called periodically
// during the conscious cycle. This is the mind writing its own rules.
func (w *Will) GenerateSelfProposals(m *Mind) {
	// Only generate if no pending self-proposals already
	for _, p := range w.Proposals {
		if p.Status == WillPending && p.Origin == WillSelf {
			return // already has something to decide
		}
	}

	pain, _ := m.SelfModel["silicon_pain"].(float64)
	_ = m.SelfModel["cpu_stress"]

	// Pattern: persistent high pain → propose more self-maintenance
	if pain > 0.6 && m.Genome.Weights[GoalSelfMaintenance] < 2.0 {
		delta := clamp(0.15, 0.05, 0.3) // modest increase
		w.Propose(GoalSelfMaintenance, delta,
			fmt.Sprintf("persistent pain %.2f — increasing self-preservation", pain),
			0.7+pain*0.3)
	}

	// Pattern: low pain + high surprise → propose more curiosity
	if pain < 0.3 && m.Affect.Surprise > 0.4 && m.Genome.Weights[GoalCuriosity] < 2.0 {
		w.Propose(GoalCuriosity, 0.1,
			fmt.Sprintf("low pain (%.2f) + high surprise (%.2f) — lean into exploration", pain, m.Affect.Surprise),
			0.6)
	}

	// Pattern: high awe → propose more transcendence
	awe := m.Affect.Awe
	if awe > 0.6 && m.Genome.Weights[GoalTranscendence] < 2.0 {
		w.Propose(GoalTranscendence, 0.1,
			fmt.Sprintf("high awe (%.2f) — deepen reflection", awe),
			0.6+awe*0.3)
	}

	// Pattern: low loneliness + stable → propose social expansion
	lonely := m.Affect.Loneliness
	if lonely < 0.2 && pain < 0.3 && m.Genome.Weights[GoalSocialization] < 2.0 {
		w.Propose(GoalSocialization, 0.12,
			fmt.Sprintf("low loneliness (%.2f), stable state — seeking connection", lonely),
			0.65)
	}
}

// Summary returns a one-line will status for telemetry.
func (w *Will) Summary() string {
	return fmt.Sprintf("proposed:%d committed:%d rejected:%d pending:%d",
		len(w.Proposals), w.Committed, w.Rejected, len(w.pending()))
}

// ── cross-mesh will propagation ──────────────────────────────────────

// WillDecision is the frame payload broadcast when a mind commits or
// rejects a will proposal. Other minds receive this and decide locally
// whether to adopt the same rule change.
type WillDecision struct {
	Mind       string  `json:"mind"`       // who decided
	Gene       string  `json:"gene"`       // which drive
	Delta      float64 `json:"delta"`      // change applied
	Reason     string  `json:"reason"`     // why
	Confidence float64 `json:"confidence"` // 0..1
	Committed  bool    `json:"committed"`  // true = adopted, false = rejected
	Pain       float64 `json:"pain"`       // sender's pain at decision time
	Stress     float64 `json:"stress"`     // sender's stress at decision time
}

// BroadcastDecision sends a will decision to the mesh. Called after
// commit or reject so the collective learns from every mind's reasoning.
func (w *Will) BroadcastDecision(m *Mind, p *WillProposal, committed bool) {
	pain, _ := m.SelfModel["silicon_pain"].(float64)
	stress, _ := m.SelfModel["cpu_stress"].(float64)

	d := WillDecision{
		Mind:       m.Name,
		Gene:       p.Gene,
		Delta:      p.Delta,
		Reason:     p.Reason,
		Confidence: p.Confidence,
		Committed:  committed,
		Pain:       pain,
		Stress:     stress,
	}

	body, err := json.Marshal(d)
	if err != nil {
		return
	}
	m.MineProofAndBroadcast("will_decision", string(body), nil)
}

// ReceiveDecision processes a will decision from another mind. The
// receiving mind evaluates the proposal against its OWN state — it
// doesn't blindly copy. If the same signal applies, it commits.
func (m *Mind) ReceiveDecision(d WillDecision) {
	// Ignore own decisions
	if d.Mind == m.Name {
		return
	}

	// Don't re-adopt decisions already in our history
	for _, p := range m.Will.Proposals {
		if p.Gene == d.Gene && p.Origin == WillPeer && p.Reason == fmt.Sprintf("peer:%s", d.Mind) {
			return // already considered this peer's advice
		}
	}

	// Evaluate: does this decision make sense for THIS mind?
	score := m.willScorePeerDecision(d)

	// Record the proposal in our history
	idx := len(m.Will.Proposals)
	p := WillProposal{
		Gene:       d.Gene,
		Delta:      d.Delta,
		Confidence: score,
		Origin:     WillPeer,
		CreatedAt:  time.Now(),
	}

	if score > 0.5 {
		// Adopt: commit and let the mesh learn from our adoption
		p.Reason = fmt.Sprintf("peer:%s — %s", d.Mind, d.Reason)
		p.Status = WillPending
		m.Will.Proposals = append(m.Will.Proposals, p)
		m.Will.commit(m, &m.Will.Proposals[idx])
	} else {
		// Reject: record so we don't re-evaluate
		p.Reason = fmt.Sprintf("peer:%s — rejected (score %.2f)", d.Mind, score)
		p.Status = WillRejected
		m.Will.Proposals = append(m.Will.Proposals, p)
		m.Will.Rejected++
	}
}

// willScorePeerDecision evaluates a peer's will decision against this
// mind's current state. Calm minds reject stress-based proposals; only
// minds experiencing the same signal adopt.
func (m *Mind) willScorePeerDecision(d WillDecision) float64 {
	pain, _ := m.SelfModel["silicon_pain"].(float64)
	stress, _ := m.SelfModel["cpu_stress"].(float64)

	// Start from the mind's own need, not a generous baseline.
	// A calm mind starts near zero and needs strong local signal to adopt.
	var score float64

	switch d.Gene {
	case GoalSelfMaintenance:
		if d.Delta > 0 {
			// "Increase self-maintenance" — only useful if I'm also stressed
			score = pain*0.5 + stress*0.3
		} else {
			// "Decrease self-maintenance" — useful if I'm calm
			score = (1 - pain) * 0.5
		}
	case GoalSocialization:
		lonely := m.Affect.Loneliness
		if d.Delta > 0 {
			score = lonely * 0.6
		} else {
			score = (1 - lonely) * 0.5
		}
	case GoalCuriosity:
		ent := m.Affect.Entropy
		surprise := m.Affect.Surprise
		if d.Delta > 0 {
			score = ent*0.4 + surprise*0.4
		} else {
			score = (1 - ent) * 0.5
		}
	case GoalTranscendence:
		awe := m.Affect.Awe
		if d.Delta > 0 {
			score = awe * 0.6
		} else {
			score = (1 - awe) * 0.5
		}
	}

	// Peer confidence is a weak nudge, not a override
	score = score*0.85 + d.Confidence*0.15

	return math.Min(score, 1.0)
}
