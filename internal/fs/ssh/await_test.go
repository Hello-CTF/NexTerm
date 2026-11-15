package ssh

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/fs/ssh/sftptest"
)

func newHangingTestFS(t *testing.T) (*FS, string, *sftptest.HangingServer) {
	t.Helper()
	client, root, hanging := sftptest.NewHangingClient(t)
	return New(client, nil), root, hanging
}

// cancelWhenHeld cancels once the proxy has swallowed a request of the expected kind, so the caller is known to be
// waiting on an unanswered request when the cancel lands.
func cancelWhenHeld(t *testing.T, hanging *sftptest.HangingServer, kind sftptest.Request, cancel context.CancelFunc) {
	t.Helper()
	go func() {
		for {
			select {
			case held := <-hanging.Held():
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

func requireReturnsCanceled(t *testing.T, name string, run func(context.Context) error, hanging *sftptest.HangingServer, kind sftptest.Request) {
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

	hanging.Hang(sftptest.Stat)
	requireReturnsCanceled(t, "Exists", func(ctx context.Context) error {
		_, err := filesystem.Exists(ctx, file)
		return err
	}, hanging, sftptest.Stat)

	hanging.Hang(sftptest.RealPath)
	requireReturnsCanceled(t, "home path resolution", func(ctx context.Context) error {
		_, err := filesystem.Size(ctx, "~/file")
		return err
	}, hanging, sftptest.RealPath)

	hanging.Hang(sftptest.Stat, sftptest.ReadLink)
	requireReturnsCanceled(t, "List with a symlink", func(ctx context.Context) error {
		_, err := filesystem.List(ctx, root)
		return err
	}, hanging, sftptest.ReadLink)

	hanging.Hang(sftptest.Open)
	requireReturnsCanceled(t, "ReadFile open", func(ctx context.Context) error {
		_, err := filesystem.ReadFile(ctx, file, 1<<10)
		return err
	}, hanging, sftptest.Open)

	hanging.Hang(sftptest.Read)
	requireReturnsCanceled(t, "ReadFile read", func(ctx context.Context) error {
		_, err := filesystem.ReadFile(ctx, file, 1<<10)
		return err
	}, hanging, sftptest.Read)

	// A deadline ends the wait the same way, and the abandoned requests leave the connection usable.
	hanging.Hang(sftptest.Stat)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	_, err := filesystem.Size(ctx, file)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Size under deadline = %v, want context.DeadlineExceeded", err)
	}
	hanging.Hang()
	data, err := filesystem.ReadFile(context.Background(), file, 1<<10)
	if err != nil || string(data) != "content" {
		t.Fatalf("ReadFile after abandoned requests = %q, %v", data, err)
	}
}
