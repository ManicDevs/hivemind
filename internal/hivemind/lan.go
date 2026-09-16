package hivemind

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// ── serverless LAN transport ──────────────────────────────────────────
// No registry, no bootstrap server, no config: every node shouts its
// address to the LAN on multicast and dials whoever it hears. Nodes
// behind NAT or across the internet link by explicit address instead
// (HIVEMIND_PEERS). The standard library is the only dependency.

const (
	lanBeaconAddr = "239.192.0.99:37799" // org-local multicast: this LAN, never routed
	beaconEvery   = 2 * time.Second
	staticEvery   = 5 * time.Second
	staticRetry   = 15 * time.Second
)

// lanBeacon is the whole discovery protocol: who I am, where my TCP is,
// and how capable I am. Older nodes send no score; they read as zero.
type lanBeacon struct {
	Node  string  `json:"node"`
	TCP   int     `json:"tcp"`
	Score float64 `json:"score"`
}

func envOff(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "0", "off", "false", "no":
		return true
	}
	return false
}

func staticPeerAddrs() []string {
	var out []string
	for _, p := range strings.Split(os.Getenv("HIVEMIND_PEERS"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// startLAN binds a TCP listener (HIVEMIND_PORT, or ephemeral for zero
// config) and brings up multicast discovery. Failure degrades to
// unix-socket-only rather than refusing to be born.
func (pm *PeerMesh) startLAN() {
	port := 0
	if p := strings.TrimSpace(os.Getenv("HIVEMIND_PORT")); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 && n < 65536 {
			port = n
		} else {
			fmt.Printf("⚠️  [PEER MESH] Ignoring bad HIVEMIND_PORT=%q, using ephemeral.\n", p)
		}
	}

	tl, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		fmt.Printf("⚠️  [PEER MESH] TCP unavailable (%v) — unix-socket mesh only.\n", err)
		return
	}
	pm.tcpListener = tl
	pm.tcpPort = tl.Addr().(*net.TCPAddr).Port
	fmt.Printf("🔒 [PEER MESH] Node %q listening TCP :%d for equals.\n", pm.node, pm.tcpPort)
	go pm.tcpAcceptLoop()

	if envOff("HIVEMIND_BEACON") {
		fmt.Println("📻 [PEER MESH] Multicast beacon off — static peers only.")
	} else {
		go pm.beaconSendLoop()
		go pm.beaconRecvLoop()
	}
	go pm.staticPeersLoop()
}

func (pm *PeerMesh) tcpAcceptLoop() {
	for {
		conn, err := pm.tcpListener.Accept()
		if err != nil {
			select {
			case <-pm.stopChan:
				return
			default:
				continue
			}
		}
		go pm.openLink(conn, false, "", "tcp", nil)
	}
}

// openLink runs the symmetric handshake exchange on any transport: the
// dialer speaks first, both sides learn the peer name, exactly one pipe
// per pair survives. expectPeer (unix discovery) must match; "" accepts
// whoever answers (TCP/statics learn the name here). onLink fires once the
// pipe is registered, before serving begins.
func (pm *PeerMesh) openLink(conn net.Conn, dialer bool, expectPeer string, transport string, onLink func(peer string)) {
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	reader := bufio.NewReader(conn)

	hs, _ := json.Marshal(peerHandshake{Node: pm.node})
	if _, err := conn.Write(append(hs, '\n')); err != nil {
		_ = conn.Close()
		return
	}

	peer, err := readHandshake(reader)
	if err != nil || peer.Node == "" || peer.Node == pm.node {
		_ = conn.Close() // silent, foreign, oversized, or self — not an equal
		return
	}
	if expectPeer != "" && peer.Node != expectPeer {
		_ = conn.Close() // socket path lied about its owner; don't mis-wire
		return
	}
	_ = conn.SetDeadline(time.Time{})

	pm.mu.Lock()
	if existing, ok := pm.conns[peer.Node]; ok {
		pm.mu.Unlock()
		_ = conn.Close() // duplicate race; keep the registered link
		_ = existing.SetDeadline(time.Now())
		return
	}
	pm.conns[peer.Node] = conn
	pm.trans[peer.Node] = transport
	first := !pm.history[peer.Node]
	pm.mu.Unlock()

	kind := "inbound"
	if dialer {
		kind = "outbound"
	}
	if first {
		fmt.Printf("🔗 [PEER MESH] Node %q linked %s via %s. Equals connected: %d\n", peer.Node, kind, transport, pm.LinkedPeers())
		// Presence: greet the new equal with a signed hello. It lands in
		// local minds (they register the link) and rides the bridge to the
		// peer, whose minds register us in turn.
		pm.swarm.Broadcast(MineMessage(pm.swarm, pm.priv, pm.pub, "hello", "PEER_HANDSHAKE:"+pm.node, []float64{0.0, 1.0, 9.81, -0.15}))
	}
	if onLink != nil {
		onLink(peer.Node)
	}
	pm.serve(conn, reader, peer.Node)
}

// ── multicast discovery ──

func (pm *PeerMesh) beaconSendLoop() {
	ticker := time.NewTicker(beaconEvery)
	defer ticker.Stop()
	for {
		select {
		case <-pm.stopChan:
			return
		case <-ticker.C:
			pm.beaconOnce()
		}
	}
}

func (pm *PeerMesh) beaconOnce() {
	body, _ := json.Marshal(lanBeacon{Node: pm.node, TCP: pm.tcpPort, Score: pm.currentCapability()})
	dst, err := net.ResolveUDPAddr("udp", lanBeaconAddr)
	if err != nil {
		return
	}
	conn, err := net.DialUDP("udp", nil, dst)
	if err != nil {
		return
	}
	defer conn.Close()
	_, _ = conn.Write(append(body, '\n'))
}

func (pm *PeerMesh) beaconRecvLoop() {
	addr, err := net.ResolveUDPAddr("udp", lanBeaconAddr)
	if err != nil {
		fmt.Printf("⚠️  [PEER MESH] Beacon receive unavailable (%v).\n", err)
		return
	}
	conn, err := net.ListenMulticastUDP("udp", nil, addr)
	if err != nil {
		fmt.Printf("⚠️  [PEER MESH] Beacon receive unavailable (%v).\n", err)
		return
	}
	pm.beaconConn = conn
	go func() {
		<-pm.stopChan
		_ = conn.Close()
	}()

	buf := make([]byte, 1024)
	for {
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-pm.stopChan:
				return
			default:
				continue
			}
		}
		var b lanBeacon
		if err := json.Unmarshal(buf[:n], &b); err != nil {
			continue
		}
		if b.Node == "" || b.Node == pm.node || b.TCP <= 0 || b.TCP > 65535 {
			continue
		}
		host, _, err := net.SplitHostPort(src.String())
		if err != nil {
			continue
		}
		target := net.JoinHostPort(host, strconv.Itoa(b.TCP))
		// File every heard super in the directory first — known nodes
		// need no dial to be worth remembering.
		pm.noteSuper(b.Node, target, b.Score, "lan")
		if pm.connected(b.Node) {
			continue
		}
		// Same dial rule as the filesystem mesh: only dial up, so every
		// pair still has exactly one initiator across both transports.
		if b.Node < pm.node {
			continue
		}
		if pm.culledRecently(b.Node) {
			continue // we cut this one; let it cool off
		}
		fmt.Printf("📻 [PEER MESH] Heard node %q at %s (capability %.2f) — dialing.\n", b.Node, target, b.Score)
		go pm.dialTCP(target)
	}
}

func (pm *PeerMesh) dialTCP(addr string) {
	t0 := time.Now()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return
	}
	pm.openLink(conn, true, "", "tcp", func(peer string) {
		pm.noteLinkRTT(peer, time.Since(t0), "tcp")
	})
}

// ── static peers (WAN without discovery) ──

func (pm *PeerMesh) staticPeersLoop() {
	addrs := staticPeerAddrs()
	if len(addrs) == 0 {
		return
	}
	lastTry := make(map[string]time.Time)
	linkedAddr := make(map[string]string) // addr -> node name once known
	ticker := time.NewTicker(staticEvery)
	defer ticker.Stop()
	for {
		select {
		case <-pm.stopChan:
			return
		case <-ticker.C:
			for _, addr := range addrs {
				if node, ok := linkedAddr[addr]; ok {
					if pm.connected(node) {
						continue
					}
					// Known node, lost link: respect the cooling-off
					// period if we were the ones who cut it.
					if pm.culledRecently(node) {
						continue
					}
				}
				if time.Since(lastTry[addr]) < staticRetry {
					continue
				}
				lastTry[addr] = time.Now()
				go pm.dialStatic(addr, linkedAddr)
			}
		}
	}
}

func (pm *PeerMesh) dialStatic(addr string, linkedAddr map[string]string) {
	t0 := time.Now()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return
	}
	pm.openLink(conn, true, "", "tcp", func(peer string) {
		pm.noteLinkRTT(peer, time.Since(t0), "tcp")
		pm.mu.Lock()
		linkedAddr[addr] = peer
		pm.mu.Unlock()
	})
}
