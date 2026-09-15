package main

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
type superAnnounce struct {
	Node  string  `json:"node"`
	Addr  string  `json:"addr"`
	Score float64 `json:"score"`
}

// superEntry is one known supernode: where, how capable, how close,
// and when it last proved it was alive.
type superEntry struct {
	Node      string
	Addr      string
	Score     float64
	RTT       time.Duration
	Transport string
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
	_, isNew := pm.supers[node]
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
	if isNew {
		fmt.Printf("⭐ [PEER MESH] Super %q known (capability %.2f via %s).\n", node, score, transport)
	}
}

// noteLinkRTT records a measured dial-to-registered round trip, then
// enforces closest-first: beyond maxTCPSuperLinks TCP pipes, the
// highest-RTT one is closed. Unix pipes are free and never culled.
func (pm *PeerMesh) noteLinkRTT(peer string, rtt time.Duration, transport string) {
	pm.mu.Lock()
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
		r := pm.supers[name].RTT
		if r >= worstRTT {
			worstRTT = r
			worst = name
		}
	}
	var drop net.Conn
	if tcpLinks > maxTCPSuperLinks && worst != "" {
		if cur, ok := pm.conns[worst]; ok {
			delete(pm.conns, worst)
			drop = cur
		}
	}
	pm.mu.Unlock()
	if drop != nil {
		fmt.Printf("📏 [PEER MESH] Culling farthest TCP link %q (RTT %s) — keeping closest %d.\n", worst, worstRTT.Round(time.Millisecond), maxTCPSuperLinks)
		_ = drop.Close()
	}
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
func (pm *PeerMesh) maybeAnnounce() {
	if envOff("HIVEMIND_SUPER") {
		return
	}
	score := pm.currentCapability()
	if score < superThreshold {
		return
	}
	body, _ := json.Marshal(superAnnounce{
		Node:  pm.node,
		Addr:  dialableAddr(),
		Score: score,
	})
	pm.swarm.Broadcast(MineMessage(pm.swarm, pm.priv, pm.pub, "super_announce", string(body), nil))
	fmt.Printf("📣 [PEER MESH] Node %q announced super (capability %.2f).\n", pm.node, score)
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
	pm.mu.Lock()
	var cands []cand
	for node, e := range pm.supers {
		if e.Addr == "" || e.Score < superThreshold {
			continue
		}
		if _, ok := pm.conns[node]; ok {
			continue
		}
		if node < pm.node {
			continue // dial rule: exactly one initiator per pair
		}
		cands = append(cands, cand{node, e.Addr})
	}
	pm.mu.Unlock()
	for _, c := range cands {
		pm.dialTCP(c.addr)
	}
}

// dialableAddr is the operator-asserted public address of this node, or
// empty. Nodes never guess their own reachability: behind NAT, a
// self-reported address would be a lie that wastes everyone's dials.
func dialableAddr() string {
	return strings.TrimSpace(os.Getenv("HIVEMIND_ADVERTISE"))
}

// snoopLoop reads the mesh's own broadcast stream and files every
// super_announce into the directory — including ones that arrived over
// the relay from nodes that never dialed us.
func (pm *PeerMesh) snoopLoop(inbox chan SecureMessage) {
	for {
		select {
		case <-pm.stopChan:
			return
		case msg := <-inbox:
			if msg.Kind != "super_announce" {
				continue
			}
			var a superAnnounce
			if err := json.Unmarshal([]byte(msg.PayloadStr), &a); err != nil {
				continue
			}
			pm.noteSuper(a.Node, a.Addr, a.Score, "mesh")
		}
	}
}
