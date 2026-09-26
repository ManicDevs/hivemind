package hivemind

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
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
	// done closes once this attempt has fully finished — won, lost, or
	// stopped — so the initiator's retry loop can sequence attempts
	// instead of racing a second simultaneous open against the first.
	done chan struct{}
	fin  *sync.Once
}

func newPendingPunch(p pendingPunch) pendingPunch {
	p.done = make(chan struct{})
	p.fin = new(sync.Once)
	return p
}

// finishPunch releases the retry loop waiting on this attempt, exactly
// once, no matter which path ended it. fin is a pointer so pendingPunch
// stays copyable and vet-clean.
func (p pendingPunch) finishPunch() {
	if p.fin != nil {
		p.fin.Do(func() { close(p.done) })
	}
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

// punchNegotiations bounds how many rendezvous instants the initiator will
// announce for one peer. awaitPunch already retries the dial; this covers
// the other half of the handshake, where our punch_req went out but the
// peer's punch_accept never came back. One dropped or slow gossip frame
// otherwise leaves the pair with nothing but the relay, forever.
const punchNegotiations = 3

// punchDialRounds and punchRoundGap pace the simultaneous open itself,
// once a rendezvous is agreed: three attempts, two seconds apart.
const (
	punchDialRounds = 3
	punchRoundGap   = 2 * time.Second
)

func (pm *PeerMesh) requestPunch(peerNode string) int {
	port, done := pm.announcePunch(peerNode)
	if done == nil {
		return 0
	}
	go pm.retryRendezvous(peerNode, done)
	return port
}

// retryRendezvous re-announces a rendezvous that ended without a link,
// waiting for each attempt to fully finish before starting the next so
// two simultaneous opens are never in flight for one peer.
//
// Only the initiator runs this. The responder answers a stranger exactly
// once (see answerPunch); keeping the cadence on the requesting side is
// what stops a peer that never accepts from looping this node forever.
func (pm *PeerMesh) retryRendezvous(peer string, done chan struct{}) {
	for attempt := 1; attempt < punchNegotiations; attempt++ {
		select {
		case <-done:
		case <-pm.stopChan:
			return
		}
		pm.mu.Lock()
		_, linked := pm.conns[peer]
		pm.mu.Unlock()
		if linked {
			return // the punch landed after all
		}
		_, next := pm.announcePunch(peer)
		if next == nil {
			return // linked, already pending, or punching off: nothing to do
		}
		done = next
	}
}

// announcePunch performs one negotiation: refuse a live pipe and refuse to
// overlap another attempt, bind a port, announce the instant, and arm
// awaitPunch. Returns the bound port plus a channel closed when this
// attempt finishes; done is nil when we declined.
func (pm *PeerMesh) announcePunch(peerNode string) (int, chan struct{}) {
	if !punchEnabled() {
		return 0, nil
	}
	pm.mu.Lock()
	if _, ok := pm.conns[peerNode]; ok {
		pm.mu.Unlock()
		return 0, nil // already linked; punching a live pipe is vandalism
	}
	if _, ok := pm.punchPending[peerNode]; ok {
		pm.mu.Unlock()
		return 0, nil // one rendezvous at a time per peer
	}
	pm.mu.Unlock()

	selfIP := pm.selfDialIP()
	if selfIP == "" {
		return 0, nil // no address the peer could dial back: don't start
	}
	fd, port, err := bindPunchPort()
	if err != nil {
		return 0, nil
	}
	at := time.Now().Add(punchLeadTime)
	pend := newPendingPunch(pendingPunch{fd: fd, localPort: port, at: at})
	pm.punchPut(peerNode, pend)

	body, _ := json.Marshal(punchFrame{Node: pm.node, IP: selfIP, Port: port, At: at.UnixNano()})
	pm.swarm.Broadcast(MineMessage(pm.swarm, pm.priv, pm.pub, "punch_req", string(body), nil))
	go pm.awaitPunch(peerNode, at)
	return port, pend.done
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
	pm.punchPut(req.Node, newPendingPunch(pendingPunch{
		fd: fd, localPort: port, ip: req.IP, port: req.Port, at: at, accepted: true,
	}))

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
	if !ok {
		return // already reaped: another path owns this attempt now
	}
	// Signal only once the whole attempt is over, so a retry never starts
	// a second simultaneous open while these dial rounds are still live.
	defer pend.finishPunch()
	if !pend.accepted {
		// No counterpart (accept never arrived): never dial blind.
		// dropPunch would double-close; the fd dies here instead.
		if pend.fd > 0 {
			closePunchFD(pend.fd)
		}
		return
	}
	// Rendezvous rounds: one instant rarely survives scheduling jitter
	// on both ends, so a missed overlap rebinds identically and tries
	// again. Three rounds, then the relay remains.
	//
	// The gap is measured from our own failure, not from the shared
	// instant: every round is then guaranteed real spacing, so a round
	// can never collapse into the one before it. Over a real network
	// the overlap window is wide enough for that; on loopback under a
	// saturated scheduler it is not, which is why the end-to-end punch
	// test declines to run on an oversubscribed host.
	t0 := time.Now()
	fd, localPort := pend.fd, pend.localPort
	for round := 0; round < punchDialRounds; round++ {
		conn, err := connectBoundFD(fd, pend.ip, pend.port)
		if err == nil {
			pm.openLink(conn, true, "", "tcp", func(p string) {
				pm.noteLinkRTT(p, time.Since(t0), "tcp")
			})
			return
		}
		closePunchFD(fd)
		if round == punchDialRounds-1 {
			break
		}
		// Rebind before waiting: the socket must be held across the gap
		// or the peer's SYN can land before we own the port again.
		var rerr error
		fd, _, rerr = bindPort(localPort)
		if rerr != nil {
			fmt.Printf("🕳️  [PEER MESH] Punch to %q lost its footing (%v) — relay remains.\n", peer, rerr)
			return
		}
		select {
		case <-time.After(punchRoundGap):
		case <-pm.stopChan:
			closePunchFD(fd)
			return
		}
	}
	fmt.Printf("🕳️  [PEER MESH] Punch to %q missed after %d rounds — relay remains.\n", peer, punchDialRounds)
}

// dropPunch abandons one rendezvous, closing its held socket.
func (pm *PeerMesh) dropPunch(peer string) {
	pm.mu.Lock()
	pend, ok := pm.punchPending[peer]
	if ok {
		delete(pm.punchPending, peer)
	}
	pm.mu.Unlock()
	if ok {
		pend.finishPunch()
		if pend.fd > 0 {
			closePunchFD(pend.fd)
		}
	}
}
