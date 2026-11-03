//go:build unix

package supervisor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHelperInstanceIDFileIsPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if _, err := HelperInstanceID(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, HelperInstanceIDFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, %v; want 0600", info.Mode(), err)
	}
}

func TestHelperInstanceIDRejectsUnsafeFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	id, err := HelperInstanceID(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, HelperInstanceIDFile)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := HelperInstanceID(dir); err == nil {
		t.Fatal("a group/world readable id file must be refused")
	}
	_ = os.Remove(path)
	target := filepath.Join(dir, "elsewhere")
	if err := os.WriteFile(target, []byte(id+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := HelperInstanceID(dir); err == nil {
		t.Fatal("a symlinked id file must be refused")
	}
}
