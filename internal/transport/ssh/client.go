package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

var nextGeneration atomic.Uint64

type ConnectStage string

const (
	ConnectStageAuth      ConnectStage = "auth"
	ConnectStageDial      ConnectStage = "dial"
	ConnectStageChannel   ConnectStage = "channel"
	ConnectStageHandshake ConnectStage = "handshake"
)

type ConnectProgress struct {
	Stage ConnectStage
	Hop   int
	Hops  int
	Host  string
	Port  int
}

type Client struct {
	ssh          *gossh.Client
	generation   uint64
	done         chan struct{}
	stateOnce    sync.Once
	closeOnce    sync.Once
	closeErr     error
	sftpMu       sync.Mutex
	sftp         *sftpState
	closers      []io.Closer
	forwardAgent bool
	env          map[string]string
	identity     string

	heartbeatMu      sync.Mutex
	heartbeatPending *heartbeatCall
}

type heartbeatCall struct {
	done chan struct{}
	err  error
}

// HostIdentity names the authenticated server: the login user plus the host key
// the connection was verified against. It is empty when unknown. Two clients
// with the same identity reached the same machine key and account, which is
// what makes it a safe cache key for per-host state.
func (c *Client) HostIdentity() string {
	return c.identity
}

func Connect(ctx context.Context, cfg Config) (*Client, error) {
	validated, err := cfg.validate()
	if err != nil {
		host, port := configErrorEndpoint(cfg, err)
		return nil, newConnectError(ErrorKindConfig, "configuration", host, port, "", err)
	}
	return connect(ctx, validated, 0, countJumps(validated), validated.Progress)
}

func countJumps(cfg Config) int {
	hops := 0
	for jump := cfg.Jump; jump != nil; jump = jump.Jump {
		hops++
	}
	return hops
}

func connect(ctx context.Context, cfg Config, depth, hops int, progress func(ConnectProgress)) (*Client, error) {
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

	var presentedKey string
	sshConfig := &gossh.ClientConfig{
		User:          cfg.User,
		Auth:          []gossh.AuthMethod{auth},
		ClientVersion: cfg.ClientVersion,
		Timeout:       cfg.ConnectTimeout,
		HostKeyCallback: func(_ string, _ net.Addr, key gossh.PublicKey) error {
			if err := verifyHostKey(connectCtx, cfg.HostKeys, cfg.Host, cfg.Port, cfg.AutoAcceptUnknown, cfg.HostKeyApproval, key); err != nil {
				return err
			}
			presentedKey = key.Type() + " " + gossh.FingerprintSHA256(key)
			return nil
		},
	}
	target := endpointString(cfg.Host, cfg.Port)
	var netConn net.Conn
	var jumpClient *Client
	if cfg.Jump != nil {
		jumpClient, err = connect(ctx, *cfg.Jump, depth+1, hops, progress)
		if err != nil {
			return nil, err
		}
		report(ConnectStageChannel)
		netConn, err = jumpClient.ssh.DialContext(connectCtx, "tcp", target)
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
	client := &Client{
		ssh:          gossh.NewClient(conn, channels, requests),
		generation:   nextGeneration.Add(1),
		done:         make(chan struct{}),
		forwardAgent: cfg.ForwardAgent,
		env:          cfg.Env,
	}
	if presentedKey != "" {
		// The endpoint is part of the identity so machines cloned with the same host key never share state.
		client.identity = cfg.User + "@" + net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)) + " " + presentedKey
	}
	if jumpClient != nil {
		client.closers = append(client.closers, jumpClient)
	}
	if cfg.ForwardAgent {
		if err := client.enableAgentForwarding(cfg.Auth.AgentSocket); err != nil {
			conn.Close()
			if jumpClient != nil {
				jumpClient.Close()
			}
			return nil, err
		}
	}
	go client.watch(conn)
	if cfg.KeepAliveInterval > 0 && cfg.KeepAliveMax > 0 {
		go client.keepalive(cfg.KeepAliveInterval, cfg.KeepAliveMax)
	}
	return client, nil
}

func (c *Client) enableAgentForwarding(socket string) error {
	if socket == "" {
		socket = os.Getenv("SSH_AUTH_SOCK")
	}
	if socket == "" {
		return fmt.Errorf("SSH agent forwarding is enabled but no agent socket is configured and SSH_AUTH_SOCK is empty")
	}
	if err := agent.ForwardToRemote(c.ssh, socket); err != nil {
		return fmt.Errorf("forward SSH agent: %w", err)
	}
	return nil
}

func (c *Client) Kind() string {
	return "ssh"
}

func (c *Client) Generation() uint64 {
	return c.generation
}

func (c *Client) IsAlive() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.stateOnce.Do(func() { close(c.done) })
		c.closeErr = c.ssh.Close()
		c.closeSFTP()
		for index := len(c.closers) - 1; index >= 0; index-- {
			c.closers[index].Close()
		}
		if errors.Is(c.closeErr, net.ErrClosed) || errors.Is(c.closeErr, context.Canceled) {
			c.closeErr = nil
		}
	})
	return c.closeErr
}

func (c *Client) Ping(ctx context.Context) (time.Duration, error) {
	result, err := c.Exec(ctx, ": ; true", base.ExecOptions{Timeout: 10 * time.Second})
	if err != nil {
		return result.Duration, err
	}
	if result.ExitCode == nil {
		return result.Duration, base.ErrExitStatusMissing
	}
	if *result.ExitCode != 0 {
		return result.Duration, &base.ExitError{Code: *result.ExitCode, Signal: result.Signal}
	}
	return result.Duration, nil
}

// Heartbeat sends one keepalive@openssh.com global request and waits for the
// server's reply. Concurrent callers share the request in flight, so a reply
// that never arrives holds at most one blocked request per connection.
func (c *Client) Heartbeat(ctx context.Context) error {
	if !c.IsAlive() {
		return base.ErrDisconnected
	}
	c.heartbeatMu.Lock()
	call := c.heartbeatPending
	if call == nil {
		call = &heartbeatCall{done: make(chan struct{})}
		c.heartbeatPending = call
		go func() {
			_, _, err := c.ssh.SendRequest("keepalive@openssh.com", true, nil)
			c.heartbeatMu.Lock()
			call.err = err
			c.heartbeatPending = nil
			c.heartbeatMu.Unlock()
			close(call.done)
		}()
	}
	c.heartbeatMu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return base.ErrDisconnected
	case <-call.done:
	}
	if call.err != nil {
		return fmt.Errorf("%w: %v", base.ErrDisconnected, call.err)
	}
	return nil
}

func (c *Client) check(ctx context.Context, generation uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if generation != 0 && generation != c.generation {
		return base.ErrStaleGeneration
	}
	if !c.IsAlive() {
		return base.ErrDisconnected
	}
	return nil
}

func (c *Client) watch(conn gossh.Conn) {
	conn.Wait()
	c.stateOnce.Do(func() { close(c.done) })
}

func (c *Client) keepalive(interval time.Duration, maximum int) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var response chan error
	missed := 0
	for {
		select {
		case <-c.done:
			return
		case err := <-response:
			response = nil
			if err == nil {
				missed = 0
			} else {
				missed++
			}
		case <-ticker.C:
			if response == nil {
				pending := make(chan error, 1)
				response = pending
				go func() {
					_, _, err := c.ssh.SendRequest("keepalive@openssh.com", true, nil)
					pending <- err
				}()
				continue
			}
			missed++
		}
		if missed >= maximum {
			c.Close()
			return
		}
	}
}
