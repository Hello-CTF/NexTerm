package relaymux

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testCert is one pinned server certificate shared by the package tests.
var testCert = func() ServerCertificate {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	cert, err := PinnedCertificate(key)
	if err != nil {
		panic(err)
	}
	return cert
}()

// quicServer is a UDPMux on loopback whose accepted sessions arrive on sessions.
type quicServer struct {
	mux      *UDPMux
	sessions chan Session
}

func newQUICServer(t testing.TB) *quicServer {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	mux, err := ListenUDPMux(conn, ServerTLSConfig(testCert))
	if err != nil {
		t.Fatal(err)
	}
	server := &quicServer{mux: mux, sessions: make(chan Session, 64)}
	go func() {
		for {
			session, err := mux.Accept(context.Background())
			if err != nil {
				return
			}
			server.sessions <- session
		}
	}()
	t.Cleanup(func() { _ = mux.Close() })
	return server
}

func (s *quicServer) addr() string { return s.mux.Addr().String() }

// wsServer is an httptest server whose upgraded carrier sessions arrive on sessions.
type wsServer struct {
	http     *httptest.Server
	sessions chan Session
	upgrades atomic.Int32
}

func newWSServer(t testing.TB) *wsServer {
	t.Helper()
	server := &wsServer{sessions: make(chan Session, 64)}
	server.http = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.upgrades.Add(1)
		session, err := AcceptWS(w, r)
		if err != nil {
			return
		}
		server.sessions <- session
	}))
	t.Cleanup(server.http.Close)
	return server
}

func (s *wsServer) url() string { return "ws" + strings.TrimPrefix(s.http.URL, "http") + WSPath }

func receive(t testing.TB, sessions chan Session) Session {
	t.Helper()
	select {
	case session := <-sessions:
		t.Cleanup(func() { _ = session.Close() })
		return session
	case <-time.After(10 * time.Second):
		t.Fatal("no server session")
		return nil
	}
}

// carrier builds a connected (client, server) session pair.
type carrier struct {
	name string
	pair func(t testing.TB) (Session, Session)
}

var carriers = []carrier{
	{name: KindQUIC, pair: func(t testing.TB) (Session, Session) {
		server := newQUICServer(t)
		client, err := DialQUIC(context.Background(), server.addr(), DialConfig{Pin: testCert.Pin})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
		return client, receive(t, server.sessions)
	}},
	{name: KindWS, pair: func(t testing.TB) (Session, Session) {
		server := newWSServer(t)
		client, err := DialWS(context.Background(), server.url(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
		return client, receive(t, server.sessions)
	}},
}

// droppingConn is a UDP socket stand-in for a network that blocks UDP: writes vanish, reads never return.
type droppingConn struct {
	closed chan struct{}
	once   atomic.Bool
}

func newDroppingConn() *droppingConn { return &droppingConn{closed: make(chan struct{})} }

func (c *droppingConn) ReadFrom([]byte) (int, net.Addr, error) {
	<-c.closed
	return 0, nil, net.ErrClosed
}
func (c *droppingConn) WriteTo(p []byte, _ net.Addr) (int, error) { return len(p), nil }
func (c *droppingConn) Close() error {
	if c.once.CompareAndSwap(false, true) {
		close(c.closed)
	}
	return nil
}
func (c *droppingConn) LocalAddr() net.Addr              { return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1} }
func (c *droppingConn) SetDeadline(time.Time) error      { return nil }
func (c *droppingConn) SetReadDeadline(time.Time) error  { return nil }
func (c *droppingConn) SetWriteDeadline(time.Time) error { return nil }
