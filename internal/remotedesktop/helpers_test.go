package remotedesktop

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"
)

// memConn is one end of an in-memory FrameConn pair. A zero buffer makes every write wait for the reader.
type memConn struct {
	in     chan []byte
	out    chan []byte
	closed chan struct{}
	peer   *memConn
	once   sync.Once
}

func newMemPair(buffer int) (*memConn, *memConn) {
	a := &memConn{in: make(chan []byte, buffer), closed: make(chan struct{})}
	b := &memConn{in: make(chan []byte, buffer), closed: make(chan struct{})}
	a.out, b.out = b.in, a.in
	a.peer, b.peer = b, a
	return a, b
}

func (c *memConn) ReadFrame() ([]byte, error) {
	select {
	case data := <-c.in:
		return data, nil
	default:
	}
	select {
	case data := <-c.in:
		return data, nil
	case <-c.closed:
		return nil, io.EOF
	case <-c.peer.closed:
		select {
		case data := <-c.in:
			return data, nil
		default:
		}
		return nil, io.EOF
	}
}

func (c *memConn) WriteFrame(data []byte) error {
	select {
	case <-c.closed:
		return io.ErrClosedPipe
	case <-c.peer.closed:
		return io.ErrClosedPipe
	default:
	}
	select {
	case c.out <- data:
		return nil
	case <-c.closed:
		return io.ErrClosedPipe
	case <-c.peer.closed:
		return io.ErrClosedPipe
	}
}

func (c *memConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func newTestCertificate(t testing.TB, name string) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, der
}

// listen starts a loopback TCP server whose handler runs once per accepted connection.
func listen(t testing.TB, handler func(net.Conn)) (host string, port int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	t.Cleanup(func() {
		_ = listener.Close()
		wg.Wait()
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				handler(conn)
			}()
		}
	}()
	addr := listener.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port
}

func mustRead(t testing.TB, r io.Reader, n int) []byte {
	t.Helper()
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Errorf("read %d bytes: %v", n, err)
	}
	return buf
}

func background() context.Context { return context.Background() }

func sprint(v any) string { return fmt.Sprintf("%v %+v %#v", v, v, v) }
