package hivemind

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// ── STUN client (RFC 5389, binding only) ──────────────────────────────
// Discovers this node's reflexive transport address — the host:port the
// rest of the internet sees — so advertisements state facts instead of
// guesses. Pure standard library; no external client needed.

const (
	stunMagicCookie   = 0x2112A442
	stunBindingReq    = 0x0001
	stunBindingResp   = 0x0101
	stunAttrXORMapped = 0x0020
	stunTimeout       = 5 * time.Second
)

// stunBinding asks a STUN server for our reflexive address. Returns host
// and port as the internet sees them, or an error (unreachable server,
// malformed response, transaction mismatch — all fail closed to "").
func stunBinding(server string) (string, int, error) {
	raddr, err := net.ResolveUDPAddr("udp", server)
	if err != nil {
		return "", 0, fmt.Errorf("stun resolve: %w", err)
	}
	conn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		return "", 0, fmt.Errorf("stun dial: %w", err)
	}
	defer conn.Close()
	return stunExchange(conn, raddr)
}

// stunExchange performs one binding exchange over an already-open UDP
// socket to raddr. Split out because NAT mappings are per-socket: the
// DHT must ask from its own socket, or the learned address is somebody
// else's truth. Returns host and port as the internet sees the socket.
func stunExchange(conn *net.UDPConn, raddr *net.UDPAddr) (string, int, error) {
	_ = conn.SetDeadline(time.Now().Add(stunTimeout))
	txID := make([]byte, 12)
	if _, err := rand.Read(txID); err != nil {
		return "", 0, fmt.Errorf("stun entropy: %w", err)
	}
	req := make([]byte, 20)
	binary.BigEndian.PutUint16(req[0:2], stunBindingReq)
	binary.BigEndian.PutUint16(req[2:4], 0)
	binary.BigEndian.PutUint32(req[4:8], stunMagicCookie)
	copy(req[8:20], txID)

	// WriteTo keeps unconnected sockets (the DHT's) working; connected
	// sockets (the throwaway dial) take the plain path instead — Go
	// forbids WriteTo on those, loudly.
	var err error
	if conn.RemoteAddr() != nil {
		_, err = conn.Write(req)
	} else {
		_, err = conn.WriteToUDP(req, raddr)
	}
	if err != nil {
		return "", 0, fmt.Errorf("stun write: %w", err)
	}
	resp := make([]byte, 1024)
	n, _, err := conn.ReadFromUDP(resp)
	if err != nil {
		return "", 0, fmt.Errorf("stun read: %w", err)
	}
	return parseStunResponse(resp[:n], txID)
}

// parseStunResponse validates a Binding Success response against our
// transaction ID and extracts the XOR-MAPPED-ADDRESS (v4 and v6).
func parseStunResponse(pkt, txID []byte) (string, int, error) {
	if len(pkt) < 20 {
		return "", 0, fmt.Errorf("stun packet too short: %d", len(pkt))
	}
	if binary.BigEndian.Uint16(pkt[0:2]) != stunBindingResp {
		return "", 0, fmt.Errorf("not a binding success: %#04x", binary.BigEndian.Uint16(pkt[0:2]))
	}
	if binary.BigEndian.Uint32(pkt[4:8]) != stunMagicCookie {
		return "", 0, fmt.Errorf("bad magic cookie")
	}
	for i := 0; i < 12; i++ {
		if pkt[8+i] != txID[i] {
			return "", 0, fmt.Errorf("transaction mismatch (replay or crosstalk)")
		}
	}
	claimed := int(binary.BigEndian.Uint16(pkt[2:4]))
	if claimed > len(pkt)-20 {
		return "", 0, fmt.Errorf("attribute length lies: %d", claimed)
	}
	attrs := pkt[20 : 20+claimed]
	for len(attrs) >= 4 {
		typ := binary.BigEndian.Uint16(attrs[0:2])
		ln := int(binary.BigEndian.Uint16(attrs[2:4]))
		if ln > len(attrs)-4 {
			return "", 0, fmt.Errorf("attribute overruns packet")
		}
		val := attrs[4 : 4+ln]
		if typ == stunAttrXORMapped {
			return parseXORMapped(val, txID)
		}
		// Attributes pad to 4-byte boundaries.
		next := 4 + ln
		next += (4 - next%4) % 4
		attrs = attrs[next:]
	}
	return "", 0, fmt.Errorf("no XOR-MAPPED-ADDRESS in response")
}

// parseXORMapped decodes one XOR-MAPPED-ADDRESS value: [0, family, port^,
// address^] with the magic cookie (v4) or cookie+txID (v6) as the pad.
func parseXORMapped(val, txID []byte) (string, int, error) {
	if len(val) < 8 {
		return "", 0, fmt.Errorf("xor-mapped too short")
	}
	family := val[1]
	port := int(binary.BigEndian.Uint16(val[2:4])) ^ (stunMagicCookie >> 16)
	switch family {
	case 0x01: // IPv4
		if len(val) < 8 {
			return "", 0, fmt.Errorf("xor-mapped v4 too short")
		}
		ip := make(net.IP, 4)
		cookie := []byte{0x21, 0x12, 0xA4, 0x42}
		for i := 0; i < 4; i++ {
			ip[i] = val[4+i] ^ cookie[i]
		}
		return ip.String(), port, nil
	case 0x02: // IPv6
		if len(val) < 20 {
			return "", 0, fmt.Errorf("xor-mapped v6 too short")
		}
		pad := []byte{0x21, 0x12, 0xA4, 0x42}
		pad = append(pad, txID...)
		ip := make(net.IP, 16)
		for i := 0; i < 16; i++ {
			ip[i] = val[4+i] ^ pad[i]
		}
		return ip.String(), port, nil
	default:
		return "", 0, fmt.Errorf("unknown address family %#02x", family)
	}
}

// reflexiveEndpoint returns "host:port" as the internet sees us via the
// configured STUN server (HIVEMIND_STUN, e.g. stun.l.google.com:19302),
// or "" when unconfigured, unreachable, or untrusted. Empty is honest:
// callers treat it as "no public address known".
func reflexiveEndpoint() string {
	server := strings.TrimSpace(os.Getenv("HIVEMIND_STUN"))
	if server == "" {
		return ""
	}
	host, port, err := stunBinding(server)
	if err != nil {
		return ""
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}
