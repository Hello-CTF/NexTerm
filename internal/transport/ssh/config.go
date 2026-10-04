package ssh

import (
	"fmt"
	"strings"
	"time"
)

const (
	DefaultConnectTimeout    = 15 * time.Second
	DefaultKeepAliveInterval = 30 * time.Second
	DefaultKeepAliveMax      = 3
)

type AuthMethod string

const (
	AuthPassword            AuthMethod = "password"
	AuthKey                 AuthMethod = "key"
	AuthAgent               AuthMethod = "agent"
	AuthKeyboardInteractive AuthMethod = "keyboard-interactive"
)

type AuthConfig struct {
	Method      AuthMethod
	Password    string
	KeyPath     string
	KeyPEM      []byte
	Passphrase  string
	AgentSocket string
	CertPath    string
	CertPEM     []byte
}

type Config struct {
	Host              string
	Port              int
	User              string
	Auth              AuthConfig
	HostKeys          HostKeyStore
	AutoAcceptUnknown bool
	HostKeyApproval   *HostKeyApproval
	ProxyURL          string
	ProxyCommand      string
	Jump              *Config
	ForwardAgent      bool
	ConnectTimeout    time.Duration
	KeepAliveInterval time.Duration
	KeepAliveMax      int
	ClientVersion     string
}

const maxJumpDepth = 16

func (c Config) validate() (Config, error) {
	host, port, err := normalizeEndpoint(c.Host, c.Port)
	if err != nil {
		return Config{}, err
	}
	c.Host = host
	c.Port = port
	if strings.TrimSpace(c.User) == "" {
		return Config{}, fmt.Errorf("SSH user is empty")
	}
	if c.HostKeys == nil {
		return Config{}, fmt.Errorf("SSH host key store is required")
	}
	if c.ProxyCommand != "" && c.ProxyURL != "" {
		return Config{}, fmt.Errorf("SSH proxy command and proxy URL are mutually exclusive")
	}
	if c.ConnectTimeout == 0 {
		c.ConnectTimeout = DefaultConnectTimeout
	}
	if c.ConnectTimeout < 0 {
		return Config{}, fmt.Errorf("SSH connect timeout must be positive")
	}
	if c.KeepAliveInterval == 0 {
		c.KeepAliveInterval = DefaultKeepAliveInterval
	}
	if c.KeepAliveMax == 0 {
		c.KeepAliveMax = DefaultKeepAliveMax
	}
	if c.KeepAliveMax < 0 {
		return Config{}, fmt.Errorf("SSH keepalive maximum must be positive")
	}
	if c.ClientVersion == "" {
		c.ClientVersion = "SSH-2.0-NexTerm"
	} else if !strings.HasPrefix(c.ClientVersion, "SSH-2.0-") {
		return Config{}, fmt.Errorf("SSH client version must start with SSH-2.0-")
	}
	if c.Jump != nil {
		jump, err := c.Jump.validate()
		if err != nil {
			return Config{}, wrapJumpConfigError(c.Jump, err)
		}
		c.Jump = &jump
	}
	return c, nil
}
