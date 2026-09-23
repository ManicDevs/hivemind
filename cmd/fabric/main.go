// Package main implements a 4-layer adaptive neural routing fabric.
// Local default: 2 continents × 3 tiers (6 nodes: master, controller,
// superpeer — no edges). Physical topology: FABRIC_ALLOW_WIDE=1
// FABRIC_CONTINENTS=7 FABRIC_TIERS=4 (28 nodes).
//
// Pure Go (CGO_ENABLED=0). Standard library + golang.org/x/crypto only.
// No external clouds, no central middlemen: every hop is a direct mTLS
// stream over loopback.
//
// Build:
//
//	CGO_ENABLED=0 go build -o bin/fabric ./cmd/fabric
//
// Run one node (identity from environment):
//
//	ENV_CONTINENT=1 ENV_TIER=4 ./bin/fabric   # NA Edge
//	ENV_CONTINENT=3 ENV_TIER=1 ./bin/fabric   # EU Master
//
// Optional:
//
//	FABRIC_ROOT_SEED=<128 hex chars>   # 64-byte pre-shared root (all nodes)
//	FABRIC_METRICS=1                   # periodic weight/loss dump
//	FABRIC_CONTINENTS=2                # active continents 1..7 (local clamped to 2)
//	FABRIC_TIERS=3                     # active tiers 1..3 (master/ctrl/superpeer)
//	FABRIC_ALLOW_WIDE=1                # required to exceed 2 continents (servers only)
package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"math/big"
	mrand "math/rand"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
)

// ─────────────────────────────────────────────────────────────────────────────
// Constants & cell format
// ─────────────────────────────────────────────────────────────────────────────

const (
	// MatrixPort is the single TCP port every node binds on its loopback IP.
	MatrixPort = 8883

	// Tiers: 1=Master, 2=Controller, 3=SuperPeer, 4=Edge
	tierMaster = 1
	tierCtrl   = 2
	tierSuper  = 3
	tierEdge   = 4

	// Continents 1..7
	contNA = 1 // North America
	contSA = 2 // South America
	contEU = 3 // Europe
	contAF = 4 // Africa
	contAS = 5 // Asia
	contOC = 6 // Australia
	contAN = 7 // Antarctica

	// Frame types inside the plaintext cell.
	frameData     = 0x01
	frameFeedback = 0x02
	frameProbe    = 0x03

	// Header layout (variable-length plaintext frame):
	//   [0]      type
	//   [1]      flags
	//   [2:4]    streamID   uint16
	//   [4:8]    sequence   uint32
	//   [8]      srcCont    uint8
	//   [9]      srcTier    uint8
	//   [10]     dstCont    uint8
	//   [11]     dstTier    uint8
	//   [12:20]  latencyUs  int64   (microseconds, EWMA at sender)
	//   [20:28]  dropRate   float64 (0..1)
	//   [28:36]  loss       float64 (LatencyMs + DropRate*100)
	//   [36:40]  payloadLen uint32
	//   [40:42]  prevCont / prevTier  (feedback reverse path)
	//   [42:64]  reserved
	//   [64:64+payloadLen] payload
	headerSize = 64

	// maxFrame is a sanity bound on a single decrypted frame.
	maxFrame = 1 << 20 // 1 MiB

	// Learning / weight bounds for gradient descent on SynapticWeight.
	weightInit    = 1.0
	weightMin     = 0.05
	weightMax     = 4.0
	learnRateUp   = 0.02
	learnRateDown = 0.05

	// Loss above this at a Master triggers a backward FEEDBACK frame.
	feedbackLossThreshold = 25.0

	// Default root seed (64 bytes) used when FABRIC_ROOT_SEED is unset.
	// All nodes MUST share the same seed in any real deployment.
	defaultRootSeedHex = "9f2c4e18a7b30d55c1e6f4a8b2d9073c" +
		"5e1a6f8b0c2d4e6f8a1b3c5d7e9f0a2b" +
		"4c6d8e0f2a4b6c8d0e2f4a6b8c0d2e4f" +
		"6a8b0c2d4e6f8a0b2c4d6e8f0a2b4c6d"
)

// ─────────────────────────────────────────────────────────────────────────────
// Active matrix (local default: 2 continents × 3 tiers = 6 nodes —
// 1 master + 1 controller + 1 superpeer per continent, no edges;
// raise FABRIC_CONTINENTS only on physical servers with FABRIC_ALLOW_WIDE=1)
// ─────────────────────────────────────────────────────────────────────────────

// NodeID uniquely identifies one fabric node.
type NodeID struct {
	Continent int // 1..7
	Tier      int // 1..4
}

func (n NodeID) String() string {
	return fmt.Sprintf("c%d.t%d", n.Continent, n.Tier)
}

func (n NodeID) index() int { return (n.Continent-1)*4 + (n.Tier - 1) }

// matrixAddr returns the explicit loopback host:port for a node.
func matrixAddr(continent, tier int) string {
	return fmt.Sprintf("127.%d.%d.%d:%d", continent, tier, 1, MatrixPort)
}

// active continents/tiers — local default is 2×4 (8 nodes). Opening more
// than 2 continents on one box requires FABRIC_ALLOW_WIDE=1 (physical
// servers only); without it the count is clamped to 2 so a typo cannot
// fork 28 processes on a dev machine.
var (
	activeContinents = loadContinents()
	activeTiers      = envClamp("FABRIC_TIERS", 3, 1, 4)
)

func loadContinents() int {
	n := envClamp("FABRIC_CONTINENTS", 2, 1, 7)
	if n > 2 && os.Getenv("FABRIC_ALLOW_WIDE") != "1" {
		log.Printf("fabric: FABRIC_CONTINENTS=%d clamped to 2 (dev single-box); set FABRIC_ALLOW_WIDE=1 only on physical servers", n)
		return 2
	}
	return n
}

func envClamp(name string, def, lo, hi int) int {
	v := envInt(name, def)
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// allNodes enumerates every node in the active fabric.
func allNodes() []NodeID {
	out := make([]NodeID, 0, activeContinents*activeTiers)
	for c := 1; c <= activeContinents; c++ {
		for t := 1; t <= activeTiers; t++ {
			out = append(out, NodeID{Continent: c, Tier: t})
		}
	}
	return out
}

// isActive reports whether id is in the active set.
func isActive(id NodeID) bool {
	return id.Continent >= 1 && id.Continent <= activeContinents &&
		id.Tier >= 1 && id.Tier <= activeTiers
}

// routeUp returns the vertical parent (tier-1) inside the same continent.
// Edge→SuperPeer→Controller→Master. Master has no parent.
func routeUp(id NodeID) (NodeID, bool) {
	if id.Tier <= tierMaster {
		return NodeID{}, false
	}
	return NodeID{Continent: id.Continent, Tier: id.Tier - 1}, true
}

// routePeers returns softmax candidate next-hops for forwarding DATA.
//
//	Tier 4 (Edge):      SuperPeer (same continent) + Controller (same) as backup
//	Tier 3 (SuperPeer): Controller (same) + SuperPeers on other continents
//	Tier 2 (Controller): Master (same) + Controllers on other continents
//	Tier 1 (Master):    all other Masters (inter-continent gateway mesh)
func routePeers(self NodeID) []NodeID {
	var out []NodeID
	add := func(n NodeID) {
		if isActive(n) {
			out = append(out, n)
		}
	}
	switch self.Tier {
	case tierEdge:
		add(NodeID{self.Continent, tierSuper})
		add(NodeID{self.Continent, tierCtrl})
	case tierSuper:
		add(NodeID{self.Continent, tierCtrl})
		for c := 1; c <= activeContinents; c++ {
			if c != self.Continent {
				add(NodeID{c, tierSuper})
			}
		}
	case tierCtrl:
		add(NodeID{self.Continent, tierMaster})
		for c := 1; c <= activeContinents; c++ {
			if c != self.Continent {
				add(NodeID{c, tierCtrl})
			}
		}
	case tierMaster:
		for c := 1; c <= activeContinents; c++ {
			if c != self.Continent {
				add(NodeID{c, tierMaster})
			}
		}
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// Environment / identity
// ─────────────────────────────────────────────────────────────────────────────

type identity struct {
	self  NodeID
	addr  string
	seed  [64]byte
	nodeK ed25519.PrivateKey
	nodeC *x509.Certificate
	caC   *x509.Certificate
	caK   ed25519.PrivateKey
	tlsC  tls.Certificate
	pool  *x509.CertPool
}

func envInt(name string, def int) int {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return def
	}
	return n
}

func loadRootSeed() ([64]byte, error) {
	var out [64]byte
	raw := strings.TrimSpace(os.Getenv("FABRIC_ROOT_SEED"))
	if raw == "" {
		raw = defaultRootSeedHex
	}
	b, err := hex.DecodeString(raw)
	if err != nil || len(b) != 64 {
		return out, fmt.Errorf("FABRIC_ROOT_SEED must be 64 bytes (128 hex chars), got %d bytes", len(b))
	}
	copy(out[:], b)
	return out, nil
}

func deriveEd25519(seed [64]byte, label string) ed25519.PrivateKey {
	h := sha256.New()
	h.Write(seed[:])
	h.Write([]byte(label))
	// Expand to ed25519.SeedSize via HKDF-like rehash.
	key := make([]byte, ed25519.SeedSize)
	t := h.Sum(nil)
	copy(key, t[:ed25519.SeedSize])
	return ed25519.NewKeyFromSeed(key)
}

// buildPKI mints an ephemeral in-memory CA + leaf from the shared root seed.
// Every node derives the identical CA, so mutual TLS works without files.
func buildPKI(self NodeID, seed [64]byte) (*identity, error) {
	caKey := deriveEd25519(seed, "fabric/ca")
	caPub := caKey.Public().(ed25519.PublicKey)

	caTpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "fabric-root-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, caPub, caKey)
	if err != nil {
		return nil, fmt.Errorf("ca cert: %w", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, fmt.Errorf("ca parse: %w", err)
	}

	nodeKey := deriveEd25519(seed, fmt.Sprintf("fabric/node/%d/%d", self.Continent, self.Tier))
	nodePub := nodeKey.Public().(ed25519.PublicKey)
	host := matrixAddr(self.Continent, self.Tier)
	ip := net.ParseIP(strings.Split(host, ":")[0])

	leafTpl := &x509.Certificate{
		SerialNumber: big.NewInt(int64(self.index() + 2)),
		Subject:      pkix.Name{CommonName: self.String()},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IPAddresses:  []net.IP{ip},
		DNSNames:     []string{self.String()},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTpl, caCert, nodePub, caKey)
	if err != nil {
		return nil, fmt.Errorf("leaf cert: %w", err)
	}
	leafCert, err := x509.ParseCertificate(leafDER)
	if err != nil {
		return nil, fmt.Errorf("leaf parse: %w", err)
	}

	// PEM bundle for tls.X509KeyPair (leaf + ca).
	leafPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	keyDER, err := x509.MarshalPKCS8PrivateKey(nodeKey)
	if err != nil {
		return nil, fmt.Errorf("marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	tlsCert, err := tls.X509KeyPair(append(leafPEM, caPEM...), keyPEM)
	if err != nil {
		return nil, fmt.Errorf("tls keypair: %w", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	return &identity{
		self:  self,
		addr:  host,
		seed:  seed,
		nodeK: nodeKey,
		nodeC: leafCert,
		caC:   caCert,
		caK:   caKey,
		tlsC:  tlsCert,
		pool:  pool,
	}, nil
}

// clientTLS builds a TLS 1.3 client config with mutual authentication.
// Chain + SAN checks run in VerifyPeerCertificate against the ephemeral CA
// (loopback matrix addresses are IPs; ServerName is irrelevant there).
func (id *identity) clientTLS() *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		MaxVersion:         tls.VersionTLS13,
		Certificates:       []tls.Certificate{id.tlsC},
		RootCAs:            id.pool,
		ClientAuth:         tls.RequireAndVerifyClientCert,
		ClientCAs:          id.pool,
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			return verifyFabricPeer(raw, id.pool)
		},
	}
}

// serverTLS builds a TLS 1.3 server config requiring client certs.
func (id *identity) serverTLS() *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		MaxVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{id.tlsC},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    id.pool,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			return verifyFabricPeer(raw, id.pool)
		},
	}
}

func verifyFabricPeer(raw [][]byte, pool *x509.CertPool) error {
	if len(raw) == 0 {
		return errors.New("fabric: empty peer chain")
	}
	cert, err := x509.ParseCertificate(raw[0])
	if err != nil {
		return fmt.Errorf("fabric: parse peer: %w", err)
	}
	opts := x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	if _, err := cert.Verify(opts); err != nil {
		return fmt.Errorf("fabric: verify peer: %w", err)
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Hourly cascading crypto: ChaCha20-Poly1305 → AES-256-GCM
// ─────────────────────────────────────────────────────────────────────────────

type hourKeys struct {
	hour   int64
	chacha cipher.AEAD
	aesGCM cipher.AEAD
}

// deriveHourlyKeys mixes the 64-byte root seed with the current epoch hour.
// Keys rotate every hour; both layers use independent subkeys.
func deriveHourlyKeys(root [64]byte, hour int64) (*hourKeys, error) {
	var hb [8]byte
	binary.BigEndian.PutUint64(hb[:], uint64(hour))

	h := sha256.New()
	h.Write(root[:])
	h.Write([]byte("|hour|"))
	h.Write(hb[:])
	master := h.Sum(nil) // 32 bytes

	chachaKey := sha256.Sum256(append([]byte("chacha|"), master...))
	aesKey := sha256.Sum256(append([]byte("aesgcm|"), master...))

	c1, err := chacha20poly1305.New(chachaKey[:])
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(aesKey[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &hourKeys{hour: hour, chacha: c1, aesGCM: gcm}, nil
}

func currentHour() int64 { return time.Now().Unix() / 3600 }

func deriveNonce(hour, seq int64, layer byte) []byte {
	var n [chacha20poly1305.NonceSize]byte // 12
	binary.BigEndian.PutUint64(n[0:8], uint64(hour))
	binary.BigEndian.PutUint32(n[8:12], uint32(seq))
	n[11] ^= layer
	return n[:]
}

// sealCell applies Layer-1 ChaCha20-Poly1305 then Layer-2 AES-256-GCM
// over a variable-length plaintext frame.
// Wire layout: [12B nonce1][12B nonce2][AES-GCM(ciphertext+tag)]
func sealCell(keys *hourKeys, seq int64, plain []byte) []byte {
	n1 := deriveNonce(keys.hour, seq, 1)
	n2 := deriveNonce(keys.hour, seq, 2)
	c1 := keys.chacha.Seal(nil, n1, plain, []byte("L1"))
	out := make([]byte, 0, 12+12+len(c1)+keys.aesGCM.Overhead())
	out = append(out, n1...)
	out = append(out, n2...)
	out = keys.aesGCM.Seal(out, n2, c1, []byte("L2"))
	return out
}

// openCell reverses the cascading pipeline.
func openCell(keys *hourKeys, wire []byte) ([]byte, error) {
	ns := chacha20poly1305.NonceSize
	if len(wire) < 2*ns+keys.aesGCM.Overhead() {
		return nil, errors.New("cell too short")
	}
	n1 := wire[0:ns]
	n2 := wire[ns : 2*ns]
	c2 := wire[2*ns:]
	c1, err := keys.aesGCM.Open(nil, n2, c2, []byte("L2"))
	if err != nil {
		return nil, fmt.Errorf("aes layer: %w", err)
	}
	plain, err := keys.chacha.Open(nil, n1, c1, []byte("L1"))
	if err != nil {
		return nil, fmt.Errorf("chacha layer: %w", err)
	}
	if len(plain) < headerSize || len(plain) > maxFrame {
		return nil, fmt.Errorf("bad plain size %d", len(plain))
	}
	return plain, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Variable-length frame: header + payload
// ─────────────────────────────────────────────────────────────────────────────

type cellHdr struct {
	Type      byte
	Flags     byte
	StreamID  uint16
	Sequence  uint32
	Src       NodeID
	Dst       NodeID
	LatencyUs int64
	DropRate  float64
	Loss      float64
	PayloadN  uint32
	Prev      NodeID // reverse-path hop for FEEDBACK
}

func (h *cellHdr) marshal(b []byte) {
	b[0] = h.Type
	b[1] = h.Flags
	binary.BigEndian.PutUint16(b[2:4], h.StreamID)
	binary.BigEndian.PutUint32(b[4:8], h.Sequence)
	b[8] = byte(h.Src.Continent)
	b[9] = byte(h.Src.Tier)
	b[10] = byte(h.Dst.Continent)
	b[11] = byte(h.Dst.Tier)
	binary.BigEndian.PutUint64(b[12:20], uint64(h.LatencyUs))
	binary.BigEndian.PutUint64(b[20:28], math.Float64bits(h.DropRate))
	binary.BigEndian.PutUint64(b[28:36], math.Float64bits(h.Loss))
	binary.BigEndian.PutUint32(b[36:40], h.PayloadN)
	b[40] = byte(h.Prev.Continent)
	b[41] = byte(h.Prev.Tier)
}

func unmarshalHdr(b []byte) cellHdr {
	return cellHdr{
		Type:      b[0],
		Flags:     b[1],
		StreamID:  binary.BigEndian.Uint16(b[2:4]),
		Sequence:  binary.BigEndian.Uint32(b[4:8]),
		Src:       NodeID{int(b[8]), int(b[9])},
		Dst:       NodeID{int(b[10]), int(b[11])},
		LatencyUs: int64(binary.BigEndian.Uint64(b[12:20])),
		DropRate:  math.Float64frombits(binary.BigEndian.Uint64(b[20:28])),
		Loss:      math.Float64frombits(binary.BigEndian.Uint64(b[28:36])),
		PayloadN:  binary.BigEndian.Uint32(b[36:40]),
		Prev:      NodeID{int(b[40]), int(b[41])},
	}
}

// packCell concatenates the fixed header with the payload (variable length).
func packCell(hdr cellHdr, payload []byte) ([]byte, error) {
	if len(payload) > maxFrame-headerSize {
		return nil, fmt.Errorf("payload %d exceeds %d", len(payload), maxFrame-headerSize)
	}
	buf := make([]byte, headerSize+len(payload))
	hdr.PayloadN = uint32(len(payload))
	hdr.marshal(buf)
	copy(buf[headerSize:], payload)
	return buf, nil
}

func unpackCell(buf []byte) (cellHdr, []byte, error) {
	if len(buf) < headerSize || len(buf) > maxFrame {
		return cellHdr{}, nil, errors.New("unpack: bad size")
	}
	h := unmarshalHdr(buf)
	n := int(h.PayloadN)
	if headerSize+n != len(buf) {
		return cellHdr{}, nil, fmt.Errorf("unpack: payloadLen %d vs frame %d", n, len(buf)-headerSize)
	}
	payload := make([]byte, n)
	copy(payload, buf[headerSize:])
	return h, payload, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Softmax routing, loss, gradient descent
// ─────────────────────────────────────────────────────────────────────────────

// computeLoss implements the real-time network loss function:
//
//	Loss = LatencyInMilliseconds + (DropRate * 100)
func computeLoss(latency time.Duration, dropRate float64) float64 {
	ms := float64(latency.Microseconds()) / 1000.0
	if ms < 0 {
		ms = 0
	}
	if dropRate < 0 {
		dropRate = 0
	}
	if dropRate > 1 {
		dropRate = 1
	}
	return ms + dropRate*100.0
}

// softmax converts SynapticWeights into a probability distribution.
// Numerically stable (max-shift).
func softmax(weights []float64) []float64 {
	if len(weights) == 0 {
		return nil
	}
	maxW := weights[0]
	for _, w := range weights[1:] {
		if w > maxW {
			maxW = w
		}
	}
	exp := make([]float64, len(weights))
	var sum float64
	for i, w := range weights {
		e := math.Exp(w - maxW)
		exp[i] = e
		sum += e
	}
	if sum == 0 {
		u := 1 / float64(len(weights))
		for i := range exp {
			exp[i] = u
		}
		return exp
	}
	for i := range exp {
		exp[i] /= sum
	}
	return exp
}

// sampleSoftmax picks an index from the distribution (thread-safe via
// math/rand's global source which is mutex-protected).
func sampleSoftmax(probs []float64) int {
	if len(probs) == 0 {
		return -1
	}
	r := mrand.Float64()
	var acc float64
	for i, p := range probs {
		acc += p
		if r <= acc {
			return i
		}
	}
	return len(probs) - 1
}

// gradientDescent applies a weight update that reduces the route's
// SynapticWeight relative to the error scale (loss).
//
//	w' = clamp(w - η * ∇),  ∇ = loss / (1 + loss) ∈ (0,1)
func gradientDescent(w, loss, eta float64) float64 {
	grad := loss / (1.0 + loss)
	nw := w - eta*grad
	if nw < weightMin {
		nw = weightMin
	}
	if nw > weightMax {
		nw = weightMax
	}
	return nw
}

// rewardWeight nudges weight upward on successful delivery (contrastive).
func rewardWeight(w float64) float64 {
	nw := w + learnRateUp
	if nw > weightMax {
		nw = weightMax
	}
	return nw
}

// ─────────────────────────────────────────────────────────────────────────────
// Peer: one persistent mTLS stream + SynapticWeight state
// ─────────────────────────────────────────────────────────────────────────────

type peer struct {
	id      NodeID
	addr    string
	conn    net.Conn
	writeMu sync.Mutex

	// SynapticWeight — float64 on every outgoing peer connection block.
	weight atomic.Uint64 // math.Float64bits

	// EMA latency (microseconds) and drop accounting for Loss.
	latencyUs atomic.Int64
	drops     atomic.Uint64
	sent      atomic.Uint64

	sendCh chan []byte // sealed wire frames
	closed chan struct{}
	once   sync.Once
}

func newPeer(id NodeID, addr string) *peer {
	p := &peer{
		id:     id,
		addr:   addr,
		weight: atomic.Uint64{},
		sendCh: make(chan []byte, 64),
		closed: make(chan struct{}),
	}
	p.weight.Store(math.Float64bits(weightInit))
	p.latencyUs.Store(1_000) // 1ms seed until measured (loopback)
	return p
}

func (p *peer) getWeight() float64 { return math.Float64frombits(p.weight.Load()) }

func (p *peer) setWeight(w float64) {
	if w < weightMin {
		w = weightMin
	}
	if w > weightMax {
		w = weightMax
	}
	p.weight.Store(math.Float64bits(w))
}

// observe records a one-way sample for Loss = LatencyMs + DropRate*100.
func (p *peer) observe(rtt time.Duration, ok bool) {
	// Use half-RTT as one-way latency estimate.
	oneWay := rtt / 2
	prev := time.Duration(p.latencyUs.Load()) * time.Microsecond
	// EMA α=0.2
	next := time.Duration(0.2*float64(oneWay) + 0.8*float64(prev))
	p.latencyUs.Store(next.Microseconds())

	p.sent.Add(1)
	if !ok {
		p.drops.Add(1)
	}
}

func (p *peer) dropRate() float64 {
	s := p.sent.Load()
	if s == 0 {
		return 0
	}
	return float64(p.drops.Load()) / float64(s)
}

func (p *peer) latency() time.Duration {
	return time.Duration(p.latencyUs.Load()) * time.Microsecond
}

func (p *peer) currentLoss() float64 {
	return computeLoss(p.latency(), p.dropRate())
}

func (p *peer) close() {
	p.once.Do(func() {
		close(p.closed)
		if p.conn != nil {
			_ = p.conn.Close()
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Fabric node
// ─────────────────────────────────────────────────────────────────────────────

type fabric struct {
	id     *identity
	keys   atomic.Pointer[hourKeys]
	peers  map[NodeID]*peer
	peerMu sync.RWMutex

	seq    atomic.Uint32
	stream atomic.Uint32
	sink   atomic.Uint32
	ln     net.Listener
	cancel context.CancelFunc
	ctx    context.Context

	// reverse adjacency: who last forwarded to us (for FEEDBACK walk-back)
	reverseMu sync.Mutex
	reverse   map[NodeID]NodeID // prev hop by (src of feedback target)

	// feedback throttle: at most one FEEDBACK burst per second
	fbMu     sync.Mutex
	lastFbAt time.Time
}

func newFabric(id *identity) (*fabric, error) {
	f := &fabric{
		id:      id,
		peers:   make(map[NodeID]*peer),
		reverse: make(map[NodeID]NodeID),
	}
	k, err := deriveHourlyKeys(id.seed, currentHour())
	if err != nil {
		return nil, err
	}
	f.keys.Store(k)
	return f, nil
}

func (f *fabric) currentKeys() *hourKeys { return f.keys.Load() }

// keyRotator swaps keys on the hour boundary.
func (f *fabric) keyRotator(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
			h := currentHour()
			if f.currentKeys().hour != h {
				if k, err := deriveHourlyKeys(f.id.seed, h); err == nil {
					f.keys.Store(k)
					log.Printf("[keys] rotated to hour %d", h)
				}
			}
		}
	}
}

// getOrCreatePeer lazily dials and registers an outgoing stream.
func (f *fabric) getOrCreatePeer(dst NodeID) (*peer, error) {
	if dst == f.id.self {
		return nil, errors.New("self")
	}
	f.peerMu.RLock()
	p, ok := f.peers[dst]
	f.peerMu.RUnlock()
	if ok {
		return p, nil
	}

	f.peerMu.Lock()
	defer f.peerMu.Unlock()
	if p, ok = f.peers[dst]; ok {
		return p, nil
	}

	addr := matrixAddr(dst.Continent, dst.Tier)
	p = newPeer(dst, addr)

	d := &net.Dialer{Timeout: 3 * time.Second}
	raw, err := d.DialContext(f.ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	tc := tls.Client(raw, f.id.clientTLS())
	if err := tc.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		raw.Close()
		return nil, err
	}
	if err := tc.HandshakeContext(f.ctx); err != nil {
		raw.Close()
		return nil, fmt.Errorf("mtls %s: %w", addr, err)
	}
	_ = tc.SetDeadline(time.Time{})

	// Identify ourselves: first frame is a PROBE with Src=self.
	p.conn = tc
	f.peers[dst] = p

	go f.writeLoop(p)
	go f.readLoop(p)

	// Kick a probe so the remote registers reverse path.
	_ = f.sendOn(p, cellHdr{
		Type: frameProbe,
		Src:  f.id.self,
		Dst:  dst,
		Prev: f.id.self,
	}, nil)

	log.Printf("[link] ↑ %s  w=%.3f  %s", dst, p.getWeight(), addr)
	return p, nil
}

// writeLoop drains sealed frames onto the TLS socket.
func (f *fabric) writeLoop(p *peer) {
	for {
		select {
		case <-f.ctx.Done():
			return
		case <-p.closed:
			return
		case wire := <-p.sendCh:
			start := time.Now()
			p.writeMu.Lock()
			_, err := p.conn.Write(wire)
			p.writeMu.Unlock()
			ok := err == nil
			p.observe(time.Since(start), ok)
			if !ok {
				p.close()
				f.dropPeer(p.id)
				return
			}
		}
	}
}

// readLoop decrypts inbound cells and dispatches them.
func (f *fabric) readLoop(p *peer) {
	defer func() {
		p.close()
		f.dropPeer(p.id)
	}()
	buf := make([]byte, 8192)
	for {
		select {
		case <-f.ctx.Done():
			return
		case <-p.closed:
			return
		default:
		}
		_ = p.conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		n, err := p.conn.Read(buf)
		if err != nil {
			if err != io.EOF {
				// deadline with no traffic is fine — keep multiplexing
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					continue
				}
			}
			return
		}
		wire := make([]byte, n)
		copy(wire, buf[:n])
		f.handleWire(p, wire)
	}
}

func (f *fabric) dropPeer(id NodeID) {
	f.peerMu.Lock()
	if p, ok := f.peers[id]; ok {
		delete(f.peers, id)
		f.peerMu.Unlock()
		p.close()
		return
	}
	f.peerMu.Unlock()
}

// sendOn packs, seals, and queues a frame on a specific peer.
func (f *fabric) sendOn(p *peer, hdr cellHdr, payload []byte) error {
	plain, err := packCell(hdr, payload)
	if err != nil {
		return err
	}
	seq := int64(f.seq.Add(1))
	wire := sealCell(f.currentKeys(), seq, plain)
	select {
	case p.sendCh <- wire:
		return nil
	case <-p.closed:
		return errors.New("peer closed")
	case <-f.ctx.Done():
		return f.ctx.Err()
	default:
		return errors.New("send queue full")
	}
}

// softmaxNextHop selects an outgoing peer by Softmax over SynapticWeights.
func (f *fabric) softmaxNextHop(exclude map[NodeID]bool) (*peer, error) {
	cands := routePeers(f.id.self)
	type pair struct {
		p *peer
		w float64
	}
	var live []pair
	for _, c := range cands {
		if exclude != nil && exclude[c] {
			continue
		}
		p, err := f.getOrCreatePeer(c)
		if err != nil {
			continue // dial failed — not a live candidate
		}
		live = append(live, pair{p, p.getWeight()})
	}
	if len(live) == 0 {
		return nil, errors.New("no live next-hop")
	}
	ws := make([]float64, len(live))
	for i, pr := range live {
		ws[i] = pr.w
	}
	probs := softmax(ws)
	idx := sampleSoftmax(probs)
	if idx < 0 {
		idx = 0
	}
	return live[idx].p, nil
}

// forwardData performs one hop: choose next via softmax, stamp metrics, send.
func (f *fabric) forwardData(hdr cellHdr, payload []byte, visited map[NodeID]bool) error {
	// Terminal: we are the destination.
	if hdr.Dst == f.id.self {
		return f.onDeliver(hdr, payload)
	}

	// At a Master with high measured loss on the ingress path → FEEDBACK.
	if f.id.self.Tier == tierMaster && hdr.Loss >= feedbackLossThreshold {
		f.emitFeedback(hdr)
	}

	next, err := f.softmaxNextHop(visited)
	if err != nil {
		return err
	}

	// Stamp our live metrics into the cell before the next hop.
	visited[f.id.self] = true
	hdr.LatencyUs = next.latencyUs.Load()
	hdr.DropRate = next.dropRate()
	hdr.Loss = next.currentLoss()
	hdr.Prev = f.id.self

	if err := f.sendOn(next, hdr, payload); err != nil {
		return err
	}
	return nil
}

// onDeliver is the Tier-1 Master sink (or any Dst match).
func (f *fabric) onDeliver(hdr cellHdr, payload []byte) error {
	loss := hdr.Loss
	if loss == 0 {
		loss = computeLoss(hdr.LatencyUsToDuration(), hdr.DropRate)
	}
	log.Printf("[rx] %s→%s type=%#x loss=%.2f pay=%d",
		hdr.Src, f.id.self, hdr.Type, loss, len(payload))

	if f.id.self.Tier == tierMaster && loss >= feedbackLossThreshold {
		f.emitFeedback(hdr)
	}
	return nil
}

// LatencyUsToDuration helper on cellHdr.
func (h cellHdr) LatencyUsToDuration() time.Duration {
	return time.Duration(h.LatencyUs) * time.Microsecond
}

// emitFeedback sends a FEEDBACK frame backward down the open
// virtual stream (Prev chain), triggering gradient descent on each hop.
func (f *fabric) emitFeedback(orig cellHdr) {
	f.fbMu.Lock()
	if time.Since(f.lastFbAt) < time.Second {
		f.fbMu.Unlock()
		return
	}
	f.lastFbAt = time.Now()
	f.fbMu.Unlock()

	fb := cellHdr{
		Type:      frameFeedback,
		Src:       f.id.self,
		Dst:       orig.Src, // walk back toward the origin edge
		Prev:      f.id.self,
		LatencyUs: orig.LatencyUs,
		DropRate:  orig.DropRate,
		Loss:      orig.Loss,
	}
	// Payload carries the degraded route for auditing.
	meta := fmt.Sprintf("fb loss=%.2f src=%s dst=%s", orig.Loss, orig.Src, orig.Dst)
	_ = f.routeBackward(fb, []byte(meta), map[NodeID]bool{f.id.self: true})
}

// routeBackward pushes FEEDBACK toward the origin, applying Gradient
// Descent on every intermediate SynapticWeight.
func (f *fabric) routeBackward(hdr cellHdr, payload []byte, visited map[NodeID]bool) error {
	if hdr.Dst == f.id.self {
		return nil // reached origin
	}
	// Prefer the reverse-path neighbor if known; else softmax among unvisited.
	var next *peer
	f.reverseMu.Lock()
	if rev, ok := f.reverse[hdr.Dst]; ok && !visited[rev] {
		f.peerMu.RLock()
		next = f.peers[rev]
		f.peerMu.RUnlock()
	}
	f.reverseMu.Unlock()

	if next == nil {
		var err error
		next, err = f.softmaxNextHop(visited)
		if err != nil {
			return err
		}
	}

	// Backward propagation: reduce SynapticWeight of this degraded channel.
	oldW := next.getWeight()
	newW := gradientDescent(oldW, hdr.Loss, learnRateDown)
	next.setWeight(newW)
	log.Printf("[bp] %s w %.3f→%.3f  loss=%.2f", next.id, oldW, newW, hdr.Loss)

	visited[f.id.self] = true
	hdr.Prev = f.id.self
	return f.sendOn(next, hdr, payload)
}

// handleWire decrypts and dispatches an inbound frame.
func (f *fabric) handleWire(from *peer, wire []byte) {
	plain, err := openCell(f.currentKeys(), wire)
	if err != nil {
		// Hour boundary skew: try previous hour once.
		if prev, perr := deriveHourlyKeys(f.id.seed, f.currentKeys().hour-1); perr == nil {
			plain, err = openCell(prev, wire)
		}
		if err != nil {
			log.Printf("[crypto] open failed from %s: %v", from.id, err)
			return
		}
	}
	hdr, payload, err := unpackCell(plain)
	if err != nil {
		return
	}

	// Record reverse path for FEEDBACK walk-back.
	f.reverseMu.Lock()
	f.reverse[hdr.Src] = hdr.Prev
	f.reverseMu.Unlock()

	// Observe RTT-ish quality from stamped metrics.
	from.latencyUs.Store(hdr.LatencyUs)

	switch hdr.Type {
	case frameProbe:
		// Lightweight liveness; bump weight slightly.
		from.setWeight(rewardWeight(from.getWeight()))
	case frameFeedback:
		// Preceding node: apply Gradient Descent relative to error scale.
		oldW := from.getWeight()
		newW := gradientDescent(oldW, hdr.Loss, learnRateDown)
		from.setWeight(newW)
		log.Printf("[bp-recv] from=%s w %.3f→%.3f loss=%.2f", hdr.Src, oldW, newW, hdr.Loss)
		// Continue walking back toward origin.
		visited := map[NodeID]bool{f.id.self: true}
		_ = f.routeBackward(hdr, payload, visited)
	case frameData:
		visited := map[NodeID]bool{f.id.self: true}
		if hdr.Prev != (NodeID{}) {
			visited[hdr.Prev] = true
		}
		if err := f.forwardData(hdr, payload, visited); err != nil {
			log.Printf("[fwd] err: %v", err)
		}
	default:
		// ignore
	}
}

// Inject places a local DATA frame into the fabric (entry at this tier).
// Sink rotates across active Master nodes so every continent in the set
// exercises final-hop delivery.
func (f *fabric) Inject(payload []byte) error {
	seq := f.seq.Add(1)
	dstCont := int(f.sink.Add(1)%uint32(activeContinents)) + 1
	hdr := cellHdr{
		Type:     frameData,
		StreamID: uint16(f.stream.Add(1)),
		Sequence: seq,
		Src:      f.id.self,
		Dst:      NodeID{Continent: dstCont, Tier: tierMaster},
	}
	visited := map[NodeID]bool{f.id.self: true}
	return f.forwardData(hdr, payload, visited)
}

// InjectTo targets a specific destination node.
func (f *fabric) InjectTo(dst NodeID, payload []byte) error {
	hdr := cellHdr{
		Type:     frameData,
		StreamID: uint16(f.stream.Add(1)),
		Sequence: f.seq.Add(1),
		Src:      f.id.self,
		Dst:      dst,
	}
	visited := map[NodeID]bool{f.id.self: true}
	return f.forwardData(hdr, payload, visited)
}

// listen starts the mTLS server on this node's matrix address.
func (f *fabric) listen() error {
	ln, err := tls.Listen("tcp", f.id.addr, f.id.serverTLS())
	if err != nil {
		return fmt.Errorf("listen %s: %w", f.id.addr, err)
	}
	f.ln = ln
	log.Printf("[listen] mTLS 1.3  %s  node=%s", f.id.addr, f.id.self)

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-f.ctx.Done():
					return
				default:
					log.Printf("[accept] %v", err)
					continue
				}
			}
			go f.serveConn(conn)
		}
	}()
	return nil
}

// serveConn handles an inbound mTLS stream (demux side of the multiplexer).
func (f *fabric) serveConn(conn net.Conn) {
	tc, ok := conn.(*tls.Conn)
	if !ok {
		conn.Close()
		return
	}
	_ = tc.SetDeadline(time.Now().Add(5 * time.Second))
	if err := tc.Handshake(); err != nil {
		log.Printf("[srv] handshake: %v", err)
		conn.Close()
		return
	}
	_ = tc.SetDeadline(time.Time{})

	// Peer identity from cert CN "cN.tM"
	remote := NodeID{}
	if certs := tc.ConnectionState().PeerCertificates; len(certs) > 0 {
		remote = parseNodeCN(certs[0].Subject.CommonName)
	}
	if remote == (NodeID{}) {
		conn.Close()
		return
	}

	p := newPeer(remote, conn.RemoteAddr().String())
	p.conn = conn
	f.peerMu.Lock()
	// Prefer existing outbound peer object; otherwise adopt inbound.
	if existing, ok := f.peers[remote]; ok {
		f.peerMu.Unlock()
		conn.Close()
		_ = existing
		return
	}
	f.peers[remote] = p
	f.peerMu.Unlock()

	go f.writeLoop(p)
	go f.readLoop(p)
	log.Printf("[link] ↓ %s  %s", remote, conn.RemoteAddr())
}

func parseNodeCN(cn string) NodeID {
	// expected "cN.tM"
	if !strings.HasPrefix(cn, "c") {
		return NodeID{}
	}
	parts := strings.Split(strings.TrimPrefix(cn, "c"), ".t")
	if len(parts) != 2 {
		return NodeID{}
	}
	c, err1 := strconv.Atoi(parts[0])
	t, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return NodeID{}
	}
	return NodeID{c, t}
}

// preconnect dials the planned route graph so softmax has live candidates.
func (f *fabric) preconnect() {
	for _, dst := range routePeers(f.id.self) {
		go func(d NodeID) {
			if _, err := f.getOrCreatePeer(d); err != nil {
				log.Printf("[pre] %s: %v", d, err)
			}
		}(dst)
	}
}

// metricsLoop optionally dumps SynapticWeights + Loss.
func (f *fabric) metricsLoop(ctx context.Context, every time.Duration) {
	if os.Getenv("FABRIC_METRICS") == "" {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			f.peerMu.RLock()
			for id, p := range f.peers {
				log.Printf("[m] %s w=%.3f lat=%s drop=%.3f loss=%.2f",
					id, p.getWeight(), p.latency().Round(time.Microsecond),
					p.dropRate(), p.currentLoss())
			}
			f.peerMu.RUnlock()
		}
	}
}

// trafficLoop emits a light periodic DATA cell so Loss/Backprop can fire
// under real loopback RTTs. Rate is deliberately modest (no flood).
func (f *fabric) trafficLoop(ctx context.Context) {
	if os.Getenv("FABRIC_TRAFFIC") == "0" {
		return
	}
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	var n uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n++
			msg := []byte(fmt.Sprintf("tick=%d node=%s t=%d", n, f.id.self, time.Now().UnixNano()))
			if err := f.Inject(msg); err != nil {
				log.Printf("[tx] %v", err)
			}
		}
	}
}

// shutdown tears down listener + peers.
func (f *fabric) shutdown() {
	if f.cancel != nil {
		f.cancel()
	}
	if f.ln != nil {
		_ = f.ln.Close()
	}
	f.peerMu.Lock()
	for _, p := range f.peers {
		p.close()
	}
	f.peers = make(map[NodeID]*peer)
	f.peerMu.Unlock()
}

// ─────────────────────────────────────────────────────────────────────────────
// main
// ─────────────────────────────────────────────────────────────────────────────

func main() {
	showMatrix := flag.Bool("matrix", false, "print the active matrix and exit")
	selfTest := flag.Bool("self-test", false, "run pack/seal/softmax self-test and exit")
	flag.Parse()

	if *showMatrix {
		printMatrix()
		return
	}
	if *selfTest {
		if err := runSelfTest(); err != nil {
			log.Fatalf("self-test: %v", err)
		}
		log.Println("self-test OK")
		return
	}

	continent := envInt("ENV_CONTINENT", 1)
	tier := envInt("ENV_TIER", 1)
	if continent < 1 || continent > 7 {
		log.Fatalf("ENV_CONTINENT must be 1..7, got %d", continent)
	}
	if tier < 1 || tier > 4 {
		log.Fatalf("ENV_TIER must be 1..4, got %d", tier)
	}
	self := NodeID{Continent: continent, Tier: tier}
	if !isActive(self) {
		log.Fatalf("node %s not in active matrix (c1..c%d × t1..t%d; set FABRIC_CONTINENTS/FABRIC_TIERS)",
			self, activeContinents, activeTiers)
	}

	seed, err := loadRootSeed()
	if err != nil {
		log.Fatal(err)
	}
	id, err := buildPKI(self, seed)
	if err != nil {
		log.Fatalf("pki: %v", err)
	}

	f, err := newFabric(id)
	if err != nil {
		log.Fatalf("fabric: %v", err)
	}
	f.ctx, f.cancel = context.WithCancel(context.Background())

	if err := f.listen(); err != nil {
		log.Fatal(err)
	}
	f.preconnect()
	go f.keyRotator(f.ctx)
	go f.metricsLoop(f.ctx, 10*time.Second)
	go f.trafficLoop(f.ctx)

	log.Printf("fabric up  node=%s  addr=%s  peers=%v",
		self, id.addr, routePeers(self))

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("shutting down…")
	f.shutdown()
}

func printMatrix() {
	fmt.Printf("active matrix (loopback :8883) — %d continents × %d tiers = %d nodes\n",
		activeContinents, activeTiers, activeContinents*activeTiers)
	fmt.Println("continent | master | controller | superpeer | edge")
	for c := 1; c <= activeContinents; c++ {
		parts := make([]string, 0, activeTiers)
		for t := 1; t <= activeTiers; t++ {
			parts = append(parts, matrixAddr(c, t))
		}
		fmt.Printf("%d | %s\n", c, strings.Join(parts, " | "))
	}
}

// runSelfTest exercises pack → seal → open → unpack, softmax, loss, GD.
func runSelfTest() error {
	seed, err := loadRootSeed()
	if err != nil {
		return err
	}
	keys, err := deriveHourlyKeys(seed, currentHour())
	if err != nil {
		return err
	}

	hdr := cellHdr{
		Type: frameData, StreamID: 1, Sequence: 42,
		Src: NodeID{4, tierEdge}, Dst: NodeID{1, tierMaster},
		LatencyUs: 12_000, DropRate: 0.02, Loss: 14.0, Prev: NodeID{4, tierSuper},
	}
	plain, err := packCell(hdr, []byte("hello-fabric"))
	if err != nil {
		return err
	}
	if len(plain) != headerSize+len("hello-fabric") {
		return fmt.Errorf("pack size %d", len(plain))
	}
	wire := sealCell(keys, 1, plain)
	back, err := openCell(keys, wire)
	if err != nil {
		return err
	}
	h2, p2, err := unpackCell(back)
	if err != nil {
		return err
	}
	if h2.Src != hdr.Src || h2.Dst != hdr.Dst || string(p2) != "hello-fabric" {
		return fmt.Errorf("roundtrip mismatch: %+v %q", h2, p2)
	}

	// Softmax sanity: higher weight → higher probability.
	probs := softmax([]float64{2.0, 1.0, 0.0})
	if !(probs[0] > probs[1] && probs[1] > probs[2]) {
		return fmt.Errorf("softmax order: %v", probs)
	}
	var sum float64
	for _, p := range probs {
		sum += p
	}
	if math.Abs(sum-1.0) > 1e-9 {
		return fmt.Errorf("softmax sum %v", sum)
	}

	// Loss formula.
	if l := computeLoss(15*time.Millisecond, 0.10); math.Abs(l-25.0) > 1e-9 {
		return fmt.Errorf("loss want 25 got %v", l)
	}

	// Gradient descent reduces weight.
	w0 := 2.0
	w1 := gradientDescent(w0, 50.0, learnRateDown)
	if w1 >= w0 {
		return fmt.Errorf("gd did not reduce: %v → %v", w0, w1)
	}

	// Matrix size matches the active env-configured set.
	if n := len(allNodes()); n != activeContinents*activeTiers {
		return fmt.Errorf("matrix nodes %d", n)
	}

	// Deterministic PKI: two derivations match.
	a, err := buildPKI(NodeID{3, tierSuper}, seed)
	if err != nil {
		return err
	}
	b, err := buildPKI(NodeID{3, tierSuper}, seed)
	if err != nil {
		return err
	}
	if hex.EncodeToString(a.nodeC.Raw) != hex.EncodeToString(b.nodeC.Raw) {
		return errors.New("pki not deterministic")
	}

	// mTLS handshake loopback (server + client in-process).
	return loopbackMTLS(a, b)
}

// loopbackMTLS proves RequireAndVerifyClientCert works with ephemeral PKI.
func loopbackMTLS(server, client *identity) error {
	ln, err := tls.Listen("tcp", "127.0.0.1:0", server.serverTLS())
	if err != nil {
		return err
	}
	defer ln.Close()

	done := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer c.Close()
		tc := c.(*tls.Conn)
		_ = tc.SetDeadline(time.Now().Add(3 * time.Second))
		if err := tc.Handshake(); err != nil {
			done <- err
			return
		}
		buf := make([]byte, 16)
		n, err := tc.Read(buf)
		if err != nil {
			done <- err
			return
		}
		_, _ = tc.Write(buf[:n])
		done <- nil
	}()

	raw, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		return err
	}
	defer raw.Close()
	tc := tls.Client(raw, client.clientTLS())
	_ = tc.SetDeadline(time.Now().Add(3 * time.Second))
	if err := tc.Handshake(); err != nil {
		return fmt.Errorf("client handshake: %w", err)
	}
	if _, err := tc.Write([]byte("ping")); err != nil {
		return err
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(tc, buf); err != nil {
		return err
	}
	if string(buf) != "ping" {
		return fmt.Errorf("echo mismatch %q", buf)
	}
	return <-done
}
