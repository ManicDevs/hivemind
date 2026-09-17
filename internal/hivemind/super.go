package hivemind

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

// ── capable supernodes, closest-first ─────────────────────────────────
// Any node may become super; none rules. Capability gates ANNOUNCING
// (only healthy nodes advertise), measured RTT decides RETENTION (every
// node keeps its closest links). Presence is the lease: stop announcing
// and the mesh forgets you within 90 seconds. No elections, no failover
// protocol, no thrones — preferred transit, nothing more.

const (
	superAnnounceEvery = 30 * time.Second
	superLease         = 90 * time.Second
	superThreshold     = 0.5 // minimum capability score to advertise
	maxTCPSuperLinks   = 3   // closest-first retention cap on TCP links
)

// superAnnounce is the PayloadStr body of a super_announce frame. Addr is
// the operator-asserted dialable address (HIVEMIND_ADVERTISE) or empty
// for LAN-only nodes, whose address travels with the beacon instead.
// DHT carries this node's DHT UDP port (0 = none) so far nodes can join
// the DHT without any prior introduction.
type superAnnounce struct {
	Node  string  `json:"node"`
	Addr  string  `json:"addr"`
	Score float64 `json:"score"`
	DHT   int     `json:"dht,omitempty"`
}

// superEntry is one known supernode: where, how capable, how close,
// which DHT identity, and when it last proved it was alive.
type superEntry struct {
	Node      string
	Addr      string
	Score     float64
	RTT       time.Duration
	Transport string
	IDHex     string // DHT identity (node-ID hex), when learned signed
	LastSeen  time.Time
}

// capability scores this node 0..1 from live telemetry: cool, idle,
// rested, long-lived, and relay-connected nodes score highest. A burning
// node scores zero — it must never advertise, however lonely the mesh.
func capability(pain, stress, fatigue, uptimeHours float64, relayOn bool) float64 {
	if pain > 0.8 || stress > 0.9 {
		return 0.0
	}
	uptime := uptimeHours
	if uptime > 1.0 {
		uptime = 1.0
	}
	score := 0.4*(1.0-pain) + 0.3*(1.0-stress) + 0.1*(1.0-fatigue) + 0.1*uptime
	if relayOn {
		score += 0.1
	}
	if score < 0 {
		return 0.0
	}
	if score > 1.0 {
		return 1.0
	}
	return score
}

// currentCapability reads the swarm's live telemetry into one number.
func (pm *PeerMesh) currentCapability() float64 {
	pm.swarm.mu.Lock()
	var pain, stress, fatigue, n float64
	for _, v := range pm.swarm.NodePain {
		pain += v
		n++
	}
	for _, v := range pm.swarm.NodeStress {
		stress += v
	}
	// Fatigue is not tracked per node; approximate from pain baseline.
	pm.swarm.mu.Unlock()
	if n > 0 {
		pain /= n
		stress /= n
	}
	fatigue = pain * 0.5
	return capability(pain, stress, fatigue, time.Since(pm.born).Hours(), pm.relayOn)
}

// noteSuper records or refreshes a directory entry. Addrs improve: a
// directly observed address (beacon source, static config) replaces an
// empty one, never the reverse.
func (pm *PeerMesh) noteSuper(node, addr string, score float64, transport string) {
	if node == "" || node == pm.node {
		return
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	_, exists := pm.supers[node]
	e, ok := pm.supers[node]
	if !ok {
		e = superEntry{Node: node, RTT: time.Hour}
	}
	if addr != "" {
		e.Addr = addr
	}
	e.Score = score
	e.Transport = transport
	e.LastSeen = time.Now()
	pm.supers[node] = e
	if !exists {
		fmt.Printf("⭐ [PEER MESH] Super %q known (capability %.2f via %s).\n", node, score, transport)
	}
}

// noteLinkRTT records a measured dial-to-registered round trip, then
// enforces closest-first: beyond maxTCPSuperLinks TCP pipes, the
// highest-RTT one is closed. Unix pipes are free and never culled.
// A culled peer gets a cooling-off period so the mesh doesn't spend
// itself redialing a link it just cut.
func (pm *PeerMesh) noteLinkRTT(peer string, rtt time.Duration, transport string) {
	pm.mu.Lock()
	if pm.culled == nil {
		pm.culled = make(map[string]time.Time) // tolerate hand-built meshes (tests)
	}
	pm.trans[peer] = transport
	if transport == "tcp" {
		if e, ok := pm.supers[peer]; ok {
			e.RTT = rtt
			pm.supers[peer] = e
		} else {
			pm.supers[peer] = superEntry{Node: peer, RTT: rtt, Transport: transport, LastSeen: time.Now()}
		}
	}
	var worst string
	var worstRTT time.Duration
	tcpLinks := 0
	for name, tr := range pm.trans {
		if tr != "tcp" {
			continue
		}
		if _, ok := pm.conns[name]; !ok {
			continue
		}
		tcpLinks++
		// Unmeasured links (inbound, never dialed) read as infinitely
		// far: unknown closeness must never outrank measured closeness,
		// or the mesh would cull proven pipes to keep mystery ones.
		rtt, ok := pm.supers[name]
		r := time.Hour
		if ok {
			r = rtt.RTT
		}
		if r >= worstRTT {
			worstRTT = r
			worst = name
		}
	}
	var drop net.Conn
	if tcpLinks > maxTCPSuperLinks && worst != "" {
		if cur, ok := pm.conns[worst]; ok {
			delete(pm.conns, worst)
			pm.culled[worst] = time.Now()
			drop = cur
		}
	}
	pm.mu.Unlock()
	if drop != nil {
		fmt.Printf("📏 [PEER MESH] Culling farthest TCP link %q (RTT %s) — keeping closest %d.\n", worst, worstRTT.Round(time.Millisecond), maxTCPSuperLinks)
		_ = drop.Close()
	}
}

// culledCooldown is how long a cut link stays cut before the mesh may
// reconsider it. Without this, static redial would re-establish every
// culled pipe within seconds and churn forever.
const culledCooldown = 5 * time.Minute

// culledRecently reports whether a peer was cut (or failed) too recently
// to deserve another dial attempt.
func (pm *PeerMesh) culledRecently(peer string) bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	since, ok := pm.culled[peer]
	if !ok {
		return false
	}
	if time.Since(since) > culledCooldown {
		delete(pm.culled, peer)
		return false
	}
	return true
}

// pruneSupers forgets entries whose lease lapsed without renewal.
func (pm *PeerMesh) pruneSupers() {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for node, e := range pm.supers {
		if time.Since(e.LastSeen) > superLease {
			delete(pm.supers, node)
		}
	}
}

// superLoop announces while capable and prunes the directory on schedule.
func (pm *PeerMesh) superLoop() {
	ticker := time.NewTicker(superAnnounceEvery)
	defer ticker.Stop()
	for {
		select {
		case <-pm.stopChan:
			return
		case <-ticker.C:
			pm.pruneSupers()
			pm.maybeAnnounce()
		}
	}
}

// maybeAnnounce publishes this node's super-advertisement when it is
// capable, not silenced, and (for WAN) explicitly dialable. The frame
// rides the standard outbound bridge: LAN peers hear it on their links,
// far nodes hear it on the relay. Silence is the resignation letter.
// Returns true when an advertisement actually went out.
func (pm *PeerMesh) maybeAnnounce() bool {
	if envOff("HIVEMIND_SUPER") {
		return false
	}
	score := pm.currentCapability()
	if score < superThreshold {
		return false
	}
	body, _ := json.Marshal(superAnnounce{
		Node:  pm.node,
		Addr:  pm.advertiseAddr(),
		Score: score,
		DHT:   pm.dhtPort(),
	})
	pm.swarm.Broadcast(MineMessage(pm.swarm, pm.priv, pm.pub, "super_announce", string(body), nil))
	fmt.Printf("📣 [PEER MESH] Node %q announced super (capability %.2f).\n", pm.node, score)
	return true
}

// superDialLoop works the supernode directory on schedule: advertised,
// capable, dialable equals get dialed closest-first by whoever should
// initiate under the dial rule.
func (pm *PeerMesh) superDialLoop() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-pm.stopChan:
			return
		case <-ticker.C:
			pm.dialSupers()
		}
	}
}

// dialSupers dials advertised, capable, unconnected supers we don't
// already have — oldest-lease-attempt first, one initiator per pair.
func (pm *PeerMesh) dialSupers() {
	type cand struct {
		node, addr string
	}
	// Phase 1, under lock: pure table reads, zero I/O. DHT lookups and
	// punch attempts happen after unlock — holding the mesh mutex across
	// network round trips would stall every heartbeat, and re-locking
	// inside (as an earlier revision did) deadlocks outright.
	pm.mu.Lock()
	var cands []cand
	var punch []string
	for node, e := range pm.supers {
		if _, ok := pm.conns[node]; ok {
			continue
		}
		if node < pm.node {
			continue // dial rule: exactly one initiator per pair
		}
		addr := e.Addr
		if addr == "" {
			// No direct address — flag for DHT resolution outside
			// the lock. Score gates advertisement, never friendship.
			if e.IDHex == "" || pm.dht == nil {
				continue
			}
			if ts, ok := pm.culled[node]; ok && time.Since(ts) <= culledCooldown {
				continue
			}
			punch = append(punch, node)
			continue
		}
		if ts, ok := pm.culled[node]; ok && time.Since(ts) <= culledCooldown {
			continue // cut recently; let it cool off
		}
		cands = append(cands, cand{node, addr})
	}
	pm.mu.Unlock()

	// Phase 2, unlocked: resolve, punch, dial — nothing here holds mu.
	for _, node := range punch {
		if pm.dht == nil {
			continue
		}
		if addr, ok := pm.dht.FindEndpoint(pm.superIDHex(node)); ok {
			pm.dialTCP(addr)
			continue
		}
		// Last resort before silence: NAT punching, if the operator
		// opted in — throttled per peer so hope never becomes spam.
		pm.mu.Lock()
		last, seen := pm.punchLast[node]
		if !seen || time.Since(last) > 5*time.Minute {
			pm.punchLast[node] = time.Now()
			pm.mu.Unlock()
			pm.requestPunch(node)
		} else {
			pm.mu.Unlock()
		}
	}
	for _, c := range cands {
		pm.dialTCP(c.addr)
	}
}

// superIDHex returns a directory entry's DHT identity, if mapped.
func (pm *PeerMesh) superIDHex(node string) string {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.supers[node].IDHex
}

// dialableAddr is the operator-asserted public address of this node, or
// empty. Nodes never guess their own reachability: behind NAT, a
// self-reported address would be a lie that wastes everyone's dials.
func dialableAddr() string {
	return strings.TrimSpace(os.Getenv("HIVEMIND_ADVERTISE"))
}

// resolveReflexive asks STUN once, in the background, for the address the
// internet actually sees. HIVEMIND_ADVERTISE always wins when set —
// explicit operator truth beats discovered truth.
func (pm *PeerMesh) resolveReflexive() {
	addr := reflexiveEndpoint()
	if addr == "" {
		return
	}
	pm.mu.Lock()
	pm.reflexive = addr
	pm.mu.Unlock()
	fmt.Printf("🌍 [PEER MESH] Reflexive address discovered: %s (the internet sees us here).\n", addr)
}

// advertiseAddr is what this node tells the mesh to dial: operator
// assertion first, STUN discovery second, silence otherwise.
func (pm *PeerMesh) advertiseAddr() string {
	if addr := dialableAddr(); addr != "" {
		return addr
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.reflexive
}

// snoopLoop reads the mesh's own broadcast stream and files every
// super_announce into the directory — including ones that arrived over
// the relay from nodes that never dialed us. Signed frames also map
// node names to DHT identities, so the directory can later resolve
// endpoints for names it has no address for.
func (pm *PeerMesh) snoopLoop(inbox chan SecureMessage) {
	for {
		select {
		case <-pm.stopChan:
			return
		case msg := <-inbox:
			switch msg.Kind {
			case "super_announce":
				var a superAnnounce
				if err := json.Unmarshal([]byte(msg.PayloadStr), &a); err != nil {
					continue
				}
				pm.noteSuper(a.Node, a.Addr, a.Score, "mesh")
				pm.setSuperID(a.Node, dhtIDFromPubKey(msg.SenderPubKey).hex())
				if a.DHT > 0 {
					pm.dhtPingHost(a.Addr, a.DHT)
				}
			case "punch_req":
				pm.answerPunch(msg)
			case "punch_accept":
				pm.completePunch(msg)
			}
		}
	}
}

// setSuperID binds a DHT identity to a directory name. Only ever called
// with keys from signature-verified frames — never from unsigned
// beacons, whose names anyone can claim. Never creates self entries:
// the mesh must not list itself as a discovery.
func (pm *PeerMesh) setSuperID(node, idHex string) {
	if node == "" || idHex == "" || node == pm.node {
		return
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	e := pm.supers[node]
	e.IDHex = idHex
	pm.supers[node] = e
}

// dhtPingHost joins the DHT through a "host:tcpPort"-style address plus
// a known DHT UDP port. Garbage in, silent skip out.
func (pm *PeerMesh) dhtPingHost(addr string, dhtPort int) {
	if pm.dht == nil || addr == "" || dhtPort <= 0 {
		return
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// Bare host without port (static-style "example.com").
		host = addr
	}
	// net.SplitHostPort on "host" without colon errors; handled above.
	pm.dhtPing(host, dhtPort)
}
