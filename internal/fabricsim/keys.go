package fabricsim

// Real per-node signing keys.
//
// Earlier revisions of this simulator decided "is this frame forged?" by
// reading Packet.Kind, which is ground truth the detector must not have. That
// made the signature check theatre: it agreed with the label the simulator had
// already assigned. This file gives every node a real ed25519 keypair so the
// defence verifies a signature the way the production fabric does — by checking
// bytes against a public key.
//
// The keys are derived deterministically from the run seed, so a run stays
// exactly reproducible: the same seed produces the same keypairs, the same
// signatures, and therefore the same verdicts. Nothing here touches the network
// or the disk, so the package's no-I/O guarantee is preserved.

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
)

// keySeedDomain separates node key material from the packet RNG, so that
// changing traffic does not change the keys and vice versa.
const keySeedDomain = "fabricsim/nodekey/v1"

// nodeKey derives the private key for the node at index idx from the run seed.
//
// Derivation is a SHA-256 chain over the seed, the domain string and the node
// index. It is not a password KDF and does not need to be: these keys exist to
// make forgery detectable inside one in-process simulation, not to protect
// anything. The production fabric derives its real keys from a [64]byte seed
// via its own scheme, and that is the code that matters for real traffic.
func nodeKey(seed uint64, idx int) ed25519.PrivateKey {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], seed)
	h := sha256.Sum256(append([]byte(keySeedDomain), buf[:]...))
	// Fold the node index in so each node gets a distinct key.
	var ib [8]byte
	binary.LittleEndian.PutUint64(ib[:], uint64(idx))
	idxHash := sha256.Sum256(append(h[:], ib[:]...))
	return ed25519.NewKeyFromSeed(idxHash[:ed25519.SeedSize])
}

// signer is the subset of Packet the signature commits to.
//
// Only wire-visible fields are covered. In particular Seq and SkewMs are
// included because a receiver uses them to infer loss and to validate the key
// window, so an attacker able to alter them could forge a frame that both looks
// lossless and passes the key check. The signature must therefore bind them.
type signer struct {
	from        NodeID
	to          NodeID
	sealedAt    int64
	seq         uint64
	declaredLen uint32
	skewMs      int64
	payload     []byte
}

// signingInput renders the fields above into a single deterministic byte
// string. Lengths are fixed-width big-endian so no two distinct field sets can
// produce the same encoding, and the payload is length-prefixed so a payload
// cannot be shifted across the boundary.
func (s signer) signingInput() []byte {
	h := sha256.New()
	h.Write(encodeID(s.from))
	h.Write(encodeID(s.to))
	writeU64(h, uint64(s.sealedAt))
	writeU64(h, s.seq)
	writeU64(h, uint64(s.declaredLen))
	writeU64(h, uint64(s.skewMs))
	writeU64(h, uint64(len(s.payload)))
	h.Write(s.payload)
	return h.Sum(nil)
}

// encodeID renders a NodeID as fixed-width bytes for the signing input.
func encodeID(n NodeID) []byte {
	return []byte{byte(n.Zone), byte(n.Tier), byte(n.Role)}
}

func writeU64(h interface{ Write([]byte) (int, error) }, v uint64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	_, _ = h.Write(b[:])
}

// signPacket attaches a valid signature to p using the sender's key.
//
// The sender is taken from p.From, so signing a frame as somebody else is not
// possible without their key: the forgery vector signs with the attacker's own
// key while claiming a defender identity, and verification fails on exactly the
// mismatch a real deployment would see.
func signPacket(p *Packet, priv ed25519.PrivateKey) {
	p.Sig = ed25519.Sign(priv, signer{
		from:        p.From,
		to:          p.To,
		sealedAt:    p.SealedAt,
		seq:         p.Seq,
		declaredLen: p.DeclaredLen,
		skewMs:      int64(p.SkewMs),
		payload:     p.Payload,
	}.signingInput())
}

// verifySignature checks p's signature against the public key registered for
// the identity the frame claims to be from.
//
// The claimed identity is the lookup key on purpose. A frame that says "I am
// c3.t2" is verified with c3.t2's public key, so a frame that merely renames
// itself does not become legitimate. The alternative — verifying against the
// key of whoever actually signed it — would authenticate every frame including
// forged ones, since every attacker holds a real key.
func verifySignature(p *Packet, top *Topology) bool {
	n := top.ByID(p.From)
	if n == nil || n.pub == nil {
		return false
	}
	if len(p.Sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(n.pub, signer{
		from:        p.From,
		to:          p.To,
		sealedAt:    p.SealedAt,
		seq:         p.Seq,
		declaredLen: p.DeclaredLen,
		skewMs:      int64(p.SkewMs),
		payload:     p.Payload,
	}.signingInput(), p.Sig)
}

// corruptSignature turns a correctly signed frame into a forged one, the way an
// attacker with no access to the defender keys would: by mutating bytes after
// signing.
//
// It flips a bit in the signature rather than blanking it, so the failure the
// defence sees is "signature does not verify" instead of "no signature was
// present". A detector that merely checked for the presence of a signature
// would pass this frame; one that actually verifies will not.
func corruptSignature(p *Packet) {
	if len(p.Sig) > 0 {
		p.Sig[0] ^= 0x01
	}
}
