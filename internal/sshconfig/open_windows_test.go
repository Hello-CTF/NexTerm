//go:build windows

package sshconfig

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// A polling inventory parse holds the config open while a concurrent removal
// (watch resync or manual delete) lands. The parse open must share delete so
// the removal never fails with a sharing violation, and the open handle keeps
// reading the pre-removal snapshot.
func TestOpenSharedDeleteAllowsRemovalWhileOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	const content = "Host alpha\n  HostName 10.0.0.1\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := openSharedDelete(path)
	if err != nil {
		t.Fatalf("openSharedDelete: %v", err)
	}
	defer file.Close()
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove while parse handle open: %v", err)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("read through open handle after removal: %v", err)
	}
	if string(data) != content {
		t.Fatalf("snapshot through handle = %q, want %q", data, content)
	}
}

// The conflict the shared-delete open guards against: a plain os.Open shares
// read and write but not delete on Windows, so a removal racing a parse
// fails outright instead of waiting for the snapshot to be released.
func TestPlainOpenBlocksRemovalWhileOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte("Host alpha\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := os.Remove(path); err == nil {
		t.Fatal("remove succeeded while a plain os.Open handle was open; expected a sharing violation")
	}
}
