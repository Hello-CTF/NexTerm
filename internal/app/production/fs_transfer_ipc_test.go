package production

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	fslocal "github.com/Hello-CTF/NexTerm/internal/fs/local"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type blockingTransferWriteFS struct {
	base.FileSystem
	started chan context.Context
}

func (f *blockingTransferWriteFS) OpenWrite(ctx context.Context, _ string, _ bool) (base.RemoteWriter, error) {
	return &blockingTransferWriter{ctx: ctx, started: f.started}, nil
}

type blockingTransferWriter struct {
	ctx     context.Context
	started chan context.Context
}

func (w *blockingTransferWriter) Write(_ []byte) (int, error) {
	w.started <- w.ctx
	<-w.ctx.Done()
	return 0, w.ctx.Err()
}

func (w *blockingTransferWriter) Sync() error  { return nil }
func (w *blockingTransferWriter) Close() error { return nil }

func dispatchFSTransferTest(dispatcher *ipc.Dispatcher, ctx context.Context, command string, input any, events ipc.Emitter) ipc.Response {
	payload, err := json.Marshal(input)
	if err != nil {
		panic(err)
	}
	return dispatcher.Dispatch(ctx, ipc.Request{Command: command, Args: payload}, ipc.Environment{Events: events})
}

func decodeFSTransferTest[T any](t *testing.T, response ipc.Response) T {
	t.Helper()
	if !response.OK {
		t.Fatalf("dispatch failed: %+v", response)
	}
	var result T
	if err := json.Unmarshal(response.Data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func newFSTransferTestDispatcher(t *testing.T, filesystem base.FileSystem, events ipc.Emitter, uploads ...*fsChunkUploadRegistry) (*ipc.Dispatcher, *session.Manager) {
	t.Helper()
	manager := session.NewManager(session.Config{Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
		return fakeProviderTransport{kind: "ssh", fs: filesystem}, nil
	})})
	t.Cleanup(func() { _ = manager.Close() })
	dispatcher := ipc.NewDispatcher()
	if err := registerFSCommands(dispatcher, manager, nil, uploads...); err != nil {
		t.Fatal(err)
	}
	return dispatcher, manager
}

func TestFSTransferIPCRunningCancelChecksOwner(t *testing.T) {
	started := make(chan context.Context, 1)
	filesystem := &blockingTransferWriteFS{FileSystem: fslocal.New(), started: started}
	dispatcher, manager := newFSTransferTestDispatcher(t, filesystem, nil)
	ownerA := ipc.WithUserID(t.Context(), "owner-a")
	ownerB := ipc.WithUserID(t.Context(), "owner-b")
	connected, err := manager.ConnectFor(ownerA, session.Asset{ID: "asset-transfer-cancel", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "source.bin")
	if err := os.WriteFile(source, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := fsRequest{
		SessionID: connected.ID, LocalPath: source, RemotePath: "remote.bin", TaskID: "shared-task", AttemptID: "attempt-running",
	}
	done := make(chan ipc.Response, 1)
	go func() {
		done <- dispatchFSTransferTest(dispatcher, ownerA, "fs_upload", request, nil)
	}()
	var transferCtx context.Context
	select {
	case transferCtx = <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("upload did not reach the blocking writer")
	}

	crossOwner := dispatchFSTransferTest(dispatcher, ownerB, "fs_cancel", fsRequest{TaskID: request.TaskID}, nil)
	if crossOwner.OK || crossOwner.Error == nil || crossOwner.Error.Code != ipc.CodeForbidden {
		t.Fatalf("cross-owner cancel = %+v, want forbidden", crossOwner)
	}
	if transferCtx.Err() != nil {
		t.Fatal("cross-owner cancel interrupted the upload")
	}
	canceled := decodeFSTransferTest[bool](t, dispatchFSTransferTest(dispatcher, ownerA, "fs_cancel", fsRequest{TaskID: request.TaskID}, nil))
	if !canceled {
		t.Fatal("owner cancel returned false for a running upload")
	}
	select {
	case response := <-done:
		if response.OK || !errors.Is(response.Error, context.Canceled) {
			t.Fatalf("canceled upload = %+v, want context cancellation", response)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("owner cancel did not unblock the upload")
	}
	after := decodeFSTransferTest[bool](t, dispatchFSTransferTest(dispatcher, ownerA, "fs_cancel", fsRequest{TaskID: request.TaskID}, nil))
	if after {
		t.Fatal("completed upload remained in the cancellation registry")
	}
}

func TestFSTransferIPCCancelBeforeBeginIsNotLost(t *testing.T) {
	started := make(chan context.Context, 1)
	filesystem := &blockingTransferWriteFS{FileSystem: fslocal.New(), started: started}
	dispatcher, manager := newFSTransferTestDispatcher(t, filesystem, nil)
	ownerA := ipc.WithUserID(t.Context(), "owner-a")
	ownerB := ipc.WithUserID(t.Context(), "owner-b")
	connected, err := manager.ConnectFor(ownerA, session.Asset{ID: "asset-transfer-early-cancel", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	request := fsRequest{
		SessionID: connected.ID, LocalPath: filepath.Join(t.TempDir(), "source.bin"), RemotePath: "remote.bin",
		TaskID: "early-task", AttemptID: "early-attempt",
	}
	for index := 0; index < 2; index++ {
		accepted := decodeFSTransferTest[bool](t, dispatchFSTransferTest(dispatcher, ownerA, "fs_cancel", fsRequest{
			TaskID: request.TaskID, AttemptID: request.AttemptID,
		}, nil))
		if !accepted {
			t.Fatalf("pending cancel %d returned false", index+1)
		}
	}
	crossOwner := dispatchFSTransferTest(dispatcher, ownerB, "fs_cancel", fsRequest{
		TaskID: request.TaskID, AttemptID: request.AttemptID,
	}, nil)
	if crossOwner.OK || crossOwner.Error == nil || crossOwner.Error.Code != ipc.CodeForbidden {
		t.Fatalf("cross-owner pending cancel = %+v, want forbidden", crossOwner)
	}

	response := dispatchFSTransferTest(dispatcher, ownerA, "fs_upload", request, nil)
	if response.OK || response.Error == nil || response.Error.Code != ipc.Code("cancelled") || !errors.Is(response.Error, context.Canceled) {
		t.Fatalf("late begin after cancel = %+v, want context.Canceled", response)
	}
	select {
	case <-started:
		t.Fatal("canceled late begin reached the writer")
	default:
	}
	after := decodeFSTransferTest[bool](t, dispatchFSTransferTest(dispatcher, ownerA, "fs_cancel", fsRequest{
		TaskID: request.TaskID, AttemptID: request.AttemptID,
	}, nil))
	if after {
		t.Fatal("consumed cancellation tombstone remained active")
	}
}

func TestFSTransferIPCStagedAndRemoteNotFoundDetailsAreDistinct(t *testing.T) {
	staged := &fakeStagedBlobResolver{dir: t.TempDir()}
	manager := session.NewManager(session.Config{Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
		return fakeProviderTransport{kind: "ssh", fs: fslocal.New()}, nil
	})})
	t.Cleanup(func() { _ = manager.Close() })
	dispatcher := ipc.NewDispatcher()
	if err := registerFSCommands(dispatcher, manager, staged); err != nil {
		t.Fatal(err)
	}
	ctx := ipc.WithUserID(t.Context(), "owner-a")
	connected, err := manager.ConnectFor(ctx, session.Asset{ID: "asset-transfer-staged-detail", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	stagedFailure := dispatchFSTransferTest(dispatcher, ctx, "fs_upload", fsRequest{
		SessionID: connected.ID, LocalPath: "other-id/upload.bin", RemotePath: "remote.bin",
		TaskID: "staged-task", AttemptID: "staged-attempt",
	}, nil)
	if stagedFailure.OK || stagedFailure.Error == nil || stagedFailure.Error.Code != ipc.CodeNotFound {
		t.Fatalf("staged failure = %+v, want not_found", stagedFailure)
	}
	detail, ok := stagedFailure.Error.Detail.(map[string]any)
	if !ok || detail["resource"] != "local_staging" || detail["reason"] != "staged_not_found" {
		t.Fatalf("staged detail = %#v, want local_staging/staged_not_found", stagedFailure.Error.Detail)
	}

	remoteFailure := dispatchFSTransferTest(dispatcher, ctx, "fs_download", fsRequest{
		SessionID: connected.ID, LocalPath: "staged-id/download.bin", RemotePath: filepath.Join(t.TempDir(), "missing.bin"),
		TaskID: "remote-task", AttemptID: "remote-attempt",
	}, nil)
	if remoteFailure.OK || remoteFailure.Error == nil || remoteFailure.Error.Code != ipc.CodeNotFound {
		t.Fatalf("remote failure = %+v, want not_found", remoteFailure)
	}
	detail, ok = remoteFailure.Error.Detail.(map[string]any)
	if !ok || detail["resource"] != "path" || detail["resource"] == "local_staging" {
		t.Fatalf("remote detail = %#v, must not be classified as local staging", remoteFailure.Error.Detail)
	}
}

func TestFSTransferIPCDownloadResumeValidatesPartialAndUsesTaskID(t *testing.T) {
	events := &fsEventRecorder{}
	dispatcher, manager := newFSTransferTestDispatcher(t, fslocal.New(), events)
	ctx := ipc.WithUserID(t.Context(), "owner-a")
	connected, err := manager.ConnectFor(ctx, session.Asset{ID: "asset-transfer-resume", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	remote := filepath.Join(root, "remote.bin")
	local := filepath.Join(root, "download.bin")
	if err := os.WriteFile(remote, []byte("abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("XXX"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := fsRequest{
		SessionID: connected.ID, RemotePath: remote, LocalPath: local, TaskID: "download-resume", AttemptID: "attempt-mismatch", Resume: true,
	}
	mismatch := dispatchFSTransferTest(dispatcher, ctx, "fs_download", request, events)
	if mismatch.OK || mismatch.Error == nil {
		t.Fatalf("mismatched resume partial = %+v, want rejection", mismatch)
	}
	detail, ok := mismatch.Error.Detail.(map[string]any)
	if !ok || detail["resource"] != "partial" || detail["reason"] != "resume_mismatch" {
		t.Fatalf("mismatch detail = %#v, want machine-readable resume_mismatch", mismatch.Error.Detail)
	}
	if data, err := os.ReadFile(local); err != nil || string(data) != "XXX" {
		t.Fatalf("rejected partial was modified: %q, %v", data, err)
	}

	if err := os.WriteFile(local, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	request.AttemptID = "attempt-resume-success"
	response := dispatchFSTransferTest(dispatcher, ctx, "fs_download", request, events)
	if got := decodeFSTransferTest[int64](t, response); got != 6 {
		t.Fatalf("resumed download size = %d, want 6", got)
	}
	if data, err := os.ReadFile(local); err != nil || string(data) != "abcdef" {
		t.Fatalf("resumed download content = %q, %v", data, err)
	}
	events.mu.Lock()
	defer events.mu.Unlock()
	if len(events.events) == 0 {
		t.Fatal("download emitted no progress")
	}
	for _, event := range events.events {
		if event.Event != ipc.TopicFSProgress {
			continue
		}
		progress, ok := event.Payload.(fslocal.Progress)
		if !ok || progress.TaskID != request.TaskID {
			t.Fatalf("progress task identity = %#v, want %q", event.Payload, request.TaskID)
		}
	}
}
