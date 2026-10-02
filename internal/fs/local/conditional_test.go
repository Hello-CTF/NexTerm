package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/fs/conditional"
)

func writeSynthetic(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readSynthetic(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func expectVersion(content string) conditional.Expectation {
	return conditional.VersionOf([]byte(content)).Expectation()
}

func requireMismatch(t *testing.T, err error) *conditional.MismatchError {
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

func assertNoTemporaryLeftovers(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".nexterm-tmp-") {
			t.Errorf("temporary file left behind: %s", entry.Name())
		}
	}
}

func TestWriteFileVersionCommitsWithBackupAndMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	writeSynthetic(t, path, "old content")
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
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
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o640 {
			t.Fatalf("mode = %v, %v", info.Mode(), err)
		}
	}
	if err := filesystem.WriteFileVersion(context.Background(), path, []byte("third"), false, expectVersion("new content")); err != nil {
		t.Fatal(err)
	}
	if got := readSynthetic(t, path); got != "third" {
		t.Fatalf("content = %q", got)
	}
	if got := readSynthetic(t, path+BackupSuffix); got != "old content" {
		t.Fatalf("backup rewritten without backup flag: %q", got)
	}
	assertNoTemporaryLeftovers(t, dir)
}

func TestWriteFileVersionCreatesOnlyWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "created.txt")
	filesystem := New()
	if err := filesystem.WriteFileVersion(context.Background(), path, []byte("fresh"), true, conditional.Absent()); err != nil {
		t.Fatal(err)
	}
	if got := readSynthetic(t, path); got != "fresh" {
		t.Fatalf("content = %q", got)
	}
	if _, err := os.Lstat(path + BackupSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("conditional create wrote a backup: %v", err)
	}
	mismatch := requireMismatch(t, filesystem.WriteFileVersion(context.Background(), path, []byte("clobber"), false, conditional.Absent()))
	if mismatch.Reason != "file already exists" {
		t.Fatalf("reason = %q", mismatch.Reason)
	}
	if got := readSynthetic(t, path); got != "fresh" {
		t.Fatalf("conditional create overwrote an existing file: %q", got)
	}
	assertNoTemporaryLeftovers(t, dir)
}

func TestWriteFileVersionRejectsStaleExpectations(t *testing.T) {
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
		{name: "absent expected", expected: conditional.Absent()},
	} {
		mismatch := requireMismatch(t, filesystem.WriteFileVersion(context.Background(), path, []byte("new"), true, test.expected))
		t.Logf("%s: %v", test.name, mismatch)
		if got := readSynthetic(t, path); got != "actual" {
			t.Fatalf("%s: target modified on mismatch: %q", test.name, got)
		}
	}
	missingPath := filepath.Join(dir, "missing.txt")
	mismatch := requireMismatch(t, filesystem.WriteFileVersion(context.Background(), missingPath, []byte("new"), false, expectVersion("actual")))
	if mismatch.Actual.Exists {
		t.Fatalf("missing target reported as present: %+v", mismatch.Actual)
	}
	if _, err := os.Lstat(missingPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mismatch created the target: %v", err)
	}
	if _, err := os.Lstat(path + BackupSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mismatch wrote a backup: %v", err)
	}
	assertNoTemporaryLeftovers(t, dir)
}

func TestWriteFileVersionRejectsInvalidExpectations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	writeSynthetic(t, path, "actual")
	filesystem := New()
	err := filesystem.WriteFileVersion(context.Background(), path, []byte("new"), false, conditional.Expectation{Exists: true, Size: 6, SHA256: "not-a-digest"})
	if err == nil || errors.Is(err, conditional.ErrVersionMismatch) {
		t.Fatalf("invalid expectation = %v", err)
	}
	if got := readSynthetic(t, path); got != "actual" {
		t.Fatalf("invalid expectation modified target: %q", got)
	}
}

func TestWriteFileVersionDetectsExternalWriteBeforeCommit(t *testing.T) {
	for _, test := range []struct {
		name    string
		mutate  func(t *testing.T, path string)
		content string
	}{
		{name: "in place write", mutate: func(t *testing.T, path string) { writeSynthetic(t, path, "external") }, content: "external"},
		{name: "delete", mutate: func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "rename replacement", mutate: func(t *testing.T, path string) {
			replacement := path + ".replacement"
			writeSynthetic(t, replacement, "external")
			if err := os.Rename(replacement, path); err != nil {
				t.Fatal(err)
			}
		}, content: "external"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "file.txt")
			writeSynthetic(t, path, "verified")
			conditionalTestHook = func() { test.mutate(t, path) }
			t.Cleanup(func() { conditionalTestHook = nil })
			filesystem := New()
			requireMismatch(t, filesystem.WriteFileVersion(context.Background(), path, []byte("new"), true, expectVersion("verified")))
			if test.content != "" {
				if got := readSynthetic(t, path); got != test.content {
					t.Fatalf("external content was clobbered: %q", got)
				}
			}
			if _, err := os.Lstat(path + BackupSuffix); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("mismatch wrote a backup: %v", err)
			}
			assertNoTemporaryLeftovers(t, dir)
		})
	}
}

func TestWriteFileVersionDetectsExternalCreateBeforeCommit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "created.txt")
	conditionalTestHook = func() { writeSynthetic(t, path, "external") }
	t.Cleanup(func() { conditionalTestHook = nil })
	filesystem := New()
	requireMismatch(t, filesystem.WriteFileVersion(context.Background(), path, []byte("new"), false, conditional.Absent()))
	if got := readSynthetic(t, path); got != "external" {
		t.Fatalf("external create was clobbered: %q", got)
	}
	assertNoTemporaryLeftovers(t, dir)
}

func TestWriteFileVersionRejectsSymlinkTargets(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim.txt")
	writeSynthetic(t, victim, "victim")
	path := filepath.Join(dir, "link.txt")
	if err := os.Symlink(victim, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	filesystem := New()
	requireMismatch(t, filesystem.WriteFileVersion(context.Background(), path, []byte("new"), true, expectVersion("victim")))
	requireMismatch(t, filesystem.WriteFileVersion(context.Background(), path, []byte("new"), false, conditional.Absent()))
	if got := readSynthetic(t, victim); got != "victim" {
		t.Fatalf("symlink victim modified: %q", got)
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink itself replaced: %v, %v", info, err)
	}
	if _, err := os.Lstat(victim + BackupSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlink write produced a victim backup: %v", err)
	}
}

func TestWriteFileVersionRejectsSymlinkSwapBeforeCommit(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim.txt")
	writeSynthetic(t, victim, "victim")
	path := filepath.Join(dir, "file.txt")
	writeSynthetic(t, path, "verified")
	conditionalTestHook = func() {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, path); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { conditionalTestHook = nil })
	filesystem := New()
	requireMismatch(t, filesystem.WriteFileVersion(context.Background(), path, []byte("new"), true, expectVersion("verified")))
	if got := readSynthetic(t, victim); got != "victim" {
		t.Fatalf("symlink swap victim modified: %q", got)
	}
	assertNoTemporaryLeftovers(t, dir)
}

func TestWriteFileVersionCancellation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	writeSynthetic(t, path, "verified")
	filesystem := New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := filesystem.WriteFileVersion(ctx, path, []byte("new"), true, expectVersion("verified")); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled write = %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	conditionalTestHook = cancel
	t.Cleanup(func() { conditionalTestHook = nil })
	if err := filesystem.WriteFileVersion(ctx, path, []byte("new"), true, expectVersion("verified")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel before commit = %v", err)
	}
	if got := readSynthetic(t, path); got != "verified" {
		t.Fatalf("cancelled write modified target: %q", got)
	}
	if _, err := os.Lstat(path + BackupSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled write produced a backup: %v", err)
	}
	assertNoTemporaryLeftovers(t, dir)
}

func TestWriteFileVersionConcurrentWritersCommitOnce(t *testing.T) {
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
