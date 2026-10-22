//go:build unix

package production

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestSSHImportServerIncludeSymlinkEscapeExcluded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	sshDir := filepath.Join(home, ".ssh")
	outside := t.TempDir()
	configPath := filepath.Join(sshDir, "config")
	writeInventoryTestFile(t, configPath, "Host alpha\n  HostName alpha.example\n")
	writeInventoryTestFile(t, filepath.Join(outside, "secret.conf"), "Host secret\n  HostName secret.example\n")
	if err := os.Symlink(filepath.Join(outside, "secret.conf"), filepath.Join(sshDir, "linked.conf")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	writeInventoryTestFile(t, configPath, "Include linked.conf\nHost alpha\n  HostName alpha.example\n")
	dispatcher, _, _, _ := sshImportTestRigMode(t, false)

	preview := sshImportPreview(t, dispatcher, `{"args":{"source":"ssh-config","path":`+strconvQuote(configPath)+`}}`)
	aliases := map[string]bool{}
	for _, host := range preview.Hosts {
		aliases[host.Alias] = true
	}
	if !aliases["alpha"] || aliases["secret"] {
		t.Fatalf("server preview hosts = %+v, want only in-bounds alpha", preview.Hosts)
	}
	boundaryDiags := 0
	for _, diagnostic := range preview.Diagnostics {
		if diagnostic.Code == "include-out-of-bounds" {
			boundaryDiags++
		}
	}
	if boundaryDiags != 1 {
		t.Fatalf("include-out-of-bounds diagnostics = %+v, want 1", preview.Diagnostics)
	}
}

func TestSSHImportServerRootSymlinkEscapeRejected(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	sshDir := filepath.Join(home, ".ssh")
	outside := t.TempDir()
	writeInventoryTestFile(t, filepath.Join(outside, "config"), "Host secret\n  HostName secret.example\n")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "config"), filepath.Join(sshDir, "config")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	dispatcher, _, _, _ := sshImportTestRigMode(t, false)

	response := dispatchStoreTest(dispatcher, "ssh_import_preview", `{"args":{"source":"ssh-config","path":`+strconvQuote(filepath.Join(sshDir, "config"))+`}}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("server preview of symlinked root = %+v, want bad_param", response)
	}
	if !strings.Contains(response.Error.Message, "escapes root boundary") {
		t.Fatalf("server preview message = %q, want root boundary rejection", response.Error.Message)
	}
}

// The production reader follows a symlink only to a regular target, matching
// the pre-open Lstat boundary: a symlinked key file reads the target bytes, a
// symlink to a directory is rejected as non-regular, and a dangling symlink
// surfaces the stat error instead of reading through.
func TestProductionReadKeyFileBoundedSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real_key")
	const content = "throwaway key material"
	if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "linked_key")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	data, err := productionReadKeyFileBounded(link)
	if err != nil {
		t.Fatalf("read through symlink: %v", err)
	}
	if string(data) != content {
		t.Fatalf("symlink read = %q, want %q", data, content)
	}

	dirLink := filepath.Join(dir, "linked_dir")
	if err := os.Symlink(dir, dirLink); err != nil {
		t.Fatalf("symlink dir: %v", err)
	}
	if _, err := productionReadKeyFileBounded(dirLink); err == nil || !strings.Contains(err.Error(), "is not a regular file") {
		t.Fatalf("symlink-to-directory read error = %v, want non-regular rejection", err)
	}

	dangling := filepath.Join(dir, "dangling_key")
	if err := os.Symlink(filepath.Join(dir, "absent"), dangling); err != nil {
		t.Fatalf("dangling symlink: %v", err)
	}
	if _, err := productionReadKeyFileBounded(dangling); err == nil {
		t.Fatal("dangling symlink read succeeded, want stat error")
	}
}

// A FIFO must be rejected by the Lstat boundary without opening — the read
// returns immediately instead of blocking on a writer that never arrives.
func TestProductionReadKeyFileBoundedFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fifo_key")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := productionReadKeyFileBounded(path)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("productionReadKeyFileBounded(%s) succeeded for non-regular file", path)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("productionReadKeyFileBounded(%s) blocked on non-regular file", path)
	}
}
