//go:build unix

package sshconfig

import (
	"os"
	"path/filepath"
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
	if err := os.Symlink(keyPath, filepath.Join(dir, "id_alias")); err != nil {
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
