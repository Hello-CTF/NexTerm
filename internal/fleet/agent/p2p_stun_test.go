package agent

import (
	"slices"
	"testing"
)

func TestPublicSTUNHost(t *testing.T) {
	for host, want := range map[string]bool{
		"nx.example.com": true, "203.0.113.7": true, "2001:db8::1": true, "[2001:db8::1]": true,
		"127.0.0.1": false, "::1": false, "0.0.0.0": false, "::": false, "10.0.0.5": false, "192.168.1.2": false,
		"172.16.0.1": false, "fd00::1": false, "169.254.1.1": false, "fe80::1": false,
		"localhost": false, "app.localhost": false, "nexterm": false, "": false,
	} {
		if got := PublicSTUNHost(host); got != want {
			t.Errorf("PublicSTUNHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestSTUNServersAddsTheConfiguredServerHost(t *testing.T) {
	config := &P2PConfig{STUN: []P2PEndpoint{{Host: "nx.example.com", Port: 3478}, {Host: "stun.example.net", Port: 3478}}, STUNPorts: []int{3478, 5349}}
	cases := []struct {
		name    string
		url     string
		proxied bool
		want    []string
	}{
		{"same host deduplicates", "https://nx.example.com", false, []string{"nx.example.com:3478", "stun.example.net:3478", "nx.example.com:5349"}},
		{"another public name", "https://term.example.org:8443/base", false, []string{"nx.example.com:3478", "stun.example.net:3478", "term.example.org:3478", "term.example.org:5349"}},
		{"public IPv6 literal", "https://[2001:db8::7]", false, []string{"nx.example.com:3478", "stun.example.net:3478", "[2001:db8::7]:3478", "[2001:db8::7]:5349"}},
		{"private IP literal skipped", "http://192.168.1.10:8080", false, []string{"nx.example.com:3478", "stun.example.net:3478"}},
		{"loopback skipped", "http://127.0.0.1:8080", false, []string{"nx.example.com:3478", "stun.example.net:3478"}},
		{"localhost skipped", "http://localhost:8080", false, []string{"nx.example.com:3478", "stun.example.net:3478"}},
		{"manual proxy skips the own host", "https://term.example.org", true, []string{"nx.example.com:3478", "stun.example.net:3478"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := STUNServers(config, tc.url, tc.proxied); !slices.Equal(got, tc.want) {
				t.Fatalf("STUNServers = %v, want %v", got, tc.want)
			}
		})
	}
	// A server that derived nothing (proxy rewrote Host) still yields the configured host with the bound ports.
	if got := STUNServers(&P2PConfig{STUN: []P2PEndpoint{}, STUNPorts: []int{3478}}, "https://nx.example.com", false); !slices.Equal(got, []string{"nx.example.com:3478"}) {
		t.Fatalf("STUNServers without a server list = %v", got)
	}
	if got := STUNServers(&P2PConfig{STUN: []P2PEndpoint{}, STUNPorts: []int{}}, "https://nx.example.com", false); len(got) != 0 {
		t.Fatalf("STUNServers with the responder off = %v", got)
	}
}
