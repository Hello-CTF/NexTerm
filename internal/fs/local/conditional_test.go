package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/fs/conditional"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
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

func TestWriteFileVersionCreateRejectsEveryOccupiedPathKind(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim.txt")
	writeSynthetic(t, victim, "victim")
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(victim, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	subdir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subdir, 0o700); err != nil {
		t.Fatal(err)
	}
	filesystem := New()
	for _, path := range []string{victim, link, subdir} {
		requireMismatch(t, filesystem.WriteFileVersion(context.Background(), path, []byte("new"), true, conditional.Absent()))
	}
	if got := readSynthetic(t, victim); got != "victim" {
		t.Fatalf("occupied-path mismatch modified content: %q", got)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink itself replaced: %v, %v", info, err)
	}
	assertNoTemporaryLeftovers(t, dir)
}

func TestWriteFileVersionDetectsExternalCreateAroundFinalCheck(t *testing.T) {
	for _, test := range []struct {
		name string
		hook *func()
	}{
		{name: "before final check", hook: &conditionalPreVerifyHook},
		{name: "after final check before commit", hook: &conditionalPreCommitHook},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "created.txt")
			*test.hook = func() { writeSynthetic(t, path, "external") }
			t.Cleanup(func() { *test.hook = nil })
			filesystem := New()
			requireMismatch(t, filesystem.WriteFileVersion(context.Background(), path, []byte("new"), false, conditional.Absent()))
			if got := readSynthetic(t, path); got != "external" {
				t.Fatalf("external create was clobbered: %q", got)
			}
			assertNoTemporaryLeftovers(t, dir)
		})
	}
}

func TestWriteFileVersionConcurrentCreatesCommitOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "created.txt")
	filesystem := New()
	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, content := range []string{"writer A", "writer B"} {
		wg.Add(1)
		go func(i int, content string) {
			defer wg.Done()
			<-start
			errs[i] = filesystem.WriteFileVersion(context.Background(), path, []byte(content), false, conditional.Absent())
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

func TestWriteFileVersionCancellation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "created.txt")
	filesystem := New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := filesystem.WriteFileVersion(ctx, path, []byte("new"), false, conditional.Absent()); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled write = %v", err)
	}
	for _, test := range []struct {
		name string
		hook *func()
	}{
		{name: "before final check", hook: &conditionalPreVerifyHook},
		{name: "before commit", hook: &conditionalPreCommitHook},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			*test.hook = cancel
			t.Cleanup(func() { *test.hook = nil })
			if err := filesystem.WriteFileVersion(ctx, path, []byte("new"), false, conditional.Absent()); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled write = %v", err)
			}
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("cancelled create produced a target: %v", err)
			}
			assertNoTemporaryLeftovers(t, dir)
		})
	}
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

func TestWriteFileVersionReplaceIsRefusedWhereUnenforceable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	writeSynthetic(t, path, "actual")
	filesystem := New()
	for _, target := range []string{path, filepath.Join(dir, "missing.txt")} {
		err := filesystem.WriteFileVersion(context.Background(), target, []byte("new"), true, expectVersion("actual"))
		if !errors.Is(err, base.ErrUnsupported) {
			t.Fatalf("replacement = %v, want base.ErrUnsupported", err)
		}
		if errors.Is(err, conditional.ErrVersionMismatch) || errors.Is(err, conditional.ErrCommitIndeterminate) {
			t.Fatalf("unsupported must be a distinct outcome: %v", err)
		}
	}
	if got := readSynthetic(t, path); got != "actual" {
		t.Fatalf("refused replacement modified target: %q", got)
	}
	if _, err := os.Lstat(path + BackupSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused replacement wrote a backup: %v", err)
	}
	assertNoTemporaryLeftovers(t, dir)
}
