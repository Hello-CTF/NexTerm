package ssh

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
	"github.com/pkg/sftp"
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

func TestWriteFileVersionCreateRejectsEveryOccupiedPathKind(t *testing.T) {
	filesystem, root := newTestFS(t, nil)
	ctx := context.Background()
	if err := filesystem.WriteFile(ctx, "plain.txt", []byte("plain"), false); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.WriteFile(ctx, "unreadable.txt", []byte("secret"), false); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.Chmod(ctx, "unreadable.txt", 0); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.Mkdir(ctx, "subdir"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "plain.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	for _, target := range []string{"plain.txt", "unreadable.txt", "subdir", "link.txt"} {
		t.Run(target, func(t *testing.T) {
			mismatch := requireRemoteMismatch(t, filesystem.WriteFileVersion(ctx, target, []byte("new"), true, conditional.Absent()))
			if mismatch.Reason != "file already exists" {
				t.Fatalf("reason = %q", mismatch.Reason)
			}
		})
	}
	assertRemoteContent(t, filesystem, "plain.txt", "plain")
	if info, err := os.Lstat(filepath.Join(root, "link.txt")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink itself replaced: %v, %v", info, err)
	}
	assertNoRemoteTemporaryLeftovers(t, root)
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
			filesystem, root := newTestFS(t, nil)
			*test.hook = func() {
				if err := filesystem.WriteFile(context.Background(), "created.txt", []byte("external"), false); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() { *test.hook = nil })
			requireRemoteMismatch(t, filesystem.WriteFileVersion(context.Background(), "created.txt", []byte("new"), false, conditional.Absent()))
			assertRemoteContent(t, filesystem, "created.txt", "external")
			assertNoRemoteTemporaryLeftovers(t, root)
		})
	}
}

func TestWriteFileVersionConcurrentCreatesCommitOnce(t *testing.T) {
	filesystem, root := newTestFS(t, nil)
	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, content := range []string{"writer A", "writer B"} {
		wg.Add(1)
		go func(i int, content string) {
			defer wg.Done()
			<-start
			errs[i] = filesystem.WriteFileVersion(context.Background(), "created.txt", []byte(content), false, conditional.Absent())
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
	assertNoRemoteTemporaryLeftovers(t, root)
}

func stubConditionalLink(t *testing.T, stub func(client *sftp.Client, oldname, newname string) error) {
	t.Helper()
	original := conditionalLink
	conditionalLink = stub
	t.Cleanup(func() { conditionalLink = original })
}

func TestWriteFileVersionCreateCommitFaultOutcomes(t *testing.T) {
	t.Run("commit then lost response is indeterminate", func(t *testing.T) {
		filesystem, root := newTestFS(t, nil)
		var calls int
		stubConditionalLink(t, func(client *sftp.Client, oldname, newname string) error {
			calls++
			if err := client.Link(oldname, newname); err != nil {
				return err
			}
			return errors.New("synthetic response loss after commit")
		})
		err := filesystem.WriteFileVersion(context.Background(), "created.txt", []byte("new"), false, conditional.Absent())
		var indeterminate *conditional.IndeterminateError
		if !errors.As(err, &indeterminate) || !errors.Is(err, conditional.ErrCommitIndeterminate) {
			t.Fatalf("lost response = %v, want IndeterminateError", err)
		}
		if errors.Is(err, conditional.ErrVersionMismatch) {
			t.Fatalf("indeterminate must not look like a mismatch: %v", err)
		}
		if indeterminate.New != conditional.VersionOf([]byte("new")) || indeterminate.Expected != conditional.Absent() {
			t.Fatalf("reconciliation details = %+v", indeterminate)
		}
		if calls != 1 {
			t.Fatalf("ambiguous commit was retried: %d link calls", calls)
		}
		assertRemoteContent(t, filesystem, "created.txt", "new")
		assertNoRemoteTemporaryLeftovers(t, root)
	})

	t.Run("transport failure with absent target is determinate and uncommitted", func(t *testing.T) {
		filesystem, root := newTestFS(t, nil)
		stubConditionalLink(t, func(client *sftp.Client, oldname, newname string) error {
			return errors.New("synthetic transport drop")
		})
		err := filesystem.WriteFileVersion(context.Background(), "created.txt", []byte("new"), false, conditional.Absent())
		if err == nil || errors.Is(err, conditional.ErrCommitIndeterminate) || errors.Is(err, conditional.ErrVersionMismatch) {
			t.Fatalf("transport drop with absent target = %v, want plain determinate error", err)
		}
		if exists, statErr := filesystem.Exists(context.Background(), "created.txt"); statErr != nil || exists {
			t.Fatalf("target = %v, %v", exists, statErr)
		}
		assertNoRemoteTemporaryLeftovers(t, root)
	})

	t.Run("server status failure is determinate and uncommitted", func(t *testing.T) {
		filesystem, root := newTestFS(t, nil)
		stubConditionalLink(t, func(client *sftp.Client, oldname, newname string) error {
			return client.Link("definitely-missing-temp", newname)
		})
		err := filesystem.WriteFileVersion(context.Background(), "created.txt", []byte("new"), false, conditional.Absent())
		if err == nil || errors.Is(err, conditional.ErrCommitIndeterminate) || errors.Is(err, conditional.ErrVersionMismatch) {
			t.Fatalf("server status failure = %v, want plain determinate error", err)
		}
		if exists, statErr := filesystem.Exists(context.Background(), "created.txt"); statErr != nil || exists {
			t.Fatalf("target = %v, %v", exists, statErr)
		}
		assertNoRemoteTemporaryLeftovers(t, root)
	})
}

func TestWriteFileVersionReplaceIsRefusedWhereUnenforceable(t *testing.T) {
	filesystem, root := newTestFS(t, nil)
	ctx := context.Background()
	if err := filesystem.WriteFile(ctx, "file.txt", []byte("actual"), false); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"file.txt", "missing.txt"} {
		err := filesystem.WriteFileVersion(ctx, target, []byte("new"), true, expectRemoteVersion("actual"))
		if !errors.Is(err, base.ErrUnsupported) {
			t.Fatalf("SFTP replacement = %v, want base.ErrUnsupported", err)
		}
		if errors.Is(err, conditional.ErrVersionMismatch) || errors.Is(err, conditional.ErrCommitIndeterminate) {
			t.Fatalf("unsupported must be a distinct outcome: %v", err)
		}
	}
	assertRemoteContent(t, filesystem, "file.txt", "actual")
	if exists, err := filesystem.Exists(ctx, "file.txt.nexterm-bak"); err != nil || exists {
		t.Fatalf("refused replacement wrote a backup: %v, %v", exists, err)
	}
	assertNoRemoteTemporaryLeftovers(t, root)
}

func TestWriteFileVersionCancellation(t *testing.T) {
	filesystem, root := newTestFS(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := filesystem.WriteFileVersion(ctx, "created.txt", []byte("new"), false, conditional.Absent()); !errors.Is(err, context.Canceled) {
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
			if err := filesystem.WriteFileVersion(ctx, "created.txt", []byte("new"), false, conditional.Absent()); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled write = %v", err)
			}
			if exists, err := filesystem.Exists(context.Background(), "created.txt"); err != nil || exists {
				t.Fatalf("cancelled create produced a target: %v, %v", exists, err)
			}
			assertNoRemoteTemporaryLeftovers(t, root)
		})
	}
}

func TestWriteFileVersionRejectsInvalidExpectations(t *testing.T) {
	filesystem, _ := newTestFS(t, nil)
	err := filesystem.WriteFileVersion(context.Background(), "file.txt", []byte("new"), false, conditional.Expectation{Exists: true, Size: 6, SHA256: "not-a-digest"})
	if err == nil || errors.Is(err, conditional.ErrVersionMismatch) {
		t.Fatalf("invalid expectation = %v", err)
	}
}
