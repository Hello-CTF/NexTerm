// Package netproxy resolves the HTTP proxy for outbound requests that must
// work where the network needs one, such as update checks and downloads.
//
// The proxy environment variables come first: HTTPS_PROXY, HTTP_PROXY and,
// unlike net/http, ALL_PROXY (which curl and most tools read, and which may
// name a socks5 proxy), with NO_PROXY excluding hosts. A desktop app started
// from the Dock or Finder inherits none of them, so when none is set on macOS
// the network settings' proxies apply. Other systems use the environment
// only. A proxy auto-configuration (PAC) script is not evaluated.
package netproxy

import (
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http/httpproxy"
)

// systemCacheTTL bounds how long a resolved system proxy is reused, so a
// changed setting applies without a restart.
const systemCacheTTL = 30 * time.Second

func getenv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

// environment returns the proxy configuration of the environment and whether
// any proxy variable is set.
func environment() (httpproxy.Config, bool) {
	all := getenv("ALL_PROXY", "all_proxy")
	config := httpproxy.Config{
		HTTPProxy:  getenv("HTTP_PROXY", "http_proxy"),
		HTTPSProxy: getenv("HTTPS_PROXY", "https_proxy"),
		NoProxy:    getenv("NO_PROXY", "no_proxy"),
	}
	if config.HTTPProxy == "" {
		config.HTTPProxy = all
	}
	if config.HTTPSProxy == "" {
		config.HTTPSProxy = all
	}
	return config, config.HTTPProxy != "" || config.HTTPSProxy != ""
}

type resolver struct {
	system func() httpproxy.Config
	now    func() time.Time

	mu       sync.Mutex
	cached   func(*url.URL) (*url.URL, error)
	cachedAt time.Time
}

func (r *resolver) proxy(request *http.Request) (*url.URL, error) {
	if config, ok := environment(); ok {
		return config.ProxyFunc()(request.URL)
	}
	r.mu.Lock()
	if r.cached == nil || r.now().Sub(r.cachedAt) >= systemCacheTTL {
		config := r.system()
		r.cached = config.ProxyFunc()
		r.cachedAt = r.now()
	}
	proxy := r.cached
	r.mu.Unlock()
	return proxy(request.URL)
}

var shared = &resolver{system: systemConfig, now: time.Now}

// Proxy is an http.Transport Proxy function: the environment's proxy, or the
// system proxy when the environment sets none.
func Proxy(request *http.Request) (*url.URL, error) { return shared.proxy(request) }

// Transport returns a copy of http.DefaultTransport that uses Proxy.
func Transport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = Proxy
	return transport
}

// proxyURL turns a proxy setting into a URL: a bare host:port is an HTTP
// proxy.
func proxyURL(scheme, hostPort string) string {
	hostPort = strings.TrimSpace(hostPort)
	if hostPort == "" {
		return ""
	}
	if strings.Contains(hostPort, "://") {
		return hostPort
	}
	return scheme + "://" + hostPort
}

// noProxyList turns system bypass entries into NO_PROXY form: "*.example"
// becomes ".example" and entries NO_PROXY cannot express are dropped.
func noProxyList(entries []string) string {
	var out []string
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		switch {
		case entry == "" || strings.EqualFold(entry, "<local>"):
			continue
		case strings.HasPrefix(entry, "*."):
			entry = entry[1:]
		case strings.Contains(entry, "*"):
			continue
		}
		out = append(out, entry)
	}
	return strings.Join(out, ",")
}
