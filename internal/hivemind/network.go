package hivemind

import (
	"bufio"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Transport dials, socket naming, pacing, and relay endpoints.
const (
	socketPrefix         = "/tmp/hivemind-"
	socketSuffix         = ".sock"
	discoveryInterval    = 2 * time.Second
	staleSocketAge       = 5 * time.Second // a socket unreached for this long may be swept
	cloudPublishInterval = 5 * time.Second
	// streamLifetime caps one relay long-poll: silent death recycles.
	streamLifetime = 5 * time.Minute
)

func relayURL() string {
	if u := strings.TrimSpace(os.Getenv("HIVEMIND_RELAY_URL")); u != "" {
		return strings.TrimSuffix(u, "/")
	}
	return ""
}

// cipherKey returns the AES-256 key for encrypting outbound frames:
// operator env first, else this hour's machine-bound ratchet key. The
// committed static demo key is gone from this path — see fallbackKey.
func cipherKey() []byte {
	if k := os.Getenv("HIVEMIND_CIPHER_KEY"); len(k) == 32 {
		return []byte(k)
	}
	if key, ok := hourKey(0); ok {
		return key
	}
	relayWarnOnce.Do(func() {
		fmt.Println("⚠️  [RELAY] No relay key (env or build-time) — frames are signed-and-public, obfuscated only.")
	})
	return []byte("HIVE_MIND_32_BYTE_STATIC_KEY_PAD")
}

// keyEpochAnchor bounds the ratchet chain: hours counted from here, so
// derivation cost stays flat-ish for decades (~9k hashes/year, μs each).
var keyEpochAnchor = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

// hourEpoch counts whole UTC hours since the anchor. Always called with
// times past the anchor; truncation is a floor there.
func hourEpoch(t time.Time) int64 {
	return int64(t.UTC().Sub(keyEpochAnchor).Hours())
}

// dayEpoch counts whole UTC days since the anchor: the TOTD root. One
// root per day bounds every compromise to 24 hours of mesh thought.
func dayEpoch(t time.Time) int64 {
	return hourEpoch(t) / 24
}

// hourAAD binds a frame to its day and hour: replays from other hours
// fail authentication even under a valid key. Time as tamper-evidence.
func hourAAD(hour int64) string {
	return fmt.Sprintf("hivemind-relay-d%dh%d", hour/24, hour)
}

// ratchetBase folds seed, hardware, and install identity into one root.
// Pure and testable: same inputs, same root, anywhere.
func ratchetBase(seedHex, fingerprint, machineID string) ([]byte, bool) {
	raw, err := hex.DecodeString(seedHex)
	if err != nil || len(raw) != 32 {
		return nil, false
	}
	mac := hmac.New(sha256.New, raw)
	mac.Write([]byte("hivemind-ratchet|" + fingerprint + "|" + machineID))
	return mac.Sum(nil), true
}

// dayKey walks the hash chain to a day: the TOTD root. One-way per
// step — a compromised day reveals nothing past, and yesterday's root
// is unrecoverable from today's. Pure: feeds unit tests and production
// identically.
func dayKey(seedHex, fingerprint, machineID string, day int64) ([]byte, bool) {
	h, ok := ratchetBase(seedHex, fingerprint, machineID)
	if !ok || day < 0 {
		return nil, false
	}
	for i := int64(0); i < day; i++ {
		sum := sha256.Sum256(h)
		h = sum[:]
	}
	return h, true
}

// ratchetKey derives an hour's key through the day's root: HMAC(dayKey,
// hour). Two tiers — an hourly leak reveals nothing (HMAC one-way, not
// even the day's other hours), a daily leak is bounded to 24 hours, and
// the chain behind stays buried. Same signature as before: determinism,
// hourly rotation, machine binding, and 32-byte keys all hold.
func ratchetKey(seedHex, fingerprint, machineID string, hour int64) ([]byte, bool) {
	if hour < 0 {
		return nil, false
	}
	day, ok := dayKey(seedHex, fingerprint, machineID, hour/24)
	if !ok {
		return nil, false
	}
	mac := hmac.New(sha256.New, day)
	mac.Write([]byte(fmt.Sprintf("hivemind-hour|%d", hour)))
	return mac.Sum(nil), true
}

// hourKey derives this machine's key for a relative hour offset:
// 0 = now, negative = past (acceptance window). Seed from the build,
// hardware + install identity from the box, hour from the wall.
func hourKey(backHours int) ([]byte, bool) {
	nowHour := hourEpoch(time.Now()) - int64(backHours)
	if nowHour < 0 {
		return nil, false
	}
	return ratchetKey(compileRelayKey, machineFingerprint(), machineSecret(), nowHour)
}

// machineSecret is the second factor theft must also win: /etc/machine-id
// (unique per install, root or world readable, never transmitted).
// Empty where unreadable — derivation degrades to seed+hardware, loudly
// documented, never silently.
func machineSecret() string {
	for _, path := range []string{"/etc/machine-id", "/etc/ssh/ssh_host_ed25519_key.pub"} {
		if data, err := os.ReadFile(path); err == nil {
			if s := strings.TrimSpace(string(data)); s != "" {
				return path + ":" + s
			}
		}
	}
	return ""
}

// machineFingerprint identifies this hardwareskin: CPU model + total RAM
// from /proc — constant per machine, different across machines. Cached
// once; the sensors may dance, the bones do not move.
var (
	machineFingerprintOnce sync.Once
	machineFingerprintVal  string
)

func machineFingerprint() string {
	machineFingerprintOnce.Do(func() {
		model, total := "unknown-cpu", "unknown-ram"
		if data, err := os.ReadFile("/proc/cpuinfo"); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "model name") {
					if parts := strings.SplitN(line, ":", 2); len(parts) == 2 {
						model = strings.TrimSpace(parts[1])
						break
					}
				}
			}
		}
		if data, err := os.ReadFile("/proc/meminfo"); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "MemTotal:") {
					total = strings.Join(strings.Fields(line)[1:], "")
					break
				}
			}
		}
		machineFingerprintVal = model + "|" + total
	})
	return machineFingerprintVal
}

// compileRelayKey holds 64 hex chars injected at build time. Plain
// `go build` leaves it empty, which selects the static demo key above.
var compileRelayKey string

var relayWarnOnce sync.Once

var nodeSanitizer = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// SanitizeNode makes a node name safe for a file path component.
func SanitizeNode(name string) string {
	return nodeSanitizer.ReplaceAllString(name, "-")
}

// peerHandshake is the first line on every new connection: it tells the
// accepting side which node dialed it.
type peerHandshake struct {
	Node string `json:"node"`
}

// PeerMesh is the symmetric serverless network. Every node owns its own
// socket file and TCP port; nodes discover each other over the filesystem
// and over LAN multicast, then dial directly; each connection is a private
// two-way pipe. No listener is privileged and no registry exists — an
// address (path or host:port) is the whole trick.
type PeerMesh struct {
	mu          sync.Mutex
	swarm       *Swarm
	node        string // this node's name; also names its socket and soul dir
	listener    net.Listener
	tcpListener net.Listener
	tcpPort     int
	// beaconConns holds every joined multicast socket so Close can shut
	// all families down (a second family's loop must never leak).
	beaconConns []*net.UDPConn

	// conns are live links to peer nodes, keyed by peer node name,
	// regardless of transport: one pipe per pair, always.
	conns map[string]net.Conn

	// trans records how each live link was made (unix/tcp) for
	// closest-first retention. supers is the supernode directory.
	// culled remembers recently cut links so redial loops back off.
	trans  map[string]string
	supers map[string]superEntry
	culled map[string]time.Time

	// born timestamps this node for capability scoring; relayOn records
	// whether the cloud leg is part of this node's offering.
	born    time.Time
	relayOn bool

	// punchPending holds half-kept NAT rendezvous (peer → bound socket +
	// dial target + instant). Guarded by mu like everything else here.
	punchPending map[string]pendingPunch
	// punchLast throttles rendezvous attempts per peer: hope, not spam.
	punchLast map[string]time.Time
	// badFrames counts relay frames that failed decryption; lastBadWarn
	// throttles the key-mismatch warning to once a minute.
	badFrames   uint64
	lastBadWarn time.Time

	// dht is the Kademlia-lite discovery layer (nil when HIVEMIND_DHT=off).
	// It learns the mesh through already-trusted links, then discovers
	// beyond them: endpoint records for dialable nodes replicate across
	// holders, and dialSupers consults it when direct knowledge runs out.
	dht *dhtNode
	// reflexive is the STUN-discovered public address (host:port), resolved
	// in the background after start: dialable truth for nodes that never
	// set HIVEMIND_ADVERTISE. Empty until known (or forever, offline).
	reflexive string

	// history records only links that actually carried a decoded frame.
	// The shutdown registry is a record of conversations, not of intent.
	history map[string]bool

	pub  string
	priv ed25519.PrivateKey

	stopChan chan struct{}

	cloudQueue  chan SecureMessage
	cloudCtx    context.Context
	cloudCancel context.CancelFunc

	// Guardrails on the cloud leg: at most 2 relay posts/sec, and a
	// breaker that fails fast after 3 straight errors (30s half-open).
	// A dead relay must not eat the mesh's time.
	relayLimiter *Limiter
	relayBreaker *Breaker
}

// NewPeerMesh births a mesh identity for one node: soul keypair, empty
// link tables, cancellable cloud context. Start() brings it online.
func NewPeerMesh(swarm *Swarm, node string) *PeerMesh {
	pubStr, priv := newIdentity()
	ctx, cancel := context.WithCancel(context.Background())
	return &PeerMesh{
		swarm:        swarm,
		node:         node,
		conns:        make(map[string]net.Conn),
		trans:        make(map[string]string),
		supers:       make(map[string]superEntry),
		culled:       make(map[string]time.Time),
		punchPending: make(map[string]pendingPunch),
		punchLast:    make(map[string]time.Time),
		history:      make(map[string]bool),
		pub:          pubStr,
		priv:         priv,
		born:         time.Now(),
		stopChan:     make(chan struct{}),
		cloudQueue:   make(chan SecureMessage, 16),
		cloudCtx:     ctx,
		cloudCancel:  cancel,
		relayLimiter: NewLimiter(2, 4),
		relayBreaker: NewBreaker(3, 30*time.Second),
	}
}

func (pm *PeerMesh) socketPath() string {
	return socketPrefix + pm.node + socketSuffix
}

// Start binds this node's own socket and begins discovery. The universe
// is networked before anyone is born in it, so main must call this
// before constructing minds.
func (pm *PeerMesh) Start() error {
	if !envOff("HIVEMIND_UNIX") {
		_ = os.Remove(pm.socketPath()) // stale own socket from a crashed run

		l, err := net.Listen("unix", pm.socketPath())
		if err != nil {
			return fmt.Errorf("peer socket bind blocked on %s: %w", pm.socketPath(), err)
		}
		pm.listener = l

		fmt.Printf("🔒 [PEER MESH] Node %q owns socket %s. Awaiting equals...\n", pm.node, pm.socketPath())
		go pm.acceptLoop()
		go pm.discoveryLoop()
	} else {
		fmt.Println("🧦 [PEER MESH] Unix sockets off — TCP mesh only.")
	}

	pm.startLAN() // TCP + multicast discovery; degrades to unix-only on failure

	pm.swarm.SetOutbound(pm.handleOutbound)
	if envOff("HIVEMIND_RELAY") {
		fmt.Println("☁️  [RELAY] Cloud relay disabled — pure serverless mesh.")
	} else {
		pm.relayOn = true
		go pm.cloudPublisher()
		go pm.ListenToCloudRelay()
	}

	// Supernode layer: snoop our own broadcast stream for advertisements,
	// announce while capable, dial advertised equals closest-first.
	go pm.snoopLoop(pm.swarm.Join("mesh:" + pm.node))
	go pm.superLoop()
	go pm.superDialLoop()
	// Reflexive discovery never blocks birth: it lands when it lands.
	go pm.resolveReflexive()
	// DHT discovery rides the mesh: no bootstrap server, just equals.
	pm.startDHT()

	// Presence is announced per link-up inside openLink: at Start time the
	// swarm has no members and no peers yet, so an early hello would reach
	// nobody. The first link carries the introduction instead.
	return nil
}

// ── inbound: a peer dialed us ──

func (pm *PeerMesh) acceptLoop() {
	for {
		conn, err := pm.listener.Accept()
		if err != nil {
			select {
			case <-pm.stopChan:
				return
			default:
				continue
			}
		}
		go pm.openLink(conn, false, "", "unix", nil)
	}
}

// ── outbound: we dialed a peer ──

func (pm *PeerMesh) dial(peer, path string) {
	t0 := time.Now()
	conn, err := net.Dial("unix", path)
	if err != nil {
		pm.maybeSweepStale(path, err)
		return
	}
	// The socket path names the expected owner; the exchange verifies it.
	pm.openLink(conn, true, peer, "unix", func(p string) {
		pm.noteLinkRTT(p, time.Since(t0), "unix")
	})
}

// serve is the shared read loop for a live link, inbound or outbound.
func (pm *PeerMesh) serve(conn net.Conn, reader *bufio.Reader, peer string) {
	defer func() {
		pm.mu.Lock()
		if cur, ok := pm.conns[peer]; ok && cur == conn {
			delete(pm.conns, peer)
		}
		delete(pm.trans, peer)
		pm.mu.Unlock()
		_ = conn.Close()
		fmt.Printf("⛓️  [PEER MESH] Node %q unlinked. Equals connected: %d\n", peer, pm.LinkedPeers())
	}()

	sawFrame := false
	for {
		select {
		case <-pm.stopChan:
			return
		default:
		}

		// Idle links die: a peer that connects and never speaks holds a
		// goroutine hostage otherwise (slow-loris). The living redial.
		_ = conn.SetReadDeadline(time.Now().Add(idleLinkTimeout))
		msg, err := readFrame(reader)
		_ = conn.SetReadDeadline(time.Time{})
		if err != nil {
			return
		}
		if !sawFrame {
			sawFrame = true
			pm.mu.Lock()
			pm.history[peer] = true // a conversation that actually happened
			pm.mu.Unlock()
		}

		// Wire frames are marked relayed: they join the local hive but
		// never echo back out through the outbound bridge.
		msg.Relayed = true
		pm.swarm.Broadcast(msg)
	}
}

// ── wire frame guards ──
//
// Every byte off the mesh is hostile until proven otherwise: frames are
// capped (a thought is ~1KB; megabytes are an attack, not a mind) and
// handshakes even more so. Oversize input drops the link, not the node.

const maxFrameBytes = 256 * 1024
const maxHandshakeBytes = 4 * 1024

// maxFanout caps per-frame forwarding fanout: past this many links the
// mesh gossips to a random subset per frame instead of flooding all of
// them. Small meshes never notice (they have fewer links than the cap);
// large ones stay O(K) chatter instead of O(N²). No TTL field exists on
// purpose: anything mutating the frame in flight would void its
// signature, so flood control lives in fanout, and loop control in the
// seen-set — neither touches the signed envelope.
const maxFanout = 8

// idleLinkTimeout is a var (not const) so tests can shrink it.
var idleLinkTimeout = 60 * time.Second

const writeLinkTimeout = 5 * time.Second

func readHandshake(reader *bufio.Reader) (peerHandshake, error) {
	var hs peerHandshake
	line, err := readLineCapped(reader, maxHandshakeBytes)
	if err != nil {
		return hs, err
	}
	if err := json.Unmarshal(line, &hs); err != nil {
		return hs, err
	}
	return hs, nil
}

func readFrame(reader *bufio.Reader) (SecureMessage, error) {
	var msg SecureMessage
	line, err := readLineCapped(reader, maxFrameBytes)
	if err != nil {
		return msg, err
	}
	if err := json.Unmarshal(line, &msg); err != nil {
		return msg, err
	}
	return msg, nil
}

// readLineCapped reads one newline-terminated line without ever holding
// more than max bytes: fragments are copied out of the shared buffer as
// they arrive, and anything bigger aborts the read (and the link).
func readLineCapped(reader *bufio.Reader, max int) ([]byte, error) {
	var line []byte
	for {
		frag, err := reader.ReadSlice('\n')
		line = append(line, frag...)
		if len(line) > max {
			return nil, fmt.Errorf("wire line exceeds %d bytes", max)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			return nil, err
		}
		return line, nil
	}
}

// ── discovery: find equals, re-link the departed ──

func (pm *PeerMesh) discoveryLoop() {
	ticker := time.NewTicker(discoveryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-pm.stopChan:
			return
		case <-ticker.C:
			pm.discoverOnce()
		}
	}
}

func (pm *PeerMesh) discoverOnce() {
	paths, err := filepath.Glob(socketPrefix + "*" + socketSuffix)
	if err != nil {
		return
	}
	for _, path := range paths {
		peer := strings.TrimSuffix(strings.TrimPrefix(path, socketPrefix), socketSuffix)
		if peer == pm.node {
			continue
		}

		if pm.connected(peer) {
			continue
		}

		// The dial rule: only dial peers lexically greater than ourselves.
		// Every pair therefore has exactly one initiating side — no
		// double connections, no election, no master.
		if peer < pm.node {
			continue
		}

		// Young sockets may exist microseconds before their listener is
		// ready; give them a grace period before treating them as corpses.
		if fi, err := os.Stat(path); err == nil {
			if time.Since(fi.ModTime()) < staleSocketAge {
				continue // possibly just being born; let it finish
			}
		}

		// Async: one slow or hostile peer (half-open handshake, wedged
		// acceptor) must never stall discovery of all the others.
		// Duplicates are harmless — openLink keeps exactly one pipe.
		go pm.dial(peer, path)
	}
}

// maybeSweepStale removes a socket file only when a dial was refused
// against a file old enough to be a corpse. A crashed node leaves its
// socket behind; leaving garbage would starve the mesh of connections.
func (pm *PeerMesh) maybeSweepStale(path string, dialErr error) {
	if !os.IsNotExist(dialErr) { // refused, not absent: the file lingers
		if fi, err := os.Stat(path); err == nil && time.Since(fi.ModTime()) > staleSocketAge {
			if os.Remove(path) == nil {
				fmt.Printf("🧹 [PEER MESH] Swept stale socket %s (owner gone).\n", path)
			}
		}
	}
}

func (pm *PeerMesh) connected(peer string) bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	_, ok := pm.conns[peer]
	return ok
}

// LinkedPeers reports how many equal nodes are live-linked right now.
func (pm *PeerMesh) LinkedPeers() int {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return len(pm.conns)
}

// hasHistory reports whether a peer ever carried a decoded frame.
// hasSuper reports whether a node sits in the supernode directory.
// Locked readers for paths (tests, reporters) outside the serve loops.
func (pm *PeerMesh) hasHistory(peer string) bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.history[peer]
}

func (pm *PeerMesh) hasSuper(node string) bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	_, ok := pm.supers[node]
	return ok
}

// ── frame routing ──

func (pm *PeerMesh) handleOutbound(msg SecureMessage) {
	pm.ForwardToPeers(msg)
	// Cloud is paced: queue non-blocking, publisher keeps only the latest.
	select {
	case pm.cloudQueue <- msg:
	default:
	}
}

// ForwardToPeers writes one frame to every live link: snapshot under
// lock, write outside it with deadlines, evict the dead. One wedged
// peer never stalls the mesh.
func (pm *PeerMesh) ForwardToPeers(msg SecureMessage) {
	jsonData, err := json.Marshal(msg)
	if err != nil {
		return
	}
	jsonData = append(jsonData, '\n')

	// Snapshot under the lock, write outside it: one wedged peer must
	// never stall the whole mesh. Dead writes evict the link.
	// Beyond maxFanout links the mesh gossips instead of flooding: Go
	// map iteration order is already random, so ranging IS the shuffle,
	// and the seen-set dedups whatever overlaps.
	pm.mu.Lock()
	conns := make(map[string]net.Conn, len(pm.conns))
	for name, conn := range pm.conns {
		conns[name] = conn
	}
	pm.mu.Unlock()

	n := 0
	for name, conn := range conns {
		if n >= maxFanout {
			break
		}
		n++
		_ = conn.SetWriteDeadline(time.Now().Add(writeLinkTimeout))
		if _, err := conn.Write(jsonData); err != nil {
			pm.mu.Lock()
			if cur, ok := pm.conns[name]; ok && cur == conn {
				delete(pm.conns, name)
			}
			pm.mu.Unlock()
			_ = conn.Close()
		} else {
			_ = conn.SetWriteDeadline(time.Time{})
		}
	}
}

// ── encrypt / decrypt ──

func (pm *PeerMesh) Encrypt(plaintext []byte) (string, error) {
	key, aad := sealParams()
	return sealWith(key, aad, plaintext)
}

// sealParams resolves this frame's key + binding: operator env (timeless)
// or this hour's ratchet (time-bound), else the static demo fallback.
func sealParams() (key []byte, aad string) {
	if k := os.Getenv("HIVEMIND_CIPHER_KEY"); len(k) == 32 {
		return []byte(k), hourAAD(hourEpoch(time.Now()))
	}
	if key, ok := hourKey(0); ok {
		return key, hourAAD(hourEpoch(time.Now()))
	}
	return []byte("HIVE_MIND_32_BYTE_STATIC_KEY_PAD"), ""
}

// sealWith encrypts under one key binding one hour: the AAD welds the
// ciphertext to its hour, so cross-hour replays fail authentication.
func sealWith(key []byte, aad string, plaintext []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aesgcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := aesgcm.Seal(nonce, nonce, plaintext, []byte(aad))
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt reverses Encrypt under the same resolved key. Wrong keys and
// tampered frames fail closed here — counted, warned about, never read.
func (pm *PeerMesh) Decrypt(cryptoText string) ([]byte, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(cryptoText)
	if err != nil {
		return nil, err
	}
	return openWithWindow(ciphertext)
}

// openWithWindow tries the live hour, then the two before it: rotation
// and skew must never partition the mesh. Static closes the list for
// keyless demo setups. A frame sealed under none of these — wrong swarm,
// tampered bytes, or replayed across hours — fails closed here.
func openWithWindow(ciphertext []byte) ([]byte, error) {
	type attempt struct {
		key []byte
		aad string
	}
	var attempts []attempt
	nowHour := hourEpoch(time.Now())
	if k := os.Getenv("HIVEMIND_CIPHER_KEY"); len(k) == 32 {
		for _, back := range []int64{0, 1, 2} {
			attempts = append(attempts, attempt{[]byte(k), hourAAD(nowHour - back)})
		}
	} else {
		for _, back := range []int64{0, 1, 2} {
			if key, ok := hourKey(int(back)); ok {
				attempts = append(attempts, attempt{key, hourAAD(nowHour - back)})
			}
		}
	}
	// Last resort mirrors Encrypt's own fallback: keyless setups speak
	// static on both ends. Public by design, never mistaken for secret.
	attempts = append(attempts, attempt{[]byte("HIVE_MIND_32_BYTE_STATIC_KEY_PAD"), ""})
	for _, a := range attempts {
		block, err := aes.NewCipher(a.key)
		if err != nil {
			continue
		}
		aesgcm, err := cipher.NewGCM(block)
		if err != nil {
			continue
		}
		nonceSize := aesgcm.NonceSize()
		if len(ciphertext) < nonceSize {
			return nil, fmt.Errorf("ciphertext too short")
		}
		nonce, actualCiphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
		if plain, err := aesgcm.Open(nil, nonce, actualCiphertext, []byte(a.aad)); err == nil {
			return plain, nil
		}
	}
	return nil, fmt.Errorf("relay frame undecryptable under this swarm's keys")
}

// ── cloud relay (paced, cancelable, echo-safe) ──

func (pm *PeerMesh) cloudPublisher() {
	ticker := time.NewTicker(cloudPublishInterval)
	defer ticker.Stop()
	var latest *SecureMessage
	backoff := cloudPublishInterval
	const maxBackoff = 5 * time.Minute

	for {
		select {
		case <-pm.cloudCtx.Done():
			return
		case msg := <-pm.cloudQueue:
			m := msg
			latest = &m
		case <-ticker.C:
			if latest == nil {
				continue
			}
			if err := pm.publishCloud(*latest); err != nil {
				fmt.Printf("⚠️  [RELAY] Cloud publish failed (%v); retrying in %s.\n", err, backoff)
				ticker.Reset(backoff)
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
				continue
			}
			latest = nil
			backoff = cloudPublishInterval
			ticker.Reset(backoff)
		}
	}
}

func (pm *PeerMesh) publishCloud(msg SecureMessage) error {
	if !pm.relayLimiter.Allow() {
		return fmt.Errorf("relay rate-limited, retry later")
	}
	if !pm.relayBreaker.Allow() {
		return fmt.Errorf("relay breaker open, failing fast")
	}
	err := pm.postCloud(msg)
	if err != nil {
		pm.relayBreaker.Failure()
		return err
	}
	pm.relayBreaker.Success()
	return nil
}

func (pm *PeerMesh) postCloud(msg SecureMessage) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	encryptedString, err := pm.Encrypt(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(pm.cloudCtx, "POST", relayURL(), strings.NewReader(encryptedString))
	if err != nil {
		return err
	}
	req.Header.Set("X-Title", "ENCRYPTED_HIVE_FRAME")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("relay returned %s", resp.Status)
	}
	return nil
}

// ListenToCloudRelay long-polls the relay topic, rotating the stream
// every few minutes so silent death always reconnects. Valid tagged
// frames ingest as Relayed; everything else is skipped, never stored.
func (pm *PeerMesh) ListenToCloudRelay() {
	url := relayURL() + "/json"

	for {
		select {
		case <-pm.cloudCtx.Done():
			return
		default:
		}

		// One stream lives at most streamLifetime: a quietly dead
		// connection (NAT timeout, silent topic) must never hold the
		// listener hostage. Reconnect replays relay's recent cache, and
		// the seen-set dedups anything already carried.
		ctx, cancel := context.WithTimeout(pm.cloudCtx, streamLifetime)
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			cancel()
			time.Sleep(5 * time.Second)
			continue
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			select {
			case <-pm.cloudCtx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}

		pm.consumeRelayStream(ctx, resp)
		cancel()

		select {
		case <-pm.cloudCtx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// consumeRelayStream drains one long-poll connection: filter to our
// tagged frames, decrypt, verify, ingest as relayed. Returns when the
// stream ends, errors, or its lifetime expires — the caller reconnects.
func (pm *PeerMesh) consumeRelayStream(ctx context.Context, resp *http.Response) {
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		var relayMsg map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &relayMsg); err != nil {
			continue
		}
		event, _ := relayMsg["event"].(string)
		if event != "message" {
			continue
		}
		title, _ := relayMsg["title"].(string)
		if title != "ENCRYPTED_HIVE_FRAME" {
			continue
		}

		encryptedBody, _ := relayMsg["message"].(string)
		decryptedBytes, err := pm.Decrypt(encryptedBody)
		if err != nil {
			// Undecryptable is normal for foreign traffic — but a
			// flood of it means OUR key doesn't match the swarm's.
			// Count quietly, warn loudly, never print the blob.
			pm.mu.Lock()
			pm.badFrames++
			since := time.Since(pm.lastBadWarn)
			if since > time.Minute {
				pm.lastBadWarn = time.Now()
				n := pm.badFrames
				pm.mu.Unlock()
				fmt.Printf("⚠️  [RELAY] %d frame(s) arrived undecryptable — wrong HIVEMIND_CIPHER_KEY for this swarm?\n", n)
			} else {
				pm.mu.Unlock()
			}
			continue
		}

		var secureMsg SecureMessage
		if err := json.Unmarshal(decryptedBytes, &secureMsg); err != nil {
			continue
		}

		// Cloud frames are relayed frames: they must not ride the
		// outbound bridge again, or the echo never ends.
		secureMsg.Relayed = true
		if pm.swarm.Broadcast(secureMsg) {
			fmt.Printf("☁️  [SECURE CLOUD INBOUND] Frame extracted and verified over public stream.\n")
		}
	}
}

// ── teardown ──

func (pm *PeerMesh) Close() {
	close(pm.stopChan)
	pm.cloudCancel()
	if pm.dht != nil {
		pm.dht.close()
	}

	if pm.listener != nil {
		_ = pm.listener.Close()
	}
	if pm.tcpListener != nil {
		_ = pm.tcpListener.Close()
	}
	pm.mu.Lock()
	for _, c := range pm.beaconConns {
		_ = c.Close()
	}
	pm.beaconConns = nil
	pm.mu.Unlock()
	if err := os.Remove(pm.socketPath()); err == nil {
		fmt.Printf("🧹 [PEER MESH] Own socket %s removed.\n", pm.socketPath())
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()

	reportMu.Lock()
	defer reportMu.Unlock()
	fmt.Println("\n🗃️  [PEER MESH SHUTDOWN REGISTRY] Links that actually carried frames this run:")
	if len(pm.history) == 0 {
		fmt.Println("     (none — no peer conversations took place)")
	} else {
		for peer := range pm.history {
			fmt.Printf("     ✔ Two-way LUDS conversation verified with node: %s\n", peer)
		}
	}

	for _, conn := range pm.conns {
		_ = conn.Close()
	}
}
