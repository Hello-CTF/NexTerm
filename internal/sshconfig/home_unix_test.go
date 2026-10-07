//go:build unix

package sshconfig

import (
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
)

func TestScanHomeSymlinkEscapeSkipped(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	outsideKey, _ := writeTestKey(t, outside, "id_outside", false)
	if err := os.Symlink(outsideKey, filepath.Join(dir, "id_linked")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	keys, diagnostics, err := ScanHome(dir, Limits{})
	if err != nil {
		t.Fatalf("ScanHome: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("escaping symlink must not be scanned: %+v", keys)
	}
	found := false
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == "symlink-escape" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing symlink-escape diagnostic: %+v", diagnostics)
	}
}

func TestScanHomeSymlinkInsideDirAcceptedOnce(t *testing.T) {
	dir := t.TempDir()
	keyPath, fingerprint := writeTestKey(t, dir, "id_real", false)
	aliasPath := filepath.Join(dir, "id_alias")
	if err := os.Symlink(keyPath, aliasPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	keys, _, err := ScanHome(dir, Limits{})
	if err != nil {
		t.Fatalf("ScanHome: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("symlink and real file must dedupe to one key: %+v", keys)
	}
	if keys[0].Fingerprint != fingerprint {
		t.Fatalf("key = %+v", keys[0])
	}
	if keys[0].Path != aliasPath {
		t.Fatalf("first sorted entry (alias) must win: %+v", keys[0])
	}
	if !slices.Contains(keys[0].AltPaths, keyPath) {
		t.Fatalf("alias entry must record resolved target in AltPaths: %+v", keys[0])
	}
}

func TestScanHomeTargetFirstRecordsLaterAlias(t *testing.T) {
	dir := t.TempDir()
	keyPath, fingerprint := writeTestKey(t, dir, "a_target", false)
	aliasPath := filepath.Join(dir, "z_alias")
	if err := os.Symlink(keyPath, aliasPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	keys, _, err := ScanHome(dir, Limits{})
	if err != nil {
		t.Fatalf("ScanHome: %v", err)
	}
	if len(keys) != 1 || keys[0].Path != keyPath || keys[0].Fingerprint != fingerprint {
		t.Fatalf("keys = %+v", keys)
	}
	if !slices.Contains(keys[0].AltPaths, aliasPath) {
		t.Fatalf("later symlink alias must be recorded in AltPaths: %+v", keys[0])
	}
}

func TestPreviewHomeBindsConfigIdentityFileViaAltPaths(t *testing.T) {
	dir := t.TempDir()
	keyPath, fingerprint := writeTestKey(t, dir, "z_target", false)
	aliasPath := filepath.Join(dir, "a_alias")
	if err := os.Symlink(keyPath, aliasPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	config := "Host viaalias\n  HostName a.example.com\n  IdentityFile " + aliasPath + "\n\n" +
		"Host viatarget\n  HostName b.example.com\n  IdentityFile " + keyPath + "\n"
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	preview, err := PreviewHome(dir, nil, nil, Limits{})
	if err != nil {
		t.Fatalf("PreviewHome: %v", err)
	}
	if len(preview.Keys) != 1 || preview.Keys[0].Fingerprint != fingerprint {
		t.Fatalf("keys = %+v", preview.Keys)
	}
	if len(preview.Keys[0].AltPaths) == 0 {
		t.Fatalf("key preview must carry AltPaths: %+v", preview.Keys[0])
	}
	if len(preview.Hosts) != 2 {
		t.Fatalf("hosts = %+v", preview.Hosts)
	}
	for _, host := range preview.Hosts {
		if host.Action != PlanAdd || host.AuthMethod != "key" {
			t.Fatalf("host = %+v", host)
		}
	}
}

func TestScanHomeSkipsFifo(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "id_fifo"), 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	keys, diagnostics, err := ScanHome(dir, Limits{})
	if err != nil {
		t.Fatalf("ScanHome: %v", err)
	}
	if len(keys) != 0 || len(diagnostics) != 0 {
		t.Fatalf("fifo must be skipped silently: keys=%+v diagnostics=%+v", keys, diagnostics)
	}
}
