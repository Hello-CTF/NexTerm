package agent

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/relaymux"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
)

// A relay refusal or goaway classifies like the control channel's frame for the same cause.
func TestRelayRefusalClassifiesLikeTheControlChannel(t *testing.T) {
	refused := func(reply relaymux.Reply) error { return relayServerError(&relaymux.RefusedError{Reply: reply}) }
	if err := refused(relaymux.Reply{Status: http.StatusForbidden, Code: "forbidden", Message: "设备已撤销"}); !isForbidden(err) || isOwnerUnavailable(err) {
		t.Fatalf("revoked = %v, want forbidden", err)
	}
	if err := refused(relaymux.Reply{Status: http.StatusForbidden, Code: "forbidden", Reason: ErrorCodeOwnerUnavailable}); !isOwnerUnavailable(err) || isForbidden(err) {
		t.Fatalf("owner unavailable = %v, want the owner wait", err)
	}
	var serverError *ServerError
	err := refused(relaymux.Reply{Status: http.StatusUpgradeRequired, Code: "version_mismatch", ServerVersion: "9.9.9"})
	if isForbidden(err) || !asServerError(err, &serverError) || serverError.Code != "version_mismatch" || serverError.ServerVersion != "9.9.9" {
		t.Fatalf("version mismatch = %v", err)
	}
}

func asServerError(err error, target **ServerError) bool {
	value, ok := err.(*ServerError)
	*target = value
	return ok
}

// Terminals on a relay stream get the SSH session variables a bridge gives them: from the TCP connection of a
// websocket carrier exactly as dialBridgeWithEnv states it, and from the UDP addresses of a QUIC session.
func TestRelaySessionEnvMatchesTheBridge(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	go func() {
		if conn, err := listener.Accept(); err == nil {
			defer func() { _ = conn.Close() }()
			_, _ = conn.Read(make([]byte, 1))
		}
	}()
	tcp, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tcp.Close() }()
	ws := (&relayLink{tcp: tcp}).sessionEnv(nil)
	if want := supervisor.SSHConnectionEnv(tcp.RemoteAddr(), tcp.LocalAddr()); !slices.Equal(ws, want) {
		t.Fatalf("websocket env = %v, want %v", ws, want)
	}

	server := &net.UDPAddr{IP: net.IPv4(203, 0, 113, 7), Port: 3478}
	local := &net.UDPAddr{IP: net.IPv4(192, 0, 2, 9), Port: 50123}
	quic := (&relayLink{}).sessionEnv(addrConn{remote: server, local: local})
	want := []string{"SSH_CONNECTION=203.0.113.7 3478 192.0.2.9 50123", "SSH_CLIENT=203.0.113.7 3478 50123"}
	if !slices.Equal(quic, want) {
		t.Fatalf("QUIC env = %v, want %v", quic, want)
	}
}

type addrConn struct {
	net.Conn
	remote, local net.Addr
}

func (c addrConn) RemoteAddr() net.Addr { return c.remote }
func (c addrConn) LocalAddr() net.Addr  { return c.local }

// A terminal stream on a real websocket relay link reaches the helper bridge with the SSH session variables of
// that link's TCP connection, as the server sees it: the server is the SSH client and this host the server.
func TestRelayTerminalStreamCarriesTheSessionEnv(t *testing.T) {
	type addresses struct{ server, agent string }
	seen := make(chan addresses, 1)
	streamOpened := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+relaymux.ConfigPath, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(relaymux.Config{Enabled: true, Version: relaymux.Version, WSPath: relaymux.WSPath})
	})
	mux.HandleFunc("GET "+relaymux.WSPath, func(w http.ResponseWriter, r *http.Request) {
		local, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
		seen <- addresses{server: local.String(), agent: r.RemoteAddr}
		session, err := relaymux.AcceptWS(w, r)
		if err != nil {
			return
		}
		pending, err := relaymux.AcceptHandshake(context.Background(), session)
		if err != nil {
			return
		}
		link, err := pending.Accept(relaymux.Reply{})
		if err != nil {
			return
		}
		defer func() { _ = link.Close() }()
		conn, _, err := link.OpenDeviceStream(context.Background(), relaymux.StreamHeader{Purpose: PurposeTerminal, RequestID: "request-1"})
		streamOpened <- err
		if err == nil {
			_, _ = conn.Write([]byte{0xff, 0xff, 0xff, 0xff})
			_, _ = io.ReadFull(conn, make([]byte, 4))
			_ = conn.Close()
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	captured := make(chan []string, 1)
	original := bridgeCreateDefaults
	bridgeCreateDefaults = func(env []string) supervisor.CreateDefaults {
		select {
		case captured <- env:
		default:
		}
		return original(env)
	}
	defer func() { bridgeCreateDefaults = original }()

	entries := []BaseURLEntry{{URL: server.URL}}
	prober, err := NewProber(entries)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := New(Options{
		Store: &memoryStore{}, DataDir: t.TempDir(), StateDir: t.TempDir(), Prober: prober, Manager: EmbeddedServiceManager(),
		Executable: "nexterm-agent", EnsureHelper: func(context.Context) error { return nil },
		Bridge: func(context.Context, string) (io.ReadWriteCloser, error) {
			local, remote := net.Pipe()
			go func() { _, _ = io.Copy(remote, remote) }()
			return local, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runtime.deviceID, runtime.secret, runtime.runCtx = "device-1", "secret", ctx
	runtime.desired.TerminalEnabled = true
	client, err := NewEndpointClient(entries[0])
	if err != nil {
		t.Fatal(err)
	}
	link, err := runtime.dialRelay(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = runtime.serveRelayLink(ctx, link)
	}()
	if err := <-streamOpened; err != nil {
		t.Fatalf("open terminal stream: %v", err)
	}
	addrs := <-seen
	var env []string
	select {
	case env = <-captured:
	case <-ctx.Done():
		t.Fatal("the terminal stream never reached the helper bridge")
	}
	serverAddr, _ := net.ResolveTCPAddr("tcp", addrs.server)
	agentAddr, _ := net.ResolveTCPAddr("tcp", addrs.agent)
	if want := supervisor.SSHConnectionEnv(serverAddr, agentAddr); !slices.Equal(env, want) {
		t.Fatalf("session env = %v, want %v", env, want)
	}
	cancel()
	<-served
	runtime.loopWg.Wait()
}

// A config route that answers 403 (a proxy rejecting the new path) means no relay, never a revocation; only the
// relay's own refusal of the Hello revokes.
func TestRelayConfigRefusalIsNotARevocation(t *testing.T) {
	var refuseHello atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+relaymux.ConfigPath, func(w http.ResponseWriter, r *http.Request) {
		if !refuseHello.Load() {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"ok":false,"error":{"code":"forbidden","message":"blocked by the proxy"}}`)
			return
		}
		_ = json.NewEncoder(w).Encode(relaymux.Config{Enabled: true, Version: relaymux.Version, WSPath: relaymux.WSPath})
	})
	mux.HandleFunc("GET "+relaymux.WSPath, func(w http.ResponseWriter, r *http.Request) {
		session, err := relaymux.AcceptWS(w, r)
		if err != nil {
			return
		}
		if pending, err := relaymux.AcceptHandshake(context.Background(), session); err == nil {
			pending.Refuse(relaymux.Reply{Status: http.StatusForbidden, Code: "forbidden", Message: "设备已撤销"})
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	client, err := NewEndpointClient(BaseURLEntry{URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{deviceID: "device-1", secret: "secret"}
	_, err = runtime.dialRelay(context.Background(), client)
	if outcome, _ := classifyRelayEnd(err); outcome != relayNotOffered {
		t.Fatalf("403 on the config route = outcome %d (%v), want not offered", outcome, err)
	}
	refuseHello.Store(true)
	_, err = runtime.dialRelay(context.Background(), client)
	if outcome, _ := classifyRelayEnd(err); outcome != relayRevoked {
		t.Fatalf("refused Hello = outcome %d (%v), want revoked", outcome, err)
	}
}
