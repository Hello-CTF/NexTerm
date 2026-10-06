package ssh

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

type testSSHServer struct {
	listener     net.Listener
	config       *gossh.ServerConfig
	hostKey      gossh.PublicKey
	root         string
	blockExec    chan struct{}
	blockOnce    sync.Once
	cols         atomic.Uint32
	rows         atomic.Uint32
	keepalives   atomic.Uint32
	stallSFTP    atomic.Bool
	stallCancel  atomic.Bool
	rejectCancel atomic.Bool
	sftpStarted  chan struct{}
	active       atomic.Int32
	agentResult  chan error
	agentKey     gossh.PublicKey
}

func newTestSSHServer(t *testing.T, authorizedKey gossh.PublicKey) *testSSHServer {
	t.Helper()
	return newTestSSHServerConfig(t, authorizedKey, nil)
}

func newTestSSHServerConfig(t *testing.T, authorizedKey gossh.PublicKey, customize func(*gossh.ServerConfig)) *testSSHServer {
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
	if customize != nil {
		customize(config)
	}
	config.AddHostKey(signer)
	server := &testSSHServer{
		listener:    newTCPListener(t),
		config:      config,
		hostKey:     signer.PublicKey(),
		root:        t.TempDir(),
		blockExec:   make(chan struct{}),
		sftpStarted: make(chan struct{}, 1),
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
	s.active.Add(1)
	tracked := &trackedConn{Conn: conn, onClose: func() { s.active.Add(-1) }}
	serverConn, channels, requests, err := gossh.NewServerConn(tracked, s.config)
	if err != nil {
		tracked.Close()
		return
	}
	defer serverConn.Close()
	forwards := &remoteForwardSet{listeners: make(map[string]net.Listener)}
	go s.handleGlobalRequests(serverConn, requests, forwards)
	defer forwards.closeAll()
	for newChannel := range channels {
		switch newChannel.ChannelType() {
		case "session":
			channel, channelRequests, err := newChannel.Accept()
			if err == nil {
				go s.handleSession(serverConn, channel, channelRequests)
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

type trackedConn struct {
	net.Conn
	onClose func()
	once    sync.Once
}

func (c *trackedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.onClose)
	return err
}

func (s *testSSHServer) handleSession(conn *gossh.ServerConn, channel gossh.Channel, requests <-chan *gossh.Request) {
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
		case "auth-agent-req@openssh.com":
			request.Reply(true, nil)
			go s.checkForwardedAgent(conn)
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
			if s.stallSFTP.Load() {
				select {
				case s.sftpStarted <- struct{}{}:
				default:
				}
				<-s.blockExec
				return
			}
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
	case "ordered-live":
		events := []struct {
			data   string
			stderr bool
		}{
			{data: "out-1"},
			{data: "err-1", stderr: true},
			{data: "out-2"},
			{data: "err-2", stderr: true},
		}
		for _, event := range events {
			if event.stderr {
				_, _ = channel.Stderr().Write([]byte(event.data))
			} else {
				_, _ = channel.Write([]byte(event.data))
			}
			var acknowledgment [1]byte
			if _, err := channel.Read(acknowledgment[:]); err != nil {
				return
			}
		}
		sendExitStatus(channel, 0)
	case "full-queues":
		for range 33 {
			_, _ = channel.Write([]byte("o"))
		}
		for range 33 {
			_, _ = channel.Stderr().Write([]byte("e"))
		}
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

type remoteForwardSet struct {
	mu        sync.Mutex
	listeners map[string]net.Listener
}

func (s *testSSHServer) handleGlobalRequests(conn *gossh.ServerConn, requests <-chan *gossh.Request, forwards *remoteForwardSet) {
	for request := range requests {
		switch request.Type {
		case "keepalive@openssh.com":
			s.keepalives.Add(1)
			request.Reply(true, nil)
		case "tcpip-forward":
			s.handleTCPIPForward(conn, request, forwards)
		case "cancel-tcpip-forward":
			s.handleCancelTCPIPForward(request, forwards)
		default:
			request.Reply(false, nil)
		}
	}
}

func (s *testSSHServer) handleTCPIPForward(conn *gossh.ServerConn, request *gossh.Request, forwards *remoteForwardSet) {
	var payload struct {
		Addr string
		Port uint32
	}
	if err := gossh.Unmarshal(request.Payload, &payload); err != nil {
		request.Reply(false, nil)
		return
	}
	host := payload.Addr
	if host == "" {
		host = "127.0.0.1"
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(int(payload.Port))))
	if err != nil {
		request.Reply(false, nil)
		return
	}
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		_ = listener.Close()
		request.Reply(false, nil)
		return
	}
	port, err := strconv.ParseUint(portText, 10, 32)
	if err != nil {
		_ = listener.Close()
		request.Reply(false, nil)
		return
	}
	key := net.JoinHostPort(payload.Addr, strconv.Itoa(int(port)))
	forwards.mu.Lock()
	if _, exists := forwards.listeners[key]; exists {
		forwards.mu.Unlock()
		_ = listener.Close()
		request.Reply(false, nil)
		return
	}
	forwards.listeners[key] = listener
	forwards.mu.Unlock()
	request.Reply(true, gossh.Marshal(struct{ Port uint32 }{uint32(port)}))
	go s.acceptRemoteForward(conn, payload.Addr, uint32(port), listener)
}

func (s *testSSHServer) handleCancelTCPIPForward(request *gossh.Request, forwards *remoteForwardSet) {
	var payload struct {
		Addr string
		Port uint32
	}
	if err := gossh.Unmarshal(request.Payload, &payload); err != nil {
		request.Reply(false, nil)
		return
	}
	if s.stallCancel.Load() {
		return
	}
	if s.rejectCancel.Load() {
		request.Reply(false, nil)
		return
	}
	key := net.JoinHostPort(payload.Addr, strconv.Itoa(int(payload.Port)))
	forwards.mu.Lock()
	listener := forwards.listeners[key]
	delete(forwards.listeners, key)
	forwards.mu.Unlock()
	if listener == nil {
		request.Reply(false, nil)
		return
	}
	_ = listener.Close()
	request.Reply(true, nil)
}

func (s *testSSHServer) acceptRemoteForward(conn *gossh.ServerConn, addr string, port uint32, listener net.Listener) {
	for {
		origin, err := listener.Accept()
		if err != nil {
			return
		}
		go s.proxyRemoteForward(conn, addr, port, origin)
	}
}

func (s *testSSHServer) proxyRemoteForward(conn *gossh.ServerConn, addr string, port uint32, origin net.Conn) {
	originHost, originPortText, err := net.SplitHostPort(origin.RemoteAddr().String())
	if err != nil {
		_ = origin.Close()
		return
	}
	originPort, err := strconv.ParseUint(originPortText, 10, 32)
	if err != nil {
		_ = origin.Close()
		return
	}
	payload := struct {
		Addr       string
		Port       uint32
		OriginAddr string
		OriginPort uint32
	}{Addr: addr, Port: port, OriginAddr: originHost, OriginPort: uint32(originPort)}
	channel, requests, err := conn.OpenChannel("forwarded-tcpip", gossh.Marshal(&payload))
	if err != nil {
		_ = origin.Close()
		return
	}
	go gossh.DiscardRequests(requests)
	go func() {
		_, _ = io.Copy(channel, origin)
		channel.CloseWrite()
	}()
	go func() {
		_, _ = io.Copy(origin, channel)
		if tcp, ok := origin.(*net.TCPConn); ok {
			tcp.CloseWrite()
		}
		channel.Close()
		origin.Close()
	}()
}

func (f *remoteForwardSet) closeAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for key, listener := range f.listeners {
		delete(f.listeners, key)
		_ = listener.Close()
	}
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

func (s *testSSHServer) checkForwardedAgent(conn *gossh.ServerConn) {
	channel, requests, err := conn.OpenChannel("auth-agent@openssh.com", nil)
	if err != nil {
		s.reportAgentResult(fmt.Errorf("open auth-agent channel: %w", err))
		return
	}
	defer channel.Close()
	go gossh.DiscardRequests(requests)
	signers, err := agent.NewClient(channel).Signers()
	if err != nil {
		s.reportAgentResult(fmt.Errorf("list forwarded agent signers: %w", err))
		return
	}
	if len(signers) != 1 || !bytes.Equal(signers[0].PublicKey().Marshal(), s.agentKey.Marshal()) {
		s.reportAgentResult(fmt.Errorf("forwarded agent signers = %d, want the single forwarded test key", len(signers)))
		return
	}
	s.reportAgentResult(nil)
}

func (s *testSSHServer) reportAgentResult(err error) {
	select {
	case s.agentResult <- err:
	default:
	}
}
