package winrm

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type AuthMethod string

const (
	AuthNTLM              AuthMethod = "ntlm"
	AuthBasic             AuthMethod = "basic"
	DefaultExecTimeout               = 60 * time.Second
	DefaultRequestTimeout            = 60 * time.Second
)

type Config struct {
	Host               string
	Port               int
	User               string
	Password           string
	Domain             string
	Auth               AuthMethod
	UseTLS             bool
	AcceptInvalidCerts bool
	TLSServerName      string
	CACert             []byte
	ProxyURL           string
	RequestTimeout     time.Duration
	InitialCWD         string
}

func (c Config) normalized() (Config, error) {
	c.Host = strings.TrimSpace(c.Host)
	c.User = strings.TrimSpace(c.User)
	c.Domain = strings.TrimSpace(c.Domain)
	c.ProxyURL = strings.TrimSpace(c.ProxyURL)
	if c.Host == "" {
		return Config{}, fmt.Errorf("WinRM host is empty")
	}
	if strings.Contains(c.Host, "://") {
		return Config{}, fmt.Errorf("WinRM host must not include a URL scheme")
	}
	if c.Port == 5986 {
		c.UseTLS = true
	}
	if c.Port == 0 {
		if c.UseTLS {
			c.Port = 5986
		} else {
			c.Port = 5985
		}
	}
	if c.Port < 1 || c.Port > 65535 {
		return Config{}, fmt.Errorf("WinRM port %d is outside 1..65535", c.Port)
	}
	if c.User == "" {
		return Config{}, fmt.Errorf("WinRM user is empty")
	}
	if c.Domain != "" && (strings.Contains(c.User, `\`) || strings.Contains(c.User, "@")) {
		return Config{}, fmt.Errorf("WinRM domain must be empty when the user includes a domain")
	}
	if c.Auth == "" {
		c.Auth = AuthNTLM
	}
	c.Auth = AuthMethod(strings.ToLower(strings.TrimSpace(string(c.Auth))))
	if c.Auth != AuthNTLM && c.Auth != AuthBasic {
		return Config{}, fmt.Errorf("unsupported WinRM authentication %q (want ntlm or basic)", c.Auth)
	}
	if c.RequestTimeout == 0 {
		c.RequestTimeout = DefaultRequestTimeout
	}
	if c.RequestTimeout < 0 {
		return Config{}, fmt.Errorf("WinRM request timeout must not be negative")
	}
	if !c.UseTLS && len(c.CACert) != 0 {
		return Config{}, fmt.Errorf("WinRM CA certificate requires TLS")
	}
	if !c.UseTLS && c.TLSServerName != "" {
		return Config{}, fmt.Errorf("WinRM TLS server name requires TLS")
	}
	if c.InitialCWD == "" {
		c.InitialCWD = `C:\`
	}
	if _, err := proxyFunction(c.ProxyURL); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c Config) principal() string {
	if c.Domain != "" {
		return c.Domain + `\` + c.User
	}
	return c.User
}

func proxyFunction(raw string) (func(*http.Request) (*url.URL, error), error) {
	if raw == "" {
		return func(*http.Request) (*url.URL, error) { return nil, nil }, nil
	}
	proxyURL, err := url.Parse(raw)
	if err != nil || !proxyURL.IsAbs() || proxyURL.Host == "" {
		return nil, fmt.Errorf("invalid WinRM proxy URL %q", raw)
	}
	switch strings.ToLower(proxyURL.Scheme) {
	case "http", "https", "socks5":
	default:
		return nil, fmt.Errorf("unsupported WinRM proxy scheme %q", proxyURL.Scheme)
	}
	return http.ProxyURL(proxyURL), nil
}
