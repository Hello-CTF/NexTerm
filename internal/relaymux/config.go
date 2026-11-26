package relaymux

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Endpoint is one QUIC relay address.
type Endpoint struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

func (e Endpoint) String() string { return net.JoinHostPort(e.Host, strconv.Itoa(e.Port)) }

// Config is the body of GET ConfigPath. A server without the relay answers 404, which a client reads as
// "dial bridges per operation as before".
type Config struct {
	Enabled bool `json:"enabled"`
	Version int  `json:"version"`
	// WSPath is the websocket carrier path relative to the server base URL.
	WSPath string `json:"ws_path"`
	// QUIC lists the relay's QUIC endpoints; empty when QUIC is off.
	QUIC []Endpoint `json:"quic"`
	ALPN string     `json:"alpn,omitempty"`
	// Pin is the base64url SHA-256 SubjectPublicKeyInfo of a self-signed relay certificate; empty when the
	// relay presents a certificate that verifies against public roots for the endpoint host.
	Pin string `json:"spki_sha256,omitempty"`
}

// DialOptions are the caller's part of a DialConfig built from a Config.
type DialOptions struct {
	Hello      Hello
	HTTPClient *http.Client
	Header     http.Header
	// UnverifiedTLS marks an HTTPS base whose certificate the caller does not verify (an agent base URL
	// marked insecure). The pin delivered over such a channel authenticates nothing, so QUIC is not used.
	UnverifiedTLS bool
}

// DialConfig builds the dial configuration for baseURL (the server's HTTPS base, as the API uses it) from the
// config that base returned. QUIC is used only when the config arrived over verified HTTPS: the QUIC server is
// authenticated by the pin or the name in that config, so a client never trusts a QUIC server more than the
// HTTPS channel that vouched for it, and never accepts one nobody vouched for.
func (c Config) DialConfig(baseURL string, options DialOptions) (DialConfig, error) {
	if !c.Enabled || c.Version != Version {
		return DialConfig{}, errors.New("relaymux: the server does not offer a compatible relay")
	}
	base, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || base.Host == "" {
		return DialConfig{}, errors.New("relaymux: bad base URL")
	}
	wsPath := c.WSPath
	if wsPath == "" {
		wsPath = WSPath
	}
	ws := *base
	switch base.Scheme {
	case "https":
		ws.Scheme = "wss"
	case "http":
		ws.Scheme = "ws"
	default:
		return DialConfig{}, errors.New("relaymux: base URL must be http or https")
	}
	ws.Path = strings.TrimRight(base.Path, "/") + wsPath
	config := DialConfig{
		WSURL:      ws.String(),
		HTTPClient: options.HTTPClient,
		Header:     options.Header,
		Hello:      options.Hello,
		ServerName: base.Hostname(),
	}
	if base.Scheme == "https" && !options.UnverifiedTLS && (c.ALPN == "" || c.ALPN == ALPN) {
		config.Pin = c.Pin
		for _, endpoint := range c.QUIC {
			if endpoint.Host != "" && endpoint.Port > 0 && endpoint.Port < 65536 {
				config.QUICAddrs = append(config.QUICAddrs, endpoint.String())
			}
		}
	}
	return config, nil
}
