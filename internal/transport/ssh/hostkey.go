package ssh

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strings"

	gossh "golang.org/x/crypto/ssh"
)

var (
	ErrHostKeyPending  = errors.New("SSH host key requires confirmation")
	ErrHostKeyChanged  = errors.New("SSH host key changed")
	ErrHostKeyConflict = errors.New("SSH host key store conflict")
)

type HostKey struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	KeyType     string `json:"keyType"`
	Fingerprint string `json:"fingerprint"`
	Key         string `json:"key"`
}

type HostKeyApproval struct {
	Fingerprint string
	Replace     bool
}

type HostKeyError struct {
	Pending   bool
	Presented HostKey
	Known     []HostKey
}

func (e *HostKeyError) Error() string {
	if e.Pending {
		return fmt.Sprintf("unknown SSH host key for %s: %s", endpointString(e.Presented.Host, e.Presented.Port), e.Presented.Fingerprint)
	}
	return fmt.Sprintf("SSH host key for %s changed; presented %s", endpointString(e.Presented.Host, e.Presented.Port), e.Presented.Fingerprint)
}

func (e *HostKeyError) Unwrap() error {
	if e.Pending {
		return ErrHostKeyPending
	}
	return ErrHostKeyChanged
}

type HostKeyStore interface {
	HostKeys(ctx context.Context, host string, port int) ([]HostKey, error)
	PutHostKey(ctx context.Context, key HostKey, replace bool) error
}

func newHostKey(host string, port int, key gossh.PublicKey) HostKey {
	return HostKey{
		Host:        host,
		Port:        port,
		KeyType:     key.Type(),
		Fingerprint: gossh.FingerprintSHA256(key),
		Key:         base64.StdEncoding.EncodeToString(key.Marshal()),
	}
}

func (k HostKey) valid() bool {
	if strings.TrimSpace(k.Host) == "" || k.Port < 1 || k.Port > 65535 || k.KeyType == "" || k.Fingerprint == "" {
		return false
	}
	key, err := base64.StdEncoding.DecodeString(k.Key)
	return err == nil && len(key) > 0
}

func verifyHostKey(ctx context.Context, store HostKeyStore, host string, port int, autoAcceptUnknown bool, approval *HostKeyApproval, presented gossh.PublicKey) error {
	key := newHostKey(host, port, presented)
	known, err := store.HostKeys(ctx, host, port)
	if err != nil {
		return fmt.Errorf("load SSH host keys: %w", err)
	}
	for _, candidate := range known {
		if candidate.KeyType == key.KeyType && candidate.Key == key.Key {
			return nil
		}
	}

	if len(known) == 0 {
		approved := autoAcceptUnknown || (approval != nil && approval.Fingerprint == key.Fingerprint)
		if !approved {
			return &HostKeyError{Pending: true, Presented: key}
		}
		if err := store.PutHostKey(ctx, key, false); err != nil {
			return fmt.Errorf("persist approved SSH host key: %w", err)
		}
		return nil
	}

	if approval == nil || !approval.Replace || approval.Fingerprint != key.Fingerprint {
		return &HostKeyError{Presented: key, Known: known}
	}
	if err := store.PutHostKey(ctx, key, true); err != nil {
		return fmt.Errorf("replace approved SSH host key: %w", err)
	}
	return nil
}

func normalizeEndpoint(host string, port int) (string, int, error) {
	host = strings.TrimSpace(host)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}
	if port == 0 {
		port = 22
	}
	if host == "" || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("invalid SSH endpoint %q:%d", host, port)
	}
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	} else {
		host = strings.ToLower(host)
	}
	return host, port, nil
}

func endpointString(host string, port int) string {
	return net.JoinHostPort(host, fmt.Sprintf("%d", port))
}
