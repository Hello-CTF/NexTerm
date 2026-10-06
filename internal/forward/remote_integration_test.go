package forward

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/ssh"
	gossh "golang.org/x/crypto/ssh"
)

type remoteForwardSSHServer struct {
	listener net.Listener
	config   *gossh.ServerConfig
}

func newRemoteForwardSSHServer(t *testing.T) *remoteForwardSSHServer {
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
	}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &remoteForwardSSHServer{listener: listener, config: config}
	go server.accept()
	t.Cleanup(func() { _ = listener.Close() })
	return server
}

func (s *remoteForwardSSHServer) accept() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *remoteForwardSSHServer) handle(conn net.Conn) {
	serverConn, channels, requests, err := gossh.NewServerConn(conn, s.config)
	if err != nil {
		_ = conn.Close()
		return
	}
	defer serverConn.Close()
	var forwardsMu sync.Mutex
	forwards := make(map[string]net.Listener)
	defer func() {
		forwardsMu.Lock()
		defer forwardsMu.Unlock()
		for key, listener := range forwards {
			delete(forwards, key)
			_ = listener.Close()
		}
	}()
	go func() {
		for request := range requests {
			switch request.Type {
			case "tcpip-forward":
				s.handleTCPIPForward(serverConn, request, forwards, &forwardsMu)
			case "cancel-tcpip-forward":
				s.handleCancelTCPIPForward(request, forwards, &forwardsMu)
			default:
				request.Reply(false, nil)
			}
		}
	}()
	for newChannel := range channels {
		_ = newChannel.Reject(gossh.UnknownChannelType, "unsupported test channel")
	}
}

func (s *remoteForwardSSHServer) handleTCPIPForward(conn *gossh.ServerConn, request *gossh.Request, forwards map[string]net.Listener, forwardsMu *sync.Mutex) {
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
	forwardsMu.Lock()
	if _, exists := forwards[key]; exists {
		forwardsMu.Unlock()
		_ = listener.Close()
		request.Reply(false, nil)
		return
	}
	forwards[key] = listener
	forwardsMu.Unlock()
	request.Reply(true, gossh.Marshal(struct{ Port uint32 }{uint32(port)}))
	go s.acceptRemoteForward(conn, payload.Addr, uint32(port), listener)
}

func (s *remoteForwardSSHServer) handleCancelTCPIPForward(request *gossh.Request, forwards map[string]net.Listener, forwardsMu *sync.Mutex) {
	var payload struct {
		Addr string
		Port uint32
	}
	if err := gossh.Unmarshal(request.Payload, &payload); err != nil {
		request.Reply(false, nil)
		return
	}
	key := net.JoinHostPort(payload.Addr, strconv.Itoa(int(payload.Port)))
	forwardsMu.Lock()
	listener := forwards[key]
	delete(forwards, key)
	forwardsMu.Unlock()
	if listener == nil {
		request.Reply(false, nil)
		return
	}
	_ = listener.Close()
	request.Reply(true, nil)
}

func (s *remoteForwardSSHServer) acceptRemoteForward(conn *gossh.ServerConn, addr string, port uint32, listener net.Listener) {
	for {
		origin, err := listener.Accept()
		if err != nil {
			return
		}
		go s.proxyRemoteForward(conn, addr, port, origin)
	}
}

func (s *remoteForwardSSHServer) proxyRemoteForward(conn *gossh.ServerConn, addr string, port uint32, origin net.Conn) {
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

func connectRemoteForwardClient(t *testing.T, server *remoteForwardSSHServer) *ssh.Client {
	t.Helper()
	host, portText, err := net.SplitHostPort(server.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	client, err := ssh.Connect(context.Background(), ssh.Config{
		Host:              host,
		Port:              port,
		User:              "test",
		Auth:              ssh.AuthConfig{Method: ssh.AuthPassword, Password: "secret"},
		HostKeys:          ssh.NewMemoryHostKeyStore(),
		AutoAcceptUnknown: true,
		ConnectTimeout:    5 * time.Second,
		KeepAliveInterval: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestRemoteForwardEndToEndOverSSH(t *testing.T) {
	server := newRemoteForwardSSHServer(t)
	client := connectRemoteForwardClient(t, server)
	targetPort := echoTargetPort(t)

	service := NewService(Config{
		Provider: DialerProviderFunc(func(context.Context, string) (base.Dialer, error) {
			return client, nil
		}),
		Policy:      Policy{Desktop: true},
		DialTimeout: 2 * time.Second,
	})
	t.Cleanup(func() { _ = service.Close() })

	spec, err := service.CreateRemote(t.Context(), CreateRemoteArgs{
		SessionID:  "ssh-session",
		BindHost:   "127.0.0.1",
		BindPort:   0,
		TargetHost: "127.0.0.1",
		TargetPort: targetPort,
	})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Kind != KindRemote || spec.ListenHost != "127.0.0.1" || spec.ListenPort == 0 {
		t.Fatalf("spec = %+v", spec)
	}
	conn := dialForward(t, spec)
	assertEcho(t, conn, "remote-over-ssh")

	if err := service.Remove(spec.ID); err != nil {
		t.Fatal(err)
	}
	if leftover, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(spec.ListenPort))), 200*time.Millisecond); err == nil {
		leftover.Close()
		t.Fatal("server-side remote port still open after Remove")
	}
}

func TestRemoteForwardEvictedWhenSSHConnectionDrops(t *testing.T) {
	server := newRemoteForwardSSHServer(t)
	client := connectRemoteForwardClient(t, server)
	targetPort := echoTargetPort(t)

	reported := make(chan error, 8)
	service := NewService(Config{
		Provider: DialerProviderFunc(func(context.Context, string) (base.Dialer, error) {
			return client, nil
		}),
		Policy:      Policy{Desktop: true},
		DialTimeout: 2 * time.Second,
		OnError:     func(err error) { reported <- err },
	})
	t.Cleanup(func() { _ = service.Close() })

	spec, err := service.CreateRemote(t.Context(), CreateRemoteArgs{
		SessionID:  "ssh-session",
		BindHost:   "127.0.0.1",
		BindPort:   0,
		TargetHost: "127.0.0.1",
		TargetPort: targetPort,
	})
	if err != nil {
		t.Fatal(err)
	}
	conn := dialForward(t, spec)
	assertEcho(t, conn, "before-drop")

	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-reported:
		if !strings.Contains(err.Error(), "监听失败") {
			t.Fatalf("reported error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("remote listener failure after SSH drop was not reported")
	}
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("established forwarded connection survived SSH drop")
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(service.List()) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("forward not evicted after SSH drop")
		}
		time.Sleep(10 * time.Millisecond)
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(spec.ListenPort)))
	for {
		leftover, err := net.DialTimeout("tcp", address, 200*time.Millisecond)
		if err != nil {
			break
		}
		leftover.Close()
		if time.Now().After(deadline) {
			t.Fatal("server-side remote port survived SSH drop")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
