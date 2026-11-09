package ssh

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/pkg/sftp"
)

func requirePOSIXFileModes(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits and symlinks are not portable to Windows")
	}
}

func writeLocal(t *testing.T, path, content string, mode fs.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func assertLocalFile(t *testing.T, path, content string, mode fs.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != mode {
		t.Fatalf("%s mode = %v, want a regular file with %v", path, info.Mode(), mode)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != content {
		t.Fatalf("%s = %q, %v; want %q", path, data, err, content)
	}
}

func TestWriteFileCancelledBeforeCommitKeepsTheOriginal(t *testing.T) {
	requirePOSIXFileModes(t)
	for _, test := range []struct {
		name string
		hook func(*FS) *func()
	}{
		{name: "after staging the content", hook: func(f *FS) *func() { return &f.stageWrittenHook }},
		{name: "before the rename", hook: func(f *FS) *func() { return &f.replacePreCommitHook }},
	} {
		t.Run(test.name, func(t *testing.T) {
			filesystem, root := newTestFS(t, nil)
			target := filepath.Join(root, "nginx.conf")
			writeLocal(t, target, "original", 0o640)
			ctx, cancel := context.WithCancel(context.Background())
			*test.hook(filesystem) = cancel
			err := filesystem.WriteFile(ctx, target, make([]byte, 4<<20), false)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled save = %v, want context.Canceled", err)
			}
			assertLocalFile(t, target, "original", 0o640)
			assertNoRemoteTemporaryLeftovers(t, root)
		})
	}
}

func TestWriteFilePreservesModeAndReplacesAtomically(t *testing.T) {
	requirePOSIXFileModes(t)
	filesystem, root := newTestFS(t, nil)
	target := filepath.Join(root, "script.sh")
	writeLocal(t, target, "#!/bin/sh\necho old\n", 0o754)
	if err := filesystem.WriteFile(t.Context(), target, []byte("#!/bin/sh\necho new\n"), false); err != nil {
		t.Fatal(err)
	}
	assertLocalFile(t, target, "#!/bin/sh\necho new\n", 0o754)
	assertNoRemoteTemporaryLeftovers(t, root)
}

func TestWriteFileCreatesAMissingFileOwnerOnly(t *testing.T) {
	requirePOSIXFileModes(t)
	filesystem, root := newTestFS(t, nil)
	target := filepath.Join(root, "fresh.txt")
	if err := filesystem.WriteFile(t.Context(), target, []byte("fresh"), false); err != nil {
		t.Fatal(err)
	}
	assertLocalFile(t, target, "fresh", 0o600)
	assertNoRemoteTemporaryLeftovers(t, root)
}

func TestWriteFileThroughASymlinkUpdatesTheTargetAndKeepsTheLink(t *testing.T) {
	requirePOSIXFileModes(t)
	filesystem, root := newTestFS(t, nil)
	if err := os.Mkdir(filepath.Join(root, "available"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "enabled"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "available", "site.conf")
	writeLocal(t, target, "old", 0o644)
	link := filepath.Join(root, "enabled", "site.conf")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	relative := filepath.Join(root, "available", "alias.conf")
	if err := os.Symlink("site.conf", relative); err != nil {
		t.Fatal(err)
	}

	if err := filesystem.WriteFile(t.Context(), link, []byte("via absolute link"), false); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("link after save = %v, %v; want it to stay a symlink", info, err)
	}
	assertLocalFile(t, target, "via absolute link", 0o644)

	if err := filesystem.WriteFile(t.Context(), relative, []byte("via relative link"), false); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(relative); err != nil || info.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("relative link after save = %v, %v; want it to stay a symlink", info, err)
	}
	assertLocalFile(t, target, "via relative link", 0o644)
	assertNoRemoteTemporaryLeftovers(t, filepath.Join(root, "available"))
	assertNoRemoteTemporaryLeftovers(t, filepath.Join(root, "enabled"))
}

func TestWriteFileFallsBackToInPlaceInANonWritableDirectory(t *testing.T) {
	requirePOSIXFileModes(t)
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permission")
	}
	filesystem, root := newTestFS(t, nil)
	dir := filepath.Join(root, "locked")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "app.conf")
	writeLocal(t, target, "old", 0o664)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if err := filesystem.WriteFile(t.Context(), target, []byte("new"), false); err != nil {
		t.Fatal(err)
	}
	assertLocalFile(t, target, "new", 0o664)
}

func TestWriteFileWithoutPosixRenameMovesTheOriginalAsideSafely(t *testing.T) {
	requirePOSIXFileModes(t)
	filesystem, root := newTestFS(t, nil)
	filesystem.posixRenameSupported = func(*sftp.Client) bool { return false }
	target := filepath.Join(root, "app.conf")
	writeLocal(t, target, "old", 0o640)
	if err := filesystem.WriteFile(t.Context(), target, []byte("new"), false); err != nil {
		t.Fatal(err)
	}
	assertLocalFile(t, target, "new", 0o640)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("directory after fallback replace = %v, want only the target", entries)
	}
}

func TestCreateFileIsExclusive(t *testing.T) {
	requirePOSIXFileModes(t)
	filesystem, root := newTestFS(t, nil)
	ctx := t.Context()
	created := filepath.Join(root, "new.conf")
	if err := filesystem.CreateFile(ctx, created); err != nil {
		t.Fatal(err)
	}
	assertLocalFile(t, created, "", 0o600)

	existing := filepath.Join(root, "nginx.conf")
	writeLocal(t, existing, "keep me", 0o644)
	if err := filesystem.CreateFile(ctx, existing); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("CreateFile over an existing file = %v, want fs.ErrExist", err)
	}
	assertLocalFile(t, existing, "keep me", 0o644)

	if err := filesystem.CreateFile(ctx, filepath.Join(root)); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("CreateFile over a directory = %v, want fs.ErrExist", err)
	}
	dangling := filepath.Join(root, "dangling")
	if err := os.Symlink(filepath.Join(root, "nowhere"), dangling); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.CreateFile(ctx, dangling); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("CreateFile over a dangling symlink = %v, want fs.ErrExist", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "nowhere")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("CreateFile followed a dangling symlink: %v", err)
	}
	if err := filesystem.CreateFile(ctx, filepath.Join(root, "missing", "child")); err == nil || errors.Is(err, fs.ErrExist) {
		t.Fatalf("CreateFile under a missing parent = %v, want an error that is not fs.ErrExist", err)
	}
}
