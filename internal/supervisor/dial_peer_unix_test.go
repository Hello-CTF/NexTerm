//go:build unix

package supervisor

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestDialSocketRejectsPeerOfAnotherUser(t *testing.T) {
	dir, err := os.MkdirTemp("", "s")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "a.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	defer func(previous func() int) { expectedPeerUID = previous }(expectedPeerUID)
	expectedPeerUID = func() int { return os.Geteuid() + 1 }
	if conn, err := dialSocket(context.Background(), path); !errors.Is(err, ErrProtocol) {
		if conn != nil {
			_ = conn.Close()
		}
		t.Fatalf("dial to a listener of another uid: err = %v, want ErrProtocol", err)
	}
	expectedPeerUID = os.Geteuid
	conn, err := dialSocket(context.Background(), path)
	if err != nil {
		t.Fatalf("dial to own listener: %v", err)
	}
	_ = conn.Close()
}

func TestEnsurePrivateDirRejectsDirectoryOfAnotherUser(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDir(dir); err != nil {
		t.Fatalf("own directory: %v", err)
	}
	defer func(previous func() int) { expectedPeerUID = previous }(expectedPeerUID)
	expectedPeerUID = func() int { return os.Geteuid() + 1 }
	if err := ensurePrivateDir(dir); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("directory of another user: err = %v, want ErrInvalidInput", err)
	}
}
