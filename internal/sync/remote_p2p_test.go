package sync

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/netproxy"
)

// The direct path is ruled out on the same grounds as QUIC on the relay: any proxy, manual or from the
// environment, and plain HTTP or unverified TLS.
func TestP2PEndpointMirrorsTheRelayChecks(t *testing.T) {
	envProxied := map[string]bool{}
	original := proxyFromEnvironment
	proxyFromEnvironment = func(request *http.Request) (*url.URL, error) {
		if envProxied[request.URL.Host] {
			return url.Parse("http://proxy.example.com:3128")
		}
		return nil, nil
	}
	t.Cleanup(func() { proxyFromEnvironment = original })
	envProxied["env.example.com"] = true

	for _, tc := range []struct {
		name     string
		config   RemoteConfig
		proxied  bool
		insecure bool
	}{
		{"verified https", RemoteConfig{URL: "https://nx.example.com"}, false, false},
		{"manual proxy", RemoteConfig{URL: "https://nx.example.com", Proxy: netproxy.Manual{Type: netproxy.TypeHTTP, Host: "proxy.example.com", Port: 3128}}, true, false},
		{"environment proxy", RemoteConfig{URL: "https://env.example.com"}, true, false},
		{"unverified tls", RemoteConfig{URL: "https://nx.example.com", Insecure: true}, false, true},
		{"plain http", RemoteConfig{URL: "http://nx.example.com"}, false, true},
	} {
		endpoint := p2pEndpoint(tc.config)
		if endpoint.Proxied != tc.proxied || endpoint.Insecure != tc.insecure || endpoint.URL != tc.config.URL {
			t.Errorf("%s: endpoint = %+v", tc.name, endpoint)
		}
	}
}
