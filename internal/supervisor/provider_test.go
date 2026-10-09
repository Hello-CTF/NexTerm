//go:build unix

package supervisor

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestIPCKillDuringBlockedInput(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	ctx := context.Background()

	id := ids.New()
	if _, err := client.Create(ctx, CreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-c", `stty raw -echo; exec sleep 60`},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
	}); err != nil {
		t.Fatal(err)
	}
	stream, err := client.Attach(ctx, id, nil)
	if err != nil {
		t.Fatal(err)
	}

	writeDone := make(chan error, 1)
	go func() {
		for {
			_, err := stream.Write(make([]byte, 256*1024))
			if err == nil || errors.Is(err, ErrUnavailable) {
				continue
			}
			writeDone <- err
			return
		}
	}()

	select {
	case err := <-writeDone:
		t.Fatalf("write stream ended before kill: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	killDone := make(chan error, 1)
	go func() { killDone <- stream.Kill(ctx) }()
	select {
	case err := <-killDone:
		if err != nil {
			t.Fatalf("Kill during blocked input = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Kill did not complete while PTY input was blocked")
	}
	select {
	case <-writeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("blocked writer was not woken by Kill")
	}
}

func TestIPCRecordingErrorTerminatesStream(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	ctx := context.Background()

	id := ids.New()
	if _, err := client.Create(ctx, CreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-c", `while true; do printf 'tick\n'; sleep 0.05; done`},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
	}); err != nil {
		t.Fatal(err)
	}

	stream, err := client.Attach(ctx, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, stream, "tick")

	session, err := supervisor.resolveSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.recording.Close(); err != nil {
		t.Fatal(err)
	}

	readDone := make(chan error, 1)
	go func() {
		buffer := make([]byte, 4096)
		for {
			if _, err := stream.Read(buffer); err != nil {
				readDone <- err
				return
			}
		}
	}()
	select {
	case err := <-readDone:
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Read after recording failure = %v, want ErrUnavailable", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Read did not wake after recording failure")
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- stream.Wait(ctx) }()
	select {
	case err := <-waitDone:
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Wait after recording failure = %v, want ErrUnavailable", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Wait did not wake after recording failure")
	}

	if err := supervisor.Kill(ctx, id); err != nil {
		t.Fatal(err)
	}
}

func TestIPCRecordingErrorUsesFatalFrame(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	ctx := context.Background()

	id := ids.New()
	if _, err := testClient(t, server).Create(ctx, CreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-c", `while true; do printf 'tick\n'; sleep 0.05; done`},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
	}); err != nil {
		t.Fatal(err)
	}

	conn := dialServer(t, server.SocketPath())
	handshakeConn(t, conn, supervisor.StateDir())
	payload, err := marshalFrame(frameAttach, attachMsg{ID: id})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFrame(conn, frameAttach, payload); err != nil {
		t.Fatal(err)
	}
	awaitFrame(t, conn, frameAttached)
	awaitFrame(t, conn, frameOutput)

	session, err := supervisor.resolveSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.recording.Close(); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		kind, _, err := readFrame(conn)
		if err != nil {
			t.Fatalf("read frames after recording failure: %v", err)
		}
		if kind == frameFatal {
			break
		}
		if kind == frameError {
			t.Fatal("async recording errors must use the fatal frame, not the request error frame")
		}
		if time.Now().After(deadline) {
			t.Fatal("no fatal frame arrived after recording failure")
		}
	}
	_ = conn.Close()

	if err := supervisor.Kill(ctx, id); err != nil {
		t.Fatal(err)
	}
}

func TestIPCConcurrentStaleSocketRecovery(t *testing.T) {
	supervisor := testSupervisor(t)
	ctx := context.Background()
	runDir, err := os.MkdirTemp("/tmp", "nx-supervisor-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	socketPath := filepath.Join(runDir, "supervisor.sock")
	if err := os.WriteFile(socketPath, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}

	const starters = 8
	var wg sync.WaitGroup
	servers := make([]*Server, starters)
	errs := make([]error, starters)
	for index := 0; index < starters; index++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			servers[slot], errs[slot] = NewServer(supervisor, socketPath)
		}(index)
	}
	wg.Wait()

	succeeded := 0
	var winner *Server
	for slot, err := range errs {
		if err == nil {
			succeeded++
			winner = servers[slot]
			continue
		}
		if !errors.Is(err, ErrAlreadyExists) {
			t.Fatalf("starter %d = %v, want ErrAlreadyExists", slot, err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("concurrent stale recovery started %d servers, want exactly 1", succeeded)
	}
	if _, err := os.Lstat(socketPath); err != nil {
		t.Fatalf("live socket must not be deleted by losing starters: %v", err)
	}
	if _, err := testClient(t, winner).List(ctx); err != nil {
		t.Fatalf("winner must serve: %v", err)
	}

	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := winner.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewServer(supervisor, socketPath)
	if err != nil {
		t.Fatalf("restart after concurrent recovery = %v", err)
	}
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = restarted.Shutdown(shutdownCtx)
	}()
}

func TestProviderErrorTranslation(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	ctx := context.Background()

	providers := map[string]base.DurableProvider{
		"embedded": NewProvider(supervisor),
		"remote":   NewRemoteProvider(testClient(t, server)),
	}
	for name, provider := range providers {
		t.Run(name, func(t *testing.T) {
			if _, err := provider.Attach(ctx, ids.New()); !errors.Is(err, durable.ErrNotFound) || errors.Is(err, ErrNotFound) {
				t.Fatalf("Attach unknown = %v, want durable.ErrNotFound only", err)
			}
			if _, err := provider.Create(ctx, base.DurableCreateOptions{Cols: 5000, Rows: 24}); !errors.Is(err, durable.ErrInvalidInput) {
				t.Fatalf("Create invalid = %v, want durable.ErrInvalidInput", err)
			}

			id := ids.New()
			attachment, err := provider.Create(ctx, base.DurableCreateOptions{
				ID:      id,
				Command: []string{"/bin/sh", "-c", "sleep 30"},
				Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = attachment.Close() }()
			if _, err := provider.Create(ctx, base.DurableCreateOptions{
				ID:      id,
				Command: []string{"/bin/sh", "-c", "sleep 30"},
			}); !errors.Is(err, durable.ErrAlreadyExists) {
				t.Fatalf("duplicate Create = %v, want durable.ErrAlreadyExists", err)
			}
			if err := attachment.Kill(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRemoteProviderCreateCompensatesOnAttachFailure(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	provider := NewRemoteProvider(testClient(t, server))

	realAttach := remoteProviderAttach
	t.Cleanup(func() { remoteProviderAttach = realAttach })

	assertCompensated := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("Create must fail when attach fails")
		}
		infos, listErr := supervisor.List(context.Background())
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(infos) != 0 {
			t.Fatalf("compensating kill must remove the session, still listed: %+v", infos)
		}
		dirents, readErr := os.ReadDir(sessionsRoot(supervisor.StateDir()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if len(dirents) != 0 {
			t.Fatalf("compensating kill must remove artifacts, found %d entries", len(dirents))
		}
	}

	t.Run("context canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		remoteProviderAttach = func(attachCtx context.Context, client *Client, id string, _ *Identity) (*Stream, error) {
			cancel()
			return realAttach(attachCtx, client, id, nil)
		}
		_, err := provider.Create(ctx, base.DurableCreateOptions{
			Command: []string{"/bin/sh", "-c", "sleep 30"},
			Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
		})
		assertCompensated(t, err)
	})

	t.Run("protocol failure", func(t *testing.T) {
		remoteProviderAttach = func(context.Context, *Client, string, *Identity) (*Stream, error) {
			return nil, errors.New("attach: malformed attach response")
		}
		_, err := provider.Create(context.Background(), base.DurableCreateOptions{
			Command: []string{"/bin/sh", "-c", "sleep 30"},
			Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
		})
		assertCompensated(t, err)
	})
}

func TestClientKillSessionControl(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	ctx := context.Background()

	id := ids.New()
	info, err := client.Create(ctx, CreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-c", "sleep 30"},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wrong := Identity{CreatedAt: info.CreatedAt, Incarnation: ids.New()}
	if err := client.Kill(ctx, id, &wrong); !errors.Is(err, ErrIdentity) {
		t.Fatalf("kill with wrong identity = %v, want ErrIdentity", err)
	}
	identity := Identity{CreatedAt: info.CreatedAt, Incarnation: info.Incarnation}
	if err := client.Kill(ctx, id, &identity); err != nil {
		t.Fatalf("kill with expected identity = %v", err)
	}
	if _, err := os.Lstat(sessionDir(supervisor.StateDir(), id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("artifacts remain after control kill: %v", err)
	}
	if err := client.Kill(ctx, id, nil); err != nil {
		t.Fatalf("repeated control kill = %v, want nil", err)
	}
	if err := client.Kill(ctx, ids.New(), nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("control kill unknown = %v, want ErrNotFound", err)
	}
}

func dialServer(t *testing.T, socketPath string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func awaitFrame(t *testing.T, conn net.Conn, kind frameType) []byte {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		got, payload, err := readFrame(conn)
		if err != nil {
			t.Fatalf("await frame %d: %v", kind, err)
		}
		if got == kind {
			return payload
		}
		if got != frameOutput {
			t.Fatalf("await frame %d: got unexpected frame %d", kind, got)
		}
	}
}
