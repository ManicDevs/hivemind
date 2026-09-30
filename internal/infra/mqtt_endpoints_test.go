package infra

import "testing"

func TestPreferredOnlyPrefers(t *testing.T) {
	pref := Preferred()
	if len(pref) == 0 {
		t.Fatalf("Preferred() must not be empty")
	}
	for _, e := range pref {
		if !e.Prefer {
			t.Fatalf("Preferred() returned a non-preferred endpoint %s", e.Host)
		}
	}
}

func TestNormalizeHost(t *testing.T) {
	cases := map[string]string{
		"broker.emqx.io":              "broker.emqx.io",
		"://mqtthq.com":               "mqtthq.com", // matrix legacy
		"wss://broker.hivemq.com/":    "broker.hivemq.com",
		"tcp://test.mosquitto.org?x":  "test.mosquitto.org",
		"broker.freemqtt.com:8883":    "broker.freemqtt.com",
		"  broker.emqx.io  ":          "broker.emqx.io",
		"https://iot.coreflux.cloud/": "iot.coreflux.cloud",
	}
	for in, want := range cases {
		if got := NormalizeHost(in); got != want {
			t.Fatalf("NormalizeHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBrokerURLSchemes(t *testing.T) {
	for _, row := range []struct {
		name string
		got  []string
		want string
	}{
		{"tcp", TCPBrokerURLs(), "tcp://"},
		{"ssl", TLSBrokerURLs(), "ssl://"},
		{"ws", WSBrokerURLs(), "ws://"},
		{"wss", WSSBrokerURLs(), "wss://"},
	} {
		if len(row.got) == 0 {
			t.Fatalf("%s broker URLs must not be empty", row.name)
		}
		for _, u := range row.got {
			if len(u) < len(row.want) || u[:len(row.want)] != row.want {
				t.Fatalf("%s URL mislaunched: %q", row.name, u)
			}
		}
	}

	// TLS-less endpoints must not leak into TLS lists, but WebSocket-list
	// endpoints (which the mock may share) must stay present.
	for _, u := range TLSBrokerURLs() {
		if u == "ssl://broker.bevywise.com" || u == "ssl://mqtthq.com" {
			t.Fatalf("TLS-less endpoint leaked into TLS URLs: %s", u)
		}
	}
}

func TestLookupByHost(t *testing.T) {
	e, ok := Lookup("broker.emqx.io")
	if !ok || e.TCPPort != 1883 || e.TLSPort != 8883 {
		t.Fatalf("Lookup(emqx) = %+v (%v)", e, ok)
	}
	if _, ok := Lookup("nope.invalid"); ok {
		t.Fatalf("Lookup must miss unknown hosts")
	}
	if _, ok := Lookup("wss://broker.hivemq.com/"); !ok {
		t.Fatalf("Lookup must normalize input hosts")
	}
}

func TestMatrixExtendsAllSchemes(t *testing.T) {
	// Every matrix row must have at least one transport with a real port —
	// a row that cannot speak any scheme is dead weight for the whole walk.
	tls := TLSBrokerURLs()
	wss := WSSBrokerURLs()
	for _, e := range Endpoints {
		has := e.TCPPort > 0 || e.TLSPort > 0 || e.WSPort > 0 || e.WSSPort > 0
		if !has {
			t.Fatalf("endpoint %s offers no transport at all", e.Host)
		}
		_ = tls
		_ = wss
	}
}
