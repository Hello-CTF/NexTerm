package ssh

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"
)

type testSSHServer struct {
	listener   net.Listener
	config     *gossh.ServerConfig
	hostKey    gossh.PublicKey
	root       string
	blockExec  chan struct{}
	blockOnce  sync.Once
	cols       atomic.Uint32
	rows       atomic.Uint32
	keepalives atomic.Uint32
}

func newTestSSHServer(t *testing.T, authorizedKey gossh.PublicKey) *testSSHServer {
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
			return nil, fmt.Errorf("invalid test credentials")
		},
		PublicKeyCallback: func(metadata gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
			if metadata.User() == "test" && authorizedKey != nil && bytes.Equal(key.Marshal(), authorizedKey.Marshal()) {
				return nil, nil
			}
			return nil, fmt.Errorf("unauthorized test key")
		},
	}
	config.AddHostKey(signer)
	server := &testSSHServer{
		listener:  newTCPListener(t),
		config:    config,
		hostKey:   signer.PublicKey(),
		root:      t.TempDir(),
		blockExec: make(chan struct{}),
	}
	go server.accept()
	t.Cleanup(func() {
		server.listener.Close()
		server.blockOnce.Do(func() { close(server.blockExec) })
	})
	return server
}

func (s *testSSHServer) accept() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn)
	}
}

func (s *testSSHServer) handleConn(conn net.Conn) {
	serverConn, channels, requests, err := gossh.NewServerConn(conn, s.config)
	if err != nil {
		conn.Close()
		return
	}
	defer serverConn.Close()
	go func() {
		for request := range requests {
			if request.Type == "keepalive@openssh.com" {
				s.keepalives.Add(1)
				request.Reply(true, nil)
			} else {
				request.Reply(false, nil)
			}
		}
	}()
	for newChannel := range channels {
		switch newChannel.ChannelType() {
		case "session":
			channel, channelRequests, err := newChannel.Accept()
			if err == nil {
				go s.handleSession(channel, channelRequests)
			}
		case "direct-tcpip":
			s.handleDirectTCPIP(newChannel)
		case "direct-streamlocal@openssh.com":
			s.handleDirectStreamLocal(newChannel)
		default:
			newChannel.Reject(gossh.UnknownChannelType, "unsupported test channel")
		}
	}
}

func (s *testSSHServer) handleSession(channel gossh.Channel, requests <-chan *gossh.Request) {
	defer channel.Close()
	var shellDone chan struct{}
	for request := range requests {
		switch request.Type {
		case "pty-req":
			var payload struct {
				Term          string
				Cols, Rows    uint32
				Width, Height uint32
				Modes         string
			}
			if err := gossh.Unmarshal(request.Payload, &payload); err == nil {
				s.cols.Store(payload.Cols)
				s.rows.Store(payload.Rows)
			}
			request.Reply(true, nil)
		case "window-change":
			var payload struct{ Cols, Rows, Width, Height uint32 }
			if err := gossh.Unmarshal(request.Payload, &payload); err == nil {
				s.cols.Store(payload.Cols)
				s.rows.Store(payload.Rows)
			}
			if request.WantReply {
				request.Reply(true, nil)
			}
		case "shell":
			request.Reply(true, nil)
			shellDone = make(chan struct{})
			go func() {
				s.handleShell(channel)
				close(shellDone)
			}()
		case "exec":
			var payload struct{ Command string }
			if err := gossh.Unmarshal(request.Payload, &payload); err != nil {
				request.Reply(false, nil)
				continue
			}
			if payload.Command == "block-request" {
				<-s.blockExec
				return
			}
			request.Reply(true, nil)
			s.execute(channel, payload.Command)
			return
		case "subsystem":
			var payload struct{ Name string }
			if err := gossh.Unmarshal(request.Payload, &payload); err != nil || payload.Name != "sftp" {
				request.Reply(false, nil)
				continue
			}
			request.Reply(true, nil)
			server, err := sftp.NewServer(channel, sftp.WithServerWorkingDirectory(s.root))
			if err != nil {
				return
			}
			server.Serve()
			server.Close()
			return
		default:
			request.Reply(false, nil)
		}
	}
	if shellDone != nil {
		<-shellDone
	}
}

func (s *testSSHServer) handleShell(channel gossh.Channel) {
	if _, err := channel.Write([]byte("ready")); err != nil {
		return
	}
	buffer := make([]byte, 1024)
	for {
		n, err := channel.Read(buffer)
		if err != nil {
			return
		}
		if _, err := channel.Write(append([]byte("echo:"), buffer[:n]...)); err != nil {
			return
		}
	}
}

func (s *testSSHServer) execute(channel gossh.Channel, command string) {
	switch command {
	case "basic":
		channel.Write([]byte("stdout"))
		channel.Stderr().Write([]byte("stderr"))
		sendExitStatus(channel, 0)
	case ": ; true":
		sendExitStatus(channel, 0)
	case "exit42":
		channel.Stderr().Write([]byte("failed"))
		sendExitStatus(channel, 42)
	case "large":
		channel.Write(bytes.Repeat([]byte("o"), 64))
		channel.Stderr().Write(bytes.Repeat([]byte("e"), 32))
		sendExitStatus(channel, 0)
	case "sleep":
		_, _ = io.Copy(io.Discard, channel)
	case "stream":
		data, _ := io.ReadAll(channel)
		channel.Write(append([]byte("stream:"), data...))
		sendExitStatus(channel, 0)
	case "dial-stdio":
		buffer := make([]byte, 4096)
		for {
			n, err := channel.Read(buffer)
			if n > 0 {
				if _, writeErr := channel.Write(buffer[:n]); writeErr != nil {
					return
				}
			}
			if err != nil {
				sendExitStatus(channel, 0)
				return
			}
		}
	default:
		channel.Stderr().Write([]byte("unknown command"))
		sendExitStatus(channel, 127)
	}
}

func sendExitStatus(channel gossh.Channel, status uint32) {
	_, _ = channel.SendRequest("exit-status", false, gossh.Marshal(struct{ Status uint32 }{status}))
}

func (s *testSSHServer) handleDirectTCPIP(newChannel gossh.NewChannel) {
	var payload struct {
		Raddr string
		Rport uint32
		Laddr string
		Lport uint32
	}
	if err := gossh.Unmarshal(newChannel.ExtraData(), &payload); err != nil {
		newChannel.Reject(gossh.Prohibited, "invalid direct-tcpip payload")
		return
	}
	target, err := net.Dial("tcp", net.JoinHostPort(payload.Raddr, fmt.Sprintf("%d", payload.Rport)))
	if err != nil {
		newChannel.Reject(gossh.ConnectionFailed, err.Error())
		return
	}
	s.proxyChannel(newChannel, target)
}

func (s *testSSHServer) handleDirectStreamLocal(newChannel gossh.NewChannel) {
	var payload struct {
		SocketPath string
		Reserved0  string
		Reserved1  uint32
	}
	if err := gossh.Unmarshal(newChannel.ExtraData(), &payload); err != nil {
		newChannel.Reject(gossh.Prohibited, "invalid direct-streamlocal payload")
		return
	}
	target, err := net.Dial("unix", payload.SocketPath)
	if err != nil {
		newChannel.Reject(gossh.ConnectionFailed, err.Error())
		return
	}
	s.proxyChannel(newChannel, target)
}

func (s *testSSHServer) proxyChannel(newChannel gossh.NewChannel, target net.Conn) {
	channel, requests, err := newChannel.Accept()
	if err != nil {
		target.Close()
		return
	}
	go gossh.DiscardRequests(requests)
	go func() {
		_, _ = io.Copy(channel, target)
		channel.CloseWrite()
	}()
	go func() {
		_, _ = io.Copy(target, channel)
		if tcp, ok := target.(*net.TCPConn); ok {
			tcp.CloseWrite()
		}
		channel.Close()
		target.Close()
	}()
}
