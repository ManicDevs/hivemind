package hivemind

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

// ── TCP simultaneous open (NAT traversal primitive) ───────────────────
// Two nodes behind permissive NATs can establish a direct TCP connection
// with no listener and no relay in the data path: both bind their local
// ports and CONNECT to each other at the same instant, so each side's
// outbound SYN punches its own NAT mapping while looking (to each NAT)
// like solicited traffic. Needs rendezvous (who, which ports, when —
// carried by DHT or relay frames) and only defeats full-cone / restricted
// NATs; symmetric NATs with port randomization stay unreachable by
// physics, not by effort. Raw syscalls because Go's net package offers
// no simultaneous-open API. Linux-tested; other Unixes analogous.

// punchTimeout bounds one attempt: rendezvous misses must fail fast so
// the mesh falls back to relay instead of hanging on hope.
const punchTimeout = 8 * time.Second

// punchFrame is the PayloadStr body of punch_req / punch_accept frames:
// who, where (their bound punch port), and exactly when (unix nanos).
type punchFrame struct {
	Node string `json:"node"`
	IP   string `json:"ip"`
	Port int    `json:"port"`
	At   int64  `json:"at"`
}

// bindPunchPort creates a bound-but-unconnected TCP socket for a future
// simultaneous open, returning the fd to HOLD (not a connection: the
// dial happens later, at the rendezvous instant).
// simultaneousDialAt is simultaneousDial with an agreed rendezvous
// instant: binds immediately, then holds until `at` before connecting,
// so both ends' SYNs overlap even across clock skew and scheduling
// jitter. An `at` in the past connects at once.
func simultaneousDialAt(localPort int, remoteHost string, remotePort int, at time.Time) (net.Conn, error) {
	fd, _, err := bindPort(localPort)
	if err != nil {
		return nil, err
	}
	// Hold for rendezvous: the overlap of the two SYNs is the whole trick.
	if wait := time.Until(at); wait > 0 {
		time.Sleep(wait)
	}
	return connectBoundFD(fd, remoteHost, remotePort)
}

// ── punch rendezvous protocol ─────────────────────────────────────────
// Hole punching needs two things no packet can provide: a bound local
// port held open, and an agreed instant. The protocol: the requester
// binds, announces (node, reflexive IP, punch port, instant) by signed
// frame; the responder binds, answers, and both dial at the instant.
// HIVEMIND_PUNCH=auto enables it; default off — punching binds ports and
// dials strangers, which stays an operator's explicit choice.

// pendingPunch is one half-kept rendezvous: our bound socket waiting
// for its instant, and where to point it then. accepted marks a fully
// negotiated pair (both ports known); without it we never dial blind.
type pendingPunch struct {
	fd        int
	localPort int
	ip        string
	port      int
	at        time.Time
	accepted  bool
}

// punchMu guards the pending table alongside pm.mu discipline: never
// hold it across sleeps, dials, or broadcasts.
func (pm *PeerMesh) punchPut(peer string, p pendingPunch) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.punchPending == nil {
		pm.punchPending = make(map[string]pendingPunch)
	}
	pm.punchPending[peer] = p
}

func punchEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("HIVEMIND_PUNCH")))
	return v == "1" || v == "on" || v == "true" || v == "auto"
}

// requestPunch starts our half: bind a punch port, hold it, announce the
// rendezvous, and dial at the instant. Returns the bound port (0 = refused:
// punching disabled, no reflexive address, or bind failure).
// punchLeadTime is how far ahead rendezvous instants are set: enough
// slack for scheduling jitter on loaded machines, short enough that
// held ports don't linger. Both ends sleep to the same instant, so
// only relative wakeup skew matters, not absolute delay.
const punchLeadTime = 8 * time.Second

func (pm *PeerMesh) requestPunch(peerNode string) int {
	if !punchEnabled() {
		return 0
	}
	pm.mu.Lock()
	if _, ok := pm.conns[peerNode]; ok {
		pm.mu.Unlock()
		return 0 // already linked; punching a live pipe is vandalism
	}
	if _, ok := pm.punchPending[peerNode]; ok {
		pm.mu.Unlock()
		return 0 // one rendezvous at a time per peer
	}
	pm.mu.Unlock()

	selfIP := pm.selfDialIP()
	if selfIP == "" {
		return 0 // no address the peer could dial back: don't start
	}
	fd, port, err := bindPunchPort()
	if err != nil {
		return 0
	}
	at := time.Now().Add(punchLeadTime)
	pm.punchPut(peerNode, pendingPunch{fd: fd, localPort: port, at: at})

	body, _ := json.Marshal(punchFrame{Node: pm.node, IP: selfIP, Port: port, At: at.UnixNano()})
	pm.swarm.Broadcast(MineMessage(pm.swarm, pm.priv, pm.pub, "punch_req", string(body), nil))
	go pm.awaitPunch(peerNode, at)
	return port
}

// selfDialIP is the IP this node tells punch peers to dial back: explicit
// operator assertion first, STUN discovery second, bare values accepted
// as-is (a lone "127.0.0.1" is a host, not an error). Empty means
// undialable — callers must not start what the peer cannot finish.
func (pm *PeerMesh) selfDialIP() string {
	for _, src := range []string{pm.advertiseAddr(), reflexiveEndpoint()} {
		if src == "" {
			continue
		}
		if host, _, err := net.SplitHostPort(src); err == nil {
			return host
		}
		return src
	}
	return ""
}

// answerPunch handles an incoming rendezvous: validate ruthlessly, bind
// our side, answer, and dial at the instant. Strangers get one shot each.
func (pm *PeerMesh) answerPunch(msg SecureMessage) {
	if !punchEnabled() {
		return
	}
	var req punchFrame
	if err := json.Unmarshal([]byte(msg.PayloadStr), &req); err != nil {
		return
	}
	if req.Node == "" || req.Node == pm.node || req.IP == "" || req.Port <= 0 {
		return
	}
	at := time.Unix(0, req.At)
	if wait := time.Until(at); wait < 2*time.Second || wait > 30*time.Second {
		return // too hot (replay?) or too far (roster-bloat): decline
	}
	if net.ParseIP(req.IP) == nil {
		return
	}
	pm.mu.Lock()
	if _, ok := pm.conns[req.Node]; ok {
		pm.mu.Unlock()
		return // already linked; nothing to punch through
	}
	if _, ok := pm.punchPending[req.Node]; ok {
		pm.mu.Unlock()
		return
	}
	pm.mu.Unlock()

	fd, port, err := bindPunchPort()
	if err != nil {
		return
	}
	selfIP := pm.selfDialIP()
	if selfIP == "" {
		// No dial-back address to offer: answering would send the peer
		// into a wall. Decline honestly instead.
		closePunchFD(fd)
		return
	}
	pm.punchPut(req.Node, pendingPunch{fd: fd, localPort: port, ip: req.IP, port: req.Port, at: at, accepted: true})

	body, _ := json.Marshal(punchFrame{Node: pm.node, IP: selfIP, Port: port, At: at.UnixNano()})
	pm.swarm.Broadcast(MineMessage(pm.swarm, pm.priv, pm.pub, "punch_accept", string(body), nil))
	go pm.awaitPunch(req.Node, at)
}

// completePunch files a punch acceptance against our pending request.
// Wrong instant, unknown peer, or empty address: not our rendezvous.
func (pm *PeerMesh) completePunch(msg SecureMessage) {
	var acc punchFrame
	if err := json.Unmarshal([]byte(msg.PayloadStr), &acc); err != nil {
		return
	}
	if acc.Node == "" || acc.IP == "" || acc.Port <= 0 {
		return
	}
	if net.ParseIP(acc.IP) == nil {
		return
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pend, ok := pm.punchPending[acc.Node]
	if !ok || pend.accepted || !pend.at.Equal(time.Unix(0, acc.At)) {
		return
	}
	pend.ip, pend.port, pend.accepted = acc.IP, acc.Port, true
	pm.punchPending[acc.Node] = pend
}

// awaitPunch sleeps until the instant, then dials from the held socket
// straight into the standard link pipeline. Win or lose, the entry and
// the fd are gone afterward — rendezvous state never accumulates.
func (pm *PeerMesh) awaitPunch(peer string, at time.Time) {
	if wait := time.Until(at); wait > 0 {
		select {
		case <-time.After(wait):
		case <-pm.stopChan:
			pm.dropPunch(peer)
			return
		}
	}
	pm.mu.Lock()
	pend, ok := pm.punchPending[peer]
	if ok {
		delete(pm.punchPending, peer)
	}
	pm.mu.Unlock()
	if !ok || !pend.accepted {
		// No counterpart (accept never arrived): never dial blind.
		// dropPunch would double-close; the fd dies here instead.
		if ok && pend.fd > 0 {
			closePunchFD(pend.fd)
		}
		return
	}
	// Rendezvous rounds: one instant rarely survives scheduling jitter
	// on both ends, so a missed overlap rebinds identically and tries
	// again. Both sides run the same cadence, so retries converge
	// instead of chasing. Three rounds, then the relay remains.
	t0 := time.Now()
	fd, localPort := pend.fd, pend.localPort
	for round := 0; round < 3; round++ {
		conn, err := connectBoundFD(fd, pend.ip, pend.port)
		if err == nil {
			pm.openLink(conn, true, "", "tcp", func(p string) {
				pm.noteLinkRTT(p, time.Since(t0), "tcp")
			})
			return
		}
		closePunchFD(fd)
		if round == 2 {
			break
		}
		select {
		case <-time.After(2 * time.Second):
		case <-pm.stopChan:
			return
		}
		var rerr error
		fd, _, rerr = bindPort(localPort)
		if rerr != nil {
			fmt.Printf("🕳️  [PEER MESH] Punch to %q lost its footing (%v) — relay remains.\n", peer, rerr)
			return
		}
	}
	fmt.Printf("🕳️  [PEER MESH] Punch to %q missed after 3 rounds — relay remains.\n", peer)
}

// dropPunch abandons one rendezvous, closing its held socket.
func (pm *PeerMesh) dropPunch(peer string) {
	pm.mu.Lock()
	pend, ok := pm.punchPending[peer]
	if ok {
		delete(pm.punchPending, peer)
	}
	pm.mu.Unlock()
	if ok && pend.fd > 0 {
		closePunchFD(pend.fd)
	}
}
