package ssh

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/fs/conditional"
)

func expectRemoteVersion(content string) conditional.Expectation {
	return conditional.VersionOf([]byte(content)).Expectation()
}

func requireRemoteMismatch(t *testing.T, err error) *conditional.MismatchError {
	t.Helper()
	var mismatch *conditional.MismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("error = %v, want *conditional.MismatchError", err)
	}
	if !errors.Is(err, conditional.ErrVersionMismatch) {
		t.Fatalf("mismatch does not match ErrVersionMismatch: %v", err)
	}
	return mismatch
}

func assertNoRemoteTemporaryLeftovers(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".nexterm-tmp-") {
			t.Errorf("temporary file left behind: %s", entry.Name())
		}
	}
}

func TestWriteFileVersionCommitsWithBackup(t *testing.T) {
	filesystem, root := newTestFS(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := filesystem.WriteFile(ctx, "file.txt", []byte("old"), false); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.WriteFileVersion(ctx, "file.txt", []byte("new"), true, expectRemoteVersion("old")); err != nil {
		t.Fatal(err)
	}
	assertRemoteContent(t, filesystem, "file.txt", "new")
	assertRemoteContent(t, filesystem, "file.txt.nexterm-bak", "old")
	if err := filesystem.WriteFileVersion(ctx, "file.txt", []byte("third"), false, expectRemoteVersion("new")); err != nil {
		t.Fatal(err)
	}
	assertRemoteContent(t, filesystem, "file.txt", "third")
	assertRemoteContent(t, filesystem, "file.txt.nexterm-bak", "old")
	assertNoRemoteTemporaryLeftovers(t, root)
}

func TestWriteFileVersionCreatesOnlyWhenAbsent(t *testing.T) {
	filesystem, root := newTestFS(t, nil)
	ctx := context.Background()
	if err := filesystem.WriteFileVersion(ctx, "created.txt", []byte("fresh"), true, conditional.Absent()); err != nil {
		t.Fatal(err)
	}
	assertRemoteContent(t, filesystem, "created.txt", "fresh")
	if exists, err := filesystem.Exists(ctx, "created.txt.nexterm-bak"); err != nil || exists {
		t.Fatalf("conditional create wrote a backup: %v, %v", exists, err)
	}
	requireRemoteMismatch(t, filesystem.WriteFileVersion(ctx, "created.txt", []byte("clobber"), false, conditional.Absent()))
	assertRemoteContent(t, filesystem, "created.txt", "fresh")
	assertNoRemoteTemporaryLeftovers(t, root)
}

func TestWriteFileVersionRejectsStaleExpectations(t *testing.T) {
	filesystem, root := newTestFS(t, nil)
	ctx := context.Background()
	if err := filesystem.WriteFile(ctx, "file.txt", []byte("actual"), false); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		expected conditional.Expectation
	}{
		{name: "wrong digest", expected: expectRemoteVersion("xxxxx!")},
		{name: "wrong size", expected: conditional.Expectation{Exists: true, Size: 99, SHA256: conditional.VersionOf([]byte("actual")).SHA256}},
		{name: "absent expected", expected: conditional.Absent()},
	} {
		t.Run(test.name, func(t *testing.T) {
			requireRemoteMismatch(t, filesystem.WriteFileVersion(ctx, "file.txt", []byte("new"), true, test.expected))
			assertRemoteContent(t, filesystem, "file.txt", "actual")
		})
	}
	requireRemoteMismatch(t, filesystem.WriteFileVersion(ctx, "missing.txt", []byte("new"), false, expectRemoteVersion("actual")))
	if exists, err := filesystem.Exists(ctx, "missing.txt"); err != nil || exists {
		t.Fatalf("mismatch created the target: %v, %v", exists, err)
	}
	if exists, err := filesystem.Exists(ctx, "file.txt.nexterm-bak"); err != nil || exists {
		t.Fatalf("mismatch wrote a backup: %v, %v", exists, err)
	}
	assertNoRemoteTemporaryLeftovers(t, root)
}

func TestWriteFileVersionDetectsExternalMutationBeforeCommit(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, filesystem *FS)
		verify func(t *testing.T, filesystem *FS)
	}{
		{name: "external write", mutate: func(t *testing.T, filesystem *FS) {
			if err := filesystem.WriteFile(context.Background(), "file.txt", []byte("external"), false); err != nil {
				t.Fatal(err)
			}
		}, verify: func(t *testing.T, filesystem *FS) {
			assertRemoteContent(t, filesystem, "file.txt", "external")
		}},
		{name: "external delete", mutate: func(t *testing.T, filesystem *FS) {
			if err := filesystem.Delete(context.Background(), "file.txt", false); err != nil {
				t.Fatal(err)
			}
		}, verify: func(t *testing.T, filesystem *FS) {
			if exists, err := filesystem.Exists(context.Background(), "file.txt"); err != nil || exists {
				t.Fatalf("deleted target reappeared: %v, %v", exists, err)
			}
		}},
		{name: "external rename replacement", mutate: func(t *testing.T, filesystem *FS) {
			if err := filesystem.WriteFile(context.Background(), "replacement.txt", []byte("external"), false); err != nil {
				t.Fatal(err)
			}
			if err := filesystem.Rename(context.Background(), "replacement.txt", "file.txt"); err != nil {
				t.Fatal(err)
			}
		}, verify: func(t *testing.T, filesystem *FS) {
			assertRemoteContent(t, filesystem, "file.txt", "external")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			filesystem, root := newTestFS(t, nil)
			ctx := context.Background()
			if err := filesystem.WriteFile(ctx, "file.txt", []byte("verified"), false); err != nil {
				t.Fatal(err)
			}
			conditionalTestHook = func() { test.mutate(t, filesystem) }
			t.Cleanup(func() { conditionalTestHook = nil })
			requireRemoteMismatch(t, filesystem.WriteFileVersion(ctx, "file.txt", []byte("new"), true, expectRemoteVersion("verified")))
			test.verify(t, filesystem)
			if exists, err := filesystem.Exists(ctx, "file.txt.nexterm-bak"); err != nil || exists {
				t.Fatalf("mismatch wrote a backup: %v, %v", exists, err)
			}
			assertNoRemoteTemporaryLeftovers(t, root)
		})
	}
}

func TestWriteFileVersionDetectsExternalCreateBeforeCommit(t *testing.T) {
	filesystem, root := newTestFS(t, nil)
	conditionalTestHook = func() {
		if err := filesystem.WriteFile(context.Background(), "created.txt", []byte("external"), false); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { conditionalTestHook = nil })
	requireRemoteMismatch(t, filesystem.WriteFileVersion(context.Background(), "created.txt", []byte("new"), false, conditional.Absent()))
	assertRemoteContent(t, filesystem, "created.txt", "external")
	assertNoRemoteTemporaryLeftovers(t, root)
}

func TestWriteFileVersionRejectsSymlinkTargets(t *testing.T) {
	filesystem, root := newTestFS(t, nil)
	ctx := context.Background()
	if err := filesystem.WriteFile(ctx, "victim.txt", []byte("victim"), false); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "victim.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	requireRemoteMismatch(t, filesystem.WriteFileVersion(ctx, "link.txt", []byte("new"), true, expectRemoteVersion("victim")))
	requireRemoteMismatch(t, filesystem.WriteFileVersion(ctx, "link.txt", []byte("new"), false, conditional.Absent()))
	assertRemoteContent(t, filesystem, "victim.txt", "victim")
	if info, err := os.Lstat(filepath.Join(root, "link.txt")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink itself replaced: %v, %v", info, err)
	}
	assertNoRemoteTemporaryLeftovers(t, root)
}

func TestWriteFileVersionCancellation(t *testing.T) {
	filesystem, root := newTestFS(t, nil)
	if err := filesystem.WriteFile(context.Background(), "file.txt", []byte("verified"), false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := filesystem.WriteFileVersion(ctx, "file.txt", []byte("new"), true, expectRemoteVersion("verified")); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled write = %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	conditionalTestHook = cancel
	t.Cleanup(func() { conditionalTestHook = nil })
	if err := filesystem.WriteFileVersion(ctx, "file.txt", []byte("new"), true, expectRemoteVersion("verified")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel before commit = %v", err)
	}
	assertRemoteContent(t, filesystem, "file.txt", "verified")
	if exists, err := filesystem.Exists(context.Background(), "file.txt.nexterm-bak"); err != nil || exists {
		t.Fatalf("cancelled write produced a backup: %v, %v", exists, err)
	}
	assertNoRemoteTemporaryLeftovers(t, root)
}

func TestWriteFileVersionRejectsInvalidExpectations(t *testing.T) {
	filesystem, _ := newTestFS(t, nil)
	err := filesystem.WriteFileVersion(context.Background(), "file.txt", []byte("new"), false, conditional.Expectation{Exists: true, Size: 6, SHA256: "not-a-digest"})
	if err == nil || errors.Is(err, conditional.ErrVersionMismatch) {
		t.Fatalf("invalid expectation = %v", err)
	}
}
