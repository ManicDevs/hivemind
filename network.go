package main

import (
	"bufio"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
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
	socketPrefix       = "/tmp/hivemind-"
	socketSuffix       = ".sock"
	discoveryInterval  = 2 * time.Second
	staleSocketAge     = 5 * time.Second  // a socket unreached for this long may be swept
	cloudPublishInterval = 5 * time.Second

	NtfyRelay = "https://ntfy.sh/cerberus-hive-relay-99"
)

// cipherKey returns the AES-256 key. With HIVEMIND_CIPHER_KEY set, relay
// frames are actually confidential; without it they are signed-and-public
// with AES obfuscation — and we say so.
func cipherKey() []byte {
	if k := os.Getenv("HIVEMIND_CIPHER_KEY"); len(k) == 32 {
		return []byte(k)
	}
	fmt.Println("⚠️  [RELAY] HIVEMIND_CIPHER_KEY unset — relay frames are signed-and-public, obfuscated only.")
	return []byte("HIVE_MIND_32_BYTE_STATIC_KEY_PAD")
}

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

// PeerMesh is the symmetric LUDS network. Every node owns its own socket
// file; nodes discover each other and dial directly; each connection is a
// private two-way pipe. No listener is privileged — one file per node is
// the whole trick, because the filesystem path is the address.
type PeerMesh struct {
	mu       sync.Mutex
	swarm    *Swarm
	node     string // this node's name; also names its socket and soul dir
	listener net.Listener

	// conns are live links to peer nodes, keyed by peer node name.
	conns map[string]net.Conn

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
		history:     make(map[string]bool),
		pub:         pubStr,
		priv:        priv,
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
	_ = os.Remove(pm.socketPath()) // stale own socket from a crashed run

	l, err := net.Listen("unix", pm.socketPath())
	if err != nil {
		return fmt.Errorf("peer socket bind blocked on %s: %w", pm.socketPath(), err)
	}
	pm.listener = l

	fmt.Printf("🔒 [PEER MESH] Node %q owns socket %s. Awaiting equals...\n", pm.node, pm.socketPath())

	pm.swarm.SetOutbound(pm.handleOutbound)
	go pm.acceptLoop()
	go pm.discoveryLoop()
	go pm.cloudPublisher()
	go pm.ListenToCloudRelay()

	// Announce ourselves locally; the outbound bridge carries it to any
	// peers as they link up. It is a fully mined + signed frame.
	msg := MineMessage(pm.swarm, pm.priv, pm.pub, "hello", "PEER_HANDSHAKE:"+pm.node, []float64{0.0, 1.0, 9.81, -0.15})
	pm.swarm.Broadcast(msg)
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
		go pm.handleInbound(conn)
	}
}

func (pm *PeerMesh) handleInbound(conn net.Conn) {
	reader := bufio.NewReader(conn)
	decoder := json.NewDecoder(reader)

	var hs peerHandshake
	if err := decoder.Decode(&hs); err != nil || hs.Node == "" || hs.Node == pm.node {
		_ = conn.Close()
		return
	}

	pm.mu.Lock()
	if existing, ok := pm.conns[hs.Node]; ok {
		// A duplicate link (both sides raced the dial rule). Keep the
		// registered one, drop the newcomer — one pipe per pair, always.
		pm.mu.Unlock()
		_ = conn.Close()
		_ = existing.SetDeadline(time.Now()) // nudge the old one to prove it lives
		return
	}
	pm.conns[hs.Node] = conn
	hasHistory := pm.history[hs.Node]
	pm.mu.Unlock()

	if !hasHistory {
		fmt.Printf("🔗 [PEER MESH] Node %q linked inbound. Equals connected: %d\n", hs.Node, pm.LinkedPeers())
	}

	pm.serve(conn, reader, decoder, hs.Node)
}

// ── outbound: we dialed a peer ──

func (pm *PeerMesh) dial(peer, path string) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		pm.maybeSweepStale(path, err)
		return
	}

	hs, _ := json.Marshal(peerHandshake{Node: pm.node})
	if _, err := conn.Write(append(hs, '\n')); err != nil {
		_ = conn.Close()
		return
	}

	pm.mu.Lock()
	if _, ok := pm.conns[peer]; ok {
		pm.mu.Unlock()
		_ = conn.Close() // duplicate race; keep the existing link
		return
	}
	pm.conns[peer] = conn
	pm.mu.Unlock()

	fmt.Printf("🔗 [PEER MESH] Linked outbound to node %q. Equals connected: %d\n", peer, pm.LinkedPeers())
	pm.serve(conn, bufio.NewReader(conn), json.NewDecoder(conn), peer)
}

// serve is the shared read loop for a live link, inbound or outbound.
func (pm *PeerMesh) serve(conn net.Conn, reader *bufio.Reader, decoder *json.Decoder, peer string) {
	defer func() {
		pm.mu.Lock()
		if cur, ok := pm.conns[peer]; ok && cur == conn {
			delete(pm.conns, peer)
		}
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

		var msg SecureMessage
		if err := decoder.Decode(&msg); err != nil {
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

	pm.mu.Lock()
	defer pm.mu.Unlock()
	for _, conn := range pm.conns {
		_, _ = conn.Write(jsonData)
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
				fmt.Printf("⚠️  [RELAY] Cloud publish failed: %v\n", err)
			}
			latest = nil
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

