package db

import (
	"context"
	"crypto/tls"
	"net"
	"testing"
)

type redisTLSTestConn struct {
	net.Conn
	closed bool
}

func (c *redisTLSTestConn) Close() error {
	c.closed = true
	return c.Conn.Close()
}

func TestRedisTLSDialerHandshakeFailureClosesRawConnection(t *testing.T) {
	raw, peer := net.Pipe()
	connection := &redisTLSTestConn{Conn: raw}
	if err := peer.Close(); err != nil {
		t.Fatal(err)
	}
	dial := newRedisTLSDialer(func(_ context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != "redis.tunnel.invalid:6379" {
			t.Fatalf("dial target = %s %s", network, address)
		}
		return connection, nil
	}, &tls.Config{InsecureSkipVerify: true})

	got, err := dial(t.Context(), "tcp", "redis.tunnel.invalid:6379")
	if err == nil {
		if got != nil {
			_ = got.Close()
		}
		t.Fatal("TLS handshake unexpectedly succeeded against a closed peer")
	}
	if got != nil {
		t.Fatal("failed TLS handshake returned a connection")
	}
	if !connection.closed {
		t.Fatal("failed TLS handshake did not close the raw connection")
	}
}
