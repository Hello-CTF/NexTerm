package ssh

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

// SFTP request packet types (draft-ietf-secsh-filexfer-02).
const (
	fxpOpen     = 3
	fxpRead     = 5
	fxpRealPath = 16
	fxpStat     = 17
	fxpReadLink = 19
)

// hangingServer sits between the SFTP client and a real server and never forwards requests of the selected types, the
// way a server blocked on a hung NFS or FUSE mount never answers them.
type hangingServer struct {
	mu   sync.Mutex
	hang map[byte]bool
	held chan byte
}

func (h *hangingServer) set(types ...byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hang = make(map[byte]bool, len(types))
	for _, kind := range types {
		h.hang[kind] = true
	}
	// Requests held by an earlier phase must not satisfy the next phase's wait.
	for {
		select {
		case <-h.held:
		default:
			return
		}
	}
}

func (h *hangingServer) holds(kind byte) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hang[kind]
}

func (h *hangingServer) forward(from io.Reader, to io.Writer) {
	header := make([]byte, 4)
	for {
		if _, err := io.ReadFull(from, header); err != nil {
			return
		}
		body := make([]byte, binary.BigEndian.Uint32(header))
		if _, err := io.ReadFull(from, body); err != nil {
			return
		}
		if len(body) > 0 && h.holds(body[0]) {
			select {
			case h.held <- body[0]:
			default:
			}
			continue
		}
		if _, err := to.Write(append(header, body...)); err != nil {
			return
		}
	}
}

func newHangingTestFS(t *testing.T) (*FS, string, *hangingServer) {
	t.Helper()
	root := t.TempDir()
	clientSide, proxyClient := net.Pipe()
	proxyServer, serverSide := net.Pipe()
	server, err := sftp.NewServer(serverSide, sftp.WithServerWorkingDirectory(root))
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve() }()
	hanging := &hangingServer{held: make(chan byte, 16)}
	go hanging.forward(proxyClient, proxyServer)
	go func() { _, _ = io.Copy(proxyClient, proxyServer) }()
	client, err := sftp.NewClientPipe(clientSide, clientSide)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		client.Close()
		server.Close()
		for _, conn := range []net.Conn{clientSide, proxyClient, proxyServer, serverSide} {
			conn.Close()
		}
		select {
		case <-serveDone:
		case <-time.After(2 * time.Second):
			t.Error("SFTP test server did not stop")
		}
	})
	return New(client, nil), root, hanging
}

// cancelWhenHeld cancels once the proxy has swallowed a request of the expected type, so the caller is known to be
// waiting on an unanswered request when the cancel lands.
func cancelWhenHeld(t *testing.T, hanging *hangingServer, kind byte, cancel context.CancelFunc) {
	t.Helper()
	go func() {
		for {
			select {
			case held := <-hanging.held:
				if held == kind {
					cancel()
					return
				}
			case <-time.After(10 * time.Second):
				return
			}
		}
	}()
}

func requireReturnsCanceled(t *testing.T, name string, run func(context.Context) error, hanging *hangingServer, kind byte) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelWhenHeld(t, hanging, kind, cancel)
	done := make(chan error, 1)
	go func() { done <- run(ctx) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("%s returned %v, want context.Canceled", name, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("%s kept waiting on an unanswered SFTP request after cancel", name)
	}
}

func TestHungServerRequestsStopWaitingWhenContextEnds(t *testing.T) {
	filesystem, root, hanging := newHangingTestFS(t)
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "file")

	hanging.set(fxpStat)
	requireReturnsCanceled(t, "Exists", func(ctx context.Context) error {
		_, err := filesystem.Exists(ctx, file)
		return err
	}, hanging, fxpStat)

	hanging.set(fxpRealPath)
	requireReturnsCanceled(t, "home path resolution", func(ctx context.Context) error {
		_, err := filesystem.Size(ctx, "~/file")
		return err
	}, hanging, fxpRealPath)

	hanging.set(fxpStat, fxpReadLink)
	requireReturnsCanceled(t, "List with a symlink", func(ctx context.Context) error {
		_, err := filesystem.List(ctx, root)
		return err
	}, hanging, fxpReadLink)

	hanging.set(fxpOpen)
	requireReturnsCanceled(t, "ReadFile open", func(ctx context.Context) error {
		_, err := filesystem.ReadFile(ctx, file, 1<<10)
		return err
	}, hanging, fxpOpen)

	hanging.set(fxpRead)
	requireReturnsCanceled(t, "ReadFile read", func(ctx context.Context) error {
		_, err := filesystem.ReadFile(ctx, file, 1<<10)
		return err
	}, hanging, fxpRead)

	// A deadline ends the wait the same way, and the abandoned requests leave the connection usable.
	hanging.set(fxpStat)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	_, err := filesystem.Size(ctx, file)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Size under deadline = %v, want context.DeadlineExceeded", err)
	}
	hanging.set()
	data, err := filesystem.ReadFile(context.Background(), file, 1<<10)
	if err != nil || string(data) != "content" {
		t.Fatalf("ReadFile after abandoned requests = %q, %v", data, err)
	}
}
