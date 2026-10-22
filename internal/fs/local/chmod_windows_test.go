//go:build windows

package local

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// Windows chmod supports the owner-write subset of the mode: the 0o200 bit
// maps to FILE_ATTRIBUTE_READONLY (set = read-only, clear = writable).
func TestChmodWindowsReadOnlySubset(t *testing.T) {
	filesystem := New()
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	readOnly := func() bool {
		t.Helper()
		pointer, err := windows.UTF16PtrFromString(path)
		if err != nil {
			t.Fatal(err)
		}
		attributes, err := windows.GetFileAttributes(pointer)
		if err != nil {
			t.Fatal(err)
		}
		return attributes&windows.FILE_ATTRIBUTE_READONLY != 0
	}
	if readOnly() {
		t.Fatal("a fresh 0o600 file must not be read-only")
	}
	if err := filesystem.Chmod(context.Background(), path, 0o444); err != nil {
		t.Fatal(err)
	}
	if !readOnly() {
		t.Fatal("chmod 0o444 did not set the read-only attribute")
	}
	if err := filesystem.Chmod(context.Background(), path, 0o600); err != nil {
		t.Fatal(err)
	}
	if readOnly() {
		t.Fatal("chmod 0o600 did not clear the read-only attribute")
	}
	if err := filesystem.Chmod(context.Background(), path, 0); err != nil {
		t.Fatal(err)
	}
	if !readOnly() {
		t.Fatal("chmod 0 did not set the read-only attribute")
	}
}
