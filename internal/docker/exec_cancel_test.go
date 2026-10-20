package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

// blockingExecSession blocks its read until closed, so a test can prove a
// cancelled exec terminates the actual stream instead of waiting for the
// remote command to finish on its own.
type blockingExecSession struct {
	closed      chan struct{}
	readStarted chan struct{}
	once        sync.Once
	readOnce    sync.Once
}

func newBlockingExecSession() *blockingExecSession {
	return &blockingExecSession{closed: make(chan struct{}), readStarted: make(chan struct{})}
}

func (s *blockingExecSession) Read([]byte) (int, error) {
	s.readOnce.Do(func() { close(s.readStarted) })
	<-s.closed
	return 0, io.ErrClosedPipe
}

func (s *blockingExecSession) Write(buffer []byte) (int, error) { return len(buffer), nil }

func (s *blockingExecSession) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

func (s *blockingExecSession) CloseWrite() error { return nil }

func (s *blockingExecSession) Resize(context.Context, uint, uint) error { return nil }

func (s *blockingExecSession) Wait(context.Context) (int, error) { return 0, nil }

func (s *blockingExecSession) IsTTY() bool { return false }

func TestServiceExecCancelTerminatesSessionStream(t *testing.T) {
	session := newBlockingExecSession()
	backend := &stubBackend{openExec: func(context.Context, ExecOptions) (ExecSession, error) {
		return session, nil
	}}
	service := NewService(StaticBackends{SDKBackend: backend})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := service.Exec(ctx, ExecRequest{SessionID: "s1", Container: "c1", Command: "sleep 300"})
		done <- err
	}()
	select {
	case <-session.readStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("exec never started reading the stream")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("exec error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled exec did not return; the stream outlived its context")
	}
	select {
	case <-session.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("session stream was not closed by the cancel")
	}
}

// newBlockingExecBackend serves a Moby API whose exec start hijacks the
// connection and holds it open — like a long-running remote command — until
// the test releases it. Cancelling the exec must terminate the stream from
// the client side; the daemon never closes first.
func newBlockingExecBackend(t *testing.T) *MobyBackend {
	t.Helper()
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		path := strings.TrimPrefix(request.URL.Path, "/v1.56")
		switch {
		case path == "/containers/c1/exec" && request.Method == http.MethodPost:
			writeTestJSON(writer, map[string]string{"Id": "e1"})
		case path == "/exec/e1/start" && request.Method == http.MethodPost:
			connection, _, err := writer.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			go func() {
				defer connection.Close()
				_, _ = fmt.Fprintf(connection, "HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.multiplexed-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
				<-release
			}()
		case path == "/exec/e1/json" && request.Method == http.MethodGet:
			_, _ = io.WriteString(writer, `{"ID":"e1","ContainerID":"c1","Running":false,"ExitCode":137}`)
		default:
			http.Error(writer, `{"message":"not found"}`, http.StatusNotFound)
		}
	}))
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	cli, err := client.New(
		client.WithHTTPClient(server.Client()),
		client.WithHost("tcp://"+serverURL.Host),
		client.WithAPIVersion("1.56"),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		close(release)
		_ = cli.Close()
		server.Close()
	})
	return NewMobyBackend(cli)
}

func TestServiceExecCancelTerminatesMobyStream(t *testing.T) {
	backend := newBlockingExecBackend(t)
	service := NewService(StaticBackends{SDKBackend: backend})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := service.Exec(ctx, ExecRequest{SessionID: "s1", Container: "c1", Command: "sleep 300"})
		done <- err
	}()
	// Let the exec reach the blocking read on the hijacked stream.
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("exec error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled exec did not return; the SDK stream outlived its context")
	}
}
