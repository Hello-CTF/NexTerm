//go:build unix

package supervisor

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	conn, err := dialSocket(context.Background(), path)
	if conn != nil {
		_ = conn.Close()
	}
	var untrusted *UntrustedEndpointError
	if !errors.Is(err, ErrUntrustedEndpoint) || errors.Is(err, ErrProtocol) || !errors.As(err, &untrusted) || untrusted.Path != path {
		t.Fatalf("dial to a listener of another uid: err = %v, want ErrUntrustedEndpoint naming %s", err, path)
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("error text %q does not name the socket", err)
	}
	expectedPeerUID = os.Geteuid
	conn, err = dialSocket(context.Background(), path)
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
	err := ensurePrivateDir(dir)
	var untrusted *UntrustedEndpointError
	if !errors.Is(err, ErrInvalidInput) || !errors.Is(err, ErrUntrustedEndpoint) || !errors.As(err, &untrusted) || untrusted.Path != dir {
		t.Fatalf("directory of another user: err = %v, want ErrInvalidInput and ErrUntrustedEndpoint naming %s", err, dir)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Fatalf("error text %q does not name the directory", err)
	}
}

func TestStateDirOfAnotherUserIsNeverAdvisedForDeletion(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	defer func(previous func() int) { expectedPeerUID = previous }(expectedPeerUID)
	expectedPeerUID = func() int { return os.Geteuid() + 1 }
	_, idErr := HelperInstanceID(stateDir)
	_, connectErr := ConnectHelper(context.Background(), HelperConfig{StateDir: stateDir, NoSpawn: true})
	for name, err := range map[string]error{"ensurePrivateDir": ensurePrivateDir(stateDir), "HelperInstanceID": idErr, "ConnectHelper": connectErr} {
		if !errors.Is(err, ErrUntrustedEndpoint) || !strings.Contains(err.Error(), stateDir) || !strings.Contains(err.Error(), "chown") || strings.Contains(err.Error(), "rm ") {
			t.Errorf("%s: %v; want the state dir named, chown advised and no rm", name, err)
		}
	}
}

func TestSocketDirOfAnotherUserMayBeDeleted(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sock")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	defer func(previous func() int) { expectedPeerUID = previous }(expectedPeerUID)
	expectedPeerUID = func() int { return os.Geteuid() + 1 }
	_, _, _, err := prepareEndpoint(filepath.Join(dir, "a.sock"))
	if !errors.Is(err, ErrUntrustedEndpoint) || !strings.Contains(err.Error(), dir) || !strings.Contains(err.Error(), "rm ") {
		t.Fatalf("prepareEndpoint: %v; want the directory named and rm advised", err)
	}
	if err := checkEndpointDirOwner(filepath.Join(dir, "a.sock")); !strings.Contains(err.Error(), "rm ") {
		t.Fatalf("checkEndpointDirOwner: %v", err)
	}
}

func TestConnectHelperRefusesUntrustedEndpointWithoutSpawning(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	endpoint, err := helperEndpoint(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "spawned")
	script := filepath.Join(t.TempDir(), "fake-helper")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	defer func(previous func() int) { expectedPeerUID = previous }(expectedPeerUID)
	expectedPeerUID = func() int { return os.Geteuid() + 1 }

	_, err = ConnectHelper(context.Background(), HelperConfig{StateDir: stateDir, Executable: script, SpawnTimeout: time.Second})
	if !errors.Is(err, ErrUntrustedEndpoint) || errors.Is(err, ErrProtocol) {
		t.Fatalf("ConnectHelper: err = %v, want ErrUntrustedEndpoint", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("ConnectHelper spawned a helper")
	}

	// The endpoint directory check alone, as ConnectHelper runs it after the state directory passes.
	endpointDir := filepath.Dir(endpoint)
	if err := os.MkdirAll(endpointDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(endpointDir) })
	err = checkEndpointDirOwner(endpoint)
	var untrusted *UntrustedEndpointError
	if !errors.Is(err, ErrUntrustedEndpoint) || !errors.As(err, &untrusted) || untrusted.Path != endpointDir {
		t.Fatalf("checkEndpointDirOwner: err = %v, want ErrUntrustedEndpoint naming %s", err, endpointDir)
	}
	expectedPeerUID = os.Geteuid
	if err := checkEndpointDirOwner(endpoint); err != nil {
		t.Fatalf("own endpoint directory: %v", err)
	}
}
