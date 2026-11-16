package ssh

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/fs/conditional"
	"github.com/Hello-CTF/NexTerm/internal/fs/ssh/sftptest"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func shortenWriteStopGrace(t *testing.T) {
	t.Helper()
	previous := writeStopGrace
	writeStopGrace = 50 * time.Millisecond
	t.Cleanup(func() { writeStopGrace = previous })
}

// waitAbandonedWritesDone waits until every write boundWrite stopped waiting for has returned.
func waitAbandonedWritesDone(t *testing.T, filesystem *FS) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		filesystem.abandonedMu.Lock()
		remaining := filesystem.abandonedTotal
		filesystem.abandonedMu.Unlock()
		if remaining == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d abandoned writes still running", remaining)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func requireUnknownOutcome(t *testing.T, name string, err error, reason error) {
	t.Helper()
	var unknown *base.WriteOutcomeUnknownError
	if !errors.As(err, &unknown) || !errors.Is(err, base.ErrWriteOutcomeUnknown) {
		t.Fatalf("%s = %v, want an unknown write outcome", name, err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("%s = %v reads as a cancellation", name, err)
	}
	if !errors.Is(unknown.Reason, reason) {
		t.Fatalf("%s stopped for %v, want %v", name, unknown.Reason, reason)
	}
}

func TestWriteStopsWaitingOnAnUnansweredMutationWithAnUnknownOutcome(t *testing.T) {
	requirePOSIXFileModes(t)
	shortenWriteStopGrace(t)
	for _, test := range []struct {
		name  string
		hang  sftptest.Request
		write func(context.Context, *FS, string) error
		path  string
	}{
		{name: "staging write", hang: sftptest.Write, path: "existing.conf", write: func(ctx context.Context, f *FS, path string) error {
			return f.WriteFile(ctx, path, []byte("new"), false)
		}},
		{name: "replacing rename", hang: sftptest.PosixRename, path: "existing.conf", write: func(ctx context.Context, f *FS, path string) error {
			return f.WriteFile(ctx, path, []byte("new"), false)
		}},
		{name: "creating link", hang: sftptest.Hardlink, path: "created.conf", write: func(ctx context.Context, f *FS, path string) error {
			return f.WriteFileVersion(ctx, path, []byte("new"), false, conditional.Absent())
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			filesystem, root, hanging := newHangingTestFS(t)
			writeLocal(t, filepath.Join(root, "existing.conf"), "original", 0o644)
			target := filepath.Join(root, test.path)
			hanging.Hang(test.hang)

			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			started := time.Now()
			err := test.write(ctx, filesystem, target)
			if elapsed := time.Since(started); elapsed > 3*time.Second {
				t.Fatalf("write returned after %v", elapsed)
			}
			requireUnknownOutcome(t, "write past its deadline", err, context.DeadlineExceeded)

			// The connection still serves other requests, and the path refuses a new write until the abandoned one
			// returns, so its late commit cannot land over newer content.
			hanging.Hang()
			data, err := filesystem.ReadFile(context.Background(), filepath.Join(root, "existing.conf"), 1<<10)
			if err != nil || string(data) != "original" {
				t.Fatalf("read after the abandoned write = %q, %v", data, err)
			}
			if err := filesystem.WriteFile(context.Background(), target, []byte("newer"), false); err == nil || errors.Is(err, base.ErrWriteOutcomeUnknown) {
				t.Fatalf("write over a pending abandoned write = %v, want a refusal", err)
			}
			if err := filesystem.WriteFile(context.Background(), filepath.Join(root, "other.conf"), []byte("other"), false); err != nil {
				t.Fatalf("write to another path = %v", err)
			}
		})
	}
}

func TestCancelledWriteOnAnUnansweredMutationReportsAnUnknownOutcome(t *testing.T) {
	requirePOSIXFileModes(t)
	shortenWriteStopGrace(t)
	filesystem, root, hanging := newHangingTestFS(t)
	target := filepath.Join(root, "existing.conf")
	writeLocal(t, target, "original", 0o644)
	hanging.Hang(sftptest.PosixRename)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelWhenHeld(t, hanging, sftptest.PosixRename, cancel)
	done := make(chan error, 1)
	go func() { done <- filesystem.WriteFile(ctx, target, []byte("new"), false) }()
	select {
	case err := <-done:
		requireUnknownOutcome(t, "cancelled write", err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled write kept waiting on an unanswered rename")
	}
}

func TestWriteStoppedOnAnUnansweredLookupChangesNothing(t *testing.T) {
	requirePOSIXFileModes(t)
	shortenWriteStopGrace(t)
	filesystem, root, hanging := newHangingTestFS(t)
	target := filepath.Join(root, "existing.conf")
	writeLocal(t, target, "original", 0o644)
	hanging.Hang(sftptest.Lstat)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := filesystem.WriteFile(ctx, target, []byte("new"), false)
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, base.ErrWriteOutcomeUnknown) {
		t.Fatalf("write stopped on a lookup = %v, want context.DeadlineExceeded", err)
	}
	hanging.Hang()
	assertLocalFile(t, target, "original", 0o644)
	// Nothing was abandoned mid-write, so the path accepts the next write at once.
	if err := filesystem.WriteFile(context.Background(), target, []byte("new"), false); err != nil {
		t.Fatal(err)
	}
	assertLocalFile(t, target, "new", 0o644)
}

func TestAbandonedWriteSettlesWhenTheServerAnswersLate(t *testing.T) {
	requirePOSIXFileModes(t)
	shortenWriteStopGrace(t)
	for _, test := range []struct {
		name string
		hang sftptest.Request
		want string
	}{
		// A late staging request finds the caller gone and removes what it staged before any commit.
		{name: "staging open", hang: sftptest.Open, want: "original"},
		{name: "staging write", hang: sftptest.Write, want: "original"},
		// A late rename has committed; only the empty staging directory remains to remove.
		{name: "replacing rename", hang: sftptest.PosixRename, want: "new"},
	} {
		t.Run(test.name, func(t *testing.T) {
			filesystem, root, hanging := newHangingTestFS(t)
			target := filepath.Join(root, "existing.conf")
			writeLocal(t, target, "original", 0o644)
			hanging.Hang(test.hang)
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			requireUnknownOutcome(t, "write past its deadline", filesystem.WriteFile(ctx, target, []byte("new"), false), context.DeadlineExceeded)

			hanging.Release()
			waitAbandonedWritesDone(t, filesystem)
			assertLocalFile(t, target, test.want, 0o644)
			assertNoRemoteTemporaryLeftovers(t, root)
			if err := filesystem.WriteFile(context.Background(), target, []byte("after"), false); err != nil {
				t.Fatalf("write once the abandoned write returned = %v", err)
			}
			assertLocalFile(t, target, "after", 0o644)
		})
	}
}

func TestAbandonedWritesPerConnectionAreBounded(t *testing.T) {
	shortenWriteStopGrace(t)
	filesystem, root, hanging := newHangingTestFS(t)
	hanging.Hang(sftptest.Hardlink)
	for index := range maxAbandonedWrites {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		err := filesystem.WriteFileVersion(ctx, filepath.Join(root, "file"+string(rune('a'+index))), []byte("x"), false, conditional.Absent())
		cancel()
		requireUnknownOutcome(t, "abandoned create", err, context.DeadlineExceeded)
	}
	err := filesystem.WriteFileVersion(context.Background(), filepath.Join(root, "one-more"), []byte("x"), false, conditional.Absent())
	if err == nil || errors.Is(err, base.ErrWriteOutcomeUnknown) {
		t.Fatalf("write past the abandoned-write bound = %v, want a refusal", err)
	}
	if _, statErr := os.Lstat(filepath.Join(root, "one-more")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("refused write created the file: %v", statErr)
	}
	hanging.Release()
	waitAbandonedWritesDone(t, filesystem)
	if err := filesystem.WriteFileVersion(context.Background(), filepath.Join(root, "one-more"), []byte("x"), false, conditional.Absent()); err != nil {
		t.Fatalf("write once the abandoned writes returned = %v", err)
	}
}
