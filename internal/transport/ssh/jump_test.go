package ssh

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func TestSSHJumpHostChainEndToEnd(t *testing.T) {
	_, jumpPrivate, jumpSigner := generateClientKey(t)
	jump := newTestSSHServer(t, jumpSigner.PublicKey())
	target := newTestSSHServer(t, nil)
	echo := startEchoListener(t, "tcp", "127.0.0.1:0")

	store := NewMemoryHostKeyStore()
	jumpAuth := AuthConfig{Method: AuthKey, KeyPEM: marshalClientKey(t, jumpPrivate)}
	targetAuth := AuthConfig{Method: AuthPassword, Password: "secret"}
	jumpCfg := testClientConfig(t, jump, jumpAuth)
	jumpCfg.HostKeys = store
	targetCfg := testClientConfig(t, target, targetAuth)
	targetCfg.HostKeys = store
	targetCfg.Jump = &jumpCfg

	client, err := Connect(context.Background(), targetCfg)
	if err != nil {
		t.Fatal(err)
	}

	result, err := client.Exec(context.Background(), "basic", base.ExecOptions{})
	if err != nil || result.Stdout != "stdout" {
		t.Fatalf("exec through jump = %+v, %v", result, err)
	}

	pty, err := client.OpenPTY(context.Background(), base.PTYOptions{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	ready := make([]byte, 5)
	if _, err := io.ReadFull(pty, ready); err != nil || string(ready) != "ready" {
		t.Fatalf("PTY through jump = %q, %v", ready, err)
	}
	pty.Close()

	filesystem, err := client.SFTP(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := filesystem.WriteFile(context.Background(), "jump.txt", []byte("sftp"), false); err != nil {
		t.Fatal(err)
	}
	data, err := filesystem.ReadFile(context.Background(), "jump.txt", 100)
	if err != nil || string(data) != "sftp" {
		t.Fatalf("SFTP through jump = %q, %v", data, err)
	}

	conn, err := client.DialContext(context.Background(), "tcp", echo.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	assertConnEcho(t, conn, "forward")
	conn.Close()

	for _, endpoint := range []struct {
		host string
		port int
	}{
		{jumpCfg.Host, jumpCfg.Port},
		{targetCfg.Host, targetCfg.Port},
	} {
		keys, err := store.HostKeys(context.Background(), endpoint.host, endpoint.port)
		if err != nil || len(keys) != 1 {
			t.Fatalf("host keys for hop %s:%d = %+v, %v", endpoint.host, endpoint.port, keys, err)
		}
	}

	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for (jump.active.Load() != 0 || target.active.Load() != 0) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if jump.active.Load() != 0 || target.active.Load() != 0 {
		t.Fatalf("connections left open after close: jump=%d target=%d", jump.active.Load(), target.active.Load())
	}
}

func TestSSHJumpChainThreeHops(t *testing.T) {
	first := newTestSSHServer(t, nil)
	second := newTestSSHServer(t, nil)
	target := newTestSSHServer(t, nil)

	store := NewMemoryHostKeyStore()
	auth := AuthConfig{Method: AuthPassword, Password: "secret"}
	firstCfg := testClientConfig(t, first, auth)
	firstCfg.HostKeys = store
	secondCfg := testClientConfig(t, second, auth)
	secondCfg.HostKeys = store
	secondCfg.Jump = &firstCfg
	targetCfg := testClientConfig(t, target, auth)
	targetCfg.HostKeys = store
	targetCfg.Jump = &secondCfg

	client, err := Connect(context.Background(), targetCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, server := range []*testSSHServer{first, second, target} {
		host, port := splitAddress(t, server.listener.Addr().String())
		keys, err := store.HostKeys(context.Background(), host, port)
		if err != nil || len(keys) != 1 {
			t.Fatalf("host keys for hop %s:%d = %+v, %v", host, port, keys, err)
		}
	}
}

func TestSSHJumpHostKeyVerificationPerHop(t *testing.T) {
	jump := newTestSSHServer(t, nil)
	target := newTestSSHServer(t, nil)
	auth := AuthConfig{Method: AuthPassword, Password: "secret"}

	t.Run("jump host key pending", func(t *testing.T) {
		store := NewMemoryHostKeyStore()
		jumpCfg := testClientConfig(t, jump, auth)
		jumpCfg.HostKeys = store
		jumpCfg.AutoAcceptUnknown = false
		targetCfg := testClientConfig(t, target, auth)
		targetCfg.HostKeys = store
		targetCfg.Jump = &jumpCfg
		_, err := Connect(context.Background(), targetCfg)
		if !errors.Is(err, ErrHostKeyPending) {
			t.Fatalf("connect = %v", err)
		}
		var keyErr *HostKeyError
		if !errors.As(err, &keyErr) || keyErr.Presented.Host != jumpCfg.Host || keyErr.Presented.Port != jumpCfg.Port {
			t.Fatalf("pending endpoint = %+v, %v", keyErr, err)
		}
	})

	t.Run("target host key pending", func(t *testing.T) {
		store := NewMemoryHostKeyStore()
		jumpCfg := testClientConfig(t, jump, auth)
		jumpCfg.HostKeys = store
		targetCfg := testClientConfig(t, target, auth)
		targetCfg.HostKeys = store
		targetCfg.AutoAcceptUnknown = false
		targetCfg.Jump = &jumpCfg
		_, err := Connect(context.Background(), targetCfg)
		if !errors.Is(err, ErrHostKeyPending) {
			t.Fatalf("connect = %v", err)
		}
		var keyErr *HostKeyError
		if !errors.As(err, &keyErr) || keyErr.Presented.Host != targetCfg.Host || keyErr.Presented.Port != targetCfg.Port {
			t.Fatalf("pending endpoint = %+v, %v", keyErr, err)
		}
	})

	t.Run("jump host key changed", func(t *testing.T) {
		store := NewMemoryHostKeyStore()
		if err := store.PutHostKey(context.Background(), newHostKey(jumpHost(t, jump), jumpPort(t, jump), hostKeyForTest(t)), false); err != nil {
			t.Fatal(err)
		}
		jumpCfg := testClientConfig(t, jump, auth)
		jumpCfg.HostKeys = store
		targetCfg := testClientConfig(t, target, auth)
		targetCfg.HostKeys = store
		targetCfg.Jump = &jumpCfg
		_, err := Connect(context.Background(), targetCfg)
		if !errors.Is(err, ErrHostKeyChanged) {
			t.Fatalf("connect = %v", err)
		}
		var keyErr *HostKeyError
		if !errors.As(err, &keyErr) || len(keyErr.Known) != 1 {
			t.Fatalf("changed detail = %+v, %v", keyErr, err)
		}
	})
}

func TestSSHJumpFailures(t *testing.T) {
	jump := newTestSSHServer(t, nil)
	target := newTestSSHServer(t, nil)

	t.Run("jump authentication failure names the jump endpoint", func(t *testing.T) {
		store := NewMemoryHostKeyStore()
		jumpCfg := testClientConfig(t, jump, AuthConfig{Method: AuthPassword, Password: "wrong"})
		jumpCfg.HostKeys = store
		targetCfg := testClientConfig(t, target, AuthConfig{Method: AuthPassword, Password: "secret"})
		targetCfg.HostKeys = store
		targetCfg.Jump = &jumpCfg
		_, err := Connect(context.Background(), targetCfg)
		if err == nil || !strings.Contains(err.Error(), jumpCfg.Host) {
			t.Fatalf("connect = %v", err)
		}
	})

	t.Run("target unreachable through jump", func(t *testing.T) {
		store := NewMemoryHostKeyStore()
		jumpCfg := testClientConfig(t, jump, AuthConfig{Method: AuthPassword, Password: "secret"})
		jumpCfg.HostKeys = store
		dead := deadEndpoint(t)
		targetCfg := testClientConfig(t, target, AuthConfig{Method: AuthPassword, Password: "secret"})
		targetCfg.HostKeys = store
		targetCfg.Host = dead.host
		targetCfg.Port = dead.port
		targetCfg.Jump = &jumpCfg
		_, err := Connect(context.Background(), targetCfg)
		if err == nil || !strings.Contains(err.Error(), "through jump host") {
			t.Fatalf("connect = %v", err)
		}
	})
}

func TestSSHKeyboardInteractiveAuthentication(t *testing.T) {
	prompts := [][]string{{"Password: "}, {"Password: ", "One-time code: ", "PIN: "}}
	for _, questions := range prompts {
		server := newTestSSHServerConfig(t, nil, func(config *gossh.ServerConfig) {
			config.KeyboardInteractiveCallback = func(metadata gossh.ConnMetadata, challenge gossh.KeyboardInteractiveChallenge) (*gossh.Permissions, error) {
				if metadata.User() != "test" {
					return nil, fmt.Errorf("unknown test user %q", metadata.User())
				}
				answers, err := challenge("", "test challenge", questions, make([]bool, len(questions)))
				if err != nil {
					return nil, err
				}
				if len(answers) != len(questions) {
					return nil, fmt.Errorf("answer count = %d, want %d", len(answers), len(questions))
				}
				for _, answer := range answers {
					if answer != "secret" {
						return nil, fmt.Errorf("keyboard-interactive rejected")
					}
				}
				return nil, nil
			}
		})
		client := connectTestClient(t, server, AuthConfig{Method: AuthKeyboardInteractive, Password: "secret"})
		if _, err := client.Ping(context.Background()); err != nil {
			t.Fatalf("prompts %v: %v", questions, err)
		}
		client.Close()
	}
}

func TestSSHKeyboardInteractiveFailureDoesNotLeakSecret(t *testing.T) {
	server := newTestSSHServerConfig(t, nil, func(config *gossh.ServerConfig) {
		config.KeyboardInteractiveCallback = func(metadata gossh.ConnMetadata, challenge gossh.KeyboardInteractiveChallenge) (*gossh.Permissions, error) {
			if _, err := challenge("", "", []string{"Password: "}, []bool{false}); err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("keyboard-interactive rejected")
		}
	})
	cfg := testClientConfig(t, server, AuthConfig{Method: AuthKeyboardInteractive, Password: "top-secret-value"})
	_, err := Connect(context.Background(), cfg)
	if err == nil {
		t.Fatal("wrong keyboard-interactive password unexpectedly connected")
	}
	if strings.Contains(err.Error(), "top-secret-value") {
		t.Fatalf("error leaks the secret: %v", err)
	}
}

func TestSSHCertificateAuthentication(t *testing.T) {
	_, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caSigner, err := gossh.NewSignerFromKey(caPrivate)
	if err != nil {
		t.Fatal(err)
	}
	_, clientPrivate, clientSigner := generateClientKey(t)
	server := newTestSSHServerConfig(t, nil, func(config *gossh.ServerConfig) {
		config.PublicKeyCallback = func(metadata gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
			cert, ok := key.(*gossh.Certificate)
			if !ok {
				return nil, fmt.Errorf("plain public keys are not authorized")
			}
			if !bytes.Equal(cert.SignatureKey.Marshal(), caSigner.PublicKey().Marshal()) {
				return nil, fmt.Errorf("certificate signed by an unknown authority")
			}
			if metadata.User() != "test" || !slices.Contains(cert.ValidPrincipals, "test") {
				return nil, fmt.Errorf("certificate principal mismatch")
			}
			return nil, nil
		}
	})

	certPath := writeClientCertificate(t, caSigner, clientSigner)
	client := connectTestClient(t, server, AuthConfig{Method: AuthKey, KeyPEM: marshalClientKey(t, clientPrivate), CertPath: certPath})
	if _, err := client.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	client.Close()

	t.Run("plain key without certificate is rejected by the cert-only server", func(t *testing.T) {
		cfg := testClientConfig(t, server, AuthConfig{Method: AuthKey, KeyPEM: marshalClientKey(t, clientPrivate)})
		if _, err := Connect(context.Background(), cfg); err == nil {
			t.Fatal("plain key unexpectedly authorized")
		}
	})

	t.Run("certificate for a different key fails", func(t *testing.T) {
		_, _, otherSigner := generateClientKey(t)
		mismatched := writeClientCertificate(t, caSigner, otherSigner)
		cfg := testClientConfig(t, server, AuthConfig{Method: AuthKey, KeyPEM: marshalClientKey(t, clientPrivate), CertPath: mismatched})
		_, err := Connect(context.Background(), cfg)
		if err == nil || !strings.Contains(err.Error(), "certificate") {
			t.Fatalf("mismatched certificate = %v", err)
		}
	})
}

func TestSSHAgentForwarding(t *testing.T) {
	_, private, signer := generateClientKey(t)
	server := newTestSSHServer(t, nil)
	server.agentKey = signer.PublicKey()
	server.agentResult = make(chan error, 1)
	socket := serveTestAgent(t, private)

	cfg := testClientConfig(t, server, AuthConfig{Method: AuthPassword, Password: "secret", AgentSocket: socket})
	cfg.ForwardAgent = true
	client, err := Connect(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	pty, err := client.OpenPTY(context.Background(), base.PTYOptions{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer pty.Close()
	select {
	case err := <-server.agentResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not observe the forwarded agent")
	}
}

func TestSSHAgentForwardingRequiresSocket(t *testing.T) {
	server := newTestSSHServer(t, nil)
	cfg := testClientConfig(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
	cfg.ForwardAgent = true
	t.Setenv("SSH_AUTH_SOCK", "")
	_, err := Connect(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "agent socket") {
		t.Fatalf("connect = %v", err)
	}
}

func TestSSHAgentForwardingExecSessions(t *testing.T) {
	_, private, signer := generateClientKey(t)
	server := newTestSSHServer(t, nil)
	server.agentKey = signer.PublicKey()
	server.agentResult = make(chan error, 2)
	socket := serveTestAgent(t, private)

	cfg := testClientConfig(t, server, AuthConfig{Method: AuthPassword, Password: "secret", AgentSocket: socket})
	cfg.ForwardAgent = true
	client, err := Connect(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	result, err := client.Exec(context.Background(), "basic", base.ExecOptions{})
	if err != nil || result.Stdout != "stdout" {
		t.Fatalf("exec with forwarding = %+v, %v", result, err)
	}
	select {
	case err := <-server.agentResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not observe the forwarded agent on the exec session")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	execConn, err := client.OpenExecConn(ctx, "basic", base.ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(execConn)
	waitErr := execConn.Wait(ctx)
	execConn.Close()
	if err != nil || string(data) != "stdout" || waitErr != nil {
		t.Fatalf("exec conn with forwarding = %q, %v; wait = %v", data, err, waitErr)
	}
	select {
	case err := <-server.agentResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not observe the forwarded agent on the exec conn session")
	}
}

func TestSSHAgentForwardingDefaultOffForExec(t *testing.T) {
	server := newTestSSHServer(t, nil)
	server.agentResult = make(chan error, 1)
	client := connectTestClient(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
	defer client.Close()
	if _, err := client.Exec(context.Background(), "basic", base.ExecOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-server.agentResult:
		t.Fatalf("agent forwarding was requested without forwardAgent: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
}

func marshalClientKey(t *testing.T, private ed25519.PrivateKey) []byte {
	t.Helper()
	block, err := gossh.MarshalPrivateKey(private, "test")
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(block)
}

func writeClientCertificate(t *testing.T, ca gossh.Signer, signer gossh.Signer) string {
	t.Helper()
	cert := &gossh.Certificate{
		Key:             signer.PublicKey(),
		CertType:        gossh.UserCert,
		KeyId:           "nexterm-test",
		ValidPrincipals: []string{"test"},
		ValidBefore:     gossh.CertTimeInfinity,
	}
	if err := cert.SignCert(rand.Reader, ca); err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/id_ed25519-cert.pub"
	if err := os.WriteFile(path, gossh.MarshalAuthorizedKey(cert), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func serveTestAgent(t *testing.T, private ed25519.PrivateKey) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("SSH agent integration uses SSH_AUTH_SOCK over a Unix socket; Windows runs the compile-only coverage")
	}
	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: private}); err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp("", "nxagent")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socket := filepath.Join(directory, "a.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				agent.ServeAgent(keyring, conn)
				conn.Close()
			}()
		}
	}()
	return socket
}

type deadEndpointAddr struct {
	host string
	port int
}

func deadEndpoint(t *testing.T) deadEndpointAddr {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host, port := splitAddress(t, listener.Addr().String())
	listener.Close()
	return deadEndpointAddr{host: host, port: port}
}

func jumpHost(t *testing.T, server *testSSHServer) string {
	t.Helper()
	host, _ := splitAddress(t, server.listener.Addr().String())
	return host
}

func jumpPort(t *testing.T, server *testSSHServer) int {
	t.Helper()
	_, port := splitAddress(t, server.listener.Addr().String())
	return port
}
