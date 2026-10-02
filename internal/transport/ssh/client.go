package ssh

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
	gossh "golang.org/x/crypto/ssh"
)

var nextGeneration atomic.Uint64

type Client struct {
	ssh        *gossh.Client
	generation uint64
	done       chan struct{}
	stateOnce  sync.Once
	closeOnce  sync.Once
	closeErr   error
	sftpMu     sync.Mutex
	sftp       *sftpState
}

func Connect(ctx context.Context, cfg Config) (*Client, error) {
	cfg, err := cfg.validate()
	if err != nil {
		return nil, err
	}
	connectCtx := ctx
	cancel := func() {}
	if cfg.ConnectTimeout > 0 {
		connectCtx, cancel = context.WithTimeout(ctx, cfg.ConnectTimeout)
	}
	defer cancel()

	auth, closers, err := authentication(connectCtx, cfg.Auth)
	if err != nil {
		return nil, err
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
			return verifyHostKey(connectCtx, cfg.HostKeys, cfg.Host, cfg.Port, cfg.AutoAcceptUnknown, cfg.HostKeyApproval, key)
		},
	}
	netConn, err := dialSSH(connectCtx, cfg.ProxyURL, cfg.Host, cfg.Port)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(connectCtx, func() { netConn.Close() })
	conn, channels, requests, err := gossh.NewClientConn(netConn, endpointString(cfg.Host, cfg.Port), sshConfig)
	stopped := stop()
	if err == nil && !stopped && connectCtx.Err() != nil {
		conn.Close()
		err = connectCtx.Err()
	}
	if err != nil {
		netConn.Close()
		if connectCtx.Err() != nil {
			return nil, connectCtx.Err()
		}
		var keyErr *HostKeyError
		if errors.As(err, &keyErr) {
			return nil, keyErr
		}
		return nil, fmt.Errorf("SSH handshake: %w", err)
	}
	client := &Client{
		ssh:        gossh.NewClient(conn, channels, requests),
		generation: nextGeneration.Add(1),
		done:       make(chan struct{}),
	}
	go client.watch(conn)
	if cfg.KeepAliveInterval > 0 && cfg.KeepAliveMax > 0 {
		go client.keepalive(cfg.KeepAliveInterval, cfg.KeepAliveMax)
	}
	return client, nil
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
