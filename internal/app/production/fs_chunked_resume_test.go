package production

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/fs/chunked"
	fslocal "github.com/Hello-CTF/NexTerm/internal/fs/local"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
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
	// Every staged chunk is re-presented and verified before commit;
	// matching chunks deduplicate without a rewrite.
	if received := writeResumeChunkForTest(t, resumedEntry, filesystem, resumedInput, 0, "abcd"); received != 4 {
		t.Fatalf("re-presented chunk received = %d, want 4", received)
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
	writeResumeChunkForTest(t, newEntry, filesystem, newInput, 0, "abcd")
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
	if err == nil || !strings.Contains(err.Error(), "session gone") {
		t.Fatalf("stagingTargets error = %v, want the broken session failure aggregated", err)
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

// Resume-by-path is fail-closed on divergent content for the supported client
// flow: the begin DTO carries no staged-chunk list, so clients re-send every
// chunk, and a staged chunk that does not match the re-sent content surfaces
// ErrChunkConflict instead of silently composing mixed content. Aborting then
// clears the foreign staging so a fresh begin can commit the new content.
func TestFSChunkedResumeFailsClosedOnDivergentContent(t *testing.T) {
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
	writeResumeChunkForTest(t, crashedEntry, filesystem, crashedInput, 1, "efgh")

	restarted, restartedTransfers := newUploadRegistryForTest(filesystem)
	second := beginResumeUploadForTest(t, restarted, restartedTransfers, ctx, "session-b", "task-2", "attempt-2", remote, 8, 4)
	if second.UploadID != first.UploadID {
		t.Fatalf("resumed begin = %q, want crashed upload %q", second.UploadID, first.UploadID)
	}

	resumedInput := fsChunkUploadRequest{SessionID: "session-b", UploadID: second.UploadID, TaskID: "task-2", AttemptID: "attempt-2"}
	resumedEntry, err := restarted.lookup(ctx, resumedInput)
	if err != nil {
		t.Fatal(err)
	}
	// The new content shares chunk 0 but diverges at chunk 1: re-sending every
	// chunk must fail closed at the divergent one.
	if received := writeResumeChunkForTest(t, resumedEntry, filesystem, resumedInput, 0, "abcd"); received != 8 {
		t.Fatalf("matching chunk received = %d, want 8 (both crashed chunks adopted)", received)
	}
	request := chunkIPCRequest(resumedInput, 1, "WXYZ")
	payload, err := base64.StdEncoding.DecodeString(request.ContentBase64)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resumedEntry.upload.WriteChunk(resumedEntry.transferCtx, filesystem, chunked.Chunk{
		Index: request.Index, Size: request.Size, SHA256: request.SHA256, Data: payload,
	}); !errors.Is(err, chunked.ErrChunkConflict) {
		t.Fatalf("divergent chunk = %v, want ErrChunkConflict", err)
	}

	if err := resumedEntry.upload.Abort(context.Background(), filesystem); err != nil {
		t.Fatal(err)
	}
	// Mirror the abort IPC handler: the entry is finished once staging is gone.
	resumedEntry.finish()
	assertNoChunkIArtifacts(t, remote)
	third := beginResumeUploadForTest(t, restarted, restartedTransfers, ctx, "session-b", "task-3", "attempt-3", remote, 8, 4)
	if third.UploadID == first.UploadID {
		t.Fatal("aborted staging was resumed again")
	}
	freshInput := fsChunkUploadRequest{SessionID: "session-b", UploadID: third.UploadID, TaskID: "task-3", AttemptID: "attempt-3"}
	freshEntry, err := restarted.lookup(ctx, freshInput)
	if err != nil {
		t.Fatal(err)
	}
	writeResumeChunkForTest(t, freshEntry, filesystem, freshInput, 0, "abcd")
	writeResumeChunkForTest(t, freshEntry, filesystem, freshInput, 1, "WXYZ")
	committed, err := freshEntry.upload.Commit(context.Background(), filesystem)
	if err != nil || committed != 8 {
		t.Fatalf("Commit = %d, %v", committed, err)
	}
	data, err := os.ReadFile(remote)
	if err != nil || string(data) != "abcdWXYZ" {
		t.Fatalf("committed target = %q, %v", data, err)
	}
	if err := restarted.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// P1-1 counterexample: a caller that only sends the chunks it believes are
// missing must never commit a mix of crashed and new content.
func TestFSChunkedResumePartialSendCannotCommitMixedContent(t *testing.T) {
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
	second := beginResumeUploadForTest(t, restarted, restartedTransfers, ctx, "session-b", "task-2", "attempt-2", remote, 8, 4)
	if second.UploadID != first.UploadID {
		t.Fatalf("resumed begin = %q, want crashed upload %q", second.UploadID, first.UploadID)
	}
	resumedInput := fsChunkUploadRequest{SessionID: "session-b", UploadID: second.UploadID, TaskID: "task-2", AttemptID: "attempt-2"}
	resumedEntry, err := restarted.lookup(ctx, resumedInput)
	if err != nil {
		t.Fatal(err)
	}
	// New content: chunk 0 happens to match, chunk 1 diverges. The caller only
	// sends the chunk it believes is missing.
	writeResumeChunkForTest(t, resumedEntry, filesystem, resumedInput, 1, "WXYZ")
	if _, err := resumedEntry.upload.Commit(context.Background(), filesystem); err == nil {
		t.Fatal("mixed content committed from unconfirmed staged chunks")
	}
	if _, err := os.Stat(remote); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed mixed commit exposed a target: %v", err)
	}
	if err := restarted.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// P1-1 counterexample: a fully staged crashed group must not be committable
// by a begin that never presented the content.
func TestFSChunkedResumeDirectCommitAfterAdoptionFails(t *testing.T) {
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
	writeResumeChunkForTest(t, crashedEntry, filesystem, crashedInput, 1, "efgh")

	restarted, restartedTransfers := newUploadRegistryForTest(filesystem)
	second := beginResumeUploadForTest(t, restarted, restartedTransfers, ctx, "session-b", "task-2", "attempt-2", remote, 8, 4)
	if second.UploadID != first.UploadID {
		t.Fatalf("resumed begin = %q, want crashed upload %q", second.UploadID, first.UploadID)
	}
	resumedInput := fsChunkUploadRequest{SessionID: "session-b", UploadID: second.UploadID, TaskID: "task-2", AttemptID: "attempt-2"}
	resumedEntry, err := restarted.lookup(ctx, resumedInput)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resumedEntry.upload.Commit(context.Background(), filesystem); err == nil {
		t.Fatal("direct commit published the crashed content without client confirmation")
	}
	if _, err := os.Stat(remote); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed direct commit exposed a target: %v", err)
	}
	if err := restarted.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// P1-2 counterexample: owner-b must not adopt, commit, or abort owner-a's
// crashed staging even with identical path and parameters.
func TestFSChunkedBeginDoesNotAdoptAcrossOwners(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.bin")
	filesystem := fslocal.New()
	ownerA := ipc.WithUserID(t.Context(), "owner-a")
	ownerB := ipc.WithUserID(t.Context(), "owner-b")

	crashed, crashedTransfers := newUploadRegistryForTest(filesystem)
	first := beginResumeUploadForTest(t, crashed, crashedTransfers, ownerA, "session-a", "task-1", "attempt-1", remote, 8, 4)
	crashedInput := fsChunkUploadRequest{SessionID: "session-a", UploadID: first.UploadID, TaskID: "task-1", AttemptID: "attempt-1"}
	crashedEntry, err := crashed.lookup(ownerA, crashedInput)
	if err != nil {
		t.Fatal(err)
	}
	writeResumeChunkForTest(t, crashedEntry, filesystem, crashedInput, 0, "abcd")

	restarted, restartedTransfers := newUploadRegistryForTest(filesystem)
	second := beginResumeUploadForTest(t, restarted, restartedTransfers, ownerB, "session-b", "task-2", "attempt-2", remote, 8, 4)
	if second.UploadID == first.UploadID {
		t.Fatal("owner-b adopted owner-a's crashed staging")
	}
	foreignInput := fsChunkUploadRequest{SessionID: "session-b", UploadID: first.UploadID, TaskID: "task-2", AttemptID: "attempt-2"}
	if _, err := restarted.lookup(ownerB, foreignInput); err == nil {
		t.Fatal("owner-b resolved owner-a's upload entry")
	}
	if files := stagingGroupFiles(t, remote); len(files) != 3 {
		t.Fatalf("owner-b's begin disturbed owner-a's staging: %v", files)
	}

	third := beginResumeUploadForTest(t, restarted, restartedTransfers, ownerA, "session-a", "task-3", "attempt-3", remote, 8, 4)
	if third.UploadID != first.UploadID {
		t.Fatalf("owner-a begin = %q, want own crashed upload %q", third.UploadID, first.UploadID)
	}
	if err := restarted.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// The ownerless 16-byte sidecar of older builds is no longer readable, so no
// begin adopts it, whether multi-account or the desktop empty owner.
func TestFSChunkedBeginNeverAdoptsLegacySidecar(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.bin")
	filesystem := fslocal.New()
	ownerB := ipc.WithUserID(t.Context(), "owner-b")

	crashed, crashedTransfers := newUploadRegistryForTest(filesystem)
	first := beginResumeUploadForTest(t, crashed, crashedTransfers, context.Background(), "session-a", "task-1", "attempt-1", remote, 8, 4)
	crashedInput := fsChunkUploadRequest{SessionID: "session-a", UploadID: first.UploadID, TaskID: "task-1", AttemptID: "attempt-1"}
	crashedEntry, err := crashed.lookup(context.Background(), crashedInput)
	if err != nil {
		t.Fatal(err)
	}
	writeResumeChunkForTest(t, crashedEntry, filesystem, crashedInput, 0, "abcd")
	legacy := make([]byte, 16)
	binary.LittleEndian.PutUint64(legacy[0:8], 8)
	binary.LittleEndian.PutUint64(legacy[8:16], 4)
	if err := os.WriteFile(chunked.StagingPath(remote, first.UploadID, "params"), legacy, 0o600); err != nil {
		t.Fatal(err)
	}

	multiAccount, multiAccountTransfers := newUploadRegistryForTest(filesystem)
	second := beginResumeUploadForTest(t, multiAccount, multiAccountTransfers, ownerB, "session-b", "task-2", "attempt-2", remote, 8, 4)
	if second.UploadID == first.UploadID {
		t.Fatal("multi-account begin adopted a legacy owner-less sidecar")
	}

	desktop, desktopTransfers := newUploadRegistryForTest(filesystem)
	third := beginResumeUploadForTest(t, desktop, desktopTransfers, context.Background(), "session-c", "task-3", "attempt-3", remote, 8, 4)
	if third.UploadID == first.UploadID {
		t.Fatal("desktop empty-owner begin adopted a legacy sidecar")
	}
	if err := multiAccount.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := desktop.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// P2: persisted staging targets keep crash leftovers discoverable after a
// restart, resolution failures aggregate into the returned error while other
// targets still sweep, and the target resolves again once the owner has a
// live session for the asset.
func TestFSChunkedRegistryStagingTargetsSurvivesRestart(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "targets.json")
	filesystem := fslocal.New()
	resolver := func(context.Context, string) (base.FileSystem, string, error) { return filesystem, "ssh", nil }
	ctx := ipc.WithUserID(t.Context(), "owner-a")
	dir := t.TempDir()

	first := newFSChunkUploadRegistry()
	first.transfers = newFSTransferRegistry()
	first.filesystem = resolver
	first.sessionAsset = func(context.Context, string) (string, error) { return "asset-1", nil }
	first.targetsPath = storePath
	beginResumeUploadForTest(t, first, first.transfers, ctx, "session-a", "task-1", "attempt-1", filepath.Join(dir, "remote.bin"), 4, 4)

	reapErr := errors.New("no live session for asset asset-1")
	second := newFSChunkUploadRegistry()
	second.transfers = newFSTransferRegistry()
	second.filesystem = resolver
	second.targetsPath = storePath
	second.reapFilesystem = func(context.Context, string, string) (base.FileSystem, error) { return nil, reapErr }
	liveDir := t.TempDir()
	beginResumeUploadForTest(t, second, second.transfers, ctx, "session-b", "task-2", "attempt-2", filepath.Join(liveDir, "live.bin"), 4, 4)

	targets, err := second.stagingTargets(context.Background())
	if !errors.Is(err, reapErr) {
		t.Fatalf("stagingTargets error = %v, want aggregated %v", err, reapErr)
	}
	if len(targets) != 1 || targets[0].Dir != liveDir {
		t.Fatalf("targets = %+v, want the live entry's dir %q", targets, liveDir)
	}

	second.reapFilesystem = func(_ context.Context, owner, assetID string) (base.FileSystem, error) {
		if owner != "owner-a" || assetID != "asset-1" {
			t.Fatalf("reap resolve = %q, %q, want owner-a/asset-1", owner, assetID)
		}
		return filesystem, nil
	}
	targets, err = second.stagingTargets(context.Background())
	if err != nil || len(targets) != 2 {
		t.Fatalf("reconnected targets = %+v, %v", targets, err)
	}
	if err := first.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// P2: a background sweep context is identity-less, so stagingTargets must
// restore each entry's owner to resolve its filesystem on a non-open
// multi-account server.
func TestFSChunkedRegistryStagingTargetsRestoresOwnerBackground(t *testing.T) {
	filesystem := fslocal.New()
	manager := session.NewManager(session.Config{
		Connector: session.ConnectorFunc(func(_ context.Context, asset session.Asset, generation uint64) (base.Transport, error) {
			return fakeProviderTransport{kind: asset.Kind, fs: filesystem}, nil
		}),
		IdleTimeout: -1,
	})
	t.Cleanup(func() { _ = manager.Close() })
	ownerA := ipc.WithUserID(t.Context(), "owner-a")
	ownerB := ipc.WithUserID(t.Context(), "owner-b")
	connectedA, err := manager.Connect(ownerA, session.Asset{ID: "asset-a", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	connectedB, err := manager.Connect(ownerB, session.Asset{ID: "asset-b", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}

	uploads := newFSChunkUploadRegistry()
	uploads.transfers = newFSTransferRegistry()
	uploads.filesystem = func(ctx context.Context, sessionID string) (base.FileSystem, string, error) {
		transport, err := manager.TransportFor(ctx, sessionID)
		if err != nil {
			return nil, "", err
		}
		provider, ok := transport.(base.FileTransport)
		if !ok {
			return nil, transport.Kind(), base.ErrUnsupported
		}
		remote, err := provider.FileSystem(ctx)
		if err != nil {
			return nil, transport.Kind(), err
		}
		return remote, transport.Kind(), nil
	}
	uploads.sessionAsset = func(ctx context.Context, sessionID string) (string, error) {
		connected, err := manager.SessionFor(ctx, sessionID)
		if err != nil {
			return "", err
		}
		return connected.Asset().ID, nil
	}
	dirA := t.TempDir()
	dirB := t.TempDir()
	beginResumeUploadForTest(t, uploads, uploads.transfers, ownerA, connectedA.ID, "task-a", "attempt-a", filepath.Join(dirA, "a.bin"), 4, 4)
	beginResumeUploadForTest(t, uploads, uploads.transfers, ownerB, connectedB.ID, "task-b", "attempt-b", filepath.Join(dirB, "b.bin"), 4, 4)

	targets, err := uploads.stagingTargets(context.Background())
	if err != nil {
		t.Fatalf("background stagingTargets: %v", err)
	}
	if len(targets) != 2 || targets[0].Dir != dirA || targets[1].Dir != dirB {
		t.Fatalf("background targets = %+v, want %q and %q", targets, dirA, dirB)
	}
	if err := uploads.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// r3 P1 counterexample via the real IPC path: a new attempt that rebinds a
// live entry must not inherit the previous attempt's confirmed chunks —
// writing only the missing chunk must never commit a mix.
func TestFSChunkedRebindNewAttemptPartialSendCannotCommit(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.bin")
	dispatcher, manager := newFSTransferTestDispatcher(t, fslocal.New(), nil)
	ctx := ipc.WithUserID(t.Context(), "owner-a")
	connected, err := manager.ConnectFor(ctx, session.Asset{ID: "asset-rebind-mix", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	first := beginChunkIPC(t, dispatcher, ctx, fsChunkUploadRequest{
		SessionID: connected.ID, RemotePath: remote, TaskID: "task-1", AttemptID: "attempt-1", TotalSize: 8, ChunkSize: 4,
	})
	decodeFSTransferTest[fsChunkUploadProgressDTO](t, dispatchFSTransferTest(dispatcher, ctx, "fs_upload_chunk", chunkIPCRequest(first, 0, "abcd"), nil))

	second := beginChunkIPC(t, dispatcher, ctx, fsChunkUploadRequest{
		SessionID: connected.ID, RemotePath: remote, TaskID: "task-2", AttemptID: "attempt-2", TotalSize: 8, ChunkSize: 4,
	})
	if second.UploadID != first.UploadID {
		t.Fatalf("rebound begin = %q, want %q", second.UploadID, first.UploadID)
	}
	// The new attempt only sends the chunk it believes is missing; its chunk 1
	// diverges from the previous attempt's content.
	decodeFSTransferTest[fsChunkUploadProgressDTO](t, dispatchFSTransferTest(dispatcher, ctx, "fs_upload_chunk", chunkIPCRequest(second, 1, "WXYZ"), nil))
	response := dispatchFSTransferTest(dispatcher, ctx, "fs_upload_commit", second, nil)
	if response.OK {
		t.Fatal("new attempt committed a mix of inherited and new chunks")
	}
	if _, err := os.Stat(remote); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed mixed commit exposed a target: %v", err)
	}
}

// r3 P1 counterexample via the real IPC path: a new attempt must not commit
// the previous attempt's fully staged content without presenting anything.
func TestFSChunkedRebindNewAttemptDirectCommitFails(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.bin")
	dispatcher, manager := newFSTransferTestDispatcher(t, fslocal.New(), nil)
	ctx := ipc.WithUserID(t.Context(), "owner-a")
	connected, err := manager.ConnectFor(ctx, session.Asset{ID: "asset-rebind-direct", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	first := beginChunkIPC(t, dispatcher, ctx, fsChunkUploadRequest{
		SessionID: connected.ID, RemotePath: remote, TaskID: "task-1", AttemptID: "attempt-1", TotalSize: 8, ChunkSize: 4,
	})
	decodeFSTransferTest[fsChunkUploadProgressDTO](t, dispatchFSTransferTest(dispatcher, ctx, "fs_upload_chunk", chunkIPCRequest(first, 0, "abcd"), nil))
	decodeFSTransferTest[fsChunkUploadProgressDTO](t, dispatchFSTransferTest(dispatcher, ctx, "fs_upload_chunk", chunkIPCRequest(first, 1, "efgh"), nil))

	second := beginChunkIPC(t, dispatcher, ctx, fsChunkUploadRequest{
		SessionID: connected.ID, RemotePath: remote, TaskID: "task-2", AttemptID: "attempt-2", TotalSize: 8, ChunkSize: 4,
	})
	if second.UploadID != first.UploadID {
		t.Fatalf("rebound begin = %q, want %q", second.UploadID, first.UploadID)
	}
	response := dispatchFSTransferTest(dispatcher, ctx, "fs_upload_commit", second, nil)
	if response.OK {
		t.Fatal("new attempt committed the previous attempt's content without presenting chunks")
	}
	if _, err := os.Stat(remote); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed direct commit exposed a target: %v", err)
	}
}

// An idempotent retry of the same task and attempt keeps its confirmation, so
// the legitimate flow of resending the remaining chunks and committing still
// works without re-presenting earlier chunks.
func TestFSChunkedSameAttemptRetryKeepsConfirmation(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.bin")
	dispatcher, manager := newFSTransferTestDispatcher(t, fslocal.New(), nil)
	ctx := ipc.WithUserID(t.Context(), "owner-a")
	connected, err := manager.ConnectFor(ctx, session.Asset{ID: "asset-same-attempt", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	input := fsChunkUploadRequest{
		SessionID: connected.ID, RemotePath: remote, TaskID: "task-1", AttemptID: "attempt-1", TotalSize: 8, ChunkSize: 4,
	}
	first := beginChunkIPC(t, dispatcher, ctx, input)
	decodeFSTransferTest[fsChunkUploadProgressDTO](t, dispatchFSTransferTest(dispatcher, ctx, "fs_upload_chunk", chunkIPCRequest(first, 0, "abcd"), nil))
	retry := beginChunkIPC(t, dispatcher, ctx, input)
	if retry.UploadID != first.UploadID {
		t.Fatalf("idempotent begin = %q, want %q", retry.UploadID, first.UploadID)
	}
	decodeFSTransferTest[fsChunkUploadProgressDTO](t, dispatchFSTransferTest(dispatcher, ctx, "fs_upload_chunk", chunkIPCRequest(retry, 1, "efgh"), nil))
	committed := decodeFSTransferTest[int64](t, dispatchFSTransferTest(dispatcher, ctx, "fs_upload_commit", retry, nil))
	if committed != 8 {
		t.Fatalf("committed = %d, want 8", committed)
	}
	data, err := os.ReadFile(remote)
	if err != nil || string(data) != "abcdefgh" {
		t.Fatalf("committed target = %q, %v", data, err)
	}
	assertNoChunkIArtifacts(t, remote)
}
