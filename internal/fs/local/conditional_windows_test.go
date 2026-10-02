//go:build windows

package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/fs/conditional"
)

func TestWindowsReplaceCommitsWithBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	writeSynthetic(t, path, "old content")
	filesystem := New()
	if err := filesystem.WriteFileVersion(context.Background(), path, []byte("new content"), true, expectVersion("old content")); err != nil {
		t.Fatal(err)
	}
	if got := readSynthetic(t, path); got != "new content" {
		t.Fatalf("content = %q", got)
	}
	if got := readSynthetic(t, path+BackupSuffix); got != "old content" {
		t.Fatalf("backup = %q", got)
	}
	if err := filesystem.WriteFileVersion(context.Background(), path, []byte("third"), false, expectVersion("new content")); err != nil {
		t.Fatal(err)
	}
	if got := readSynthetic(t, path+BackupSuffix); got != "old content" {
		t.Fatalf("backup rewritten without backup flag: %q", got)
	}
	assertNoTemporaryLeftovers(t, dir)
}

func TestWindowsReplaceRejectsStaleExpectations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	writeSynthetic(t, path, "actual")
	filesystem := New()
	for _, test := range []struct {
		name     string
		expected conditional.Expectation
	}{
		{name: "wrong digest", expected: expectVersion("xxxxx!")},
		{name: "wrong size", expected: conditional.Expectation{Exists: true, Size: 99, SHA256: conditional.VersionOf([]byte("actual")).SHA256}},
	} {
		t.Run(test.name, func(t *testing.T) {
			requireMismatch(t, filesystem.WriteFileVersion(context.Background(), path, []byte("new"), true, test.expected))
			if got := readSynthetic(t, path); got != "actual" {
				t.Fatalf("target modified on mismatch: %q", got)
			}
			if _, err := os.Lstat(path + BackupSuffix); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("mismatch wrote a backup: %v", err)
			}
		})
	}
	mismatch := requireMismatch(t, filesystem.WriteFileVersion(context.Background(), filepath.Join(dir, "missing.txt"), []byte("new"), false, expectVersion("actual")))
	if mismatch.Actual.Exists {
		t.Fatalf("missing target reported as present: %+v", mismatch.Actual)
	}
	assertNoTemporaryLeftovers(t, dir)
}

func TestWindowsReplaceExcludesNonCooperativeWriteAfterFinalVerify(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	writeSynthetic(t, path, "verified")
	var hookErr error
	conditionalPreCommitHook = func() {
		hookErr = os.WriteFile(path, []byte("external"), 0o600)
	}
	t.Cleanup(func() { conditionalPreCommitHook = nil })
	filesystem := New()
	if err := filesystem.WriteFileVersion(context.Background(), path, []byte("new"), true, expectVersion("verified")); err != nil {
		t.Fatalf("kernel-excluded external write should not fail the commit: %v (hook: %v)", err, hookErr)
	}
	if hookErr == nil {
		t.Fatal("non-cooperative write succeeded despite the kernel share/lock exclusion")
	}
	if got := readSynthetic(t, path); got != "new" {
		t.Fatalf("content = %q", got)
	}
	if got := readSynthetic(t, path+BackupSuffix); got != "verified" {
		t.Fatalf("backup = %q", got)
	}
	assertNoTemporaryLeftovers(t, dir)
}

func TestWindowsReplaceRefusesOrWaitsOutPreExistingWriter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	writeSynthetic(t, path, "verified")
	external, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	filesystem := New()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	err = filesystem.WriteFileVersion(ctx, path, []byte("new"), true, expectVersion("verified"))
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pre-existing writer not excluded: %v", err)
	}
	if got := readSynthetic(t, path); got != "verified" {
		t.Fatalf("blocked write modified target: %q", got)
	}
	if err := external.Close(); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.WriteFileVersion(context.Background(), path, []byte("new"), true, expectVersion("verified")); err != nil {
		t.Fatal(err)
	}
	if got := readSynthetic(t, path); got != "new" {
		t.Fatalf("content after writer left = %q", got)
	}
	if got := readSynthetic(t, path+BackupSuffix); got != "verified" {
		t.Fatalf("backup = %q", got)
	}
}

func TestWindowsReplaceDetectsRenameReplacementAfterFinalVerify(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	writeSynthetic(t, path, "verified")
	conditionalPreCommitHook = func() {
		replacement := filepath.Join(dir, "replacement.txt")
		writeSynthetic(t, replacement, "external")
		if err := os.Rename(replacement, path); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { conditionalPreCommitHook = nil })
	filesystem := New()
	requireMismatch(t, filesystem.WriteFileVersion(context.Background(), path, []byte("new"), true, expectVersion("verified")))
	if got := readSynthetic(t, path); got != "external" {
		t.Fatalf("rename replacement was clobbered: %q", got)
	}
	if _, err := os.Lstat(path + BackupSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mismatch wrote a backup: %v", err)
	}
	assertNoTemporaryLeftovers(t, dir)
}

func TestWindowsReplaceDetectsDeleteAfterFinalVerify(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	writeSynthetic(t, path, "verified")
	conditionalPreCommitHook = func() {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { conditionalPreCommitHook = nil })
	filesystem := New()
	requireMismatch(t, filesystem.WriteFileVersion(context.Background(), path, []byte("new"), true, expectVersion("verified")))
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("external delete was undone by a commit: %v", err)
	}
	assertNoTemporaryLeftovers(t, dir)
}

func TestWindowsReplaceRejectsSymlinkTargets(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim.txt")
	writeSynthetic(t, victim, "victim")
	path := filepath.Join(dir, "link.txt")
	if err := os.Symlink(victim, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	filesystem := New()
	requireMismatch(t, filesystem.WriteFileVersion(context.Background(), path, []byte("new"), true, expectVersion("victim")))
	if got := readSynthetic(t, victim); got != "victim" {
		t.Fatalf("symlink victim modified: %q", got)
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink itself replaced: %v, %v", info, err)
	}
}

func TestWindowsReplaceCancellation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	writeSynthetic(t, path, "verified")
	filesystem := New()
	for _, test := range []struct {
		name string
		hook *func()
	}{
		{name: "before final verify", hook: &conditionalPreVerifyHook},
		{name: "before commit", hook: &conditionalPreCommitHook},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			*test.hook = cancel
			t.Cleanup(func() { *test.hook = nil })
			if err := filesystem.WriteFileVersion(ctx, path, []byte("new"), true, expectVersion("verified")); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled write = %v", err)
			}
			if got := readSynthetic(t, path); got != "verified" {
				t.Fatalf("cancelled write modified target: %q", got)
			}
			if _, err := os.Lstat(path + BackupSuffix); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("cancelled write produced a backup: %v", err)
			}
			assertNoTemporaryLeftovers(t, dir)
		})
	}
}

func TestWindowsReplaceConcurrentWritersCommitOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	writeSynthetic(t, path, "verified")
	filesystem := New()
	expected := expectVersion("verified")
	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, content := range []string{"writer A", "writer B"} {
		wg.Add(1)
		go func(i int, content string) {
			defer wg.Done()
			<-start
			errs[i] = filesystem.WriteFileVersion(context.Background(), path, []byte(content), false, expected)
		}(i, content)
	}
	close(start)
	wg.Wait()
	var committed, mismatched int
	for _, err := range errs {
		switch {
		case err == nil:
			committed++
		case errors.Is(err, conditional.ErrVersionMismatch):
			mismatched++
		default:
			t.Fatalf("unexpected concurrent result: %v", err)
		}
	}
	if committed != 1 || mismatched != 1 {
		t.Fatalf("concurrent results = %v", errs)
	}
	if got := readSynthetic(t, path); got != "writer A" && got != "writer B" {
		t.Fatalf("final content = %q", got)
	}
	assertNoTemporaryLeftovers(t, dir)
}
