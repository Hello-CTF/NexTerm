//go:build unix

package production

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
