package ssh

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/fs/conditional"
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
		arm  func(*FS, context.CancelFunc)
	}{
		{name: "after staging the content", arm: func(f *FS, cancel context.CancelFunc) { f.stageWrittenHook = func(string) { cancel() } }},
		{name: "before the rename", arm: func(f *FS, cancel context.CancelFunc) { f.replacePreCommitHook = cancel }},
	} {
		t.Run(test.name, func(t *testing.T) {
			filesystem, root := newTestFS(t, nil)
			target := filepath.Join(root, "nginx.conf")
			writeLocal(t, target, "original", 0o640)
			ctx, cancel := context.WithCancel(context.Background())
			test.arm(filesystem, cancel)
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
	filesystem.posixRename = func(*sftp.Client, string, string) error { return sftp.ErrSSHFxOpUnsupported }
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

// stagedExposure reports how the content written so far could be reached by another account: the staged file's own
// mode and, when it sits in a staging directory, that directory's mode.
func stagedExposure(t *testing.T, root, temporary string) (file, dir fs.FileMode) {
	t.Helper()
	local := filepath.Join(root, filepath.FromSlash(temporary))
	if filepath.IsAbs(filepath.FromSlash(temporary)) {
		local = filepath.FromSlash(temporary)
	}
	info, err := os.Lstat(local)
	if err != nil {
		t.Fatalf("staged file %s: %v", local, err)
	}
	file = info.Mode().Perm()
	parent, err := os.Lstat(filepath.Dir(local))
	if err != nil {
		t.Fatal(err)
	}
	return file, parent.Mode().Perm()
}

func TestStagedContentIsNeverReadableByOtherAccounts(t *testing.T) {
	requirePOSIXFileModes(t)
	for _, test := range []struct {
		name       string
		privateDir bool
	}{
		{name: "inside a private staging directory", privateDir: true},
		{name: "beside the target when no directory can be made"},
	} {
		t.Run(test.name, func(t *testing.T) {
			filesystem, root := newTestFS(t, nil)
			if !test.privateDir {
				filesystem.mkdirStage = func(*sftp.Client, string) error { return &sftp.StatusError{Code: uint32(sftp.ErrSSHFxFailure)} }
			}
			target := filepath.Join(root, "id_ed25519")
			writeLocal(t, target, "old secret", 0o600)
			var observed int
			filesystem.stageWrittenHook = func(temporary string) {
				observed++
				file, dir := stagedExposure(t, root, temporary)
				if test.privateDir {
					if dir != 0o700 || filepath.Dir(temporary) == filepath.Dir(target) {
						t.Errorf("staging directory mode = %v for %s, want a private 0700 directory", dir, temporary)
					}
					return
				}
				if file&0o077 != 0 {
					t.Errorf("staged file mode = %v while content is written, want at most 0600", file)
				}
			}
			if err := filesystem.WriteFile(t.Context(), target, []byte("new secret"), false); err != nil {
				t.Fatal(err)
			}
			if observed != 1 {
				t.Fatalf("staging hook ran %d times, want 1", observed)
			}
			assertLocalFile(t, target, "new secret", 0o600)
			assertNoRemoteTemporaryLeftovers(t, root)
		})
	}
}

func TestWriteFileVersionStagesPrivately(t *testing.T) {
	requirePOSIXFileModes(t)
	filesystem, root := newTestFS(t, nil)
	filesystem.stageWrittenHook = func(temporary string) {
		if _, dir := stagedExposure(t, root, temporary); dir != 0o700 {
			t.Errorf("conditional create staging directory mode = %v, want 0700", dir)
		}
	}
	if err := filesystem.WriteFileVersion(t.Context(), filepath.Join(root, "created.env"), []byte("TOKEN=x"), false, conditional.Absent()); err != nil {
		t.Fatal(err)
	}
	assertLocalFile(t, filepath.Join(root, "created.env"), "TOKEN=x", 0o600)
	assertNoRemoteTemporaryLeftovers(t, root)
}

// secondaryGroup returns a group the test account belongs to other than its primary one, so a file can be given
// an owner the temporary copy does not get by default.
func secondaryGroup(t *testing.T) int {
	t.Helper()
	groups, err := os.Getgroups()
	if err != nil {
		t.Skipf("supplementary groups unavailable: %v", err)
	}
	for _, group := range groups {
		if group != os.Getegid() {
			return group
		}
	}
	t.Skip("the test account belongs to no supplementary group")
	return 0
}

func TestWriteFileKeepsTheOriginalGroup(t *testing.T) {
	requirePOSIXFileModes(t)
	group := secondaryGroup(t)
	for _, test := range []struct {
		name      string
		chownFail bool
	}{
		{name: "replaced when chown succeeds"},
		{name: "rewritten in place when chown is refused", chownFail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			filesystem, root := newTestFS(t, nil)
			if test.chownFail {
				filesystem.chown = func(*sftp.Client, string, int, int) error {
					return &sftp.StatusError{Code: uint32(sftp.ErrSSHFxPermissionDenied)}
				}
			}
			target := filepath.Join(root, "shared.conf")
			writeLocal(t, target, "old", 0o660)
			if err := os.Chown(target, -1, group); err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(target)
			if err != nil {
				t.Fatal(err)
			}
			if err := filesystem.WriteFile(t.Context(), target, []byte("new"), false); err != nil {
				t.Fatal(err)
			}
			assertLocalFile(t, target, "new", 0o660)
			after, err := os.Lstat(target)
			if err != nil {
				t.Fatal(err)
			}
			entries, err := filesystem.List(t.Context(), root)
			if err != nil || len(entries) != 1 || entries[0].Group != strconv.Itoa(group) {
				t.Fatalf("listing after save = %+v, %v; want only the target with group %d", entries, err, group)
			}
			if test.chownFail && !os.SameFile(before, after) {
				t.Fatal("a refused chown still replaced the file, handing it to the writing account")
			}
			assertNoRemoteTemporaryLeftovers(t, root)
		})
	}
}

func TestWriteFileFallsBackInPlaceWhenTheServerRefusesTheRename(t *testing.T) {
	requirePOSIXFileModes(t)
	for _, test := range []struct {
		name   string
		rename func(*sftp.Client, string, string) error
	}{
		{name: "posix-rename refused (EBUSY on a bind mount)", rename: func(*sftp.Client, string, string) error {
			return &sftp.StatusError{Code: uint32(sftp.ErrSSHFxFailure)}
		}},
		{name: "posix-rename denied (sticky directory)", rename: func(*sftp.Client, string, string) error {
			return os.ErrPermission
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			filesystem, root := newTestFS(t, nil)
			filesystem.posixRename = test.rename
			target := filepath.Join(root, "hosts")
			writeLocal(t, target, "127.0.0.1 old", 0o644)
			before, err := os.Lstat(target)
			if err != nil {
				t.Fatal(err)
			}
			if err := filesystem.WriteFile(t.Context(), target, []byte("127.0.0.1 new"), false); err != nil {
				t.Fatal(err)
			}
			assertLocalFile(t, target, "127.0.0.1 new", 0o644)
			after, err := os.Lstat(target)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(before, after) {
				t.Fatal("a refused rename did not fall back to rewriting the original in place")
			}
			assertNoRemoteTemporaryLeftovers(t, root)
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Fatalf("directory after fallback = %v, want only the target", entries)
			}
		})
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
