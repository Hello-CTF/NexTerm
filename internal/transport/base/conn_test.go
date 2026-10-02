package base

import (
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

type testStream struct {
	net.Conn
	writeClosed atomic.Bool
}

func (s *testStream) CloseWrite() error {
	s.writeClosed.Store(true)
	return nil
}

func TestStreamConnDeadlinesHalfCloseAndClose(t *testing.T) {
	streamSide, echoSide := net.Pipe()
	stream := &testStream{Conn: streamSide}
	defer echoSide.Close()
	go func() {
		buffer := make([]byte, 32)
		for {
			n, err := echoSide.Read(buffer)
			if err != nil {
				return
			}
			if _, err := echoSide.Write(buffer[:n]); err != nil {
				return
			}
		}
	}()

	conn := NewStreamConn(stream, testAddr("local"), testAddr("remote"))
	defer conn.Close()
	if conn.LocalAddr().String() != "local" || conn.RemoteAddr().String() != "remote" {
		t.Fatalf("addresses were not preserved")
	}
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("first")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 5)
	if _, err := io.ReadFull(conn, buffer); err != nil || string(buffer) != "first" {
		t.Fatalf("echo = %q, %v", buffer, err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Read(buffer); err == nil {
		t.Fatal("read deadline did not fire")
	} else {
		var netErr net.Error
		if !errors.As(err, &netErr) || !netErr.Timeout() {
			t.Fatalf("deadline error = %T %v", err, err)
		}
	}
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("again")); err != nil {
		t.Fatalf("connection unusable after deadline reset: %v", err)
	}
	if _, err := io.ReadFull(conn, buffer); err != nil || string(buffer) != "again" {
		t.Fatalf("echo after deadline = %q, %v", buffer, err)
	}

	if err := conn.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if err := conn.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if !stream.writeClosed.Load() {
		t.Fatal("underlying CloseWrite was not called")
	}
	if _, err := conn.Write([]byte("closed")); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("write after CloseWrite = %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStreamConnCloseWriteFlushesAcceptedBytes(t *testing.T) {
	reader, writer := io.Pipe()
	stream := &blockingWriteStream{
		reader:           reader,
		started:          make(chan struct{}),
		allow:            make(chan struct{}),
		closeWriteCalled: make(chan struct{}),
	}
	defer writer.Close()
	conn := NewStreamConn(stream, nil, nil)
	defer conn.Close()
	if _, err := conn.Write([]byte("payload")); err != nil {
		t.Fatal(err)
	}
	<-stream.started
	closed := make(chan error, 1)
	go func() { closed <- conn.CloseWrite() }()
	select {
	case <-stream.closeWriteCalled:
		t.Fatal("CloseWrite overtook an accepted payload write")
	case <-time.After(30 * time.Millisecond):
	}
	close(stream.allow)
	select {
	case <-stream.closeWriteCalled:
	case <-time.After(time.Second):
		t.Fatal("CloseWrite did not complete after the payload flush")
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
}

type blockingWriteStream struct {
	reader           *io.PipeReader
	started          chan struct{}
	allow            chan struct{}
	closeWriteCalled chan struct{}
	startOnce        atomic.Bool
	closeOnce        atomic.Bool
}

func (s *blockingWriteStream) Read(p []byte) (int, error) {
	return s.reader.Read(p)
}

func (s *blockingWriteStream) Write(p []byte) (int, error) {
	if s.startOnce.CompareAndSwap(false, true) {
		close(s.started)
	}
	<-s.allow
	return len(p), nil
}

func (s *blockingWriteStream) CloseWrite() error {
	if s.closeOnce.CompareAndSwap(false, true) {
		close(s.closeWriteCalled)
	}
	return nil
}

func (s *blockingWriteStream) Close() error {
	return s.reader.Close()
}

func TestOutputLimitDefaults(t *testing.T) {
	limits := (OutputLimits{}).Normalized()
	if limits.Stdout != DefaultStdoutLimit || limits.Stderr != DefaultStderrLimit {
		t.Fatalf("unexpected defaults: %+v", limits)
	}
	limits = (OutputLimits{Stdout: -1, Stderr: 12}).Normalized()
	if limits.Stdout != -1 || limits.Stderr != 12 {
		t.Fatalf("explicit limits were not preserved: %+v", limits)
	}
}

type testAddr string

func (a testAddr) Network() string { return "test" }
func (a testAddr) String() string  { return string(a) }
