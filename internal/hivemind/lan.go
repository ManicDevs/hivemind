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
	lanBeaconAddr  = "239.192.0.99:37799" // org-local multicast: this LAN, never routed
	lanBeaconAddr6 = "[ff05::99]:37799"   // site-local IPv6 twin of the above
	beaconEvery    = 2 * time.Second
	staticEvery    = 5 * time.Second
	staticRetry    = 15 * time.Second
)

// lanBeacon is the whole discovery protocol: who I am, where my TCP is,
// how capable I am, and where my DHT listens (0 = no DHT — older nodes).
// Receivers tolerate absent fields; discovery degrades, never breaks.
type lanBeacon struct {
	Node  string  `json:"node"`
	TCP   int     `json:"tcp"`
	Score float64 `json:"score"`
	DHT   int     `json:"dht,omitempty"`
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
	if err != nil && port != 0 {
		// Requested port busy: fall back to ephemeral rather than
		// abandoning TCP entirely. A random port meshes; no port isolates.
		fmt.Printf("⚠️  [PEER MESH] TCP :%d unavailable (%v) — falling back to ephemeral.\n", port, err)
		tl, err = net.Listen("tcp", ":0")
	}
	if err != nil {
		fmt.Printf("⚠️  [PEER MESH] TCP unavailable (%v) — unix-socket mesh only.\n", err)
		return
	}
	tcpAddr, ok := tl.Addr().(*net.TCPAddr)
	if !ok {
		fmt.Printf("⚠️  [PEER MESH] TCP address not TCP (?!) — unix-socket mesh only.\n")
		_ = tl.Close()
		return
	}
	pm.tcpListener = tl
	pm.tcpPort = tcpAddr.Port
	// Name the serving families honestly: a wildcard bind serves both
	// stacks where the OS allows dual-stack, v4 only elsewhere.
	family := "IPv4+IPv6 dual"
	if !tcpAddr.IP.IsUnspecified() {
		if tcpAddr.IP.To4() != nil {
			family = "IPv4"
		} else {
			family = "IPv6"
		}
	}
	fmt.Printf("🔒 [PEER MESH] Node %q listening TCP :%d for equals (%s).\n", pm.node, pm.tcpPort, family)
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
	if _, ok := pm.conns[peer.Node]; ok {
		pm.mu.Unlock()
		// Duplicate race: keep the registered link, drop the newcomer.
		// Deliberately no liveness probe here — nudging the old link
		// would flap healthy-but-quiet pipes, and a truly dead one
		// reaps itself on the 60s idle deadline while discovery redials.
		_ = conn.Close()
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
		// Learning starts at first contact, not at the next 30s tick:
		// advertise capability immediately so the new peer files us now.
		pm.maybeAnnounce()
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
	body, _ := json.Marshal(lanBeacon{Node: pm.node, TCP: pm.tcpPort, Score: pm.currentCapability(), DHT: pm.dhtPort()})
	// Shout on both stacks; either may be deaf and that is fine.
	for _, target := range []string{lanBeaconAddr, lanBeaconAddr6} {
		dst, err := net.ResolveUDPAddr("udp", target)
		if err != nil {
			continue
		}
		conn, err := net.DialUDP("udp", nil, dst)
		if err != nil {
			continue
		}
		_, _ = conn.Write(append(body, '\n'))
		_ = conn.Close()
	}
}

func (pm *PeerMesh) beaconRecvLoop() {
	// Listen on both stacks; each unavailable family is a warning,
	// not a failure — the other may still carry the LAN.
	joined := 0
	for _, target := range []string{lanBeaconAddr, lanBeaconAddr6} {
		addr, err := net.ResolveUDPAddr("udp", target)
		if err != nil {
			continue
		}
		conn, err := net.ListenMulticastUDP("udp", nil, addr)
		if err != nil {
			continue
		}
		if pm.beaconConn == nil {
			pm.beaconConn = conn // first socket owns shutdown duty
		}
		joined++
		go pm.serveBeaconConn(conn)
	}
	if joined == 0 {
		fmt.Printf("⚠️  [PEER MESH] Beacon receive unavailable (no multicast group joined).\n")
		return
	}
	go func() {
		<-pm.stopChan
		if pm.beaconConn != nil {
			_ = pm.beaconConn.Close()
		}
	}()
}

// serveBeaconConn reads one multicast socket to the shared handler.
// Closing one socket must not kill the other family's loop: errors
// that arrive with shutdown stop, anything else just continues.
func (pm *PeerMesh) serveBeaconConn(conn *net.UDPConn) {
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
		pm.handleBeacon(buf[:n], src.String())
	}
}

// handleBeacon files one heard advertisement and dials by the same
// rules on either stack: the source IP (v4 or v6) plus the advertised
// TCP port is always a dialable pair.
func (pm *PeerMesh) handleBeacon(raw []byte, src string) {
	var b lanBeacon
	if err := json.Unmarshal(raw, &b); err != nil {
		return
	}
	if b.Node == "" || b.Node == pm.node || b.TCP <= 0 || b.TCP > 65535 {
		return
	}
	host, _, err := net.SplitHostPort(src)
	if err != nil {
		return
	}
	target := net.JoinHostPort(host, strconv.Itoa(b.TCP))
	// File every heard super in the directory first — known nodes
	// need no dial to be worth remembering.
	pm.noteSuper(b.Node, target, b.Score, "lan")
	if b.DHT > 0 {
		// Join the DHT through the beaconer: discovery bootstraps
		// discovery, no introducer needed beyond this packet.
		pm.dhtPing(host, b.DHT)
	}
	if pm.connected(b.Node) {
		return
	}
	// Same dial rule as the filesystem mesh: only dial up, so every
	// pair still has exactly one initiator across both transports.
	if b.Node < pm.node {
		return
	}
	if pm.culledRecently(b.Node) {
		return // we cut this one; let it cool off
	}
	fmt.Printf("📻 [PEER MESH] Heard node %q at %s (capability %.2f) — dialing.\n", b.Node, target, b.Score)
	go pm.dialTCP(target)
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
				pm.mu.Lock()
				known, knownOk := linkedAddr[addr]
				pm.mu.Unlock()
				if knownOk {
					if pm.connected(known) {
						continue
					}
					// Known node, lost link: respect the cooling-off
					// period if we were the ones who cut it.
					if pm.culledRecently(known) {
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
		// linkedAddr is touched from every dial goroutine: guard it.
		pm.mu.Lock()
		linkedAddr[addr] = peer
		pm.mu.Unlock()
	})
}
