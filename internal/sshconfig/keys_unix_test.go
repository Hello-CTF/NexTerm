//go:build unix

package sshconfig

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestInspectKeyFileFIFO(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fifo_key")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	assertInspectReturns(t, path)

	privPath, _ := writeLegacyEncryptedRSA(t, dir, "enc_key")
	if err := syscall.Mkfifo(privPath+".pub", 0o600); err != nil {
		t.Fatalf("mkfifo pub: %v", err)
	}
	assertInspectReturns(t, privPath)
}

func assertInspectReturns(t *testing.T, path string) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := InspectKeyFile(path)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("InspectKeyFile(%s) succeeded for non-regular file", path)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("InspectKeyFile(%s) blocked on non-regular file", path)
	}
}
