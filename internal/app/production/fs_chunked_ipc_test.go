package production

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	fslocal "github.com/Hello-CTF/NexTerm/internal/fs/local"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
)

func chunkIPCRequest(input fsChunkUploadRequest, index int64, data string) fsChunkUploadRequest {
	digest := sha256.Sum256([]byte(data))
	input.Index = index
	input.Size = int64(len(data))
	input.SHA256 = hex.EncodeToString(digest[:])
	input.ContentBase64 = base64.StdEncoding.EncodeToString([]byte(data))
	return input
}

func beginChunkIPC(t *testing.T, dispatcher *ipc.Dispatcher, ctx context.Context, input fsChunkUploadRequest) fsChunkUploadRequest {
	t.Helper()
	result := decodeFSTransferTest[fsChunkUploadBeginDTO](t, dispatchFSTransferTest(dispatcher, ctx, "fs_upload_begin", input, nil))
	if result.UploadID == "" || result.ChunkSize != input.ChunkSize {
		t.Fatalf("begin result = %+v", result)
	}
	input.UploadID = result.UploadID
	return input
}

func assertNoChunkIArtifacts(t *testing.T, remote string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(remote), "."+filepath.Base(remote)+".nexterm-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("upload artifacts = %v, %v", matches, err)
	}
}

func TestFSChunkedIPCOutOfOrderDuplicateAndAtomicCommit(t *testing.T) {
	dispatcher, manager := newFSTransferTestDispatcher(t, fslocal.New(), nil)
	ctx := ipc.WithUserID(t.Context(), "owner-a")
	connected, err := manager.ConnectFor(ctx, session.Asset{ID: "asset-chunked-ipc", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	remote := filepath.Join(t.TempDir(), "remote.bin")
	if err := os.WriteFile(remote, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := beginChunkIPC(t, dispatcher, ctx, fsChunkUploadRequest{
		SessionID: connected.ID, RemotePath: remote, TaskID: "chunk-task", AttemptID: "chunk-attempt", TotalSize: 10, ChunkSize: 4,
	})
	otherOwner := ipc.WithUserID(t.Context(), "owner-b")
	denied := dispatchFSTransferTest(dispatcher, otherOwner, "fs_upload_chunk", chunkIPCRequest(input, 0, "abcd"), nil)
	if denied.OK || denied.Error == nil || denied.Error.Code != ipc.CodeNotFound {
		t.Fatalf("cross-owner chunk = %+v, want not_found", denied)
	}
	first := decodeFSTransferTest[fsChunkUploadProgressDTO](t, dispatchFSTransferTest(dispatcher, ctx, "fs_upload_chunk", chunkIPCRequest(input, 2, "ij"), nil))
	if first.Received != 2 || first.Total != 10 {
		t.Fatalf("first progress = %+v", first)
	}
	second := decodeFSTransferTest[fsChunkUploadProgressDTO](t, dispatchFSTransferTest(dispatcher, ctx, "fs_upload_chunk", chunkIPCRequest(input, 0, "abcd"), nil))
	duplicate := decodeFSTransferTest[fsChunkUploadProgressDTO](t, dispatchFSTransferTest(dispatcher, ctx, "fs_upload_chunk", chunkIPCRequest(input, 2, "ij"), nil))
	if second.Received != 6 || duplicate.Received != second.Received {
		t.Fatalf("duplicate progress = %+v after %+v", duplicate, second)
	}
	if data, err := os.ReadFile(remote); err != nil || string(data) != "old" {
		t.Fatalf("target changed before commit: %q, %v", data, err)
	}
	decodeFSTransferTest[fsChunkUploadProgressDTO](t, dispatchFSTransferTest(dispatcher, ctx, "fs_upload_chunk", chunkIPCRequest(input, 1, "efgh"), nil))
	committed := decodeFSTransferTest[int64](t, dispatchFSTransferTest(dispatcher, ctx, "fs_upload_commit", input, nil))
	if committed != 10 {
		t.Fatalf("committed = %d, want 10", committed)
	}
	if data, err := os.ReadFile(remote); err != nil || string(data) != "abcdefghij" {
		t.Fatalf("committed target = %q, %v", data, err)
	}
	assertNoChunkIArtifacts(t, remote)
}

func TestFSChunkedIPCCancelAndAbortCleanPartialChunks(t *testing.T) {
	started := make(chan context.Context, 1)
	filesystem := &blockingTransferWriteFS{FileSystem: fslocal.New(), started: started}
	dispatcher, manager := newFSTransferTestDispatcher(t, filesystem, nil)
	ctx := ipc.WithUserID(t.Context(), "owner-a")
	connected, err := manager.ConnectFor(ctx, session.Asset{ID: "asset-chunked-cancel", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	remote := filepath.Join(t.TempDir(), "remote.bin")
	input := beginChunkIPC(t, dispatcher, ctx, fsChunkUploadRequest{
		SessionID: connected.ID, RemotePath: remote, TaskID: "chunk-cancel-task", AttemptID: "chunk-cancel-attempt", TotalSize: 4, ChunkSize: 4,
	})
	done := make(chan ipc.Response, 1)
	go func() {
		done <- dispatchFSTransferTest(dispatcher, ctx, "fs_upload_chunk", chunkIPCRequest(input, 0, "abcd"), nil)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("chunk did not reach the blocking writer")
	}
	accepted := decodeFSTransferTest[bool](t, dispatchFSTransferTest(dispatcher, ctx, "fs_cancel", fsCancelRequest{TaskID: input.TaskID, AttemptID: input.AttemptID}, nil))
	if !accepted {
		t.Fatal("running chunk cancellation was not accepted")
	}
	select {
	case response := <-done:
		if response.OK || response.Error == nil || response.Error.Code != ipc.Code("cancelled") || !errors.Is(response.Error, context.Canceled) {
			t.Fatalf("canceled chunk = %+v", response)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not unblock the chunk")
	}
	if response := dispatchFSTransferTest(dispatcher, ctx, "fs_upload_abort", input, nil); !response.OK {
		t.Fatalf("abort failed: %+v", response)
	}
	assertNoChunkIArtifacts(t, remote)
	if _, err := os.Stat(remote); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled upload exposed a target: %v", err)
	}
	if response := dispatchFSTransferTest(dispatcher, ctx, "fs_upload_abort", input, nil); !response.OK {
		t.Fatalf("idempotent abort failed: %+v", response)
	}
}

type commitCleanupFailureFS struct {
	*fslocal.FileSystem
	manifestDeletes int
}

func (f *commitCleanupFailureFS) Delete(ctx context.Context, path string, isDir bool) error {
	if strings.HasSuffix(path, ".manifest") {
		f.manifestDeletes++
		if f.manifestDeletes <= 2 {
			return errors.New("cleanup blocked")
		}
	}
	return f.FileSystem.Delete(ctx, path, isDir)
}

func TestFSChunkedIPCCommitCleanupFailureCanBeRetried(t *testing.T) {
	destination := &commitCleanupFailureFS{FileSystem: fslocal.New()}
	dispatcher, manager := newFSTransferTestDispatcher(t, destination, nil)
	ctx := ipc.WithUserID(t.Context(), "owner-a")
	connected, err := manager.ConnectFor(ctx, session.Asset{ID: "asset-chunked-cleanup", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	remote := filepath.Join(t.TempDir(), "remote.bin")
	input := beginChunkIPC(t, dispatcher, ctx, fsChunkUploadRequest{
		SessionID: connected.ID, RemotePath: remote, TaskID: "chunk-cleanup-task", AttemptID: "chunk-cleanup-attempt", TotalSize: 4, ChunkSize: 4,
	})
	decodeFSTransferTest[fsChunkUploadProgressDTO](t, dispatchFSTransferTest(dispatcher, ctx, "fs_upload_chunk", chunkIPCRequest(input, 0, "abcd"), nil))
	response := dispatchFSTransferTest(dispatcher, ctx, "fs_upload_commit", input, nil)
	if response.OK || response.Error == nil || !strings.Contains(response.Error.Error(), "cleanup blocked") || !strings.Contains(response.Error.Error(), "清理上传临时文件失败") {
		t.Fatalf("commit cleanup error = %+v", response)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(remote), "."+filepath.Base(remote)+".nexterm-*.manifest"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("commit artifact before retry = %v, %v", matches, err)
	}
	if response := dispatchFSTransferTest(dispatcher, ctx, "fs_upload_abort", input, nil); !response.OK {
		t.Fatalf("second abort failed: %+v", response)
	}
	assertNoChunkIArtifacts(t, remote)
}

func postChunkHTTP(t *testing.T, url, owner, command string, input any) ipc.Response {
	t.Helper()
	payload, err := json.Marshal(struct {
		Command string `json:"cmd"`
		Args    any    `json:"args"`
	}{Command: command, Args: input})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Owner", owner)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("HTTP status = %d", res.StatusCode)
	}
	var response ipc.Response
	if err := json.NewDecoder(res.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestFSChunkedIPCHTTPRoundTrip(t *testing.T) {
	uploads := newFSChunkUploadRegistry()
	dispatcher, manager := newFSTransferTestDispatcher(t, fslocal.New(), nil, uploads)
	ctx := ipc.WithUserID(t.Context(), "owner-a")
	connected, err := manager.ConnectFor(ctx, session.Asset{ID: "asset-chunked-http", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	rpcHandler := ipc.NewRPCHandler(dispatcher, ipc.Environment{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		owner := r.Header.Get("X-Test-Owner")
		if owner == "" {
			owner = "owner-a"
		}
		rpcHandler.ServeHTTP(w, r.WithContext(ipc.WithUserID(r.Context(), owner)))
	}))
	defer server.Close()

	remote := filepath.Join(t.TempDir(), "remote.bin")
	input := fsChunkUploadRequest{
		SessionID: connected.ID, RemotePath: remote, TaskID: "http-task", AttemptID: "http-attempt", TotalSize: 10, ChunkSize: 4,
	}
	begin := decodeFSTransferTest[fsChunkUploadBeginDTO](t, postChunkHTTP(t, server.URL, "owner-a", "fs_upload_begin", input))
	input.UploadID = begin.UploadID
	denied := postChunkHTTP(t, server.URL, "owner-b", "fs_upload_chunk", chunkIPCRequest(input, 0, "abcd"))
	if denied.OK || denied.Error == nil || denied.Error.Code != ipc.CodeNotFound {
		t.Fatalf("HTTP cross-owner chunk = %+v", denied)
	}
	for _, chunk := range []fsChunkUploadRequest{
		chunkIPCRequest(input, 2, "ij"),
		chunkIPCRequest(input, 0, "abcd"),
		chunkIPCRequest(input, 2, "ij"),
		chunkIPCRequest(input, 1, "efgh"),
	} {
		decodeFSTransferTest[fsChunkUploadProgressDTO](t, postChunkHTTP(t, server.URL, "owner-a", "fs_upload_chunk", chunk))
	}
	committed := decodeFSTransferTest[int64](t, postChunkHTTP(t, server.URL, "owner-a", "fs_upload_commit", input))
	if committed != 10 {
		t.Fatalf("HTTP committed = %d", committed)
	}
	if data, err := os.ReadFile(remote); err != nil || string(data) != "abcdefghij" {
		t.Fatalf("HTTP target = %q, %v", data, err)
	}
	assertNoChunkIArtifacts(t, remote)

	abortRemote := filepath.Join(t.TempDir(), "abort.bin")
	abortInput := fsChunkUploadRequest{
		SessionID: connected.ID, RemotePath: abortRemote, TaskID: "http-abort-task", AttemptID: "http-abort-attempt", TotalSize: 4, ChunkSize: 4,
	}
	abortBegin := decodeFSTransferTest[fsChunkUploadBeginDTO](t, postChunkHTTP(t, server.URL, "owner-a", "fs_upload_begin", abortInput))
	abortInput.UploadID = abortBegin.UploadID
	decodeFSTransferTest[fsChunkUploadProgressDTO](t, postChunkHTTP(t, server.URL, "owner-a", "fs_upload_chunk", chunkIPCRequest(abortInput, 0, "abcd")))
	if response := postChunkHTTP(t, server.URL, "owner-a", "fs_upload_abort", abortInput); !response.OK {
		t.Fatalf("HTTP abort = %+v", response)
	}
	assertNoChunkIArtifacts(t, abortRemote)
	if err := uploads.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
