package ssh

import (
	"context"
	"errors"
	"fmt"
	"net"

	gossh "golang.org/x/crypto/ssh"
)

// LoginFailureClass is the coarse failure class of a test login, grouped for
// result presentation: network, host key, authentication, vault, or config.
type LoginFailureClass string

const (
	LoginFailureNetwork LoginFailureClass = "network"
	LoginFailureHostKey LoginFailureClass = "host_key"
	LoginFailureAuth    LoginFailureClass = "auth"
	LoginFailureVault   LoginFailureClass = "vault"
	LoginFailureConfig  LoginFailureClass = "config"
)

// VaultError marks an SSH credential failure that happened before the
// handshake, while materializing the credential from the vault. TestLogin
// never returns it; callers wrap vault resolution failures so
// ClassifyLoginFailure reports them as LoginFailureVault.
type VaultError struct{ Err error }

func (e *VaultError) Error() string { return "SSH credential vault: " + e.Err.Error() }
func (e *VaultError) Unwrap() error { return e.Err }

// ClassifyLoginFailure maps a test-login error to its coarse failure class.
// It returns an empty class when err is nil.
func ClassifyLoginFailure(err error) LoginFailureClass {
	if err == nil {
		return ""
	}
	var vaultErr *VaultError
	if errors.As(err, &vaultErr) {
		return LoginFailureVault
	}
	var keyErr *HostKeyError
	if errors.As(err, &keyErr) {
		return LoginFailureHostKey
	}
	var connectErr *ConnectError
	if errors.As(err, &connectErr) {
		switch connectErr.Kind {
		case ErrorKindConfig:
			return LoginFailureConfig
		case ErrorKindHostKeyPending, ErrorKindHostKeyChanged:
			return LoginFailureHostKey
		case ErrorKindAuth:
			return LoginFailureAuth
		}
	}
	return LoginFailureNetwork
}

// TestLogin runs a real SSH handshake with the configured host key and
// credential checks, then closes the connection. It never registers a managed
// session, never opens channels, and never persists host keys: the host key
// check is strictly read-only against recorded and approved keys, so unknown
// keys are never accepted regardless of Config.AutoAcceptUnknown and
// Config.HostKeyApproval.
func TestLogin(ctx context.Context, cfg Config) error {
	validated, err := cfg.validate()
	if err != nil {
		host, port := configErrorEndpoint(cfg, err)
		return newConnectError(ErrorKindConfig, "configuration", host, port, "", err)
	}
	client, err := testLoginHandshake(ctx, validated, 0, countJumps(validated), validated.Progress)
	if err != nil {
		return err
	}
	client.Close()
	return nil
}

// loginConn is the established test-login connection: a raw SSH client,
// possibly chained over a jump hop that closes together with it.
type loginConn interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
	Close() error
}

func testLoginHandshake(ctx context.Context, cfg Config, depth, hops int, progress func(ConnectProgress)) (loginConn, error) {
	if depth > maxJumpDepth {
		return nil, newConnectError(ErrorKindConfig, "configuration", cfg.Host, cfg.Port, "", fmt.Errorf("SSH jump chain exceeds %d hops", maxJumpDepth))
	}
	connectCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()

	report := func(stage ConnectStage) {
		if progress == nil {
			return
		}
		hop := 0
		if depth > 0 {
			hop = hops - depth + 1
		}
		progress(ConnectProgress{Stage: stage, Hop: hop, Hops: hops, Host: cfg.Host, Port: cfg.Port})
	}
	report(ConnectStageAuth)
	auth, closers, err := authentication(connectCtx, cfg.Auth)
	if err != nil {
		return nil, newConnectError(ErrorKindAuth, "authentication setup", cfg.Host, cfg.Port, "", err)
	}
	defer func() {
		for _, closer := range closers {
			closer.Close()
		}
	}()

	sshConfig := &gossh.ClientConfig{
		User:          cfg.User,
		Auth:          []gossh.AuthMethod{auth},
		ClientVersion: cfg.ClientVersion,
		Timeout:       cfg.ConnectTimeout,
		HostKeyCallback: func(_ string, _ net.Addr, key gossh.PublicKey) error {
			return checkLoginHostKey(connectCtx, cfg.HostKeys, cfg.Host, cfg.Port, key)
		},
	}

	target := endpointString(cfg.Host, cfg.Port)
	var netConn net.Conn
	var jumpClient loginConn
	if cfg.Jump != nil {
		jumpClient, err = testLoginHandshake(ctx, *cfg.Jump, depth+1, hops, progress)
		if err != nil {
			return nil, err
		}
		report(ConnectStageChannel)
		netConn, err = jumpClient.DialContext(connectCtx, "tcp", target)
		if err != nil {
			jumpClient.Close()
			return nil, classifyJumpDialError(cfg.Host, cfg.Port, endpointString(cfg.Jump.Host, cfg.Jump.Port), err)
		}
	} else {
		report(ConnectStageDial)
		netConn, err = dialSSH(connectCtx, cfg, cfg.Host, cfg.Port)
		if err != nil {
			return nil, classifyDialError(cfg.Host, cfg.Port, err)
		}
	}
	stop := context.AfterFunc(connectCtx, func() { netConn.Close() })
	report(ConnectStageHandshake)
	conn, channels, requests, err := gossh.NewClientConn(netConn, target, sshConfig)
	stopped := stop()
	if err == nil && !stopped && connectCtx.Err() != nil {
		conn.Close()
		err = connectCtx.Err()
	}
	if err != nil {
		netConn.Close()
		if jumpClient != nil {
			jumpClient.Close()
		}
		proxy, jump := dialHopLabels(cfg, jumpClient != nil)
		if connectCtx.Err() != nil {
			return nil, newConnectError(contextErrorKind(connectCtx.Err()), "handshake", cfg.Host, cfg.Port, proxy, connectCtx.Err()).withJump(jump)
		}
		return nil, classifyHandshakeError(cfg.Host, cfg.Port, proxy, jump, err)
	}
	client := gossh.NewClient(conn, channels, requests)
	if jumpClient != nil {
		return &loginJumpClient{Client: client, jump: jumpClient}, nil
	}
	return client, nil
}

// loginJumpClient closes the jump hop together with the target connection.
type loginJumpClient struct {
	*gossh.Client
	jump loginConn
}

func (c *loginJumpClient) Close() error {
	err := c.Client.Close()
	c.jump.Close()
	return err
}

// checkLoginHostKey verifies the presented host key against recorded and
// approved keys without persisting anything. Unknown keys are never accepted.
// A presented key that contradicts a recorded or approved key of the same
// type is reported as changed; the approved ledger counts as recorded trust
// because this read-only check never persists approved keys itself.
func checkLoginHostKey(ctx context.Context, store HostKeyStore, host string, port int, presented gossh.PublicKey) error {
	key := newHostKey(host, port, presented)
	known, err := store.HostKeys(ctx, host, port)
	if err != nil {
		return fmt.Errorf("load SSH host keys: %w", err)
	}
	var knownSameType []HostKey
	recorded := make(map[string]struct{})
	for _, candidate := range known {
		if candidate.KeyType != key.KeyType {
			continue
		}
		knownSameType = append(knownSameType, candidate)
		recorded[candidate.Fingerprint] = struct{}{}
		if candidate.Key == key.Key {
			return nil
		}
	}
	approved, err := loadApprovedHostKeys(ctx, store, host, port)
	if err != nil {
		return fmt.Errorf("load approved SSH host keys: %w", err)
	}
	for _, candidate := range approved {
		if candidate.KeyType != key.KeyType {
			continue
		}
		if candidate.Fingerprint == key.Fingerprint {
			return nil
		}
		if _, exists := recorded[candidate.Fingerprint]; !exists {
			knownSameType = append(knownSameType, candidate)
			recorded[candidate.Fingerprint] = struct{}{}
		}
	}
	if len(knownSameType) == 0 {
		return &HostKeyError{Pending: true, Presented: key}
	}
	return &HostKeyError{Presented: key, Known: knownSameType}
}
