package telnet

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const testTimeout = 5 * time.Second

// fakeServer is a Telnet peer the test drives byte by byte.
type fakeServer struct {
	listener net.Listener
	accepted chan net.Conn
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &fakeServer{listener: listener, accepted: make(chan net.Conn, 8)}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			server.accepted <- conn
		}
	}()
	t.Cleanup(func() { _ = listener.Close() })
	return server
}

func (s *fakeServer) config() Config {
	addr := s.listener.Addr().(*net.TCPAddr)
	return Config{Host: addr.IP.String(), Port: addr.Port, DialTimeout: testTimeout}
}

func (s *fakeServer) accept(t *testing.T) net.Conn {
	t.Helper()
	select {
	case conn := <-s.accepted:
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	case <-time.After(testTimeout):
		t.Fatal("no connection accepted")
		return nil
	}
}

func expectBytes(t *testing.T, conn net.Conn, want []byte) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(testTimeout))
	got := make([]byte, len(want))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("server read: %v (want %v)", err, want)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("server received %v, want %v", got, want)
	}
}

func expectNothing(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	buf := make([]byte, 64)
	if n, err := conn.Read(buf); n > 0 {
		t.Fatalf("server received unexpected %v", buf[:n])
	} else if err == nil {
		t.Fatal("expected a read timeout")
	}
}

func send(t *testing.T, conn net.Conn, data ...byte) {
	t.Helper()
	if _, err := conn.Write(data); err != nil {
		t.Fatal(err)
	}
}

var openingRequests = []byte{
	cmdIAC, cmdWILL, optNAWS, cmdIAC, cmdWILL, optTTYPE, cmdIAC, cmdWILL, optSGA,
	cmdIAC, cmdDO, optSGA, cmdIAC, cmdDO, optEcho,
}

// pump reads the client side like a session output pump and hands every chunk over a channel.
func pump(channel base.Channel) <-chan []byte {
	out := make(chan []byte, 64)
	go func() {
		defer close(out)
		buf := make([]byte, 1024)
		for {
			n, err := channel.Read(buf)
			if n > 0 {
				out <- append([]byte(nil), buf[:n]...)
			}
			if err != nil {
				return
			}
		}
	}()
	return out
}

func expectOutput(t *testing.T, output <-chan []byte, want string) {
	t.Helper()
	var got []byte
	deadline := time.After(testTimeout)
	for len(got) < len(want) {
		select {
		case chunk, ok := <-output:
			if !ok {
				t.Fatalf("output ended after %q, want %q", got, want)
			}
			got = append(got, chunk...)
		case <-deadline:
			t.Fatalf("output %q, want %q", got, want)
		}
	}
	if string(got) != want {
		t.Fatalf("output %q, want %q", got, want)
	}
}

func openTerminal(t *testing.T, server *fakeServer, cols, rows uint32) (base.Channel, net.Conn) {
	t.Helper()
	transport, err := Connect(context.Background(), server.config())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	channel, err := transport.OpenPTY(context.Background(), base.PTYOptions{Cols: cols, Rows: rows, Term: "xterm-256color"})
	if err != nil {
		t.Fatal(err)
	}
	peer := server.accept(t)
	expectBytes(t, peer, openingRequests)
	return channel, peer
}

func TestNegotiationAnswersRequestsOnce(t *testing.T) {
	server := newFakeServer(t)
	channel, peer := openTerminal(t, server, 80, 24)
	output := pump(channel)

	// Acknowledgments of the client's own requests: NAWS reports the size at once, the others need no reply.
	send(t, peer, cmdIAC, cmdDO, optNAWS, cmdIAC, cmdDO, optTTYPE, cmdIAC, cmdWILL, optEcho, cmdIAC, cmdWILL, optSGA)
	expectBytes(t, peer, []byte{cmdIAC, cmdSB, optNAWS, 0, 80, 0, 24, cmdIAC, cmdSE})

	send(t, peer, cmdIAC, cmdSB, optTTYPE, ttypeSEND, cmdIAC, cmdSE)
	expectBytes(t, peer, append(append([]byte{cmdIAC, cmdSB, optTTYPE, ttypeIS}, "xterm-256color"...), cmdIAC, cmdSE))

	// Unknown options are refused; BINARY is accepted in both directions.
	send(t, peer, cmdIAC, cmdDO, 99, cmdIAC, cmdWILL, 98, cmdIAC, cmdDO, optBinary, cmdIAC, cmdWILL, optBinary)
	expectBytes(t, peer, []byte{cmdIAC, cmdWONT, 99, cmdIAC, cmdDONT, 98, cmdIAC, cmdWILL, optBinary, cmdIAC, cmdDO, optBinary})

	// Repeating a request for an option already on, or refusing one already off, gets no answer.
	send(t, peer, cmdIAC, cmdWILL, optEcho, cmdIAC, cmdDO, optBinary, cmdIAC, cmdWONT, 98)
	send(t, peer, 'o', 'k')
	expectOutput(t, output, "ok")
	expectNothing(t, peer)

	// The server turns echo off: the client confirms once.
	send(t, peer, cmdIAC, cmdWONT, optEcho)
	expectBytes(t, peer, []byte{cmdIAC, cmdDONT, optEcho})
}

func TestResizeReportsWindowSizeAfterNAWSIsOn(t *testing.T) {
	server := newFakeServer(t)
	channel, peer := openTerminal(t, server, 80, 24)
	output := pump(channel)

	// Before the server accepts NAWS, a resize is only remembered.
	if err := channel.Resize(context.Background(), 100, 30); err != nil {
		t.Fatal(err)
	}
	send(t, peer, 'x')
	expectOutput(t, output, "x")
	expectNothing(t, peer)

	send(t, peer, cmdIAC, cmdDO, optNAWS)
	expectBytes(t, peer, []byte{cmdIAC, cmdSB, optNAWS, 0, 100, 0, 30, cmdIAC, cmdSE})

	// A size byte of 255 is doubled inside the subnegotiation.
	if err := channel.Resize(context.Background(), 300, 255); err != nil {
		t.Fatal(err)
	}
	expectBytes(t, peer, []byte{cmdIAC, cmdSB, optNAWS, 1, 44, 0, 255, 255, cmdIAC, cmdSE})
}

func TestDataEscapingAndLineEndings(t *testing.T) {
	server := newFakeServer(t)
	channel, peer := openTerminal(t, server, 80, 24)
	output := pump(channel)

	send(t, peer, 'a', cmdIAC, cmdIAC, 'b', '\r', 0, 'c', '\r', '\n', cmdIAC, 241, 'd')
	expectOutput(t, output, "a\xffb\rc\r\nd")

	if _, err := channel.Write([]byte("x\xffy\r")); err != nil {
		t.Fatal(err)
	}
	expectBytes(t, peer, []byte{'x', cmdIAC, cmdIAC, 'y', '\r', 0})
	if _, err := channel.Write([]byte("ls\r\n")); err != nil {
		t.Fatal(err)
	}
	expectBytes(t, peer, []byte("ls\r\n"))

	// In binary mode carriage returns pass through untouched in both directions.
	send(t, peer, cmdIAC, cmdDO, optBinary, cmdIAC, cmdWILL, optBinary)
	expectBytes(t, peer, []byte{cmdIAC, cmdWILL, optBinary, cmdIAC, cmdDO, optBinary})
	send(t, peer, '\r', 0, 'z')
	expectOutput(t, output, "\r\x00z")
	if _, err := channel.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	expectBytes(t, peer, []byte{'\r'})
}

func TestSubnegotiationSplitAcrossReads(t *testing.T) {
	server := newFakeServer(t)
	channel, peer := openTerminal(t, server, 80, 24)
	output := pump(channel)

	send(t, peer, cmdIAC, cmdDO, optTTYPE, cmdIAC, cmdSB, optTTYPE)
	expectNothing(t, peer)
	send(t, peer, ttypeSEND, cmdIAC)
	send(t, peer, cmdSE, 'h', 'i')
	expectBytes(t, peer, append(append([]byte{cmdIAC, cmdSB, optTTYPE, ttypeIS}, "xterm-256color"...), cmdIAC, cmdSE))
	expectOutput(t, output, "hi")
}

func TestServerCloseEndsTerminal(t *testing.T) {
	server := newFakeServer(t)
	channel, peer := openTerminal(t, server, 80, 24)
	output := pump(channel)

	send(t, peer, 'b', 'y', 'e')
	expectOutput(t, output, "bye")
	_ = peer.Close()
	select {
	case _, ok := <-output:
		if ok {
			t.Fatal("unexpected output after close")
		}
	case <-time.After(testTimeout):
		t.Fatal("read did not end after the server closed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	if err := channel.Wait(ctx); err != nil {
		t.Fatalf("Wait after a clean close: %v", err)
	}
	if _, err := channel.Write([]byte("x")); !errors.Is(err, base.ErrClosed) {
		t.Fatalf("Write after close: %v", err)
	}
}

func TestTransportCloseEndsTerminals(t *testing.T) {
	server := newFakeServer(t)
	transport, err := Connect(context.Background(), server.config())
	if err != nil {
		t.Fatal(err)
	}
	channel, err := transport.OpenPTY(context.Background(), base.PTYOptions{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	peer := server.accept(t)
	output := pump(channel)
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-output:
	case <-time.After(testTimeout):
		t.Fatal("read did not end after transport close")
	}
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	if err := channel.Wait(ctx); err != nil {
		t.Fatalf("Wait after close: %v", err)
	}
	_ = peer.SetReadDeadline(time.Now().Add(testTimeout))
	if _, err := io.ReadAll(peer); err != nil {
		t.Fatalf("server side did not see a clean close: %v", err)
	}
	if transport.IsAlive() {
		t.Fatal("transport alive after close")
	}
	if _, err := transport.OpenPTY(context.Background(), base.PTYOptions{Cols: 80, Rows: 24}); !errors.Is(err, base.ErrClosed) {
		t.Fatalf("OpenPTY after close: %v", err)
	}
}

func TestConnectDialsOnceForTheFirstTerminal(t *testing.T) {
	server := newFakeServer(t)
	_, _ = openTerminal(t, server, 80, 24)
	select {
	case conn := <-server.accepted:
		_ = conn.Close()
		t.Fatal("the first terminal dialed a second connection")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestConnectFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	server := newFakeServer(t)
	if _, err := Connect(ctx, server.config()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled connect: %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	if _, err := Connect(context.Background(), Config{Host: "127.0.0.1", Port: port, DialTimeout: testTimeout}); err == nil {
		t.Fatal("connect to a closed port succeeded")
	}

	blocked := dialerFunc(func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	started := time.Now()
	if _, err := Connect(context.Background(), Config{Host: "192.0.2.1", DialTimeout: 50 * time.Millisecond, Dialer: blocked}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("dial timeout: %v", err)
	}
	if time.Since(started) > testTimeout {
		t.Fatal("dial timeout not applied")
	}

	for _, config := range []Config{{Host: " "}, {Host: "h", Port: 70000}} {
		if _, err := New(config); err == nil {
			t.Fatalf("config %+v accepted", config)
		}
	}
	transport, err := New(Config{Host: "router"})
	if err != nil {
		t.Fatal(err)
	}
	if transport.Address() != net.JoinHostPort("router", strconv.Itoa(DefaultPort)) {
		t.Fatalf("default address %s", transport.Address())
	}
	if _, err := transport.Exec(context.Background(), "show version", base.ExecOptions{}); !errors.Is(err, base.ErrUnsupported) {
		t.Fatalf("Exec: %v", err)
	}
}

func TestOpenPTYRejectsStaleGeneration(t *testing.T) {
	transport, err := New(Config{Host: "router"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.OpenPTY(context.Background(), base.PTYOptions{ExpectedGeneration: transport.Generation() + 1}); !errors.Is(err, base.ErrStaleGeneration) {
		t.Fatalf("stale generation: %v", err)
	}
}

type dialerFunc func(ctx context.Context, network, address string) (net.Conn, error)

func (f dialerFunc) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return f(ctx, network, address)
}
