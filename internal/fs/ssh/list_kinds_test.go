package ssh

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/pkg/sftp"
)

func TestListResolvesSymlinkTargetKindsAndDotHidden(t *testing.T) {
	filesystem, root := newTestFS(t, nil)
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dir", "inner"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".config"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"to-dir": "dir", "to-file": "file", "broken": "missing"} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := filesystem.List(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]base.FileEntry{}
	for _, entry := range entries {
		got[entry.Name] = entry
	}
	for name, want := range map[string]struct {
		kind, target base.FileKind
		hidden       bool
	}{
		"to-dir":  {base.FileSymlink, base.FileDirectory, false},
		"to-file": {base.FileSymlink, base.FileFile, false},
		"broken":  {base.FileSymlink, "", false},
		"dir":     {base.FileDirectory, "", false},
		"file":    {base.FileFile, "", false},
		".config": {base.FileDirectory, "", true},
	} {
		entry := got[name]
		if entry.Kind != want.kind || entry.TargetKind != want.target || entry.Hidden != want.hidden {
			t.Fatalf("%s = %+v, want kind %q target %q hidden %v", name, entry, want.kind, want.target, want.hidden)
		}
	}
	inner, err := filesystem.List(context.Background(), got["to-dir"].Path)
	if err != nil || len(inner) != 1 || inner[0].Name != "inner" {
		t.Fatalf("browse symlinked directory = %+v, %v", inner, err)
	}
}

func stubPosixRenameUnsupported(filesystem *FS) {
	filesystem.posixRename = func(*sftp.Client, string, string) error {
		return &sftp.StatusError{Code: uint32(sftp.ErrSSHFxOpUnsupported)}
	}
}

func remoteNames(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(entries))
	for index, entry := range entries {
		names[index] = entry.Name()
	}
	return names
}

func TestAtomicRenameReplacesWithoutPosixRename(t *testing.T) {
	filesystem, root := newTestFS(t, nil)
	stubPosixRenameUnsupported(filesystem)
	target := filepath.Join(root, "target")
	staged := filepath.Join(root, "staged")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.AtomicRename(context.Background(), staged, target); err != nil {
		t.Fatal(err)
	}
	assertRemoteContent(t, filesystem, target, "new")
	if names := remoteNames(t, root); len(names) != 1 || names[0] != "target" {
		t.Fatalf("directory after replace = %v, want only the target", names)
	}
}

func TestAtomicRenameFallbackRestoresTargetWhenRenameInFails(t *testing.T) {
	filesystem, root := newTestFS(t, nil)
	stubPosixRenameUnsupported(filesystem)
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := filesystem.AtomicRename(context.Background(), filepath.Join(root, "missing-staged"), target)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("AtomicRename error = %v, want the missing staged file", err)
	}
	assertRemoteContent(t, filesystem, target, "old")
	if names := remoteNames(t, root); len(names) != 1 || names[0] != "target" {
		t.Fatalf("directory after failed replace = %v, want only the restored target", names)
	}
}

func TestAtomicRenameReportsOtherPosixRenameErrors(t *testing.T) {
	filesystem, root := newTestFS(t, nil)
	refused := errors.New("refused")
	filesystem.posixRename = func(*sftp.Client, string, string) error { return refused }
	target := filepath.Join(root, "target")
	staged := filepath.Join(root, "staged")
	for _, path := range []string{target, staged} {
		if err := os.WriteFile(path, []byte(filepath.Base(path)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := filesystem.AtomicRename(context.Background(), staged, target); !errors.Is(err, refused) {
		t.Fatalf("AtomicRename error = %v, want %v", err, refused)
	}
	assertRemoteContent(t, filesystem, target, "target")
}

func TestAtomicRenameUsesPosixRenameWhenAdvertised(t *testing.T) {
	filesystem, root := newTestFS(t, nil)
	target := filepath.Join(root, "target")
	staged := filepath.Join(root, "staged")
	for _, path := range []string{target, staged} {
		if err := os.WriteFile(path, []byte(filepath.Base(path)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := filesystem.AtomicRename(context.Background(), staged, target); err != nil {
		t.Fatal(err)
	}
	assertRemoteContent(t, filesystem, target, "staged")
}
