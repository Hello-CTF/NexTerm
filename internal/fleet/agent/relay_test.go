package agent

import (
	"net"
	"net/http"
	"slices"
	"testing"

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
