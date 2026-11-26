package fleetserver

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/p2p/grant"
)

const (
	// EnvP2P turns direct client-to-agent connections on or off for the whole server (default on).
	EnvP2P = "NEXTERM_P2P"
	// EnvSTUNListen lists the UDP addresses of the built-in STUN responder (default ":3478"; set it empty to
	// disable the responder).
	EnvSTUNListen = "NEXTERM_STUN_LISTEN"
	// EnvSTUNAdvertise lists the host:port STUN addresses handed to clients and agents. Unset, they are the
	// host name the caller used to reach the server plus each port the responder listens on.
	EnvSTUNAdvertise = "NEXTERM_STUN_ADVERTISE"
	// EnvSTUNExternal lists additional STUN servers (host or host:port, default port 3478) chosen by the
	// administrator; none is built in.
	EnvSTUNExternal = "NEXTERM_STUN_EXTERNAL"

	DefaultSTUNListen = ":3478"
	defaultSTUNPort   = 3478

	p2pGatherMS = 3000
	p2pCheckMS  = 8000
)

// P2PSettings is the server-wide direct-connection policy and STUN configuration.
type P2PSettings struct {
	Enabled       bool
	STUNListen    []string
	STUNAdvertise []agent.P2PEndpoint
	STUNExternal  []agent.P2PEndpoint
}

// ParseP2PEnv reads the P2P and STUN environment. lookup distinguishes an unset variable from an empty one
// (os.LookupEnv), because an empty NEXTERM_STUN_LISTEN disables the responder while an unset one keeps the default.
func ParseP2PEnv(lookup func(string) (string, bool)) (P2PSettings, error) {
	settings := P2PSettings{Enabled: true, STUNListen: []string{DefaultSTUNListen}}
	if raw, ok := lookup(EnvP2P); ok && strings.TrimSpace(raw) != "" {
		enabled, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return P2PSettings{}, fmt.Errorf("invalid %s value %q (want 1 or 0)", EnvP2P, raw)
		}
		settings.Enabled = enabled
	}
	if raw, ok := lookup(EnvSTUNListen); ok {
		settings.STUNListen = nil
		for _, item := range splitList(raw) {
			host, port, err := net.SplitHostPort(item)
			if err != nil {
				return P2PSettings{}, fmt.Errorf("invalid %s entry %q: want [host]:port", EnvSTUNListen, item)
			}
			if _, err := parsePort(port); err != nil {
				return P2PSettings{}, fmt.Errorf("invalid %s entry %q: %w", EnvSTUNListen, item, err)
			}
			if host != "" && net.ParseIP(host) == nil {
				return P2PSettings{}, fmt.Errorf("invalid %s entry %q: the host must be an IP address or empty", EnvSTUNListen, item)
			}
			if !slices.Contains(settings.STUNListen, item) {
				settings.STUNListen = append(settings.STUNListen, item)
			}
		}
	}
	var err error
	if settings.STUNAdvertise, err = parseSTUNEndpoints(lookup, EnvSTUNAdvertise, false); err != nil {
		return P2PSettings{}, err
	}
	if settings.STUNExternal, err = parseSTUNEndpoints(lookup, EnvSTUNExternal, true); err != nil {
		return P2PSettings{}, err
	}
	return settings, nil
}

func splitList(raw string) []string {
	var items []string
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}

func parsePort(raw string) (int, error) {
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("port %q must be 1-65535", raw)
	}
	return port, nil
}

func parseSTUNEndpoints(lookup func(string) (string, bool), key string, defaultPort bool) ([]agent.P2PEndpoint, error) {
	raw, _ := lookup(key)
	var endpoints []agent.P2PEndpoint
	for _, item := range splitList(raw) {
		host, portText, err := net.SplitHostPort(item)
		port := defaultSTUNPort
		switch {
		case err == nil:
			if port, err = parsePort(portText); err != nil {
				return nil, fmt.Errorf("invalid %s entry %q: %w", key, item, err)
			}
		case defaultPort:
			host = strings.Trim(item, "[]")
			if strings.Contains(host, ":") && net.ParseIP(host) == nil {
				return nil, fmt.Errorf("invalid %s entry %q: want host or host:port", key, item)
			}
		default:
			return nil, fmt.Errorf("invalid %s entry %q: want host:port", key, item)
		}
		if !validSTUNHost(host) {
			return nil, fmt.Errorf("invalid %s entry %q: bad host", key, item)
		}
		endpoint := agent.P2PEndpoint{Host: host, Port: port}
		if !slices.Contains(endpoints, endpoint) {
			endpoints = append(endpoints, endpoint)
		}
	}
	return endpoints, nil
}

func validSTUNHost(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

// WithP2P applies the server-wide direct-connection settings; without it direct connections are off.
func WithP2P(settings P2PSettings) Option {
	return func(s *Service) {
		s.p2p.mu.Lock()
		s.p2p.settings = settings
		s.p2p.mu.Unlock()
	}
}

// WithP2PForceRelay installs the per-device "always use the relay" check: forceRelay reports whether userID
// chose the relay for deviceID, and a true answer refuses the grant. Without it the server does not consult any
// per-device choice; the client then has to honor its own stored setting by not asking for a grant.
func WithP2PForceRelay(forceRelay func(ctx context.Context, userID, deviceID string) (bool, error)) Option {
	return func(s *Service) {
		s.p2p.mu.Lock()
		s.p2p.forceRelay = forceRelay
		s.p2p.mu.Unlock()
	}
}

// SetP2PSTUNPorts records the UDP ports the built-in STUN responder actually listens on; the config
// endpoints advertise them under the caller's host name when NEXTERM_STUN_ADVERTISE is unset.
func (s *Service) SetP2PSTUNPorts(ports []int) {
	s.p2p.mu.Lock()
	s.p2p.stunPorts = append([]int(nil), ports...)
	s.p2p.mu.Unlock()
}

// P2PEnabled reports the server-wide direct-connection switch.
func (s *Service) P2PEnabled() bool {
	s.p2p.mu.Lock()
	defer s.p2p.mu.Unlock()
	return s.p2p.settings.Enabled
}

// p2pConfig builds the config view for r; derived STUN addresses use the host name r reached the server by.
func (s *Service) p2pConfig(r *http.Request) (agent.P2PConfig, error) {
	s.p2p.mu.Lock()
	settings := s.p2p.settings
	ports := append([]int(nil), s.p2p.stunPorts...)
	s.p2p.mu.Unlock()
	config := agent.P2PConfig{
		Enabled:   settings.Enabled,
		STUN:      []agent.P2PEndpoint{},
		TURN:      []agent.P2PEndpoint{},
		GrantKeys: nil,
		Timeouts:  agent.P2PTimeouts{GatherMS: p2pGatherMS, CheckMS: p2pCheckMS},
	}
	if !settings.Enabled {
		config.GrantKeys = []grant.PublicKey{}
		return config, nil
	}
	if len(settings.STUNAdvertise) > 0 {
		config.STUN = append(config.STUN, settings.STUNAdvertise...)
	} else if host := requestHostname(r); host != "" {
		for _, port := range ports {
			config.STUN = appendEndpoint(config.STUN, agent.P2PEndpoint{Host: host, Port: port})
		}
	}
	for _, endpoint := range settings.STUNExternal {
		config.STUN = appendEndpoint(config.STUN, endpoint)
	}
	keys, err := s.p2p.keys.published(r.Context(), s)
	if err != nil {
		return agent.P2PConfig{}, err
	}
	config.GrantKeys = keys
	return config, nil
}

func appendEndpoint(endpoints []agent.P2PEndpoint, endpoint agent.P2PEndpoint) []agent.P2PEndpoint {
	if slices.Contains(endpoints, endpoint) {
		return endpoints
	}
	return append(endpoints, endpoint)
}

// requestHostname is the host name of the Host header without its port, IPv6 brackets removed.
func requestHostname(r *http.Request) string {
	host := r.Host
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	host = strings.Trim(host, "[]")
	if !validSTUNHost(host) {
		return ""
	}
	return host
}

// p2pService holds the direct-connection state of the fleet service.
type p2pService struct {
	mu         sync.Mutex
	settings   P2PSettings
	stunPorts  []int
	forceRelay func(ctx context.Context, userID, deviceID string) (bool, error)

	keys   *p2pKeyRing
	grants *p2pGrants
	audits *keyedLimiter
}

func newP2PService() *p2pService {
	return &p2pService{keys: &p2pKeyRing{}, grants: newP2PGrants(), audits: newKeyedLimiter(p2pAuditRate, p2pAuditBurst)}
}
