package ssh

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

type HostKeyProbeState string

const (
	HostKeyProbeKnown   HostKeyProbeState = "known"
	HostKeyProbeChanged HostKeyProbeState = "changed"
	HostKeyProbePending HostKeyProbeState = "pending"
)

type HostKeyProbeResult struct {
	Host        string
	Port        int
	KeyType     string
	Fingerprint string
	State       HostKeyProbeState
	Known       []HostKey
}

var errHostKeyProbeDone = errors.New("SSH host key probe complete")

func ProbeHostKey(ctx context.Context, cfg Config) (*HostKeyProbeResult, error) {
	validated, err := cfg.validate()
	if err != nil {
		return nil, newConnectError(ErrorKindConfig, "configuration", cfg.Host, cfg.Port, "", err)
	}
	cfg = validated
	probeCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()

	conn, viaJump, err := dialProbeChain(probeCtx, cfg)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	var presented gossh.PublicKey
	clientConfig := &gossh.ClientConfig{
		User:          "nexterm-host-key-probe",
		ClientVersion: "SSH-2.0-NexTerm",
		Timeout:       cfg.ConnectTimeout,
		HostKeyCallback: func(_ string, _ net.Addr, key gossh.PublicKey) error {
			presented = key
			return errHostKeyProbeDone
		},
	}
	stop := context.AfterFunc(probeCtx, func() { conn.Close() })
	_, _, _, handshakeErr := gossh.NewClientConn(conn, endpointString(cfg.Host, cfg.Port), clientConfig)
	stop()
	if presented == nil {
		proxy, jump := dialHopLabels(cfg, viaJump)
		if probeCtx.Err() != nil {
			return nil, newConnectError(contextErrorKind(probeCtx.Err()), "handshake", cfg.Host, cfg.Port, proxy, probeCtx.Err()).withJump(jump)
		}
		if handshakeErr == nil {
			handshakeErr = fmt.Errorf("SSH handshake ended without a host key")
		}
		return nil, classifyHandshakeError(cfg.Host, cfg.Port, proxy, jump, handshakeErr)
	}

	key := newHostKey(cfg.Host, cfg.Port, presented)
	result := &HostKeyProbeResult{Host: cfg.Host, Port: cfg.Port, KeyType: key.KeyType, Fingerprint: key.Fingerprint, State: HostKeyProbePending}
	known, err := cfg.HostKeys.HostKeys(ctx, cfg.Host, cfg.Port)
	if err != nil {
		return nil, fmt.Errorf("load SSH host keys: %w", err)
	}
	var knownSameType []HostKey
	for _, candidate := range known {
		if candidate.KeyType != key.KeyType {
			continue
		}
		knownSameType = append(knownSameType, candidate)
		if candidate.Key == key.Key {
			result.State = HostKeyProbeKnown
			return result, nil
		}
	}
	if matchesApprovedKey(loadApprovedHostKeys(ctx, cfg.HostKeys, cfg.Host, cfg.Port), key) {
		result.State = HostKeyProbeKnown
		return result, nil
	}
	if len(knownSameType) > 0 {
		result.State = HostKeyProbeChanged
		result.Known = knownSameType
	}
	return result, nil
}

func ProbeReachability(ctx context.Context, cfg Config, timeout time.Duration) error {
	host, port, err := normalizeEndpoint(cfg.Host, cfg.Port)
	if err != nil {
		return newConnectError(ErrorKindConfig, "configuration", cfg.Host, cfg.Port, "", err)
	}
	cfg.Host, cfg.Port = host, port
	if cfg.Jump != nil {
		jump, err := cfg.Jump.validate()
		if err != nil {
			return newConnectError(ErrorKindConfig, "configuration", cfg.Jump.Host, cfg.Jump.Port, "", err)
		}
		cfg.Jump = &jump
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	conn, _, err := dialProbeChain(ctx, cfg)
	if err != nil {
		return err
	}
	return conn.Close()
}

func dialProbeChain(ctx context.Context, cfg Config) (net.Conn, bool, error) {
	if cfg.Jump == nil {
		conn, err := dialSSH(ctx, cfg, cfg.Host, cfg.Port)
		if err != nil {
			return nil, false, classifyDialError(cfg.Host, cfg.Port, err)
		}
		return conn, false, nil
	}
	jumpClient, err := connect(ctx, *cfg.Jump, 0)
	if err != nil {
		return nil, false, err
	}
	conn, err := jumpClient.ssh.DialContext(ctx, "tcp", endpointString(cfg.Host, cfg.Port))
	if err != nil {
		jumpClient.Close()
		return nil, true, classifyJumpDialError(cfg.Host, cfg.Port, endpointString(cfg.Jump.Host, cfg.Jump.Port), err)
	}
	return &jumpChainConn{Conn: conn, jump: jumpClient}, true, nil
}

type jumpChainConn struct {
	net.Conn
	jump *Client
}

func (c *jumpChainConn) Close() error {
	err := c.Conn.Close()
	c.jump.Close()
	return err
}
