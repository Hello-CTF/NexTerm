package production

import (
	"context"
	"encoding/base64"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/fs/chunked"
	fslocal "github.com/Hello-CTF/NexTerm/internal/fs/local"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func newUploadRegistryForTest(filesystem base.FileSystem) (*fsChunkUploadRegistry, *fsTransferRegistry) {
	transfers := newFSTransferRegistry()
	uploads := newFSChunkUploadRegistry()
	uploads.transfers = transfers
	uploads.filesystem = func(context.Context, string) (base.FileSystem, string, error) {
		return filesystem, "ssh", nil
	}
	return uploads, transfers
}

func beginUploadRegistryForTest(t *testing.T, uploads *fsChunkUploadRegistry, transfers *fsTransferRegistry, ctx context.Context, sessionID, remote string) (fsChunkUploadRequest, *fsChunkUploadEntry) {
	t.Helper()
	input := fsChunkUploadRequest{
		SessionID: sessionID, RemotePath: remote, TaskID: sessionID + "-task", AttemptID: sessionID + "-attempt", TotalSize: 4, ChunkSize: 4,
	}
	result, err := uploads.begin(ctx, transfers, input)
	if err != nil {
		t.Fatal(err)
	}
	input.UploadID = result.UploadID
	entry, err := uploads.lookup(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	return input, entry
}

func writeUploadRegistryChunkForTest(t *testing.T, entry *fsChunkUploadEntry, filesystem base.FileSystem, input fsChunkUploadRequest) {
	t.Helper()
	request := chunkIPCRequest(input, 0, "abcd")
	data, err := base64.StdEncoding.DecodeString(request.ContentBase64)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.upload.WriteChunk(entry.transferCtx, filesystem, chunked.Chunk{
		Index: request.Index, Size: request.Size, SHA256: request.SHA256, Data: data,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestFSChunkedBeginRejectsInvalidSessionWithoutEntry(t *testing.T) {
	uploads := newFSChunkUploadRegistry()
	dispatcher, _ := newFSTransferTestDispatcher(t, fslocal.New(), nil, uploads)
	ctx := ipc.WithUserID(t.Context(), "owner-a")
	response := dispatchFSTransferTest(dispatcher, ctx, "fs_upload_begin", fsChunkUploadRequest{
		SessionID: "missing", RemotePath: "remote.bin", TaskID: "invalid-task", AttemptID: "invalid-attempt", TotalSize: 4, ChunkSize: 4,
	}, nil)
	if response.OK || response.Error == nil {
		t.Fatalf("invalid session begin = %+v", response)
	}
	if len(uploads.entries) != 0 {
		t.Fatalf("invalid session left %d upload entries", len(uploads.entries))
	}
	if err := uploads.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestFSChunkedRegistryTouchPreventsExpiryAndIdleExpiryCleansBothMaps(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.bin")
	filesystem := fslocal.New()
	uploads, transfers := newUploadRegistryForTest(filesystem)
	ctx := ipc.WithUserID(t.Context(), "owner-a")
	input, entry := beginUploadRegistryForTest(t, uploads, transfers, ctx, "session-a", remote)
	writeUploadRegistryChunkForTest(t, entry, filesystem, input)
	key := fsTransferAttemptKey{owner: "owner-a", taskID: input.TaskID, attemptID: input.AttemptID}
	transfers.mu.Lock()
	attempt := transfers.attempts[key]
	attempt.updatedAt = time.Now().Add(-fsTransferAttemptRetention - time.Second)
	transfers.mu.Unlock()
	entry.touch()
	transfers.mu.Lock()
	expired := transfers.pruneLocked(time.Now())
	transfers.mu.Unlock()
	runFSExpirations(expired)
	if len(expired) != 0 || len(uploads.entries) != 1 || len(transfers.attempts) != 1 {
		t.Fatalf("active upload expired: callbacks=%d uploads=%d attempts=%d", len(expired), len(uploads.entries), len(transfers.attempts))
	}

	transfers.mu.Lock()
	attempt.updatedAt = time.Now().Add(-fsTransferAttemptRetention - time.Second)
	expired = transfers.pruneLocked(time.Now())
	transfers.mu.Unlock()
	runFSExpirations(expired)
	if len(expired) != 1 || len(uploads.entries) != 0 || len(transfers.attempts) != 0 {
		t.Fatalf("idle upload was not reclaimed: callbacks=%d uploads=%d attempts=%d", len(expired), len(uploads.entries), len(transfers.attempts))
	}
	if !errors.Is(entry.transferCtx.Err(), context.Canceled) {
		t.Fatalf("expired transfer context = %v", entry.transferCtx.Err())
	}
	assertNoChunkIArtifacts(t, remote)
	if err := uploads.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestFSChunkedRegistryCloseSessionOnlyCleansOwnedSession(t *testing.T) {
	root := t.TempDir()
	remoteA := filepath.Join(root, "a.bin")
	remoteB := filepath.Join(root, "b.bin")
	filesystem := fslocal.New()
	uploads, transfers := newUploadRegistryForTest(filesystem)
	ctx := ipc.WithUserID(t.Context(), "owner-a")
	inputA, entryA := beginUploadRegistryForTest(t, uploads, transfers, ctx, "session-a", remoteA)
	inputB, entryB := beginUploadRegistryForTest(t, uploads, transfers, ctx, "session-b", remoteB)
	writeUploadRegistryChunkForTest(t, entryA, filesystem, inputA)
	writeUploadRegistryChunkForTest(t, entryB, filesystem, inputB)
	if err := uploads.closeSession("session-a"); err != nil {
		t.Fatal(err)
	}
	assertNoChunkIArtifacts(t, remoteA)
	matches, err := filepath.Glob(filepath.Join(root, ".b.bin.nexterm-*.part-*"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("other session artifact = %v, %v", matches, err)
	}
	if len(uploads.entries) != 1 {
		t.Fatalf("closeSession left %d entries, want 1", len(uploads.entries))
	}
	if err := uploads.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertNoChunkIArtifacts(t, remoteB)
	if len(transfers.attempts) != 0 {
		t.Fatalf("shutdown left %d cancellation entries", len(transfers.attempts))
	}
	if !errors.Is(entryB.transferCtx.Err(), context.Canceled) {
		t.Fatalf("shutdown transfer context = %v", entryB.transferCtx.Err())
	}
	if err := uploads.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestProductionFSChunkedShutdownLifecycle(t *testing.T) {
	filesystem := fslocal.New()
	manager := session.NewManager(session.Config{Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
		return fakeProviderTransport{kind: "ssh", fs: filesystem}, nil
	})})
	production, err := NewProductionWithServices(
		Config{Events: ipc.EmitterFunc(func(context.Context, ipc.Event) error { return nil })},
		ProductionServices{Sessions: manager},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })
	ctx := ipc.WithUserID(t.Context(), "owner-a")
	connected, err := manager.ConnectFor(ctx, session.Asset{ID: "asset-upload-shutdown", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	remote := filepath.Join(t.TempDir(), "remote.bin")
	input := beginChunkIPC(t, production.Dispatcher, ctx, fsChunkUploadRequest{
		SessionID: connected.ID, RemotePath: remote, TaskID: "shutdown-task", AttemptID: "shutdown-attempt", TotalSize: 4, ChunkSize: 4,
	})
	decodeFSTransferTest[fsChunkUploadProgressDTO](t, dispatchFSTransferTest(production.Dispatcher, ctx, "fs_upload_chunk", chunkIPCRequest(input, 0, "abcd"), nil))
	if len(production.Services.fsUploads.entries) != 1 {
		t.Fatal("upload did not register before shutdown")
	}
	if err := production.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(production.Services.fsUploads.entries) != 0 || len(production.Services.fsUploads.transfers.attempts) != 0 {
		t.Fatalf("shutdown left uploads=%d attempts=%d", len(production.Services.fsUploads.entries), len(production.Services.fsUploads.transfers.attempts))
	}
	assertNoChunkIArtifacts(t, remote)
}

type gatedWriteFS struct {
	*fslocal.FileSystem
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (f *gatedWriteFS) OpenWrite(ctx context.Context, path string, appendMode bool) (base.RemoteWriter, error) {
	writer, err := f.FileSystem.OpenWrite(ctx, path, appendMode)
	if err != nil {
		return nil, err
	}
	return &gatedWriter{RemoteWriter: writer, ctx: ctx, started: f.started, release: f.release, once: &f.once}, nil
}

type gatedWriter struct {
	base.RemoteWriter
	ctx     context.Context
	started chan struct{}
	release chan struct{}
	once    *sync.Once
}

func (w *gatedWriter) Write(data []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	select {
	case <-w.release:
		return len(data), nil
	case <-w.ctx.Done():
		return 0, w.ctx.Err()
	}
}

func TestFSChunkedRegistryBlockedRPCIsNotExpired(t *testing.T) {
	destination := &gatedWriteFS{FileSystem: fslocal.New(), started: make(chan struct{}), release: make(chan struct{})}
	uploads := newFSChunkUploadRegistry()
	dispatcher, manager := newFSTransferTestDispatcher(t, destination, nil, uploads)
	ctx := ipc.WithUserID(t.Context(), "owner-a")
	connected, err := manager.ConnectFor(ctx, session.Asset{ID: "asset-in-flight", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	input := beginChunkIPC(t, dispatcher, ctx, fsChunkUploadRequest{
		SessionID: connected.ID, RemotePath: filepath.Join(t.TempDir(), "remote.bin"), TaskID: "in-flight-task", AttemptID: "in-flight-attempt", TotalSize: 4, ChunkSize: 4,
	})
	entry, err := uploads.lookup(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan ipc.Response, 1)
	go func() {
		done <- dispatchFSTransferTest(dispatcher, ctx, "fs_upload_chunk", chunkIPCRequest(input, 0, "abcd"), nil)
	}()
	select {
	case <-destination.started:
	case <-time.After(2 * time.Second):
		t.Fatal("chunk did not reach the blocking writer")
	}
	key := fsTransferAttemptKey{owner: "owner-a", taskID: input.TaskID, attemptID: input.AttemptID}
	attempt := uploads.transfers.attempts[key]
	uploads.transfers.mu.Lock()
	attempt.updatedAt = time.Now().Add(-fsTransferAttemptRetention - time.Second)
	expired := uploads.transfers.pruneLocked(time.Now())
	uploads.transfers.mu.Unlock()
	runFSExpirations(expired)
	if len(expired) != 0 || entry.transferCtx.Err() != nil {
		t.Fatalf("blocked RPC expired: callbacks=%d err=%v", len(expired), entry.transferCtx.Err())
	}
	uploads.transfers.expire(key, attempt)
	if entry.transferCtx.Err() != nil {
		t.Fatalf("expire canceled blocked RPC: %v", entry.transferCtx.Err())
	}
	close(destination.release)
	if response := <-done; !response.OK {
		t.Fatalf("blocked chunk failed after expiry check: %+v", response)
	}
	if err := uploads.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestFSTransferRegistryOneShotOperationExpiresOnlyAfterFinish(t *testing.T) {
	transfers := newFSTransferRegistry()
	ctx := ipc.WithUserID(t.Context(), "owner-a")
	transferCtx, taskID, finish, err := transfers.begin(ctx, "long-task", "long-attempt")
	if err != nil {
		t.Fatal(err)
	}
	key := fsTransferAttemptKey{owner: "owner-a", taskID: taskID, attemptID: "long-attempt"}
	attempt := transfers.attempts[key]
	transfers.mu.Lock()
	attempt.updatedAt = time.Now().Add(-fsTransferAttemptRetention - time.Second)
	expired := transfers.pruneLocked(time.Now())
	transfers.mu.Unlock()
	runFSExpirations(expired)
	if len(expired) != 0 || transferCtx.Err() != nil {
		t.Fatalf("one-shot operation expired before finish: callbacks=%d err=%v", len(expired), transferCtx.Err())
	}
	transfers.expire(key, attempt)
	if transferCtx.Err() != nil {
		t.Fatalf("expire canceled one-shot operation: %v", transferCtx.Err())
	}
	finish()
	transfers.mu.Lock()
	attempt.updatedAt = time.Now().Add(-fsTransferAttemptRetention - time.Second)
	expired = transfers.pruneLocked(time.Now())
	transfers.mu.Unlock()
	runFSExpirations(expired)
	if len(transfers.attempts) != 0 {
		t.Fatalf("finished attempt was not pruned: %d", len(transfers.attempts))
	}
	transfers.close()
}

func TestFSChunkedRegistryInFlightOperationsExpireOnlyAfterLastLeave(t *testing.T) {
	uploads, transfers := newUploadRegistryForTest(fslocal.New())
	ctx := ipc.WithUserID(t.Context(), "owner-a")
	input, entry := beginUploadRegistryForTest(t, uploads, transfers, ctx, "session-a", "remote.bin")
	key := fsTransferAttemptKey{owner: "owner-a", taskID: input.TaskID, attemptID: input.AttemptID}
	firstDone := entry.enter()
	secondDone := entry.enter()
	transfers.mu.Lock()
	attempt := transfers.attempts[key]
	attempt.updatedAt = time.Now().Add(-fsTransferAttemptRetention - time.Second)
	expired := transfers.pruneLocked(time.Now())
	transfers.mu.Unlock()
	runFSExpirations(expired)
	if len(expired) != 0 || attempt.inFlight != 2 {
		t.Fatalf("overlapping RPCs expired: callbacks=%d inFlight=%d", len(expired), attempt.inFlight)
	}
	transfers.expire(key, attempt)
	if len(transfers.attempts) != 1 || entry.transferCtx.Err() != nil {
		t.Fatalf("in-flight expire canceled the attempt: attempts=%d err=%v", len(transfers.attempts), entry.transferCtx.Err())
	}
	firstDone()
	transfers.mu.Lock()
	attempt.updatedAt = time.Now().Add(-fsTransferAttemptRetention - time.Second)
	expired = transfers.pruneLocked(time.Now())
	transfers.mu.Unlock()
	runFSExpirations(expired)
	if len(expired) != 0 || attempt.inFlight != 1 {
		t.Fatalf("last RPC was not protected: callbacks=%d inFlight=%d", len(expired), attempt.inFlight)
	}
	secondDone()
	transfers.mu.Lock()
	attempt.updatedAt = time.Now().Add(-fsTransferAttemptRetention - time.Second)
	expired = transfers.pruneLocked(time.Now())
	transfers.mu.Unlock()
	runFSExpirations(expired)
	if len(expired) != 1 || len(uploads.entries) != 0 || len(transfers.attempts) != 0 {
		t.Fatalf("idle attempt was not reclaimed: callbacks=%d uploads=%d attempts=%d", len(expired), len(uploads.entries), len(transfers.attempts))
	}
	if err := uploads.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestFSChunkedBeginPanicFinishesAttempt(t *testing.T) {
	uploads := newFSChunkUploadRegistry()
	dispatcher, manager := newFSTransferTestDispatcher(t, fslocal.New(), nil, uploads)
	ctx := ipc.WithUserID(t.Context(), "owner-a")
	connected, err := manager.ConnectFor(ctx, session.Asset{ID: "asset-begin-panic", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	uploads.beforeRegister = func() {
		panic("begin hook panic")
	}
	defer func() { uploads.beforeRegister = nil }()
	input := fsChunkUploadRequest{
		SessionID: connected.ID, RemotePath: "remote.bin", TaskID: "panic-task", AttemptID: "panic-attempt", TotalSize: 4, ChunkSize: 4,
	}
	response := dispatchFSTransferTest(dispatcher, ctx, "fs_upload_begin", input, nil)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeInternal {
		t.Fatalf("panic begin response = %+v", response)
	}
	if len(uploads.entries) != 0 {
		t.Fatalf("panic begin left %d upload entries", len(uploads.entries))
	}
	key := fsTransferAttemptKey{owner: "owner-a", taskID: input.TaskID, attemptID: input.AttemptID}
	attempt := uploads.transfers.attempts[key]
	if attempt == nil || attempt.state != fsTransferAttemptEnded || attempt.inFlight != 0 {
		t.Fatalf("panic attempt = %+v, want ended/inFlight=0", attempt)
	}
	uploads.transfers.mu.Lock()
	attempt.updatedAt = time.Now().Add(-fsTransferAttemptRetention - time.Second)
	expired := uploads.transfers.pruneLocked(time.Now())
	uploads.transfers.mu.Unlock()
	runFSExpirations(expired)
	if len(uploads.transfers.attempts) != 0 {
		t.Fatalf("ended panic attempt was not reclaimed: %d", len(uploads.transfers.attempts))
	}
	if err := uploads.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestFSChunkedRegistryBeginAfterCloseSessionIsRejected(t *testing.T) {
	uploads, transfers := newUploadRegistryForTest(fslocal.New())
	ctx := ipc.WithUserID(t.Context(), "owner-a")
	beginReady := make(chan struct{})
	resumeBegin := make(chan struct{})
	uploads.beforeRegister = func() {
		close(beginReady)
		<-resumeBegin
	}
	input := fsChunkUploadRequest{
		SessionID: "session-a", RemotePath: "remote.bin", TaskID: "race-task", AttemptID: "race-attempt", TotalSize: 4, ChunkSize: 4,
	}
	beginResult := make(chan error, 1)
	go func() {
		_, err := uploads.begin(ctx, transfers, input)
		beginResult <- err
	}()
	<-beginReady
	if err := uploads.closeSession("session-a"); err != nil {
		t.Fatal(err)
	}
	close(resumeBegin)
	if err := <-beginResult; err == nil {
		t.Fatal("stale begin registered after closeSession")
	}
	if len(uploads.entries) != 0 {
		t.Fatalf("stale begin left %d upload entries", len(uploads.entries))
	}
	key := fsTransferAttemptKey{owner: "owner-a", taskID: input.TaskID, attemptID: input.AttemptID}
	transfers.mu.Lock()
	attempt := transfers.attempts[key]
	transfers.mu.Unlock()
	if attempt == nil || attempt.state != fsTransferAttemptEnded {
		t.Fatalf("stale attempt = %+v, want ended", attempt)
	}

	uploads.beforeRegister = nil
	input.TaskID = "reopen-task"
	input.AttemptID = "reopen-attempt"
	if _, err := uploads.begin(ctx, transfers, input); err != nil {
		t.Fatalf("begin after close completed = %v", err)
	}
	if err := uploads.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestFSTransferRegistryPrunesCancelPendingWithoutCancelFunc(t *testing.T) {
	transfers := newFSTransferRegistry()
	ctx := ipc.WithUserID(t.Context(), "owner-a")
	accepted, err := transfers.cancel(ctx, "pending-task", "pending-attempt")
	if err != nil || !accepted {
		t.Fatalf("pending cancel = %v, %v", accepted, err)
	}
	key := fsTransferAttemptKey{owner: "owner-a", taskID: "pending-task", attemptID: "pending-attempt"}
	transfers.mu.Lock()
	transfers.attempts[key].updatedAt = time.Now().Add(-fsTransferAttemptRetention - time.Second)
	expired := transfers.pruneLocked(time.Now())
	transfers.mu.Unlock()
	runFSExpirations(expired)
	if len(transfers.attempts) != 0 {
		t.Fatalf("pending cancellation was not pruned: %d", len(transfers.attempts))
	}
	transfers.close()
}

func TestFSChunkedRegistryCloseRejectsNewBegin(t *testing.T) {
	uploads, transfers := newUploadRegistryForTest(fslocal.New())
	if err := uploads.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx := ipc.WithUserID(t.Context(), "owner-a")
	_, err := uploads.begin(ctx, transfers, fsChunkUploadRequest{
		SessionID: "session-a", RemotePath: "remote.bin", TaskID: "closed-task", AttemptID: "closed-attempt", TotalSize: 4, ChunkSize: 4,
	})
	if err == nil {
		t.Fatal("begin after shutdown succeeded")
	}
	if len(uploads.entries) != 0 || len(transfers.attempts) != 0 {
		t.Fatalf("closed registry accepted state: uploads=%d attempts=%d", len(uploads.entries), len(transfers.attempts))
	}
}
