package session

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/ssh"
	gossh "golang.org/x/crypto/ssh"
)

// lifetimeSSHServer is a minimal in-process SSH server: one ed25519 host key,
// password auth, and a session channel that runs the host's shell wired to
// the channel. Tests pin the host key out of band instead of auto-accepting
// it.
type lifetimeSSHServer struct {
	listener net.Listener
	config   *gossh.ServerConfig
	hostKey  gossh.PublicKey
	port     int
}

func newLifetimeSSHServer(t *testing.T) *lifetimeSSHServer {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	config := &gossh.ServerConfig{
		PasswordCallback: func(metadata gossh.ConnMetadata, password []byte) (*gossh.Permissions, error) {
			if metadata.User() == "test" && string(password) == "secret" {
				return nil, nil
			}
			return nil, errors.New("invalid test credentials")
		},
	}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, portString, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		t.Fatal(err)
	}
	server := &lifetimeSSHServer{listener: listener, config: config, hostKey: signer.PublicKey(), port: port}
	go server.accept()
	t.Cleanup(func() { _ = listener.Close() })
	return server
}

func (s *lifetimeSSHServer) accept() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn)
	}
}

func (s *lifetimeSSHServer) handleConn(conn net.Conn) {
	serverConn, channels, requests, err := gossh.NewServerConn(conn, s.config)
	if err != nil {
		_ = conn.Close()
		return
	}
	defer func() { _ = serverConn.Close() }()
	go gossh.DiscardRequests(requests)
	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(gossh.UnknownChannelType, "unsupported test channel")
			continue
		}
		channel, channelRequests, err := newChannel.Accept()
		if err != nil {
			continue
		}
		go s.handleSession(channel, channelRequests)
	}
}

func (s *lifetimeSSHServer) handleSession(channel gossh.Channel, requests <-chan *gossh.Request) {
	defer func() { _ = channel.Close() }()
	for request := range requests {
		switch request.Type {
		case "pty-req", "window-change":
			_ = request.Reply(true, nil)
		case "shell":
			_ = request.Reply(true, nil)
			s.runShell(channel)
			return
		default:
			_ = request.Reply(false, nil)
		}
	}
}

// lifetimeShell names a shell the test host can execute: /bin/sh on Unix,
// the Windows command processor (with banner/prompt noise the tests tolerate
// because they match markers by substring) elsewhere. The lifetime tests only
// require a process that runs `echo <marker>` lines over the channel.
func lifetimeShell() (string, []string) {
	if runtime.GOOS == "windows" {
		comspec := os.Getenv("COMSPEC")
		if comspec == "" {
			comspec = `C:\Windows\System32\cmd.exe`
		}
		return comspec, []string{"/Q"}
	}
	return "/bin/sh", nil
}

func (s *lifetimeSSHServer) runShell(channel gossh.Channel) {
	name, args := lifetimeShell()
	command := exec.Command(name, args...)
	command.Stdin = channel
	command.Stdout = channel
	command.Stderr = channel
	if err := command.Start(); err != nil {
		return
	}
	_ = command.Wait()
	_, _ = channel.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
}

func (s *lifetimeSSHServer) clientConfig(t *testing.T) ssh.Config {
	t.Helper()
	store := ssh.NewMemoryHostKeyStore()
	key := ssh.HostKey{
		Host:        "127.0.0.1",
		Port:        s.port,
		KeyType:     s.hostKey.Type(),
		Fingerprint: gossh.FingerprintSHA256(s.hostKey),
		Key:         base64.StdEncoding.EncodeToString(s.hostKey.Marshal()),
	}
	if err := store.PutHostKey(context.Background(), key, false); err != nil {
		t.Fatal(err)
	}
	return ssh.Config{
		Host:              "127.0.0.1",
		Port:              s.port,
		User:              "test",
		Auth:              ssh.AuthConfig{Method: ssh.AuthPassword, Password: "secret"},
		HostKeys:          store,
		ConnectTimeout:    5 * time.Second,
		KeepAliveInterval: -1,
	}
}

func waitForLifetimeTerminalBytes(t *testing.T, terminal *fakeTerminal, marker string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if strings.Contains(string(terminal.bytes()), marker) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("terminal never recorded %q; raw=%q", marker, terminal.bytes())
}

// The replacement channel opened for a reconnected SSH session must outlive
// the reconnect attempt: transports bind an opened channel's lifetime to the
// context they receive, so an attempt-scoped context tore the fresh channel
// down moments after Reconnect returned and the next Write failed with
// "terminal tab closed". The window is waited out deterministically here;
// the post-reconnect shell must still carry output.
func TestReconnectReplacementChannelOutlivesAttempt(t *testing.T) {
	server := newLifetimeSSHServer(t)
	config := server.clientConfig(t)
	terminals := newFakeTerminalFactory()
	manager := NewManager(Config{
		Connector: ConnectorFunc(func(ctx context.Context, asset Asset, generation uint64) (base.Transport, error) {
			return ssh.Connect(ctx, config)
		}),
		Terminals: terminals, ReconnectMax: 10, ReconnectBackoff: []time.Duration{0}, OpenAccess: true,
	})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()

	connected, err := manager.Connect(ctx, Asset{ID: "ssh-asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(ctx, OpenTabOptions{SessionID: connected.ID, ClientID: "client-a", ChannelID: "channel-a", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Write(ctx, info.ID, "client-a", []byte("echo lifetime-first\n")); err != nil {
		t.Fatal(err)
	}
	waitForLifetimeTerminalBytes(t, terminals.terminal(info.ID), "lifetime-first", 10*time.Second)

	if err := manager.Disconnect(connected.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reconnect(ctx, connected.ID); err != nil {
		t.Fatal(err)
	}

	// The replacement channel must survive past the reconnect call; when it
	// was bound to the attempt's context it was closed asynchronously within
	// moments of Reconnect returning, so this write used to fail with
	// "terminal tab closed" and the second echo never reached the terminal.
	time.Sleep(500 * time.Millisecond)
	if err := manager.Write(ctx, info.ID, "client-a", []byte("echo lifetime-second\n")); err != nil {
		t.Fatalf("write on the reconnected tab: %v", err)
	}
	waitForLifetimeTerminalBytes(t, terminals.terminal(info.ID), "lifetime-second", 10*time.Second)
}
