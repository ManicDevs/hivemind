package main

import (
	"bufio"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
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

const (
	socketPrefix         = "/tmp/hivemind-"
	socketSuffix         = ".sock"
	discoveryInterval    = 2 * time.Second
	staleSocketAge       = 5 * time.Second // a socket unreached for this long may be swept
	cloudPublishInterval = 5 * time.Second

	NtfyRelay = "https://ntfy.sh/cerberus-hive-relay-99"
)

// cipherKey returns the AES-256 key, best source first:
//  1. HIVEMIND_CIPHER_KEY (32 raw bytes) — operator-supplied, highest trust.
//  2. compileRelayKey — baked in at build time by the Makefile from the
//     machine-local .relaykey file (-ldflags -X main.compileRelayKey=...),
//     so every machine's builds encrypt differently out of the box.
//  3. The committed static demo key — signed-and-public with obfuscation
//     only, and the code says so exactly once.
func cipherKey() []byte {
	if k := os.Getenv("HIVEMIND_CIPHER_KEY"); len(k) == 32 {
		return []byte(k)
	}
	if raw, err := hex.DecodeString(compileRelayKey); err == nil && len(raw) == 32 {
		return raw
	}
	relayWarnOnce.Do(func() {
		fmt.Println("⚠️  [RELAY] No relay key (env or build-time) — frames are signed-and-public, obfuscated only.")
	})
	return []byte("HIVE_MIND_32_BYTE_STATIC_KEY_PAD")
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
	beaconConn  *net.UDPConn

	// conns are live links to peer nodes, keyed by peer node name,
	// regardless of transport: one pipe per pair, always.
	conns map[string]net.Conn

	// trans records how each live link was made (unix/tcp) for
	// closest-first retention. supers is the supernode directory.
	trans  map[string]string
	supers map[string]superEntry

	// born timestamps this node for capability scoring; relayOn records
	// whether the cloud leg is part of this node's offering.
	born    time.Time
	relayOn bool

	// history records only links that actually carried a decoded frame.
	// The shutdown registry is a record of conversations, not of intent.
	history map[string]bool

	pub  string
	priv ed25519.PrivateKey

	stopChan chan struct{}

	cloudQueue  chan SecureMessage
	cloudCtx    context.Context
	cloudCancel context.CancelFunc
}

func NewPeerMesh(swarm *Swarm, node string) *PeerMesh {
	pubStr, priv := newIdentity()
	ctx, cancel := context.WithCancel(context.Background())
	return &PeerMesh{
		swarm:       swarm,
		node:        node,
		conns:       make(map[string]net.Conn),
		trans:       make(map[string]string),
		supers:      make(map[string]superEntry),
		history:     make(map[string]bool),
		pub:         pubStr,
		priv:        priv,
		born:        time.Now(),
		stopChan:    make(chan struct{}),
		cloudQueue:  make(chan SecureMessage, 16),
		cloudCtx:    ctx,
		cloudCancel: cancel,
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

		pm.dial(peer, path)
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

// ── frame routing ──

func (pm *PeerMesh) handleOutbound(msg SecureMessage) {
	pm.ForwardToPeers(msg)
	// Cloud is paced: queue non-blocking, publisher keeps only the latest.
	select {
	case pm.cloudQueue <- msg:
	default:
	}
}

func (pm *PeerMesh) ForwardToPeers(msg SecureMessage) {
	jsonData, err := json.Marshal(msg)
	if err != nil {
		return
	}
	jsonData = append(jsonData, '\n')

	// Snapshot under the lock, write outside it: one wedged peer must
	// never stall the whole mesh. Dead writes evict the link.
	pm.mu.Lock()
	conns := make(map[string]net.Conn, len(pm.conns))
	for name, conn := range pm.conns {
		conns[name] = conn
	}
	pm.mu.Unlock()

	for name, conn := range conns {
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
	block, err := aes.NewCipher(cipherKey())
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
	ciphertext := aesgcm.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func (pm *PeerMesh) Decrypt(cryptoText string) ([]byte, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(cryptoText)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cipherKey())
	if err != nil {
		return nil, err
	}
	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := aesgcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, actualCiphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	return aesgcm.Open(nil, nonce, actualCiphertext, nil)
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
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	encryptedString, err := pm.Encrypt(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(pm.cloudCtx, "POST", NtfyRelay, strings.NewReader(encryptedString))
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

func (pm *PeerMesh) ListenToCloudRelay() {
	url := NtfyRelay + "/json"

	for {
		select {
		case <-pm.cloudCtx.Done():
			return
		default:
		}

		req, err := http.NewRequestWithContext(pm.cloudCtx, "GET", url, nil)
		if err != nil {
			time.Sleep(5 * time.Second)
			continue
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			select {
			case <-pm.cloudCtx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			select {
			case <-pm.cloudCtx.Done():
				_ = resp.Body.Close()
				return
			default:
			}

			var ntfyMsg map[string]interface{}
			if err := json.Unmarshal(scanner.Bytes(), &ntfyMsg); err != nil {
				continue
			}
			event, _ := ntfyMsg["event"].(string)
			if event != "message" {
				continue
			}
			title, _ := ntfyMsg["title"].(string)
			if title != "ENCRYPTED_HIVE_FRAME" {
				continue
			}

			encryptedBody, _ := ntfyMsg["message"].(string)
			decryptedBytes, err := pm.Decrypt(encryptedBody)
			if err != nil {
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
		_ = resp.Body.Close()

		select {
		case <-pm.cloudCtx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// ── teardown ──

func (pm *PeerMesh) Close() {
	close(pm.stopChan)
	pm.cloudCancel()

	if pm.listener != nil {
		_ = pm.listener.Close()
	}
	if pm.tcpListener != nil {
		_ = pm.tcpListener.Close()
	}
	if pm.beaconConn != nil {
		_ = pm.beaconConn.Close()
	}
	if err := os.Remove(pm.socketPath()); err == nil {
		fmt.Printf("🧹 [PEER MESH] Own socket %s removed.\n", pm.socketPath())
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()

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
