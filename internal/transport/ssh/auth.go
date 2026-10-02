package ssh

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func authentication(ctx context.Context, cfg AuthConfig) (ssh.AuthMethod, []io.Closer, error) {
	switch cfg.Method {
	case AuthPassword:
		return ssh.Password(cfg.Password), nil, nil
	case AuthKey:
		key, err := privateKey(cfg)
		if err != nil {
			return nil, nil, err
		}
		return ssh.PublicKeys(key), nil, nil
	case AuthAgent:
		socket := cfg.AgentSocket
		if socket == "" {
			socket = os.Getenv("SSH_AUTH_SOCK")
		}
		if socket == "" {
			return nil, nil, fmt.Errorf("SSH agent socket is not configured and SSH_AUTH_SOCK is empty")
		}
		conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
		if err != nil {
			return nil, nil, fmt.Errorf("connect SSH agent: %w", err)
		}
		agentClient := agent.NewClient(conn)
		return ssh.PublicKeysCallback(agentClient.Signers), []io.Closer{conn}, nil
	default:
		return nil, nil, fmt.Errorf("unsupported SSH authentication method %q", cfg.Method)
	}
}

func privateKey(cfg AuthConfig) (ssh.Signer, error) {
	pem := cfg.KeyPEM
	if len(pem) == 0 {
		if strings.TrimSpace(cfg.KeyPath) == "" {
			return nil, fmt.Errorf("SSH private key path or content is required")
		}
		var err error
		pem, err = os.ReadFile(cfg.KeyPath)
		if err != nil {
			return nil, fmt.Errorf("read SSH private key: %w", err)
		}
	}
	if cfg.Passphrase != "" {
		key, err := ssh.ParsePrivateKeyWithPassphrase(pem, []byte(cfg.Passphrase))
		if err != nil {
			return nil, fmt.Errorf("parse encrypted SSH private key: %w", err)
		}
		return key, nil
	}
	key, err := ssh.ParsePrivateKey(pem)
	if err != nil {
		return nil, fmt.Errorf("parse SSH private key: %w", err)
	}
	return key, nil
}
