package main

import (
	"crypto/tls"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"strconv"
	"strings"
	"testing"

	"gitlab.torproject.org/cerberus-droid/hivemind/internal/infra"
)

// TestTransportTLSIsTheDefault is the fix for the cleartext-channel
// caveat: the bus must not silently fall back to tcp://1883.
func TestTransportTLSIsTheDefault(t *testing.T) {
	t.Setenv("HIVEMIND_MQTT_TLS", "")
	if !mqttTLSOn() {
		t.Fatal("transport TLS is not the default")
	}
}

// TestBrokerURLPrefersTLS checks the URL actually built for the endpoints
// the relay dials, including the ones that have no TLS port at all.
func TestBrokerURLPrefersTLS(t *testing.T) {
	t.Setenv("HIVEMIND_MQTT_TLS", "")

	for _, ep := range infra.Preferred() {
		url, secure := brokerURL(ep.Host, ep.TCPPort, ep.TLSPort)
		if ep.TLSPort > 0 {
			if !secure {
				t.Fatalf("%s has TLS %d but was dialled in the clear (%q)", ep.Host, ep.TLSPort, url)
			}
			if !strings.HasPrefix(url, "ssl://") {
				t.Fatalf("%s: expected an ssl:// URL, got %q", ep.Host, url)
			}
			if !strings.HasSuffix(url, ":"+strconv.Itoa(ep.TLSPort)) {
				t.Fatalf("%s: dialled the wrong port: %q", ep.Host, url)
			}
			continue
		}
		// No TLS port offered: it must be refused, never dialled clear.
		if url != "" {
			t.Fatalf("%s offers no TLS but produced URL %q — silent downgrade", ep.Host, url)
		}
		if secure {
			t.Fatalf("%s: reported secure with no TLS port", ep.Host)
		}
	}
}

// TestTLSOptOutIsExplicitAndUnambiguous: the escape hatch exists, but only
// when an operator asks for it by name.
func TestTLSOptOutIsExplicitAndUnambiguous(t *testing.T) {
	for _, v := range []string{"off", "OFF", "0", "false", "no"} {
		t.Setenv("HIVEMIND_MQTT_TLS", v)
		if mqttTLSOn() {
			t.Fatalf("HIVEMIND_MQTT_TLS=%q did not disable TLS", v)
		}
	}
	// Anything else -- including a typo -- keeps TLS on. A downgrade must
	// never be the result of a misspelling.
	for _, v := range []string{"", "on", "1", "true", "yes", "of", "offf", "tls"} {
		t.Setenv("HIVEMIND_MQTT_TLS", v)
		if !mqttTLSOn() {
			t.Fatalf("HIVEMIND_MQTT_TLS=%q silently disabled TLS", v)
		}
	}
	t.Setenv("HIVEMIND_MQTT_TLS", "off")
	if url, secure := brokerURL("broker.emqx.io", 1883, 8883); url != "tcp://broker.emqx.io:1883" || secure {
		t.Fatalf("opt-out did not produce the cleartext URL: %q secure=%v", url, secure)
	}
}

// TestTLSConfigNeverSkipsVerification is the guarantee that makes the TLS
// worth having. There is no InsecureSkipVerify knob, ServerName is always
// the dialled host (an empty one would trust whatever was presented), and
// the floor is TLS 1.2.
func TestTLSConfigNeverSkipsVerification(t *testing.T) {
	for _, host := range []string{"broker.emqx.io", "broker.hivemq.com", "test.mosquitto.org"} {
		cfg := mqttTLSConfig(host)
		if cfg.InsecureSkipVerify {
			t.Fatalf("%s: certificate verification disabled", host)
		}
		if cfg.ServerName != host {
			t.Fatalf("%s: ServerName is %q, not the dialled host", host, cfg.ServerName)
		}
		if cfg.MinVersion < tls.VersionTLS12 {
			t.Fatalf("%s: TLS floor is 0x%x, below 1.2", host, cfg.MinVersion)
		}
		if cfg.RootCAs != nil {
			t.Fatalf("%s: RootCAs pinned, which is not what public brokers need", host)
		}
	}
}

// TestLiveCountAgreesWithPerBrokerHealth stops the two views of "is this
// broker up" from diverging. They did: the aggregate used the client's
// own IsConnected(), which reports true while a connection attempt is
// still in flight, so a broker whose certificate cannot be verified was
// counted live in /healthz.mesh while showing connected:false in the
// broker list. An operator reading the summary would have believed a
// broken TLS broker was carrying traffic.
func TestLiveCountAgreesWithPerBrokerHealth(t *testing.T) {
	busKey(t)
	m := newMQTTMesh(newRelay(), "")

	// Nothing has completed a verified session yet, so nothing is live.
	if got := m.live(); got != 0 {
		t.Fatalf("live=%d before any broker connected", got)
	}
	for _, b := range m.snapshot() {
		if b.Connected {
			t.Fatalf("%s reported connected with no session", b.Host)
		}
	}

	// Flip the verified flag the way our OnConnect handler does, and both
	// views must move together.
	m.peers[0].markConnected()
	if got, snap := m.live(), connectedIn(m.snapshot()); got != snap {
		t.Fatalf("live=%d but %d brokers report connected", got, snap)
	}
	if m.live() != 1 {
		t.Fatalf("live=%d after one verified session, want 1", m.live())
	}

	// And a failure clears it from both.
	m.peers[0].markFailed(errTest{})
	if got, snap := m.live(), connectedIn(m.snapshot()); got != snap || got != 0 {
		t.Fatalf("after failure live=%d connected=%d, want 0 and agreement", got, snap)
	}
}

func connectedIn(bs []brokerStatus) int {
	n := 0
	for _, b := range bs {
		if b.Connected {
			n++
		}
	}
	return n
}

type errTest struct{}

func (errTest) Error() string { return "test" }

// TestPublishGateUsesVerifiedSession pins the rule that we only publish to a
// broker we have a verified session with. The publish loop used to consult
// paho's IsConnected(), the same optimistic flag that made health claim a
// certificate-broken broker was live. Health and the wire disagreed about
// who was connected, which is the sort of thing that hides a broken broker
// until you go looking for the traffic it was supposed to carry.
func TestPublishGateUsesVerifiedSession(t *testing.T) {
	busKey(t)
	m := newMQTTMesh(newRelay(), "")
	p := m.peers[0]

	// No client yet: nothing to publish on.
	if m.publishable(p) {
		t.Fatal("publishable with no client")
	}

	// The subtle case. Give it a real paho client, as connectAll would.
	// paho builds the client before dialling, so a non-nil client says
	// nothing about whether the TLS handshake ever verified. If we trusted
	// it, a broker that cannot complete verification would still be handed
	// frames.
	p.client = mqtt.NewClient(mqtt.NewClientOptions())
	if p.hasSession() {
		t.Fatal("unexpected verified session")
	}
	if m.publishable(p) {
		t.Fatal("publishable on a client with no verified session: frames " +
			"would be offered to a broker whose TLS never validated")
	}

	// Verified session, but no client object to publish through.
	q := m.peers[1]
	q.markConnected()
	if m.publishable(q) {
		t.Fatal("publishable without a client")
	}

	// Both present, and only now is a frame allowed out.
	p.markConnected()
	if !m.publishable(p) {
		t.Fatal("not publishable despite client and verified session")
	}

	// A verification failure closes the gate again.
	p.markFailed(errTest{})
	if m.publishable(p) {
		t.Fatal("failed broker is still publishable")
	}
}
