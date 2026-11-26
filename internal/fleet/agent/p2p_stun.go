package agent

import (
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// PublicSTUNHost reports whether host can name a STUN server for peers outside the server's network: not a
// loopback, unspecified, link-local or private IP literal, not localhost and not a single-label name (such as a
// container service name a reverse proxy put in Host).
func PublicSTUNHost(host string) bool {
	host = strings.TrimSuffix(strings.Trim(host, "[]"), ".")
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return !(ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate())
	}
	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return false
	}
	return strings.Contains(host, ".")
}

// STUNServers lists the STUN servers of config as host:port. Unless proxied (a manual proxy is configured, so the
// host the caller dials is not its own route to the server), it also pairs the host of serverURL, the server
// address the caller connects to, with every port in config.STUNPorts: that keeps the built-in responder usable
// when a reverse proxy rewrote the Host header the server derived its list from. A loopback, private or
// link-local IP literal and localhost are not added. Duplicates are dropped; the server's entries come first.
func STUNServers(config *P2PConfig, serverURL string, proxied bool) []string {
	if config == nil {
		return nil
	}
	var servers []string
	add := func(host string, port int) {
		host = strings.Trim(host, "[]")
		if host == "" || port <= 0 || port > 65535 {
			return
		}
		address := net.JoinHostPort(host, strconv.Itoa(port))
		if !slices.Contains(servers, address) {
			servers = append(servers, address)
		}
	}
	for _, endpoint := range config.STUN {
		add(endpoint.Host, endpoint.Port)
	}
	if proxied || len(config.STUNPorts) == 0 {
		return servers
	}
	parsed, err := url.Parse(serverURL)
	if err != nil {
		return servers
	}
	host := parsed.Hostname()
	if host == "" || strings.EqualFold(host, "localhost") {
		return servers
	}
	if net.ParseIP(host) != nil && !PublicSTUNHost(host) {
		return servers
	}
	for _, port := range config.STUNPorts {
		add(host, port)
	}
	return servers
}
