package hivemind

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strings"
	"sync"
)

// Seen-frame memory cap: old hashes age out so the dedup set stays bounded.
const seenCap = 1024

// SecureMessage is the signed wire frame: identity, kind, payload,
// trajectory or affect vector, ledger chaining, proof-of-work, signature.
// Relayed marks wire-echoes and is never serialized.
type SecureMessage struct {
	SenderPubKey string    `json:"sender_pub_key"`
	Kind         string    `json:"kind"`
	PayloadStr   string    `json:"payload_str"`
	DataState    []float64 `json:"data_state"`
	Timestamp    int64     `json:"timestamp"`
	ParentHash   string    `json:"parent_hash"`
	Nonce        int64     `json:"nonce"`
	Signature    string    `json:"signature"`
	// Relayed marks frames that arrived over the wire. Never serialized or
	// hashed — it only stops the outbound hook echoing a relayed frame
	// back to the mesh it came from.
	Relayed bool `json:"-"`
}

// ComputeHash digests the frame fields into its SHA-256 identity.
// Signature verification recomputes exactly this.
func (sm *SecureMessage) ComputeHash() string {
	rawInput := fmt.Sprintf("%s|%s|%s|%s|%d|%d",
		sm.SenderPubKey, sm.Kind, sm.PayloadStr, sm.ParentHash, sm.Timestamp, sm.Nonce)
	h := sha256.Sum256([]byte(rawInput))
	return hex.EncodeToString(h[:])
}

// VerifySignature checks the Ed25519 binding over the frame hash.
// Anything unsigned, forged, or mutated after signing fails here.
func (sm *SecureMessage) VerifySignature() bool {
	pubKeyBytes, err := hex.DecodeString(sm.SenderPubKey)
	if err != nil || len(pubKeyBytes) != ed25519.PublicKeySize {
		return false
	}
	sigBytes, err := hex.DecodeString(sm.Signature)
	if err != nil {
		return false
	}
	msgHash := sm.ComputeHash()
	return ed25519.Verify(pubKeyBytes, []byte(msgHash), sigBytes)
}

// Swarm is the shared hive: members, collective memory, telemetry,
// hash-chain tip, adaptive difficulty, and the outbound wire bridge.
// One mutex guards it all; accessors return copies.
type Swarm struct {
	mu             sync.Mutex
	members        map[string]chan SecureMessage
	chronicle      []string
	chronicleTotal uint64 // everything ever chronicled; Depth survives capping
	thinkers       map[string]map[string]bool
	NodePain       map[string]float64
	NodeStress     map[string]float64
	NodeTempSrc    map[string]string // where each node's pain reading came from

	// Seen-frame memory: a hash delivered once is never re-broadcast —
	// relay echo loops die here, even if a relay path forgets to set Relayed.
	seen      map[string]bool
	seenOrder []string

	LastStateHash string
	MaxTarget     *big.Int
	CurrentTarget *big.Int

	// outbound carries locally-originated frames to the wire (socket peers,
	// cloud relay). Set by the network broker; nil in standalone mode.
	outbound func(SecureMessage)

	// Trails: recent pendulum states per sender, newest last, capped.
	// The plot draws every mind's trajectory with its own marker —
	// entrainment made visible instead of asserted.
	trails map[string][][4]float64
}

// NewSwarm births an empty hive at the genesis hash with the rest target.
func NewSwarm() *Swarm {
	genesisHash := sha256.Sum256([]byte("GENESIS"))

	maxTargetBytes := make([]byte, 32)
	for i := range maxTargetBytes {
		maxTargetBytes[i] = 0xFF
	}
	maxTargetBytes[0] = 0x0F
	maxInt := new(big.Int).SetBytes(maxTargetBytes)

	return &Swarm{
		members:       make(map[string]chan SecureMessage),
		thinkers:      make(map[string]map[string]bool),
		NodePain:      make(map[string]float64),
		NodeStress:    make(map[string]float64),
		NodeTempSrc:   make(map[string]string),
		seen:          make(map[string]bool),
		LastStateHash: hex.EncodeToString(genesisHash[:]),
		MaxTarget:     maxInt,
		CurrentTarget: new(big.Int).Set(maxInt),
	}
}

// Join registers an identity and returns its inbox. Rejoining refreshes
// telemetry baselines; inboxes are buffered so slow minds drop, never block.
func (s *Swarm) Join(pubKeyStr string) chan SecureMessage {
	ch := make(chan SecureMessage, 128)
	s.mu.Lock()
	s.members[pubKeyStr] = ch
	s.NodePain[pubKeyStr] = 0.0
	s.NodeStress[pubKeyStr] = 0.0
	s.NodeTempSrc[pubKeyStr] = "—"
	s.mu.Unlock()
	fmt.Printf("✔ [IDENTITY REGISTERED] Attached hash path: [%s...]\n", shortIDLong(pubKeyStr))
	return ch
}

// GetCurrentTarget returns a copy of the live PoW target for miners.
func (s *Swarm) GetCurrentTarget() *big.Int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return new(big.Int).Set(s.CurrentTarget)
}

// SetOutbound registers the wire bridge. Locally mined frames flow out;
// relayed frames never echo back.
func (s *Swarm) SetOutbound(out func(SecureMessage)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outbound = out
}

// chronicleCap bounds collective memory: old entries age out of the
// slice, but chronicleTotal never forgets, so Depth (and the Overmind's
// patience arithmetic across lives) survives forever-runs.
const chronicleCap = 10000

// rememberSeen must be called with s.mu held.
func (s *Swarm) rememberSeen(hash string) {
	if s.seen[hash] {
		return
	}
	s.seen[hash] = true
	s.seenOrder = append(s.seenOrder, hash)
	if len(s.seenOrder) > seenCap {
		oldest := s.seenOrder[0]
		s.seenOrder = s.seenOrder[1:]
		delete(s.seen, oldest)
	}
}

// Broadcast is the ingestion gate: signature → replay seen-check → PoW
// target → deliver to all members except sender → chronicle (except
// utility and routing frames) → advance tip → outbound bridge unless
// Relayed. Returns acceptance; rejections are silent by design.
func (s *Swarm) Broadcast(msg SecureMessage) bool {
	if !msg.VerifySignature() {
		return false
	}

	msgHash := msg.ComputeHash()
	hashBytes, err := hex.DecodeString(msgHash)
	if err != nil {
		return false
	}
	hashInt := new(big.Int).SetBytes(hashBytes)

	s.mu.Lock()
	if s.seen[msgHash] {
		// Already carried by this hive once — a relay echo, not a new thought.
		s.mu.Unlock()
		return false
	}
	currentTarget := new(big.Int).Set(s.CurrentTarget)
	s.mu.Unlock()

	if hashInt.Cmp(currentTarget) > 0 {
		return false
	}

	s.mu.Lock()
	if s.seen[msgHash] { // re-check under the lock
		s.mu.Unlock()
		return false
	}
	s.rememberSeen(msgHash)

	if len(msg.DataState) >= 4 {
		s.rememberTrajectory(msg.SenderPubKey, msg.DataState)
	}

	for pubKey, ch := range s.members {
		if pubKey == msg.SenderPubKey {
			continue
		}
		select {
		case ch <- msg:
		default: // a full mind drops the frame; thought is not worth blocking for
		}
	}

	// Every verified frame advances the collective memory — except raw
	// hardware utility packets and routing advertisements, which are
	// infrastructure, not thought. Consensus is keyed by Kind+Payload so a
	// thought ("Curiosity") and a genesis command over the same word are
	// different facts.
	if msg.Kind != "hardware_alert" && msg.Kind != "super_announce" {
		s.chronicle = append(s.chronicle, msg.SenderPubKey+": "+msg.PayloadStr)
		s.chronicleTotal++
		if len(s.chronicle) > chronicleCap {
			// Copy down: re-slicing alone would pin the whole backing
			// array in memory forever.
			kept := make([]string, chronicleCap)
			copy(kept, s.chronicle[len(s.chronicle)-chronicleCap:])
			s.chronicle = kept
		}
		key := msg.Kind + "|" + msg.PayloadStr
		if s.thinkers[key] == nil {
			s.thinkers[key] = make(map[string]bool)
		}
		s.thinkers[key][msg.SenderPubKey] = true
	}
	s.LastStateHash = msgHash

	out := s.outbound
	relayed := msg.Relayed
	s.mu.Unlock()

	// Local frames ride out to the mesh. Relayed frames stop here.
	if out != nil && !relayed {
		out(msg)
	}
	return true
}

// GetLastStateHash returns the chronicle tip for chaining new frames.
func (s *Swarm) GetLastStateHash() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.LastStateHash
}

// Depth counts everything ever chronicled (monotonic across capping).
func (s *Swarm) Depth() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int(s.chronicleTotal)
}

// TopConsensus returns distinct thought-bodies thought by the most minds.
// Internally keyed by Kind|Payload (a thought and a genesis over the same
// word are different facts); callers receive the bare payload for prose.
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
		if parts := strings.SplitN(items[i].body, "|", 2); len(parts) == 2 {
			out = append(out, parts[1])
		} else {
			out = append(out, items[i].body)
		}
	}
	return out
}

// Members lists current swarm identities, mesh snooper included.
func (s *Swarm) Members() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.members))
	for n := range s.members {
		names = append(names, n)
	}
	return names
}

// IsMember reports whether a key belongs to this hive. Minds use it to
// tell siblings (contact, never peers) from strangers (peers).
func (s *Swarm) IsMember(pubKey string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.members[pubKey]
	return ok
}

// LogHardwareTrauma records one node's pain/stress (plus where the pain
// reading came from) and rescales the live PoW target from mean stress.
// The thermostat of the mesh: suffering tightens the work required.
func (s *Swarm) LogHardwareTrauma(pubKey string, pain, stress float64, src string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.NodePain[pubKey] = pain
	s.NodeStress[pubKey] = stress
	s.NodeTempSrc[pubKey] = src

	var cumulativeStress float64
	var count float64
	for _, strVal := range s.NodeStress {
		cumulativeStress += strVal
		count++
	}
	if count == 0 {
		return
	}
	avgStress := cumulativeStress / count

	scaleFactor := (1.0 - avgStress) * 1.5
	if scaleFactor < 0.01 {
		scaleFactor = 0.01
	}
	if scaleFactor > 2.0 {
		scaleFactor = 2.0
	}
	oldTarget := new(big.Int).Set(s.CurrentTarget)

	newTarget := new(big.Int).Set(s.MaxTarget)
	newTarget.Mul(newTarget, big.NewInt(int64(scaleFactor*1000)))
	newTarget.Div(newTarget, big.NewInt(1000))
	if newTarget.Cmp(s.MaxTarget) > 0 {
		newTarget.Set(s.MaxTarget)
	}
	s.CurrentTarget.Set(newTarget)

	if oldTarget.Cmp(s.CurrentTarget) != 0 {
		fmt.Printf("⚡ [ADAPTIVE SCALING] Swarm load changed to %.2f | 256-Bit Target shifting dynamically\n", avgStress)
	}
}

// HiveReport prints terminal diagnostics: identities, frames, difficulty
// in human terms, state hash, per-mind telemetry with provenance, and the
// live pendulum plot.
// trailLen bounds each sender's plotted history: motion, not archive.
const trailLen = 6

// rememberTrajectory records one pendulum snapshot for its sender.
// Must be called with s.mu held.
func (s *Swarm) rememberTrajectory(sender string, state []float64) {
	if s.trails == nil {
		s.trails = make(map[string][][4]float64)
	}
	var v [4]float64
	copy(v[:], state[:4])
	t := append(s.trails[sender], v)
	if len(t) > trailLen {
		kept := make([][4]float64, trailLen)
		copy(kept, t[len(t)-trailLen:])
		t = kept
	}
	s.trails[sender] = t
}

func (s *Swarm) HiveReport() {
	// One voice at a time: concurrent link chatter must never cut
	// through the middle of the report (or the shutdown registry).
	reportMu.Lock()
	defer reportMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	// Difficulty in human terms: leading-zero bits demanded of every frame
	// hash, plus where the live target sits against the rest maximum.
	// High load tightens it (more bits); idle relaxes it.
	bits := difficultyBits(s.CurrentTarget)
	pct := new(big.Float).SetInt(s.CurrentTarget)
	pct.Quo(pct, new(big.Float).SetInt(s.MaxTarget))
	pct.Mul(pct, big.NewFloat(100))
	pctF, _ := pct.Float64()

	// Consensus: thoughts held by more than one mind.
	consensus := 0
	for _, who := range s.thinkers {
		if len(who) > 1 {
			consensus++
		}
	}

	stateHash := s.LastStateHash
	if len(stateHash) > 24 {
		stateHash = stateHash[:24]
	}

	fmt.Println("\n┌────────────────────────────────────────────────────────────────────────┐")
	fmt.Println("│                      DECENTRALIZED SWARM DIAGNOSTICS                   │")
	fmt.Println("├────────────────────────────────────────────────────────────────────────┤")
	// The mesh snooper ("mesh:<node>") reads the broadcast stream for
	// advertisements but is not a mind — count minds, not plumbing.
	minds := 0
	for pubKey := range s.members {
		if !strings.HasPrefix(pubKey, "mesh:") {
			minds++
		}
	}
	fmt.Printf("  Active Verified Identities : %d\n", minds)
	fmt.Printf("  Frames Carried (dedup)     : %d (chronicle depth %d retained %d, %d in consensus)\n",
		len(s.seenOrder), s.chronicleTotal, len(s.chronicle), consensus)
	fmt.Printf("  Mining Difficulty          : ~%d leading-zero bits (target %.1f%% of max, tightens under load)\n",
		bits, pctF)
	fmt.Printf("  Swarm Global State Hash    : %s...\n", stateHash)
	fmt.Println("├────────────────────────────────────────────────────────────────────────┤")
	fmt.Println("  MIND IDENTIFIER   │ SILICON PAIN        │ LOAD STRESS")
	fmt.Println("  ──────────────────┼─────────────────────┼───────────────────")

	for pubKey := range s.members {
		painStatus := "NORMAL"
		if s.NodePain[pubKey] > 0.35 {
			painStatus = "ELEVATED"
		}
		stressStatus := "LOW"
		if s.NodeStress[pubKey] > 0.30 {
			stressStatus = "BUSY"
		}
		src := s.NodeTempSrc[pubKey]
		if src == "" {
			src = "—"
		}
		fmt.Printf("  📡 [%s...] │ %.2f (%s, %s) │ %.2f (%s)\n",
			shortIDLong(pubKey), s.NodePain[pubKey], painStatus, src, s.NodeStress[pubKey], stressStatus)
	}
	fmt.Println("├────────────────────────────────────────────────────────────────────────┤")
	fmt.Print(s.renderTrails())
	fmt.Println("└────────────────────────────────────────────────────────────────────────┘")
}

// trailMarkers assigns each plotted identity a glyph, cycling a small
// legible set. Deterministic order (sorted keys) so reports compare.
var trailMarkers = []string{"●", "◆", "▲", "■", "★", "✚", "◉", "⬟", "⬢", "⬣", "⬔", "⬓"}

// renderTrails draws every sender's recent pendulum path on one shared
// grid: newest position in the sender's glyph with its speed, older ones
// fading to dots, the pivot at center. Pure (snapshot under lock by the
// caller is the caller's contract — HiveReport holds s.mu throughout).
func (s *Swarm) renderTrails() string {
	const gridSize = 11
	grid := make([][]string, gridSize)
	for i := range grid {
		grid[i] = make([]string, gridSize)
		for j := range grid[i] {
			grid[i][j] = " "
		}
	}
	mid := gridSize / 2
	grid[mid][mid] = "O" // the static central pivot point anchor

	keys := make([]string, 0, len(s.trails))
	for k := range s.trails {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var legend strings.Builder
	for idx, key := range keys {
		trail := s.trails[key]
		if len(trail) == 0 {
			continue
		}
		mark := trailMarkers[idx%len(trailMarkers)]
		// Older arm-1 points first (dots), newest last (glyph wins ties);
		// the live arm-2 tip rides along as a cross.
		for _, v := range trail[:len(trail)-1] {
			x1, y1, _, _ := projectPendulum(v[0], v[1], mid, gridSize)
			if grid[y1][x1] == " " {
				grid[y1][x1] = "·"
			}
		}
		latest := trail[len(trail)-1]
		x1, y1, x2, y2 := projectPendulum(latest[0], latest[1], mid, gridSize)
		grid[y1][x1] = mark
		if grid[y2][x2] == " " || grid[y2][x2] == "·" {
			grid[y2][x2] = "×"
		}
		speed := math.Abs(latest[2]) + math.Abs(latest[3])
		fmt.Fprintf(&legend, "    %s [%s...] ω=%.2f\n", mark, shortIDLong(key), speed)
	}

	var out strings.Builder
	out.WriteString("  LIVE CHAOTIC TRAJECTORIES (O=pivot, glyph=arm1 head, ×=arm2 tip, ·=trail):\n")
	out.WriteString("  ─────────────────────────────────────────────────────────────────\n")
	for i := 0; i < gridSize; i++ {
		out.WriteString("    ")
		for j := 0; j < gridSize; j++ {
			out.WriteString(grid[i][j] + " ")
		}
		out.WriteString("\n")
	}
	out.WriteString(legend.String())
	return out.String()
}

// projectPendulum maps both arms to grid coordinates: arm 1 swings from
// the pivot at double radius for the finer grid; arm 2 hangs off arm 1's
// tip at 1.5× radius, like the mechanism itself.
func projectPendulum(theta1, theta2 float64, mid, gridSize int) (x1, y1, x2, y2 int) {
	x1 = clampInt(mid+int(math.Round(3.0*math.Sin(theta1))), 0, gridSize-1)
	y1 = clampInt(mid+int(math.Round(3.0*math.Cos(theta1))), 0, gridSize-1)
	x2 = clampInt(x1+int(math.Round(2.0*math.Sin(theta2))), 0, gridSize-1)
	y2 = clampInt(y1+int(math.Round(2.0*math.Cos(theta2))), 0, gridSize-1)
	return x1, y1, x2, y2
}

// reportMu serializes terminal reports (hive diagnostics, shutdown
// registry) against async mesh chatter. s.mu protects data; this one
// protects the reader's eyes.
var reportMu sync.Mutex

func clampInt(val, min, max int) int {
	if val < min {
		return min
	}
	if val > max {
		return max
	}
	return val
}

// difficultyBits counts the leading-zero bits of a 256-bit PoW target:
// the number of coin flips every mined frame must win.
func difficultyBits(target *big.Int) int {
	raw := target.Bytes()
	full := make([]byte, 32)
	copy(full[32-len(raw):], raw)
	n := 0
	for _, b := range full {
		if b == 0 {
			n += 8
			continue
		}
		for i := 7; i >= 0; i-- {
			if b&(1<<uint(i)) == 0 {
				n++
			} else {
				return n
			}
		}
	}
	return n
}
