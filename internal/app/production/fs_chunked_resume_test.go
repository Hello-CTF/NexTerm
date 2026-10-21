package production

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/fs/chunked"
	fslocal "github.com/Hello-CTF/NexTerm/internal/fs/local"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func beginResumeUploadForTest(t *testing.T, uploads *fsChunkUploadRegistry, transfers *fsTransferRegistry, ctx context.Context, sessionID, taskID, attemptID, remote string, total int64, chunkSize int) fsChunkUploadBeginDTO {
	t.Helper()
	result, err := uploads.begin(ctx, transfers, fsChunkUploadRequest{
		SessionID: sessionID, RemotePath: remote, TaskID: taskID, AttemptID: attemptID, TotalSize: total, ChunkSize: chunkSize,
	})
	if err != nil {
		t.Fatalf("begin %s/%s: %v", taskID, attemptID, err)
	}
	return result
}

func writeResumeChunkForTest(t *testing.T, entry *fsChunkUploadEntry, filesystem base.FileSystem, input fsChunkUploadRequest, index int64, data string) int64 {
	t.Helper()
	request := chunkIPCRequest(input, index, data)
	payload, err := base64.StdEncoding.DecodeString(request.ContentBase64)
	if err != nil {
		t.Fatal(err)
	}
	received, err := entry.upload.WriteChunk(entry.transferCtx, filesystem, chunked.Chunk{
		Index: request.Index, Size: request.Size, SHA256: request.SHA256, Data: payload,
	})
	if err != nil {
		t.Fatalf("chunk %d: %v", index, err)
	}
	return received
}

func stagingGroupFiles(t *testing.T, remote string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(remote), "."+filepath.Base(remote)+".nexterm-*"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func TestFSChunkedBeginResumesStagedUploadAfterRestart(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.bin")
	filesystem := fslocal.New()
	ctx := ipc.WithUserID(t.Context(), "owner-a")

	crashed, crashedTransfers := newUploadRegistryForTest(filesystem)
	first := beginResumeUploadForTest(t, crashed, crashedTransfers, ctx, "session-a", "task-1", "attempt-1", remote, 8, 4)
	crashedInput := fsChunkUploadRequest{SessionID: "session-a", UploadID: first.UploadID, TaskID: "task-1", AttemptID: "attempt-1"}
	crashedEntry, err := crashed.lookup(ctx, crashedInput)
	if err != nil {
		t.Fatal(err)
	}
	writeResumeChunkForTest(t, crashedEntry, filesystem, crashedInput, 0, "abcd")
	// The process dies here: no Close, no Abort, staging stays on the destination.

	restarted, restartedTransfers := newUploadRegistryForTest(filesystem)
	second := beginResumeUploadForTest(t, restarted, restartedTransfers, ctx, "session-b", "task-2", "attempt-2", remote, 8, 4)
	if second.UploadID != first.UploadID {
		t.Fatalf("restarted begin = %q, want resumed %q", second.UploadID, first.UploadID)
	}
	files := stagingGroupFiles(t, remote)
	if len(files) != 3 || !strings.Contains(files[0], first.UploadID) || !strings.Contains(files[1], first.UploadID) || !strings.Contains(files[2], first.UploadID) {
		t.Fatalf("duplicate staging groups after resume: %v", files)
	}

	resumedInput := fsChunkUploadRequest{SessionID: "session-b", UploadID: second.UploadID, TaskID: "task-2", AttemptID: "attempt-2"}
	resumedEntry, err := restarted.lookup(ctx, resumedInput)
	if err != nil {
		t.Fatal(err)
	}
	if received := writeResumeChunkForTest(t, resumedEntry, filesystem, resumedInput, 1, "efgh"); received != 8 {
		t.Fatalf("received after resumed chunk = %d, want 8 (4 staged + 4 new)", received)
	}
	committed, err := resumedEntry.upload.Commit(context.Background(), filesystem)
	if err != nil || committed != 8 {
		t.Fatalf("Commit = %d, %v", committed, err)
	}
	data, err := os.ReadFile(remote)
	if err != nil || string(data) != "abcdefgh" {
		t.Fatalf("committed target = %q, %v", data, err)
	}
	assertNoChunkIArtifacts(t, remote)
	if err := restarted.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestFSChunkedBeginDoesNotResumeMismatchedParams(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.bin")
	filesystem := fslocal.New()
	ctx := ipc.WithUserID(t.Context(), "owner-a")

	crashed, crashedTransfers := newUploadRegistryForTest(filesystem)
	first := beginResumeUploadForTest(t, crashed, crashedTransfers, ctx, "session-a", "task-1", "attempt-1", remote, 8, 4)
	crashedInput := fsChunkUploadRequest{SessionID: "session-a", UploadID: first.UploadID, TaskID: "task-1", AttemptID: "attempt-1"}
	crashedEntry, err := crashed.lookup(ctx, crashedInput)
	if err != nil {
		t.Fatal(err)
	}
	writeResumeChunkForTest(t, crashedEntry, filesystem, crashedInput, 0, "abcd")

	restarted, restartedTransfers := newUploadRegistryForTest(filesystem)
	for index, mismatch := range []struct {
		total     int64
		chunkSize int
	}{
		{total: 12, chunkSize: 4},
		{total: 8, chunkSize: 8},
	} {
		attempt := "attempt-2-" + string(rune('a'+index))
		result := beginResumeUploadForTest(t, restarted, restartedTransfers, ctx, "session-a", "task-2", attempt, remote, mismatch.total, mismatch.chunkSize)
		if result.UploadID == first.UploadID {
			t.Fatalf("params %+v resumed the crashed upload %q", mismatch, first.UploadID)
		}
	}
	files := stagingGroupFiles(t, remote)
	if len(files) != 3 {
		t.Fatalf("mismatched begins disturbed the crashed staging: %v", files)
	}
	if err := restarted.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestFSChunkedBeginAfterReapCreatesNewID(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.bin")
	filesystem := fslocal.New()
	ctx := ipc.WithUserID(t.Context(), "owner-a")

	crashed, crashedTransfers := newUploadRegistryForTest(filesystem)
	first := beginResumeUploadForTest(t, crashed, crashedTransfers, ctx, "session-a", "task-1", "attempt-1", remote, 8, 4)
	crashedInput := fsChunkUploadRequest{SessionID: "session-a", UploadID: first.UploadID, TaskID: "task-1", AttemptID: "attempt-1"}
	crashedEntry, err := crashed.lookup(ctx, crashedInput)
	if err != nil {
		t.Fatal(err)
	}
	writeResumeChunkForTest(t, crashedEntry, filesystem, crashedInput, 0, "abcd")
	if _, err := chunked.ReapStaleStaging(context.Background(), filesystem, filepath.Dir(remote), 0); err != nil {
		t.Fatalf("ReapStaleStaging: %v", err)
	}

	restarted, restartedTransfers := newUploadRegistryForTest(filesystem)
	second := beginResumeUploadForTest(t, restarted, restartedTransfers, ctx, "session-a", "task-2", "attempt-2", remote, 8, 4)
	if second.UploadID == first.UploadID {
		t.Fatal("reaped staging was resumed")
	}
	if err := restarted.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestFSChunkedBeginConcurrentResumeKeepsSingleID(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.bin")
	filesystem := fslocal.New()
	ctx := ipc.WithUserID(t.Context(), "owner-a")

	crashed, crashedTransfers := newUploadRegistryForTest(filesystem)
	first := beginResumeUploadForTest(t, crashed, crashedTransfers, ctx, "session-a", "task-1", "attempt-1", remote, 8, 4)
	crashedInput := fsChunkUploadRequest{SessionID: "session-a", UploadID: first.UploadID, TaskID: "task-1", AttemptID: "attempt-1"}
	crashedEntry, err := crashed.lookup(ctx, crashedInput)
	if err != nil {
		t.Fatal(err)
	}
	writeResumeChunkForTest(t, crashedEntry, filesystem, crashedInput, 0, "abcd")

	restarted, restartedTransfers := newUploadRegistryForTest(filesystem)
	const racers = 8
	ids := make([]string, racers)
	errs := make([]error, racers)
	var wg sync.WaitGroup
	for index := 0; index < racers; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			result, err := restarted.begin(ctx, restartedTransfers, fsChunkUploadRequest{
				SessionID: "session-b", RemotePath: remote, TaskID: "task-2", AttemptID: "attempt-" + string(rune('a'+index)), TotalSize: 8, ChunkSize: 4,
			})
			if err != nil {
				errs[index] = err
				return
			}
			ids[index] = result.UploadID
		}(index)
	}
	wg.Wait()
	for index := 0; index < racers; index++ {
		if errs[index] != nil {
			t.Fatalf("racer %d: %v", index, errs[index])
		}
		if ids[index] != first.UploadID {
			t.Fatalf("racer %d got duplicate ID %q, want %q", index, ids[index], first.UploadID)
		}
	}
	restarted.mu.Lock()
	entries := len(restarted.entries)
	restarted.mu.Unlock()
	if entries != 1 {
		t.Fatalf("concurrent resume registered %d entries, want 1", entries)
	}
	if files := stagingGroupFiles(t, remote); len(files) != 3 {
		t.Fatalf("concurrent resume duplicated staging: %v", files)
	}
	if err := restarted.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestFSChunkedBeginSameAttemptIsIdempotent(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.bin")
	uploads, transfers := newUploadRegistryForTest(fslocal.New())
	ctx := ipc.WithUserID(t.Context(), "owner-a")
	input := fsChunkUploadRequest{SessionID: "session-a", RemotePath: remote, TaskID: "task-1", AttemptID: "attempt-1", TotalSize: 8, ChunkSize: 4}

	first, err := uploads.begin(ctx, transfers, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := uploads.begin(ctx, transfers, input)
	if err != nil {
		t.Fatalf("idempotent begin: %v", err)
	}
	if second.UploadID != first.UploadID {
		t.Fatalf("idempotent begin = %q, want %q", second.UploadID, first.UploadID)
	}
	if len(uploads.entries) != 1 || len(transfers.attempts) != 1 {
		t.Fatalf("idempotent begin registered uploads=%d attempts=%d", len(uploads.entries), len(transfers.attempts))
	}
	if err := uploads.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestFSChunkedBeginNewAttemptRebindsEntry(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.bin")
	filesystem := fslocal.New()
	uploads, transfers := newUploadRegistryForTest(filesystem)
	ctx := ipc.WithUserID(t.Context(), "owner-a")

	first := beginResumeUploadForTest(t, uploads, transfers, ctx, "session-a", "task-1", "attempt-1", remote, 8, 4)
	oldInput := fsChunkUploadRequest{SessionID: "session-a", UploadID: first.UploadID, TaskID: "task-1", AttemptID: "attempt-1"}
	oldEntry, err := uploads.lookup(ctx, oldInput)
	if err != nil {
		t.Fatal(err)
	}
	writeResumeChunkForTest(t, oldEntry, filesystem, oldInput, 0, "abcd")

	second := beginResumeUploadForTest(t, uploads, transfers, ctx, "session-a", "task-2", "attempt-2", remote, 8, 4)
	if second.UploadID != first.UploadID {
		t.Fatalf("rebound begin = %q, want %q", second.UploadID, first.UploadID)
	}
	if _, err := uploads.lookup(ctx, oldInput); err == nil {
		t.Fatal("superseded attempt still resolves the entry")
	}
	if !errors.Is(oldEntry.transferCtx.Err(), context.Canceled) {
		t.Fatalf("superseded transfer context = %v", oldEntry.transferCtx.Err())
	}

	newInput := fsChunkUploadRequest{SessionID: "session-a", UploadID: second.UploadID, TaskID: "task-2", AttemptID: "attempt-2"}
	newEntry, err := uploads.lookup(ctx, newInput)
	if err != nil {
		t.Fatal(err)
	}
	if received := writeResumeChunkForTest(t, newEntry, filesystem, newInput, 1, "efgh"); received != 8 {
		t.Fatalf("received after rebound chunk = %d, want 8", received)
	}
	committed, err := newEntry.upload.Commit(context.Background(), filesystem)
	if err != nil || committed != 8 {
		t.Fatalf("Commit = %d, %v", committed, err)
	}
	data, err := os.ReadFile(remote)
	if err != nil || string(data) != "abcdefgh" {
		t.Fatalf("committed target = %q, %v", data, err)
	}
	if err := uploads.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestFSChunkedBeginDoesNotHijackLiveEntryStaging(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.bin")
	filesystem := fslocal.New()
	uploads, transfers := newUploadRegistryForTest(filesystem)
	ctx := ipc.WithUserID(t.Context(), "owner-a")

	first := beginResumeUploadForTest(t, uploads, transfers, ctx, "session-a", "task-1", "attempt-1", remote, 8, 4)
	activeInput := fsChunkUploadRequest{SessionID: "session-a", UploadID: first.UploadID, TaskID: "task-1", AttemptID: "attempt-1"}
	activeEntry, err := uploads.lookup(ctx, activeInput)
	if err != nil {
		t.Fatal(err)
	}
	writeResumeChunkForTest(t, activeEntry, filesystem, activeInput, 0, "abcd")

	second := beginResumeUploadForTest(t, uploads, transfers, ctx, "session-b", "task-2", "attempt-2", remote, 8, 4)
	if second.UploadID == first.UploadID {
		t.Fatal("parallel begin hijacked the live entry's staging")
	}
	if len(uploads.entries) != 2 {
		t.Fatalf("parallel begin left %d entries, want 2", len(uploads.entries))
	}
	if received := writeResumeChunkForTest(t, activeEntry, filesystem, activeInput, 1, "efgh"); received != 8 {
		t.Fatalf("live entry received = %d, want 8", received)
	}
	if err := uploads.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestFSChunkedRegistryStagingTargets(t *testing.T) {
	root := t.TempDir()
	filesystem := fslocal.New()
	transfers := newFSTransferRegistry()
	uploads := newFSChunkUploadRegistry()
	uploads.transfers = transfers
	uploads.filesystem = func(_ context.Context, sessionID string) (base.FileSystem, string, error) {
		if sessionID == "session-broken" {
			return nil, "", errors.New("session gone")
		}
		return filesystem, "ssh", nil
	}
	ctx := ipc.WithUserID(t.Context(), "owner-a")
	dirA := filepath.Join(root, "a")
	dirNested := filepath.Join(root, "a", "nested")
	dirB := filepath.Join(root, "b")
	for _, begin := range []struct {
		sessionID string
		remote    string
	}{
		{sessionID: "session-a", remote: filepath.Join(dirA, "one.bin")},
		{sessionID: "session-a", remote: filepath.Join(dirNested, "two.bin")},
		{sessionID: "session-b", remote: filepath.Join(dirB, "three.bin")},
		{sessionID: "session-broken", remote: filepath.Join(root, "gone.bin")},
	} {
		beginResumeUploadForTest(t, uploads, transfers, ctx, begin.sessionID, "task-"+begin.sessionID+begin.remote, "attempt", begin.remote, 4, 4)
	}

	targets, err := uploads.stagingTargets(context.Background())
	if err != nil {
		t.Fatalf("stagingTargets: %v", err)
	}
	want := []string{dirA, dirNested, dirB}
	if len(targets) != len(want) {
		t.Fatalf("targets = %+v, want dirs %v", targets, want)
	}
	for index, dir := range want {
		if targets[index].Dir != dir || targets[index].FileSystem != base.FileSystem(filesystem) {
			t.Fatalf("target %d = %+v, want %v", index, targets[index], dir)
		}
	}

	empty, err := newFSChunkUploadRegistry().stagingTargets(context.Background())
	if err != nil || len(empty) != 0 {
		t.Fatalf("nil-resolver stagingTargets = %+v, %v", empty, err)
	}
	// Closing surfaces the broken session's cleanup failure instead of hiding it.
	if err := uploads.Close(context.Background()); err == nil || !strings.Contains(err.Error(), "session gone") {
		t.Fatalf("Close error = %v, want the broken session cleanup failure", err)
	}
}
