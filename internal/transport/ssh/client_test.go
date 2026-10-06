package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func TestSSHHostKeyExecPTYAndSFTP(t *testing.T) {
	server := newTestSSHServer(t, nil)
	cfg := testClientConfig(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
	cfg.AutoAcceptUnknown = false
	if _, err := Connect(context.Background(), cfg); !errors.Is(err, ErrHostKeyPending) {
		t.Fatalf("first connection = %v", err)
	}
	presented := newHostKey(cfg.Host, cfg.Port, server.hostKey)
	cfg.HostKeyApproval = &HostKeyApproval{Fingerprint: presented.Fingerprint}
	client, err := Connect(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })

	t.Run("exec and exit semantics", func(t *testing.T) {
		result, err := client.Exec(context.Background(), "basic", base.ExecOptions{})
		if err != nil || result.Stdout != "stdout" || result.Stderr != "stderr" || result.ExitCode == nil || *result.ExitCode != 0 || result.Truncated {
			t.Fatalf("exec = %+v, %v", result, err)
		}
		result, err = client.Exec(context.Background(), "exit42", base.ExecOptions{})
		if err != nil || result.ExitCode == nil || *result.ExitCode != 42 || result.Stderr != "failed" {
			t.Fatalf("exit 42 = %+v, %v", result, err)
		}
		result, err = client.Exec(context.Background(), "large", base.ExecOptions{Limits: base.OutputLimits{Stdout: 8, Stderr: 4}})
		if err != nil || len(result.Stdout) != 8 || len(result.Stderr) != 4 || !result.Truncated {
			t.Fatalf("limited exec = %+v, %v", result, err)
		}
		if _, err := client.Exec(context.Background(), "basic", base.ExecOptions{ExpectedGeneration: client.Generation() + 1}); !errors.Is(err, base.ErrStaleGeneration) {
			t.Fatalf("stale generation = %v", err)
		}
		if _, err := client.Ping(context.Background()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("streaming exec", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		channel, err := client.OpenExec(ctx, "stream", base.ExecOptions{})
		if err != nil {
			t.Fatal(err)
		}
		defer channel.Close()
		if channel.ID() == "" || channel.Generation() != client.Generation() {
			t.Fatalf("channel identity = %q/%d", channel.ID(), channel.Generation())
		}
		if _, err := channel.Write([]byte("abc")); err != nil {
			t.Fatal(err)
		}
		if err := channel.CloseWrite(); err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(channel)
		if err != nil || string(data) != "stream:abc" {
			t.Fatalf("stream = %q, %v", data, err)
		}
		if err := channel.Wait(ctx); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("PTY and shared connection", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		pty, err := client.OpenPTY(ctx, base.PTYOptions{Cols: 80, Rows: 24, ExpectedGeneration: client.Generation()})
		if err != nil {
			t.Fatal(err)
		}
		defer pty.Close()
		ready := make([]byte, 5)
		if _, err := io.ReadFull(pty, ready); err != nil || string(ready) != "ready" {
			t.Fatalf("PTY ready = %q, %v", ready, err)
		}
		if _, err := pty.Write([]byte("abc")); err != nil {
			t.Fatal(err)
		}
		echo := make([]byte, 8)
		if _, err := io.ReadFull(pty, echo); err != nil || string(echo) != "echo:abc" {
			t.Fatalf("PTY echo = %q, %v", echo, err)
		}
		if err := pty.Resize(ctx, 120, 40); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(time.Second)
		for (server.cols.Load() != 120 || server.rows.Load() != 40) && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if server.cols.Load() != 120 || server.rows.Load() != 40 {
			t.Fatalf("PTY size = %dx%d", server.cols.Load(), server.rows.Load())
		}
		result, err := client.Exec(context.Background(), "basic", base.ExecOptions{})
		if err != nil || result.Stdout != "stdout" || !client.IsAlive() {
			t.Fatalf("exec with open PTY = %+v, %v", result, err)
		}
	})

	t.Run("SFTP shares client", func(t *testing.T) {
		filesystem, err := client.SFTP(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := filesystem.WriteFile(context.Background(), "shared.txt", []byte("sftp"), false); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		errorsFound := make(chan error, 2)
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := client.Exec(context.Background(), "basic", base.ExecOptions{})
				errorsFound <- err
			}()
		}
		wg.Wait()
		close(errorsFound)
		for err := range errorsFound {
			if err != nil {
				t.Fatal(err)
			}
		}
		data, err := filesystem.ReadFile(context.Background(), "shared.txt", 100)
		if err != nil || string(data) != "sftp" {
			t.Fatalf("shared SFTP = %q, %v", data, err)
		}
	})
}

func TestSSHCancellationKeepsSharedConnection(t *testing.T) {
	server := newTestSSHServer(t, nil)
	client := connectTestClient(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := client.Exec(ctx, "sleep", base.ExecOptions{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("exec cancellation = %v", err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("exec cancellation did not unblock")
	}
	if !client.IsAlive() {
		t.Fatal("cancelling one exec closed the shared connection")
	}
	if _, err := client.Exec(context.Background(), "basic", base.ExecOptions{}); err != nil {
		t.Fatalf("exec after cancellation: %v", err)
	}

	started = time.Now()
	_, err := client.OpenExec(context.Background(), "block-request", base.ExecOptions{Timeout: 50 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("startup cancellation = %v", err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("startup cancellation did not unblock")
	}
	server.blockOnce.Do(func() { close(server.blockExec) })
}

func TestClientCloseUnblocksStalledSFTP(t *testing.T) {
	server := newTestSSHServer(t, nil)
	server.stallSFTP.Store(true)
	defer func() { server.blockOnce.Do(func() { close(server.blockExec) }) }()
	client := connectTestClient(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
	sftpResult := make(chan error, 1)
	go func() {
		_, err := client.SFTP(context.Background())
		sftpResult <- err
	}()
	select {
	case <-server.sftpStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("SFTP initialization did not reach the stalled version handshake")
	}
	closeResult := make(chan error, 1)
	go func() { closeResult <- client.Close() }()
	select {
	case <-closeResult:
	case <-time.After(2 * time.Second):
		t.Fatal("Client.Close hung behind stalled SFTP initialization")
	}
	select {
	case err := <-sftpResult:
		if err == nil {
			t.Fatal("stalled SFTP initialization unexpectedly succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closing the client did not unblock SFTP initialization")
	}
}

func TestSSHKeepalive(t *testing.T) {
	server := newTestSSHServer(t, nil)
	cfg := testClientConfig(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
	cfg.KeepAliveInterval = 20 * time.Millisecond
	cfg.KeepAliveMax = 3
	client, err := Connect(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	deadline := time.Now().Add(time.Second)
	for server.keepalives.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if server.keepalives.Load() == 0 {
		t.Fatal("server received no SSH keepalive request")
	}
	if !client.IsAlive() {
		t.Fatal("answered keepalive closed the connection")
	}
}

func TestSSHPrivateKeyAuthentication(t *testing.T) {
	_, private, signer := generateClientKey(t)
	server := newTestSSHServer(t, signer.PublicKey())
	block, err := gossh.MarshalPrivateKey(private, "test")
	if err != nil {
		t.Fatal(err)
	}
	client := connectTestClient(t, server, AuthConfig{Method: AuthKey, KeyPEM: pem.EncodeToMemory(block)})
	if _, err := client.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSSHAgentAuthentication(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SSH agent integration uses SSH_AUTH_SOCK over a Unix socket; Windows runs the compile-only coverage")
	}
	_, private, signer := generateClientKey(t)
	server := newTestSSHServer(t, signer.PublicKey())
	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: private}); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(t.TempDir(), "agent.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
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
	client := connectTestClient(t, server, AuthConfig{Method: AuthAgent, AgentSocket: socket})
	if _, err := client.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSSHDirectDialAndExecConn(t *testing.T) {
	server := newTestSSHServer(t, nil)
	client := connectTestClient(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
	echo := startEchoListener(t, "tcp", "127.0.0.1:0")
	conn, err := client.DialContext(context.Background(), "tcp", echo.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	assertConnEcho(t, conn, "tcp")
	conn.Close()
	unixRoot := t.TempDir()
	t.Run("unix direct-streamlocal", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("direct-streamlocal integration needs a local Unix echo socket; Windows has compile-only coverage")
		}
		socket := filepath.Join(unixRoot, "e.sock")
		unixEcho := startEchoListener(t, "unix", socket)
		defer unixEcho.Close()
		conn, err := client.DialContext(context.Background(), "unix", socket)
		if err != nil {
			t.Fatal(err)
		}
		assertConnEcho(t, conn, "unix")
		conn.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	execConn, err := client.OpenExecConn(ctx, "dial-stdio", base.ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer execConn.Close()
	if _, err := execConn.Write([]byte("docker")); err != nil {
		t.Fatal(err)
	}
	if err := execConn.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(execConn)
	waitErr := execConn.Wait(ctx)
	stderr, _ := io.ReadAll(execConn.Stderr())
	if err != nil || string(data) != "docker" || waitErr != nil {
		t.Fatalf("exec conn = %q, %v; wait = %v; stderr = %q", data, err, waitErr, stderr)
	}
}

func TestSSHExecConnOrderedOutput(t *testing.T) {
	server := newTestSSHServer(t, nil)
	client := connectTestClient(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	execConn, err := client.OpenExecConn(ctx, "ordered-live", base.ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer execConn.Close()
	expected := []struct {
		data   string
		stderr bool
	}{
		{data: "out-1"},
		{data: "err-1", stderr: true},
		{data: "out-2"},
		{data: "err-2", stderr: true},
	}
	for index, want := range expected {
		event, err := execConn.NextOutput(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if string(event.Data) != want.data || event.Stderr != want.stderr || event.Sequence != uint64(index+1) {
			t.Fatalf("event %d = %+v", index, event)
		}
		if _, err := execConn.Write([]byte{byte(index)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := execConn.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if _, err := execConn.NextOutput(ctx); !errors.Is(err, io.EOF) {
		t.Fatalf("ordered stream EOF = %v", err)
	}
	if err := execConn.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := execConn.Read(make([]byte, 1)); !errors.Is(err, base.ErrOutputMode) {
		t.Fatalf("raw read after ordered selection = %v", err)
	}
}

func TestSSHExecConnCloseWriteAfterRemoteClose(t *testing.T) {
	server := newTestSSHServer(t, nil)
	client := connectTestClient(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	execConn, err := client.OpenExecConn(ctx, "basic", base.ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer execConn.Close()
	if err := execConn.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if err := execConn.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite after remote close = %v", err)
	}
}

func TestSSHWaitDrainsFullRawQueues(t *testing.T) {
	server := newTestSSHServer(t, nil)
	client := connectTestClient(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := client.Exec(ctx, "full-queues", base.ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stdout != strings.Repeat("o", 33) || result.Stderr != strings.Repeat("e", 33) {
		t.Fatalf("full queue output = %q/%q", result.Stdout, result.Stderr)
	}
	if result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("full queue exit = %+v", result.ExitCode)
	}
}

func testClientConfig(t *testing.T, server *testSSHServer, auth AuthConfig) Config {
	t.Helper()
	host, port := splitAddress(t, server.listener.Addr().String())
	return Config{
		Host:              host,
		Port:              port,
		User:              "test",
		Auth:              auth,
		HostKeys:          NewMemoryHostKeyStore(),
		AutoAcceptUnknown: true,
		ConnectTimeout:    2 * time.Second,
		KeepAliveInterval: -1,
	}
}

func connectTestClient(t *testing.T, server *testSSHServer, auth AuthConfig) *Client {
	t.Helper()
	client, err := Connect(context.Background(), testClientConfig(t, server, auth))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

func generateClientKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey, gossh.Signer) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return public, private, signer
}

func startEchoListener(t *testing.T, network, address string) net.Listener {
	t.Helper()
	listener, err := net.Listen(network, address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buffer := make([]byte, 128)
		for {
			n, err := conn.Read(buffer)
			if err != nil {
				return
			}
			if _, err := conn.Write(buffer[:n]); err != nil {
				return
			}
		}
	}()
	return listener
}

func assertConnEcho(t *testing.T, conn net.Conn, message string) {
	t.Helper()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte(message)); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, len(message))
	if _, err := io.ReadFull(conn, data); err != nil || string(data) != message {
		t.Fatalf("connection echo = %q, %v", data, err)
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
}
