package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	hm "gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind"
	"gitlab.torproject.org/cerberus-droid/hivemind/internal/infra"
)

// The MQTT bridge is a mesh, not a single hop.
//
// Every message we accept is published to *all* reachable brokers, so the
// loss of any one broker is a non-event. Every broker is also subscribed, so
// traffic minted by other relays reaches our local bus and reaches our own
// HTTP subscribers. Because we both publish and subscribe on the same
// namespace, each message echoes back to us once per broker — dedup by
// message ID collapses those N copies (including our own) to a single
// delivery.

const (
	// mqttTopicPrefix is the namespace the bridge owns on every broker.
	mqttTopicPrefix = "hive/"

	// dedupWindow is how long a published message ID is remembered so the
	// echo arriving from each broker is only accepted once. Messages expire
	// on the relay after an hour, so an hour-long window is safe; anything
	// older than that is stale regardless.
	dedupWindow = time.Hour

	// maxSeenIDs bounds the dedup set so a long-lived relay that sees heavy
	// traffic cannot grow without limit.
	maxSeenIDs = 20000
)

// brokerPeer is one public MQTT broker in the failover mesh.
type brokerPeer struct {
	name string
	host string
	url  string
	user string
	pass string
	// secure records the transport this peer was dialled on. A peer with
	// no usable URL is left empty and reported unavailable rather than
	// quietly dialled in the clear.
	secure bool

	client mqtt.Client

	mu        sync.Mutex
	connected bool
	lastErr   string
	since     time.Time
	out       uint64
	in        uint64
	dupes     uint64
}

func (p *brokerPeer) markConnected() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.connected = true
	p.lastErr = ""
	p.since = time.Now()
}

func (p *brokerPeer) markFailed(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.connected = false
	if err != nil {
		p.lastErr = err.Error()
	}
}

func (p *brokerPeer) addOut(n uint64) {
	p.mu.Lock()
	p.out += n
	p.mu.Unlock()
}

// publishable is the single condition for handing a frame to a broker: we
// must have a client, and we must have a verified session on it. Both are
// required. A non-nil client alone proves nothing -- paho builds one before
// dialling -- and that gap is how a broker that cannot complete a verified
// handshake ends up on the receive path.
func (m *mqttMesh) publishable(p *brokerPeer) bool {
	return p.client != nil && p.hasSession()
}

// hasSession reports whether we hold a *verified* session with this broker.
// It is the single gate for "may we talk to this peer", shared by the
// publish path, the live count and the health view. It deliberately does not
// consult paho's IsConnected(), which reports true while a connection
// attempt is still in flight -- that made the publish loop and /healthz
// disagree about the same broker.
func (p *brokerPeer) hasSession() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.connected
}

func (p *brokerPeer) addIn(n uint64) {
	p.mu.Lock()
	p.in += n
	p.mu.Unlock()
}

func (p *brokerPeer) addDup(n uint64) {
	p.mu.Lock()
	p.dupes += n
	p.mu.Unlock()
}

// brokerStatus is the per-broker health record the relay publishes on
// /healthz and the world map renders.
type brokerStatus struct {
	Name       string `json:"name"`
	Host       string `json:"host"`
	URL        string `json:"url"`
	TLS        bool   `json:"tls"`
	Connected  bool   `json:"connected"`
	Outbound   uint64 `json:"outbound"`
	Inbound    uint64 `json:"inbound"`
	Duplicates uint64 `json:"duplicates"`
	Uptime     string `json:"uptime,omitempty"`
	LastError  string `json:"last_error,omitempty"`
}

// mqttMesh fans every message out to all reachable brokers and folds what
// comes back into the local bus exactly once.
type mqttMesh struct {
	peers []*brokerPeer
	r     *relay

	mu   sync.Mutex
	seen map[string]time.Time

	outbound uint64
	inbound  uint64
	dupes    uint64
	// rejected counts inbound frames that never authenticated: garbage, a
	// stale namespace, or a stranger publishing into our fleet's topics.
	// It is the number that proves the bus is not open to anyone with a
	// broker account, so it is published rather than swallowed -- which is
	// also why the bridge subscribes to its own namespace only: other
	// fleets' traffic must not be able to inflate it.
	rejected uint64
	// sealing records whether the wire is encrypted. False only when an
	// operator explicitly turns it off, and visible on /healthz so a
	// plaintext bus can never be mistaken for a protected one.
	sealing bool

	// ns is this fleet's MQTT topic namespace, derived from the root.
	ns     string
	nsOnce sync.Once
}

// newMQTTMesh builds the mesh. When explicit is non-empty only that broker
// is used (the RELAY_MQTT pin); otherwise every Prefer=true entry in the
// infrastructure matrix joins.
func newMQTTMesh(r *relay, explicit string) *mqttMesh {
	m := &mqttMesh{r: r, seen: make(map[string]time.Time), sealing: relaySealEnabled()}
	capturePahoErrors()
	announceTLSPolicy()
	announceKeyPosture()
	if !m.sealing {
		log.Printf("🚨 [MQTT] SEALING OFF (HIVEMIND_RELAY_SEAL=off) — payloads cross public brokers in the clear and anyone may inject. Not production.\n")
	}
	if explicit != "" {
		ep, ok := infra.Lookup(infra.NormalizeHost(explicit))
		if ok {
			m.peers = append(m.peers, peerFor(ep))
			return m
		}
		// A host outside the matrix is still usable. TLS on the standard
		// TLS port; cleartext only if the operator turned TLS off, since
		// nothing tells us this host even speaks TLS.
		h := infra.NormalizeHost(explicit)
		u, secure := "ssl://"+h+":8883", true
		if !mqttTLSOn() {
			u, secure = "tcp://"+h+":1883", false
		}
		m.peers = append(m.peers, &brokerPeer{name: explicit, host: h, url: u, secure: secure})
		return m
	}
	for _, ep := range infra.Preferred() {
		m.peers = append(m.peers, peerFor(ep))
	}
	return m
}

func peerFor(ep infra.Endpoint) *brokerPeer {
	p := &brokerPeer{
		name: ep.Name,
		host: ep.Host,
		user: ep.Username,
		pass: ep.Password,
	}
	p.url, p.secure = brokerURL(ep.Host, ep.TCPPort, ep.TLSPort)
	return p
}

// connectAll dials every broker concurrently — serially it would cost 3s per
// dead host. Each peer gets its own client ID (a broker rejects a second
// session using an ID it already holds) and is subscribed on success.
func (m *mqttMesh) connectAll(timeout time.Duration) {
	var wg sync.WaitGroup
	for i, p := range m.peers {
		if p.url == "" {
			continue
		}
		wg.Add(1)
		go func(i int, p *brokerPeer) {
			defer wg.Done()
			m.dial(i, p, timeout)
		}(i, p)
	}
	wg.Wait()

	live := 0
	for _, p := range m.peers {
		p.mu.Lock()
		if p.connected {
			live++
		} else if p.lastErr == "" {
			p.lastErr = "not dialled (no TCP port)"
		}
		p.mu.Unlock()
	}
	if live == 0 {
		log.Printf("📡 [MQTT] No public broker reachable — HTTP-only mode\n")
		return
	}
	log.Printf("📡 [MQTT] mesh up: %d/%d brokers connected, publishing+subscribe both ways\n", live, len(m.peers))
}

func (m *mqttMesh) dial(idx int, p *brokerPeer, timeout time.Duration) {
	if p.url == "" {
		// No transport this endpoint offers under the current policy.
		// Saying so beats dialling the cleartext port in spite of it.
		p.markFailed(fmt.Errorf("no usable transport (TLS required, endpoint offers no TLS port)"))
		log.Printf("⚠️  [MQTT] %s skipped: no TLS port in the endpoint matrix, and transport TLS is required\n", p.host)
		return
	}
	opts := mqtt.NewClientOptions().
		AddBroker(p.url).
		SetClientID(fmt.Sprintf("hivemind-relay-%d-%d", idx, time.Now().UnixNano())).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(10 * time.Second).
		SetKeepAlive(30 * time.Second).
		SetCleanSession(true)
	if p.secure {
		// paho takes the config here. ServerName is the host we dialled,
		// so the certificate must actually name the broker we asked for
		// rather than whatever the far end chooses to present.
		opts.SetTLSConfig(mqttTLSConfig(p.host))
	}
	if p.user != "" {
		opts.SetUsername(p.user)
		opts.SetPassword(p.pass)
	}
	// paho reconnects underneath us; reflect that in the health record so
	// the map never shows a dead broker as live.
	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		p.markFailed(err)
		log.Printf("📡 [MQTT] %s lost: %v\n", p.host, err)
	})
	opts.SetOnConnectHandler(func(mqtt.Client) {
		p.markConnected()
		log.Printf("📡 [MQTT] connected %s (%s)\n", p.name, p.url)
	})

	client := mqtt.NewClient(opts)
	p.client = client

	token := client.Connect()
	if !token.WaitTimeout(timeout) || token.Error() != nil {
		err := token.Error()
		if err == nil {
			err = fmt.Errorf("connect timed out after %s", timeout)
		}
		p.markFailed(err)
		log.Printf("⚠️  [MQTT] %s unreachable: %v\n", p.host, err)
		return
	}
	p.markConnected()

	// Subscribe so traffic minted elsewhere lands on our local bus.
	for _, filter := range m.mqttFilters() {
		t := client.Subscribe(filter, 1, m.onMessage)
		if !t.WaitTimeout(timeout) || t.Error() != nil {
			err := t.Error()
			if err == nil {
				err = fmt.Errorf("subscribe timed out")
			}
			log.Printf("⚠️  [MQTT] %s subscribe %s: %v\n", p.host, filter, err)
		}
	}
}

// onMessage handles one inbound MQTT delivery. The same message arrives once
// per broker it was published to, so everything funnels through markSeen.
// The delivering client is kept so inbound credit lands on the broker that
// actually carried the message — the map attributes traffic to real wires.
func (m *mqttMesh) onMessage(c mqtt.Client, msg mqtt.Message) {
	// Frames not addressed to this fleet are not ours to route. These
	// brokers are shared, so this is the first and cheapest gate: drop
	// them before spending a decryption attempt, and without counting
	// them, so another tenant's traffic can neither reach our bus nor
	// inflate the rejected counter that is our intrusion signal.
	scope := m.mqttTopic("")
	topicName := msg.Topic()
	if !strings.HasPrefix(topicName, scope) {
		// Someone else's namespaced traffic on a shared broker, and -- since
		// the cutover -- anyone's pre-v2 traffic too. Dropped before any
		// decryption attempt and without being counted, so it can neither
		// reach our bus nor inflate the intrusion signal.
		return
	}
	topicName = topicName[len(scope):]
	// Authenticate before anything else. A frame that does not open under
	// a real key is dropped unread: never parsed, never counted as seen, so
	// an anonymous publisher cannot inject content, forge an ID, or poison
	// the dedup set. This is the whole admission policy on an open bus.
	body := msg.Payload()
	if m.sealing {
		opened, _, err := hm.OpenRelayPayload(string(body))
		if err != nil {
			m.noteRejected()
			return
		}
		body = opened
	}
	var decoded message
	if err := json.Unmarshal(body, &decoded); err != nil {
		m.noteRejected()
		return
	}
	if decoded.ID == "" {
		m.noteRejected()
		return
	}
	peer := m.peerFor(c)
	if !m.markSeen(decoded.ID) {
		// Already handled — our own echo, or a duplicate from another broker.
		// Credit the duplicate to the broker that repeated it, not to all of
		// them, so the counters describe the mesh rather than the arithmetic.
		if peer != nil {
			peer.addDup(1)
		}
		m.mu.Lock()
		m.dupes++
		m.mu.Unlock()
		return
	}
	if peer != nil {
		peer.addIn(1)
	}
	m.mu.Lock()
	m.inbound++
	m.mu.Unlock()
	m.r.inject(topicName, decoded)
}

// mqttTopic is the one place a wire topic is built. Publishing and
// subscribing both go through it, because when they built the string
// independently they silently drifted: the bridge published to the bare
// topic while subscribing to the namespaced one, so it stopped seeing
// its own echoes and nothing failed loudly. One function, no drift.
func (m *mqttMesh) mqttTopic(topicName string) string {
	return mqttTopicPrefix + m.mqttNamespace() + "/" + topicName
}

// mqttSubscribeFilter covers exactly what mqttTopic produces.
func (m *mqttMesh) mqttSubscribeFilter() string {
	return m.mqttTopic("#")
}

// mqttFilters is every filter this node listens on, which is more than one
// topic helper can express:
//
//	own namespace   always, so we hear our own traffic
//	previous root   while a rotation is live, so old-root frames are
//	                actually delivered rather than never delivered
//
// There is deliberately no legacy "hive/+/#" filter here, and no way to
// add one. That filter is what a rolling v1 -> v2 upgrade would need, and
// it is why the upgrade is a cutover instead: it drags in every other
// hivemind fleet's unnamespaced v1 traffic, and accepting v1 means
// accepting frames that a leaked forward-chained day root can forge. See
// DecryptLegacyFrame for reading old data offline, and the README runbook
// for the migration.
func (m *mqttMesh) mqttFilters() []string {
	filters := []string{m.mqttSubscribeFilter()}
	if hm.RootRotationActive() {
		for _, ns := range hm.Namespaces()[1:] {
			filters = append(filters, mqttTopicPrefix+ns+"/#")
		}
	}
	return filters
}

// mqttNamespace is this fleet's topic namespace, resolved once and cached
// so every broker and every publish agrees on it.
func (m *mqttMesh) mqttNamespace() string {
	m.nsOnce.Do(func() {
		ns := hm.Namespace()
		if ns == "" {
			// No root means no namespace. Fail closed into an unroutable
			// prefix rather than broadcasting onto the shared bare topic.
			ns = "no-key"
		}
		m.ns = ns
	})
	return m.ns
}

// noteRejected counts one frame refused at the door. Kept out of the
// dedup path deliberately: a stranger must not be able to spend our
// bounded dedup budget by flooding unauthenticated traffic.
func (m *mqttMesh) noteRejected() {
	m.mu.Lock()
	m.rejected++
	m.mu.Unlock()
}

// peerFor maps a paho client back to the peer that owns it.
func (m *mqttMesh) peerFor(c mqtt.Client) *brokerPeer {
	if c == nil {
		return nil
	}
	for _, p := range m.peers {
		if p.client == c {
			return p
		}
	}
	return nil
}

// markSeen records an ID and reports whether it was new.
func (m *mqttMesh) markSeen(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	if _, dup := m.seen[id]; dup {
		return false
	}
	// Keep the set bounded. Prune expired IDs first; if a burst of fresh IDs
	// still fills it, drop the oldest entries outright so the set can never
	// exceed its bound. Forgetting an ID only costs a duplicate delivery, so
	// evicting early is the safe direction to fail in.
	if len(m.seen) >= maxSeenIDs {
		for k, t := range m.seen {
			if now.Sub(t) > dedupWindow {
				delete(m.seen, k)
			}
		}
		for len(m.seen) >= maxSeenIDs {
			oldestKey, oldest := "", time.Time{}
			for k, t := range m.seen {
				if oldestKey == "" || t.Before(oldest) {
					oldestKey, oldest = k, t
				}
			}
			if oldestKey == "" {
				break
			}
			delete(m.seen, oldestKey)
		}
	}
	m.seen[id] = now
	return true
}

// publish fans a message out to every connected broker. A broker that is
// down costs one lost copy, not the message.
//
// When sealing is on (the default) the body is AES-256-GCM ciphertext under
// this hour's ratchet key, so broker operators and every observer between
// us and them see only ciphertext. Sealing fails closed: with no usable key
// nothing is published at all, because a relay that quietly downgrades to
// plaintext the first time a key fails to derive is worse than one that
// drops the message and says so.
func (m *mqttMesh) publish(topicName string, msg message) {
	plain, err := json.Marshal(msg)
	if err != nil {
		return
	}
	payload := plain
	if m.sealing {
		sealed, err := hm.SealRelayPayload(plain)
		if err != nil {
			log.Printf("🚨 [MQTT] refusing to publish %q unsealed: %v\n", topicName, err)
			return
		}
		payload = []byte(sealed)
	}
	for _, p := range m.peers {
		if !m.publishable(p) {
			continue
		}
		if t := p.client.Publish(m.mqttTopic(topicName), 1, false, payload); t.WaitTimeout(2*time.Second) && t.Error() != nil {
			log.Printf("⚠️  [MQTT] publish to %s: %v\n", p.host, t.Error())
			continue
		}
		p.addOut(1)
		m.mu.Lock()
		m.outbound++
		m.mu.Unlock()
	}
}

// relaySealEnabled reports whether the MQTT wire is encrypted. On unless an
// operator opts out, and the opt-out is loud, logged, and reported on
// /healthz — never a silent downgrade.
func relaySealEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("HIVEMIND_RELAY_SEAL"))) {
	case "off", "0", "false", "no":
		return false
	default:
		return true
	}
}

// live counts brokers currently connected.
// cleartextHosts counts peers actually dialled without TLS. Any non-zero
// value means the channel is exposed and /healthz says so.
func (m *mqttMesh) cleartextHosts() int {
	n := 0
	for _, p := range m.peers {
		if p.url != "" && !p.secure {
			n++
		}
	}
	return n
}

// tlsHosts counts peers dialled over TLS, so /healthz can say "fully
// encrypted channel" rather than leaving it to be inferred from URLs.
func (m *mqttMesh) tlsHosts() int {
	n := 0
	for _, p := range m.peers {
		if p.secure {
			n++
		}
	}
	return n
}

// live counts brokers we have a verified session with. It reads the same
// p.connected flag the per-broker health view does, deliberately: the
// client's own IsConnected() reports true while a connection attempt is
// still in flight, so a broker whose certificate cannot be verified was
// counted as live here and not connected there -- the two views
// disagreeing about the same broker, which is worse than either being
// wrong alone.
func (m *mqttMesh) live() int {
	n := 0
	for _, p := range m.peers {
		if p.hasSession() {
			n++
		}
	}
	return n
}

// snapshot returns per-broker health, in matrix order.
func (m *mqttMesh) snapshot() []brokerStatus {
	out := make([]brokerStatus, 0, len(m.peers))
	for _, p := range m.peers {
		p.mu.Lock()
		st := brokerStatus{
			Name: p.name,
			Host: p.host,
			URL:  p.url,
			TLS:  p.secure,
			// p.connected, not p.client.IsConnected(): paho reports a
			// connection before the handshake has been verified, so a
			// broker whose certificate cannot be validated looked "live"
			// on the map while carrying nothing. This flag is set only by
			// our OnConnect handler, which fires after a completed MQTT
			// CONNACK over an established, verified TLS session.
			Connected:  p.connected,
			Outbound:   p.out,
			Inbound:    p.in,
			Duplicates: p.dupes,
			LastError:  p.lastErr,
		}
		if !p.since.IsZero() && st.Connected {
			st.Uptime = time.Since(p.since).Truncate(time.Second).String()
		}
		p.mu.Unlock()
		out = append(out, st)
	}
	return out
}

// meshStats is the aggregate counters block on /healthz.
type meshStats struct {
	Brokers       int    `json:"brokers"`
	Live          int    `json:"live"`
	Outbound      uint64 `json:"outbound"`
	Inbound       uint64 `json:"inbound"`
	Duplicates    uint64 `json:"duplicates"`
	Rejected      uint64 `json:"rejected"`
	Sealed        bool   `json:"sealed"`
	TLS           bool   `json:"tls"`
	Cleartext     int    `json:"cleartext_hosts"`
	TLSHosts      int    `json:"tls_hosts"`
	Topic         string `json:"topic_prefix"`
	Bidirectional bool   `json:"bidirectional"`
	// Key tier, so /healthz answers "which key is this node speaking"
	// instead of leaving it to be inferred from a matching ciphertext.
	KeyTier    string `json:"key_tier"`
	Namespace  string `json:"namespace"`
	KeyRoots   int    `json:"key_roots"`
	KeySource  string `json:"key_source"`
	LeafWindow int    `json:"leaf_window_minutes"`
	Rotating   bool   `json:"root_rotation"`
	Namespaces int    `json:"listening_namespaces"`
}

func (m *mqttMesh) stats() meshStats {
	m.mu.Lock()
	defer m.mu.Unlock()
	tier := hm.CurrentKeyTier()
	return meshStats{
		Brokers:    len(m.peers),
		Live:       m.live(),
		Outbound:   m.outbound,
		Inbound:    m.inbound,
		Duplicates: m.dupes,
		Rejected:   m.rejected,
		Sealed:     m.sealing,
		// "no dialled peer is in the clear". A peer with no usable
		// transport is not dialled at all, so it cannot leak; counting it
		// as a TLS failure would report a fleet as unencrypted when the
		// only problem is that one broker offers no TLS port.
		TLS:           m.cleartextHosts() == 0,
		Cleartext:     m.cleartextHosts(),
		Topic:         mqttTopicPrefix,
		Bidirectional: m.inbound > 0,
		TLSHosts:      m.tlsHosts(),
		KeyTier:       tier.Tier,
		Namespace:     tier.Namespace,
		KeyRoots:      tier.Roots,
		KeySource:     tier.Source,
		LeafWindow:    tier.Window,
		Rotating:      hm.RootRotationActive(),
		Namespaces:    len(hm.Namespaces()),
	}
}
