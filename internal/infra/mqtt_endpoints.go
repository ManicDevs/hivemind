// Package infra holds the target infrastructure data matrix for the
// public MQTT brokers the optional relay bridge may dial.
//
// Hosts are strictly hostnames (no scheme, no path, no URI). Schemes are
// applied only when building broker URLs from the port matrix.
package infra

import (
	"strconv"
	"strings"
)

// Endpoint is one public MQTT broker with all supported transport ports.
// A port value of 0 means that transport is not offered (matrix "null").
// Prefer=false keeps a matrix row for the record but skips it in the
// relay auto-fallback walk (hosts that time out from this network).
type Endpoint struct {
	Name     string
	Host     string
	TCPPort  int
	TLSPort  int
	WSPort   int
	WSSPort  int
	Username string // empty = anonymous
	Password string
	Prefer   bool // true = eligible for auto-fallback
}

// Endpoints is the TARGET INFRASTRUCTURE DATA MATRIX (NODE_1..NODE_8).
// Hosts are strictly hostnames; ports are bound per protocol.
// Prefer marks hosts that answer TCP from a typical network; NODE_4 and
// NODE_5 stay in the matrix but are skipped by auto-fallback when false.
var Endpoints = []Endpoint{
	{
		Name:    "EMQX Open Public Cluster",
		Host:    "broker.emqx.io",
		TCPPort: 1883,
		TLSPort: 8883,
		WSPort:  8083,
		WSSPort: 8084,
		Prefer:  true,
	},
	{
		Name:    "HiveMQ Sandbox Cluster",
		Host:    "broker.hivemq.com",
		TCPPort: 1883,
		TLSPort: 8883,
		WSPort:  8000,
		WSSPort: 8000,
		Prefer:  true,
	},
	{
		Name:    "Eclipse Mosquitto Global Test System",
		Host:    "test.mosquitto.org",
		TCPPort: 1883,
		TLSPort: 8883,
		WSPort:  8080,
		WSSPort: 8081,
		Prefer:  true,
	},
	{
		Name:    "Eclipse Foundation IoT Sandbox Project",
		Host:    "mqtt.eclipseprojects.io",
		TCPPort: 1883,
		TLSPort: 8883,
		WSPort:  80,
		WSSPort: 443,
		Prefer:  false, // DNS ok, TCP times out from this network
	},
	{
		Name:    "MQTTHQ Public Developer Sandbox",
		Host:    "mqtthq.com", // matrix had "://mqtthq.com" — host only
		TCPPort: 1883,
		TLSPort: 0,
		WSPort:  8083,
		WSSPort: 0,
		Prefer:  false, // DNS ok, TCP times out from this network
	},
	{
		Name:    "Coreflux Open Cloud Instance",
		Host:    "iot.coreflux.cloud",
		TCPPort: 1883,
		TLSPort: 8883,
		WSPort:  5000,
		WSSPort: 443,
		Prefer:  true,
	},
	{
		Name:    "Bevywise CrystalMQ Sandbox Network",
		Host:    "broker.bevywise.com",
		TCPPort: 1883,
		TLSPort: 0,
		WSPort:  10443,
		WSSPort: 0,
		Prefer:  true,
	},
	{
		Name:     "FreeMQTT Public Cloud Instance",
		Host:     "broker.freemqtt.com",
		TCPPort:  1883,
		TLSPort:  8883,
		WSPort:   8083,
		WSSPort:  8084,
		Username: "freemqtt",
		Password: "public",
		Prefer:   true,
	},
}

// Preferred returns matrix entries eligible for auto-fallback (Prefer).
func Preferred() []Endpoint {
	out := make([]Endpoint, 0, len(Endpoints))
	for _, e := range Endpoints {
		if e.Prefer {
			out = append(out, e)
		}
	}
	return out
}

// NormalizeHost strips any accidental scheme/path from a matrix host so
// callers only ever see a bare hostname.
func NormalizeHost(host string) string {
	h := strings.TrimSpace(host)
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	if i := strings.IndexAny(h, "/?#"); i >= 0 {
		h = h[:i]
	}
	if i := strings.LastIndex(h, ":"); i >= 0 && !strings.Contains(h[i:], "]") {
		// drop :port if someone embedded one — ports come from the matrix
		if _, err := strconv.Atoi(h[i+1:]); err == nil {
			h = h[:i]
		}
	}
	return h
}

// TCPBrokerURLs returns plain-TCP broker URLs in matrix order (primary bridge).
// Endpoints without a TCP port are skipped.
func TCPBrokerURLs() []string {
	return brokerURLs("tcp://", func(e Endpoint) int { return e.TCPPort })
}

// TLSBrokerURLs returns TLS broker URLs in matrix order (secure fallback).
// Endpoints without a TLS port are skipped.
func TLSBrokerURLs() []string {
	return brokerURLs("ssl://", func(e Endpoint) int { return e.TLSPort })
}

// WSBrokerURLs returns WebSocket broker URLs in matrix order.
func WSBrokerURLs() []string {
	return brokerURLs("ws://", func(e Endpoint) int { return e.WSPort })
}

// WSSBrokerURLs returns WebSocket-over-TLS broker URLs in matrix order.
func WSSBrokerURLs() []string {
	return brokerURLs("wss://", func(e Endpoint) int { return e.WSSPort })
}

// Lookup returns the endpoint for a hostname, if present.
func Lookup(host string) (Endpoint, bool) {
	n := NormalizeHost(host)
	for _, e := range Endpoints {
		if e.Host == n {
			return e, true
		}
	}
	return Endpoint{}, false
}

func brokerURLs(scheme string, port func(Endpoint) int) []string {
	urls := make([]string, 0, len(Endpoints))
	for _, e := range Endpoints {
		p := port(e)
		if p <= 0 {
			continue
		}
		urls = append(urls, scheme+NormalizeHost(e.Host)+":"+strconv.Itoa(p))
	}
	return urls
}
