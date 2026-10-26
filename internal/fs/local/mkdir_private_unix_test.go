//go:build unix

package local

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestMkdirPrivateCreatesOwnerOnlyDirectory(t *testing.T) {
	target := filepath.Join(t.TempDir(), "private")
	if err := New().MkdirPrivate(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(target)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("private dir = %v, %v; want a 0700 directory", info, err)
	}
}

func TestMkdirPrivateRefusesExistingPaths(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "existing")
	if err := os.Mkdir(existing, 0o777); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(existing, link); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(root, "dangling")
	if err := os.Symlink(filepath.Join(root, "missing"), dangling); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{existing, link, dangling} {
		if err := New().MkdirPrivate(t.Context(), target); !errors.Is(err, fs.ErrExist) {
			t.Fatalf("MkdirPrivate(%s) = %v, want fs.ErrExist", target, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("dangling symlink target was created: %v", err)
	}
	if err := New().MkdirPrivate(t.Context(), filepath.Join(root, "a", "b")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("MkdirPrivate created parents: %v", err)
	}
	var _ base.PrivateDirCreator = New()
}
