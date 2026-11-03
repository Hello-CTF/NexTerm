//go:build unix

package supervisor

import (
	"errors"
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

func TestHelperInstanceIDReplacesUnsafeFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	id, err := HelperInstanceID(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, HelperInstanceIDFile)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	repaired, err := HelperInstanceID(dir)
	if err != nil {
		t.Fatalf("a group/world readable id file must be replaced: %v", err)
	}
	assertPrivateIDFile(t, path, repaired)

	_ = os.Remove(path)
	target := filepath.Join(dir, "elsewhere")
	if err := os.WriteFile(target, []byte(id+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	repaired, err = HelperInstanceID(dir)
	if err != nil {
		t.Fatalf("a symlinked id file must be replaced: %v", err)
	}
	assertPrivateIDFile(t, path, repaired)
	data, err := os.ReadFile(target)
	if err != nil || string(data) != id+"\n" {
		t.Fatalf("symlink target changed: %q, %v", data, err)
	}
}

func assertPrivateIDFile(t *testing.T, path, id string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("id file mode = %v, want a 0600 regular file", info.Mode())
	}
	again, err := HelperInstanceID(filepath.Dir(path))
	if err != nil || again != id {
		t.Fatalf("id not stable after repair: %q vs %q, %v", again, id, err)
	}
}

func TestHelperInstanceIDKeepsFileOfAnotherUser(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if _, err := HelperInstanceID(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, HelperInstanceIDFile)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func(previous func() int) { expectedPeerUID = previous }(expectedPeerUID)
	expectedPeerUID = func() int { return os.Geteuid() + 1 }
	if _, err := readInstanceID(path); err == nil || errors.Is(err, errInstanceIDInvalid) {
		t.Fatalf("file of another user: err = %v, want a hard error", err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("file of another user was modified")
	}
}

func TestHelperInstanceIDReplacesUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode 0000 file, so it is never unreadable")
	}
	dir := filepath.Join(t.TempDir(), "state")
	if _, err := HelperInstanceID(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, HelperInstanceIDFile)
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	id, err := HelperInstanceID(dir)
	if err != nil {
		t.Fatalf("an unreadable id file must be replaced: %v", err)
	}
	assertPrivateIDFile(t, path, id)
}
