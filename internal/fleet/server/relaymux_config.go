package fleetserver

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/relaymux"
)

const (
	// EnvRelayQUIC turns the QUIC carrier of the multiplexed relay on or off (default on). The websocket
	// carrier on the HTTPS port is always available while device management is.
	EnvRelayQUIC = "NEXTERM_RELAY_QUIC"
	// EnvRelayQUICListen lists the UDP addresses of the QUIC relay when the STUN responder is off; while STUN
	// runs, the relay shares its sockets and this is ignored. Default empty.
	EnvRelayQUICListen = "NEXTERM_RELAY_QUIC_LISTEN"
	// EnvRelayQUICAdvertise lists the host:port QUIC endpoints handed to clients and agents. Unset, they are
	// the host name the caller used to reach the server plus each bound relay port, unless that name is a
	// loopback, private or single-label one.
	EnvRelayQUICAdvertise = "NEXTERM_RELAY_QUIC_ADVERTISE"
	// EnvRelayQUICCert and EnvRelayQUICKey name a PEM certificate chain and key for the QUIC relay, for a
	// server whose clients should verify it against public roots. Unset, the relay uses a self-signed
	// certificate whose key pin the config endpoint publishes.
	EnvRelayQUICCert = "NEXTERM_RELAY_QUIC_CERT"
	EnvRelayQUICKey  = "NEXTERM_RELAY_QUIC_KEY"

	// RelayQUICKeyFile is the self-signed relay key in the data directory.
	RelayQUICKeyFile = "relay-quic.key"
)

// RelaySettings is the multiplexed relay configuration.
type RelaySettings struct {
	QUIC          bool
	QUICListen    []string
	QUICAdvertise []relaymux.Endpoint
	CertFile      string
	KeyFile       string
}

// ParseRelayEnv reads the relay environment.
func ParseRelayEnv(lookup func(string) (string, bool)) (RelaySettings, error) {
	settings := RelaySettings{QUIC: true}
	if raw, ok := lookup(EnvRelayQUIC); ok && strings.TrimSpace(raw) != "" {
		enabled, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return RelaySettings{}, fmt.Errorf("invalid %s value %q (want 1 or 0)", EnvRelayQUIC, raw)
		}
		settings.QUIC = enabled
	}
	if raw, ok := lookup(EnvRelayQUICListen); ok {
		for _, item := range splitList(raw) {
			host, port, err := net.SplitHostPort(item)
			if err != nil {
				return RelaySettings{}, fmt.Errorf("invalid %s entry %q: want [host]:port", EnvRelayQUICListen, item)
			}
			if _, err := parsePort(port); err != nil {
				return RelaySettings{}, fmt.Errorf("invalid %s entry %q: %w", EnvRelayQUICListen, item, err)
			}
			if host != "" && net.ParseIP(host) == nil {
				return RelaySettings{}, fmt.Errorf("invalid %s entry %q: the host must be an IP address or empty", EnvRelayQUICListen, item)
			}
			if !slices.Contains(settings.QUICListen, item) {
				settings.QUICListen = append(settings.QUICListen, item)
			}
		}
	}
	advertised, err := parseSTUNEndpoints(lookup, EnvRelayQUICAdvertise, false)
	if err != nil {
		return RelaySettings{}, err
	}
	for _, endpoint := range advertised {
		settings.QUICAdvertise = append(settings.QUICAdvertise, relaymux.Endpoint{Host: endpoint.Host, Port: endpoint.Port})
	}
	settings.CertFile, _ = lookup(EnvRelayQUICCert)
	settings.KeyFile, _ = lookup(EnvRelayQUICKey)
	settings.CertFile, settings.KeyFile = strings.TrimSpace(settings.CertFile), strings.TrimSpace(settings.KeyFile)
	if (settings.CertFile == "") != (settings.KeyFile == "") {
		return RelaySettings{}, fmt.Errorf("%s and %s must be set together", EnvRelayQUICCert, EnvRelayQUICKey)
	}
	return settings, nil
}

// WithRelay applies the relay settings; without it the QUIC carrier is off and the websocket carrier on.
func WithRelay(settings RelaySettings) Option {
	return func(s *Service) {
		s.relay.mu.Lock()
		s.relay.settings = settings
		s.relay.mu.Unlock()
	}
}

// SetRelayQUIC records the UDP ports the QUIC relay listens on and the pin of its self-signed certificate
// (empty for a CA-issued one); the config endpoints advertise them.
func (s *Service) SetRelayQUIC(ports []int, pin string) {
	s.relay.mu.Lock()
	s.relay.quicPorts = append([]int(nil), ports...)
	s.relay.pin = pin
	s.relay.mu.Unlock()
}

// relayConfig builds the relay config view for r.
func (s *Service) relayConfig(r *http.Request) relaymux.Config {
	s.relay.mu.Lock()
	settings := s.relay.settings
	ports := append([]int(nil), s.relay.quicPorts...)
	pin := s.relay.pin
	s.relay.mu.Unlock()
	config := relaymux.Config{Enabled: true, Version: relaymux.Version, WSPath: relaymux.WSPath, QUIC: []relaymux.Endpoint{}}
	if !settings.QUIC || len(ports) == 0 {
		return config
	}
	config.ALPN = relaymux.ALPN
	config.Pin = pin
	if len(settings.QUICAdvertise) > 0 {
		config.QUIC = append(config.QUIC, settings.QUICAdvertise...)
	} else if host := publicRequestHost(r); host != "" {
		for _, port := range ports {
			endpoint := relaymux.Endpoint{Host: host, Port: port}
			if !slices.Contains(config.QUIC, endpoint) {
				config.QUIC = append(config.QUIC, endpoint)
			}
		}
	}
	return config
}

// publicRequestHost is the request host name when a client elsewhere can reach it: a loopback, private or
// link-local address and a single-label name (localhost, a LAN short name) are left out, since a QUIC endpoint
// derived from them would only point a remote client at itself or nowhere. NEXTERM_RELAY_QUIC_ADVERTISE
// covers deployments that want them.
func publicRequestHost(r *http.Request) string {
	host := requestHostname(r)
	if host == "" {
		return ""
	}
	if address, err := netip.ParseAddr(host); err == nil {
		address = address.Unmap()
		if address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast() || address.IsUnspecified() {
			return ""
		}
		return host
	}
	name := strings.TrimSuffix(host, ".")
	if !strings.Contains(name, ".") || isLocalOrPrivate(name) {
		return ""
	}
	return host
}

// serveRelayConfig handles GET /relay/config for a signed-in client (session cookie, as the other session
// routes) or an agent (device id and secret in the headers of GET /agent/p2p/config).
func (s *Service) serveRelayConfig(w http.ResponseWriter, r *http.Request) {
	if identityFrom(r) == nil {
		deviceID := strings.TrimSpace(r.Header.Get(agent.HeaderDeviceID))
		secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if deviceID == "" || !ok || secret == "" {
			writeFleetJSON(w, http.StatusUnauthorized, ipc.Failure(ipc.NewError(ipc.CodeForbidden, bridgeSessionReloginMessage)))
			return
		}
		if _, err := s.AuthenticateAgent(r.Context(), deviceID, secret); err != nil {
			writeFleetFailure(w, err)
			return
		}
		writeFleetJSON(w, http.StatusOK, s.relayConfig(r))
		return
	}
	s.requireFleetSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeFleetJSON(w, http.StatusOK, s.relayConfig(r))
	})).ServeHTTP(w, r)
}
