//go:build unix

package production

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestInventoryServerIncludeSymlinkEscapeRejected(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	service, database := newInventoryTestService(t, false)
	sshDir := filepath.Join(home, ".ssh")
	outside := t.TempDir()
	mainPath := filepath.Join(sshDir, "config")
	writeInventoryTestFile(t, mainPath, "Host alpha\n  HostName 10.0.0.1\n")
	writeInventoryTestFile(t, filepath.Join(outside, "secret.conf"), "Host secret\n  HostName secret.example\n")
	if err := os.Symlink(filepath.Join(outside, "secret.conf"), filepath.Join(sshDir, "linked.conf")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	writeInventoryTestFile(t, mainPath, "Include linked.conf\nHost alpha\n  HostName 10.0.0.1\n")
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	if _, err := service.sync(ctx, inventorySyncRequest{Source: store.InventorySourceSSHConfig, Path: mainPath}); err == nil {
		t.Fatal("server sync accepted a symlink escape include")
	} else if !strings.Contains(err.Error(), "快照不完整") {
		t.Fatalf("sync error = %v, want incomplete snapshot", err)
	}
	if rows, err := database.AssetList(ctx, false); err != nil || len(rows) != 0 {
		t.Fatalf("symlink escape reconciled assets: %+v, err=%v", rows, err)
	}
}

func TestInventoryServerRootSymlinkEscapeRejected(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	service, database := newInventoryTestService(t, false)
	sshDir := filepath.Join(home, ".ssh")
	outside := t.TempDir()
	writeInventoryTestFile(t, filepath.Join(outside, "config"), "Host secret\n  HostName secret.example\n")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "config"), filepath.Join(sshDir, "config")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	if _, err := service.sync(ctx, inventorySyncRequest{Source: store.InventorySourceSSHConfig, Path: filepath.Join(sshDir, "config")}); err == nil {
		t.Fatal("server sync accepted a symlinked root config escaping ~/.ssh")
	}
	if rows, err := database.AssetList(ctx, false); err != nil || len(rows) != 0 {
		t.Fatalf("symlinked root reconciled assets: %+v, err=%v", rows, err)
	}
}
