package hivemind

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ── Kademlia-lite DHT (UDP, standard library only) ────────────────────
// Decentralized peer discovery with no registry and no bootstrap server
// baked in: node IDs are SHA-256 of soul public keys, k-buckets route by
// XOR distance, and endpoint records (node → dialable address) replicate
// across the closest holders. The mesh bootstraps it from already-trusted
// links; from there it discovers on its own.

const (
	dhtK           = 8
	dhtAlpha       = 3
	dhtRPCTimeout  = 3 * time.Second
	dhtRecordTTL   = 10 * time.Minute
	dhtBucketCount = 256
)

// dhtID is a 256-bit node identifier: SHA-256 of the hex soul public key.
type dhtID [32]byte

func dhtIDFromPubKey(pubHex string) dhtID {
	raw, err := hex.DecodeString(pubHex)
	if err != nil || len(raw) == 0 {
		sum := sha256.Sum256([]byte(pubHex))
		return sum
	}
	return sha256.Sum256(raw)
}

func (id dhtID) hex() string { return hex.EncodeToString(id[:]) }

// xorDist returns the XOR distance bucket index: higher means farther.
// Standard Kademlia: bucket i holds distances in [2^i, 2^(i+1)).
func xorBucket(a, b dhtID) int {
	for i := 0; i < 32; i++ {
		x := a[i] ^ b[i]
		if x != 0 {
			for bit := 7; bit >= 0; bit-- {
				if x&(1<<uint(bit)) != 0 {
					return i*8 + (7 - bit)
				}
			}
		}
	}
	return -1 // identical
}

// dhtPeer is one known node: who, where (UDP + TCP), when proven alive.
type dhtPeer struct {
	ID       dhtID
	UDP      *net.UDPAddr
	TCP      int
	LastSeen time.Time
}

// dhtRecord is a stored endpoint advertisement with a lease.
type dhtRecord struct {
	Val     string // "host:tcpPort" dialable address
	Expires time.Time
}

// dhtRPC is the wire envelope. Types: ping, pong, find, nodes, store,
// stored, findval, value. Peers lists carry (ID, UDP, TCP) triples. Tx
// correlates responses to requests (see request): responses without a
// matching live request are dropped, never processed.
type dhtRPC struct {
	Type   string        `json:"t"`
	Tx     string        `json:"tx,omitempty"`
	From   string        `json:"from"` // hex node ID
	TCP    int           `json:"tcp,omitempty"`
	Target string        `json:"target,omitempty"` // hex node ID
	Key    string        `json:"key,omitempty"`
	Val    string        `json:"val,omitempty"`
	Peers  []dhtWirePeer `json:"peers,omitempty"`
}

type dhtWirePeer struct {
	ID  string `json:"id"`
	UDP string `json:"udp"`
	TCP int    `json:"tcp"`
}

// dhtNode is one DHT participant.
type dhtNode struct {
	mu      sync.Mutex
	self    dhtID
	tcpPort int
	conn    *net.UDPConn
	buckets [dhtBucketCount][]dhtPeer
	store   map[string]dhtRecord
	stop    chan struct{}

	// pending correlates responses to live requests by Tx. Guarded by
	// pmu (never d.mu: serve holds d.mu while filing, and delivery
	// must never wedge behind table maintenance).
	pmu     sync.Mutex
	pending map[string]chan dhtRPC
}

// newDHT binds a UDP socket (port 0 = ephemeral) and starts serving.
func newDHT(selfHexPubKey string, tcpPort, udpPort int) (*dhtNode, error) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("0.0.0.0"), Port: udpPort})
	if err != nil {
		return nil, fmt.Errorf("dht bind: %w", err)
	}
	d := &dhtNode{
		self:    dhtIDFromPubKey(selfHexPubKey),
		tcpPort: tcpPort,
		conn:    conn,
		store:   make(map[string]dhtRecord),
		pending: make(map[string]chan dhtRPC),
		stop:    make(chan struct{}),
	}
	go d.serve()
	return d, nil
}

// udpAddr returns our bound socket address for bootstrapping others.
func (d *dhtNode) udpAddr() *net.UDPAddr {
	if a, ok := d.conn.LocalAddr().(*net.UDPAddr); ok {
		return a
	}
	return nil
}

func (d *dhtNode) close() {
	select {
	case <-d.stop:
	default:
		close(d.stop)
	}
	_ = d.conn.Close()
}

// notePeer inserts or refreshes a peer in its k-bucket, evicting the
// stalest entry when full. Self and empty addresses never enter.
func (d *dhtNode) notePeer(id dhtID, udp *net.UDPAddr, tcp int) {
	if id == d.self || udp == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	b := xorBucket(d.self, id)
	if b < 0 || b >= dhtBucketCount {
		return
	}
	bucket := d.buckets[b]
	for i, p := range bucket {
		if p.ID == id {
			bucket[i].UDP = udp
			bucket[i].TCP = tcp
			bucket[i].LastSeen = time.Now()
			d.buckets[b] = bucket
			return
		}
	}
	d.buckets[b] = append(bucket, dhtPeer{ID: id, UDP: udp, TCP: tcp, LastSeen: time.Now()})
	if len(d.buckets[b]) > dhtK {
		// Evict the stalest: least-recently-seen sorts last.
		sort.SliceStable(d.buckets[b], func(i, j int) bool {
			return d.buckets[b][i].LastSeen.After(d.buckets[b][j].LastSeen)
		})
		d.buckets[b] = d.buckets[b][:dhtK]
	}
}

// closest returns up to k known peers nearest to target, skipping one
// excluded ID (self on lookup, the requester on find). The target itself
// is never excluded — lookup must be able to return exactly what it seeks.
func (d *dhtNode) closest(target dhtID, k int, exclude dhtID) []dhtPeer {
	d.mu.Lock()
	defer d.mu.Unlock()
	var all []dhtPeer
	seen := map[dhtID]bool{exclude: true}
	for _, bucket := range d.buckets {
		for _, p := range bucket {
			if !seen[p.ID] && time.Since(p.LastSeen) < dhtRecordTTL {
				seen[p.ID] = true
				all = append(all, p)
			}
		}
	}
	sort.Slice(all, func(i, j int) bool {
		return dhtLess(all[i].ID, all[j].ID, target)
	})
	if len(all) > k {
		all = all[:k]
	}
	return all
}

// dhtLess orders IDs by XOR distance to target.
func dhtLess(a, b, target dhtID) bool {
	for i := 0; i < 32; i++ {
		xa, xb := a[i]^target[i], b[i]^target[i]
		if xa != xb {
			return xa < xb
		}
	}
	return false
}

// peerCount counts live routing entries (for tests and telemetry).
func (d *dhtNode) peerCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, bucket := range d.buckets {
		for _, p := range bucket {
			if time.Since(p.LastSeen) < dhtRecordTTL {
				n++
			}
		}
	}
	return n
}

// ── RPC transport ──

func (d *dhtNode) send(to *net.UDPAddr, msg dhtRPC) error {
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = d.conn.WriteToUDP(raw, to)
	return err
}

func (d *dhtNode) serve() {
	buf := make([]byte, 4096)
	for {
		n, src, err := d.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-d.stop:
				return
			default:
				continue
			}
		}
		var msg dhtRPC
		if err := json.Unmarshal(buf[:n], &msg); err != nil || msg.From == "" {
			continue // garbage or anonymous: not a peer
		}
		var from dhtID
		if raw, err := hex.DecodeString(msg.From); err != nil || len(raw) != 32 {
			continue
		} else {
			copy(from[:], raw)
		}
		if from == d.self {
			continue // our own echo, not a peer
		}
		// Responses route to the live request waiting for this Tx.
		// Anything uncorrelated is late, forged, or stray: drop it.
		switch msg.Type {
		case "pong", "nodes", "stored", "value":
			d.pmu.Lock()
			ch, ok := d.pending[msg.Tx]
			if ok {
				delete(d.pending, msg.Tx)
			}
			d.pmu.Unlock()
			if ok {
				select {
				case ch <- msg:
				default:
				}
			}
			// A response still proves liveness: file the sender.
			d.notePeer(from, src, msg.TCP)
			continue
		}
		// Every valid request proves liveness: file the sender first.
		d.notePeer(from, src, msg.TCP)
		d.handle(msg, src, from)
	}
}

func (d *dhtNode) handle(msg dhtRPC, src *net.UDPAddr, from dhtID) {
	switch msg.Type {
	case "ping":
		_ = d.send(src, dhtRPC{Type: "pong", Tx: msg.Tx, From: d.self.hex(), TCP: d.tcpPort})
	case "find":
		var target dhtID
		if raw, err := hex.DecodeString(msg.Target); err == nil && len(raw) == 32 {
			copy(target[:], raw)
		} else {
			return
		}
		var out []dhtWirePeer
		for _, p := range d.closest(target, dhtK, from) {
			out = append(out, dhtWirePeer{ID: p.ID.hex(), UDP: p.UDP.String(), TCP: p.TCP})
		}
		_ = d.send(src, dhtRPC{Type: "nodes", Tx: msg.Tx, From: d.self.hex(), TCP: d.tcpPort, Peers: out})
	case "store":
		if msg.Key == "" || msg.Val == "" {
			return
		}
		d.mu.Lock()
		if len(d.store) < 1024 {
			d.store[msg.Key] = dhtRecord{Val: msg.Val, Expires: time.Now().Add(dhtRecordTTL)}
		}
		d.mu.Unlock()
		_ = d.send(src, dhtRPC{Type: "stored", Tx: msg.Tx, From: d.self.hex(), TCP: d.tcpPort})
	case "findval":
		d.mu.Lock()
		rec, ok := d.store[msg.Key]
		if ok && time.Now().After(rec.Expires) {
			delete(d.store, msg.Key)
			ok = false
		}
		d.mu.Unlock()
		if ok {
			_ = d.send(src, dhtRPC{Type: "value", Tx: msg.Tx, From: d.self.hex(), TCP: d.tcpPort, Key: msg.Key, Val: rec.Val})
			return
		}
		var target dhtID
		if raw, err := hex.DecodeString(msg.Key); err == nil && len(raw) == 32 {
			copy(target[:], raw)
		} else {
			return
		}
		var out []dhtWirePeer
		for _, p := range d.closest(target, dhtK, from) {
			out = append(out, dhtWirePeer{ID: p.ID.hex(), UDP: p.UDP.String(), TCP: p.TCP})
		}
		_ = d.send(src, dhtRPC{Type: "nodes", Tx: msg.Tx, From: d.self.hex(), TCP: d.tcpPort, Peers: out})
	}
}

// ── client: ping, lookup, endpoint store ──

// request performs one RPC round trip over the main socket (so our
// source address is the stable one peers file, never a throwaway):
// stamp a Tx, register the waiter, send, await or time out.
func (d *dhtNode) request(to *net.UDPAddr, msg dhtRPC) (*dhtRPC, error) {
	tx := make([]byte, 8)
	if _, err := rand.Read(tx); err != nil {
		return nil, err
	}
	msg.Tx = hex.EncodeToString(tx)

	wait := make(chan dhtRPC, 1)
	d.pmu.Lock()
	d.pending[msg.Tx] = wait
	d.pmu.Unlock()
	defer func() {
		d.pmu.Lock()
		delete(d.pending, msg.Tx)
		d.pmu.Unlock()
	}()

	raw, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	if _, err := d.conn.WriteToUDP(raw, to); err != nil {
		return nil, err
	}
	select {
	case resp := <-wait:
		r := resp
		return &r, nil
	case <-time.After(dhtRPCTimeout):
		return nil, fmt.Errorf("dht timeout waiting for %q", msg.Type)
	case <-d.stop:
		return nil, fmt.Errorf("dht closed")
	}
}

// Ping proves a peer alive and files it on success.
func (d *dhtNode) Ping(to *net.UDPAddr) error {
	resp, err := d.request(to, dhtRPC{Type: "ping", From: d.self.hex(), TCP: d.tcpPort})
	if err != nil {
		return err
	}
	if resp.Type != "pong" {
		return fmt.Errorf("unexpected %q", resp.Type)
	}
	var id dhtID
	if raw, err := hex.DecodeString(resp.From); err == nil && len(raw) == 32 {
		copy(id[:], raw)
		d.notePeer(id, to, resp.TCP)
	}
	return nil
}

// Lookup performs iterative Kademlia lookup: ask alpha closest, fold in
// their answers, repeat until nobody new appears. Returns closest known.
func (d *dhtNode) Lookup(targetHex string) []dhtPeer {
	var target dhtID
	if raw, err := hex.DecodeString(targetHex); err != nil || len(raw) != 32 {
		return nil
	} else {
		copy(target[:], raw)
	}
	queried := map[dhtID]bool{d.self: true}
	for round := 0; round < 8; round++ {
		cands := d.closest(target, dhtAlpha, d.self)
		var fresh []dhtPeer
		for _, c := range cands {
			if !queried[c.ID] {
				queried[c.ID] = true
				fresh = append(fresh, c)
			}
		}
		if len(fresh) == 0 {
			break
		}
		for _, f := range fresh {
			resp, err := d.request(f.UDP, dhtRPC{Type: "find", From: d.self.hex(), TCP: d.tcpPort, Target: targetHex})
			if err != nil {
				continue
			}
			for _, wp := range resp.Peers {
				var id dhtID
				raw, err := hex.DecodeString(wp.ID)
				if err != nil || len(raw) != 32 {
					continue
				}
				copy(id[:], raw)
				udp, err := net.ResolveUDPAddr("udp", wp.UDP)
				if err != nil {
					continue
				}
				d.notePeer(id, udp, wp.TCP)
			}
		}
	}
	return d.closest(target, dhtK, d.self)
}

// StoreEndpoint replicates our dialable address at the k closest holders.
func (d *dhtNode) StoreEndpoint(nodeHex, addr string) {
	holders := d.Lookup(nodeHex)
	if len(holders) == 0 {
		return
	}
	for _, h := range holders {
		_, _ = d.request(h.UDP, dhtRPC{Type: "store", From: d.self.hex(), TCP: d.tcpPort, Key: nodeHex, Val: addr})
	}
}

// FindEndpoint retrieves a dialable address for a node ID, asking the
// mesh when it isn't held locally.
func (d *dhtNode) FindEndpoint(nodeHex string) (string, bool) {
	d.mu.Lock()
	if rec, ok := d.store[nodeHex]; ok && time.Now().Before(rec.Expires) {
		val := rec.Val
		d.mu.Unlock()
		return val, true
	}
	d.mu.Unlock()
	for _, h := range d.Lookup(nodeHex) {
		resp, err := d.request(h.UDP, dhtRPC{Type: "findval", From: d.self.hex(), TCP: d.tcpPort, Key: nodeHex})
		if err != nil || resp == nil {
			continue
		}
		if resp.Type == "value" && resp.Val != "" {
			return resp.Val, true
		}
	}
	return "", false
}

// ── mesh integration ──

// startDHT brings up this node's DHT endpoint (HIVEMIND_DHT_PORT, or
// ephemeral) unless disabled. Failure degrades to mesh-only discovery;
// the hive never refuses birth over a discovery helper.
func (pm *PeerMesh) startDHT() {
	if envOff("HIVEMIND_DHT") {
		return
	}
	port := 0
	if p := strings.TrimSpace(os.Getenv("HIVEMIND_DHT_PORT")); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 && n < 65536 {
			port = n
		}
	}
	d, err := newDHT(pm.pub, pm.tcpPort, port)
	if err != nil {
		fmt.Printf("⚠️  [PEER MESH] DHT unavailable (%v) — mesh discovery only.\n", err)
		return
	}
	pm.dht = d
	if a := d.udpAddr(); a != nil {
		fmt.Printf("🔍 [PEER MESH] DHT listening %s for node %q.\n", a, pm.node)
	}
	go pm.endpointPublishLoop()
}

// dhtPort reports our DHT UDP port, or 0 when the DHT is off.
func (pm *PeerMesh) dhtPort() int {
	if pm.dht == nil {
		return 0
	}
	if a := pm.dht.udpAddr(); a != nil {
		return a.Port
	}
	return 0
}

// dhtPing joins the DHT through one learned address. Best effort:
// unreachable holders simply never answer.
func (pm *PeerMesh) dhtPing(host string, port int) {
	if pm.dht == nil || host == "" || port <= 0 {
		return
	}
	addr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return
	}
	go func() {
		_ = pm.dht.Ping(addr)
	}()
}

// endpointPublishLoop replicates our dialable address in the DHT once a
// minute so far nodes can find us without any introducer. Silence when
// we have nothing dialable to say.
func (pm *PeerMesh) endpointPublishLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-pm.stopChan:
			return
		case <-ticker.C:
			if pm.dht == nil {
				return
			}
			if addr := pm.advertiseAddr(); addr != "" {
				pm.dht.StoreEndpoint(dhtIDFromPubKey(pm.pub).hex(), addr)
			}
		}
	}
}
