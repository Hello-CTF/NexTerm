package remote

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type exitedChannel struct {
	net.Conn
	stderr string
}

func (c exitedChannel) Stderr() io.Reader { return strings.NewReader(c.stderr) }

// A host helper that predates the file channel exits with a usage error; the
// mux reports what it printed.
func TestMuxReportsAChannelThatExitedBeforeThePreamble(t *testing.T) {
	mux := NewMux(func(context.Context) (net.Conn, error) {
		local, remote := net.Pipe()
		_ = remote.Close()
		return exitedChannel{Conn: local, stderr: "supervisor-helper: unknown option --files\n"}, nil
	})
	defer mux.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := mux.Open(ctx)
	if err == nil || !strings.Contains(err.Error(), "unknown option --files") {
		t.Fatalf("open = %v, want the helper's usage error", err)
	}
	if mux.Ready() {
		t.Fatal("mux reports ready after a failed channel")
	}
}

// The server ends, cancelling its operations, once the client closes the channel.
func TestServeStdioEndsWhenTheClientCloses(t *testing.T) {
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	served := make(chan error, 1)
	go func() { served <- ServeStdio(context.Background(), serverIn, serverOut) }()
	mux := NewMux(func(context.Context) (net.Conn, error) {
		return pipeConn{Reader: clientIn, Writer: clientOut, closers: []io.Closer{clientIn, clientOut}}, nil
	})
	files := NewFileSystem(Options{Dial: mux.Open})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dir := t.TempDir()
	if _, err := files.List(ctx, dir); err != nil {
		t.Fatal(err)
	}
	watchDone := make(chan error, 1)
	handle, err := files.Watch(ctx, func(base.WatchEvent) {}, func(err error) { watchDone <- err })
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Set([]string{dir}); err != nil {
		t.Fatal(err)
	}
	_ = mux.Close()
	select {
	case <-served:
	case <-time.After(10 * time.Second):
		t.Fatal("server kept running after the client closed the channel")
	}
	select {
	case err := <-watchDone:
		if err == nil {
			t.Fatal("a watch cut off by the closed channel reported a clean end")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the watch did not end with its channel")
	}
}

type pipeConn struct {
	io.Reader
	io.Writer
	closers []io.Closer
}

func (c pipeConn) Close() error {
	for _, closer := range c.closers {
		_ = closer.Close()
	}
	return nil
}
func (pipeConn) LocalAddr() net.Addr              { return &net.UnixAddr{} }
func (pipeConn) RemoteAddr() net.Addr             { return &net.UnixAddr{} }
func (pipeConn) SetDeadline(time.Time) error      { return nil }
func (pipeConn) SetReadDeadline(time.Time) error  { return nil }
func (pipeConn) SetWriteDeadline(time.Time) error { return nil }
