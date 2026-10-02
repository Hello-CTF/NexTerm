package forward

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

type recordingDialer struct {
	upstream  string
	mu        sync.Mutex
	addresses []string
	networks  []string
}

func (d *recordingDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d.mu.Lock()
	d.addresses = append(d.addresses, address)
	d.networks = append(d.networks, network)
	d.mu.Unlock()
	return (&net.Dialer{}).DialContext(ctx, "tcp", d.upstream)
}

func (d *recordingDialer) calls() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.addresses...)
}

type errorDialer struct {
	err error
}

func (d errorDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, d.err
}

type switchProvider struct {
	mu       sync.Mutex
	dialer   base.Dialer
	err      error
	failures int
	calls    int
}

func (p *switchProvider) CurrentDialer(context.Context, string) (base.Dialer, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.failures > 0 {
		p.failures--
		return nil, errors.New("session reconnecting")
	}
	return p.dialer, p.err
}

func (p *switchProvider) set(dialer base.Dialer) {
	p.mu.Lock()
	p.dialer = dialer
	p.err = nil
	p.mu.Unlock()
}

func (p *switchProvider) setFailures(count int) {
	p.mu.Lock()
	p.failures = count
	p.mu.Unlock()
}

func (p *switchProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func startEchoServer(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return listener.Addr().String()
}

func newLoopbackService(t *testing.T, provider DialerProvider) *Service {
	t.Helper()
	service := NewService(Config{
		Provider:         provider,
		Policy:           Policy{Desktop: true},
		Reconnect:        ReconnectPolicy{Attempts: 1, Backoff: time.Millisecond},
		DialTimeout:      time.Second,
		HandshakeTimeout: time.Second,
	})
	t.Cleanup(func() { _ = service.Close() })
	return service
}

func dialForward(t *testing.T, spec Spec) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(spec.ListenPort))), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func negotiateTestSOCKS(t *testing.T, conn net.Conn) {
	t.Helper()
	if err := writeAll(conn, []byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	var response [2]byte
	if _, err := io.ReadFull(conn, response[:]); err != nil {
		t.Fatal(err)
	}
	if response != [2]byte{5, 0} {
		t.Fatalf("SOCKS negotiation = %v", response)
	}
}

func writeTestSOCKSRequest(t *testing.T, conn net.Conn, command, addressType byte, host string, port uint16) {
	t.Helper()
	request := []byte{5, command, 0, addressType}
	switch addressType {
	case 1:
		request = append(request, net.ParseIP(host).To4()...)
	case 3:
		request = append(request, byte(len(host)))
		request = append(request, host...)
	case 4:
		request = append(request, net.ParseIP(host).To16()...)
	default:
		t.Fatalf("unsupported test address type %d", addressType)
	}
	var encodedPort [2]byte
	binary.BigEndian.PutUint16(encodedPort[:], port)
	request = append(request, encodedPort[:]...)
	if err := writeAll(conn, request); err != nil {
		t.Fatal(err)
	}
}

func readTestSOCKSReply(t *testing.T, conn net.Conn) byte {
	t.Helper()
	var response [10]byte
	if _, err := io.ReadFull(conn, response[:]); err != nil {
		t.Fatal(err)
	}
	if response[0] != 5 || response[2] != 0 || response[3] != 1 {
		t.Fatalf("SOCKS response = %v", response)
	}
	return response[1]
}

func assertEcho(t *testing.T, conn net.Conn, payload string) {
	t.Helper()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := writeAll(conn, []byte(payload)); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, response); err != nil {
		t.Fatal(err)
	}
	if string(response) != payload {
		t.Fatalf("echo = %q, want %q", response, payload)
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
}
