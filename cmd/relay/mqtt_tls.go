package main

import (
	"crypto/tls"
	"log"
	"os"
	"strconv"
	"strings"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	hm "gitlab.torproject.org/cerberus-droid/hivemind/internal/hivemind"
)

// MQTT transport policy.
//
// The bus payload is sealed and authenticated, but until now it crossed
// these public brokers in a cleartext TCP session. That leaves the whole
// channel exposed: an observer on the path reads the topic names, the
// frame sizes and the timing of every hive, and can drop or replay frames
// even though they cannot forge one. Sealing buys confidentiality of
// content; only TLS buys confidentiality of the channel.
//
// TLS is therefore the default and the only sane production posture, so
// this does not degrade quietly. A broker whose certificate will not
// verify, or which offers no TLS port at all, is reported unavailable with
// the reason -- it is not silently downgraded, because a fleet that
// believes it is encrypted while it is not is worse than a fleet that
// knows it has three brokers instead of six.
//
// Verification is strict and always is: system roots, hostname match,
// TLS 1.2 floor. There is deliberately no InsecureSkipVerify knob. An
// operator who genuinely cannot use TLS has a better answer available --
// put a TLS terminator in front of their own broker -- than a switch that
// disables authentication of the server.
const (
	// mqttTLSDefault requires TLS.
	mqttTLSDefault = true
	// mqttTLSMin is the floor. 1.3 is negotiated wherever offered.
	mqttTLSMin = tls.VersionTLS12
)

// mqttTLSOn reports whether TLS is required. HIVEMIND_MQTT_TLS=off is
// the explicit, loud, health-visible escape hatch for a broker this
// network cannot reach over TLS.
func mqttTLSOn() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("HIVEMIND_MQTT_TLS"))) {
	case "off", "0", "false", "no":
		return false
	}
	return mqttTLSDefault
}

// mqttTLSConfig is the verification config for a broker hostname. Built
// per host so ServerName is always the name we dialled, never empty --
// an empty ServerName is how a client ends up trusting whatever
// certificate the far end presents.
func mqttTLSConfig(host string) *tls.Config {
	return &tls.Config{
		ServerName: host,
		MinVersion: mqttTLSMin,
		// RootCAs nil means the host's system trust store, which is the
		// point: these are public brokers with publicly trusted certs.
		RootCAs:            nil,
		InsecureSkipVerify: false,
	}
}

// brokerURL picks the transport for one endpoint, in one place, so the
// URL dialled and the TLS policy applied cannot disagree.
//
// Returns an empty URL when the endpoint offers no usable transport under
// the current policy, which marks it unavailable rather than quietly
// reaching for the cleartext port.
func brokerURL(host string, tcpPort, tlsPort int) (url string, secure bool) {
	if mqttTLSOn() {
		if tlsPort <= 0 {
			return "", false
		}
		return "ssl://" + host + ":" + strconv.Itoa(tlsPort), true
	}
	if tcpPort <= 0 {
		return "", false
	}
	return "tcp://" + host + ":" + strconv.Itoa(tcpPort), false
}

// announceTLSPolicy states the transport posture once at startup, and
// loudly when it has been turned off.
func announceTLSPolicy() {
	if mqttTLSOn() {
		log.Printf("🔒 [MQTT] transport TLS required (min 1.2, full verification, system roots)\n")
		return
	}
	log.Printf("🚨 [MQTT] TRANSPORT TLS OFF (HIVEMIND_MQTT_TLS=off) — topic names, frame sizes and timing are visible to the network, and frames can be dropped or replayed. Payloads stay sealed, but the channel does not. Not production.\n")
}

// announceKeyPosture states the live key configuration once at startup.
//
// The v1 grace this used to describe is gone by design, not by setting: no
// environment variable can put pre-v2 frames back on the bus. What is left
// to announce is a root rotation, because that genuinely changes what we
// listen on and an operator should not have to read the source to know it.
func announceKeyPosture() {
	if hm.RootRotationActive() {
		log.Printf("🔄 [KEYS] root rotation active — listening on %d namespaces so old-root frames are delivered\n", len(hm.Namespaces()))
	}
}

// capturePahoErrors routes the client's own loggers into ours. Without
// this, a certificate that cannot be verified fails inside the library and
// the operator sees nothing but a broker that never comes up -- which is
// indistinguishable, from the outside, from a network problem. paho's
// loggers are a NOOP by default, so nothing was being reported at all.
func capturePahoErrors() {
	l := log.New(os.Stderr, "🔌 [paho] ", log.LstdFlags)
	mqtt.ERROR = l
	mqtt.WARN = l
	mqtt.CRITICAL = l
	// DEBUG stays a NOOP: it is per-packet and would drown the log.
}
