//go:build darwin || linux

package production

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	fslocal "github.com/Hello-CTF/NexTerm/internal/fs/local"
	sshfs "github.com/Hello-CTF/NexTerm/internal/fs/ssh"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/local"
	"github.com/pkg/sftp"
)

type fsEventRecorder struct {
	mu     sync.Mutex
	events []ipc.Event
}

func (r *fsEventRecorder) Emit(_ context.Context, event ipc.Event) error {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
	return nil
}

func (r *fsEventRecorder) count(topic ipc.Topic) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, event := range r.events {
		if event.Event == topic {
			count++
		}
	}
	return count
}

func TestProductionFSCommandsUseLiveTransportAndEmitProgress(t *testing.T) {
	events := &fsEventRecorder{}
	connector := session.ConnectorFunc(func(_ context.Context, _ session.Asset, _ uint64) (base.Transport, error) {
		return local.NewWithConfig(local.Config{Shell: "/bin/sh"}), nil
	})
	manager := session.NewManager(session.Config{Connector: connector})
	production, err := NewProductionWithServices(Config{Events: events}, ProductionServices{Sessions: manager})
	if err != nil {
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })
	connected, err := manager.Connect(t.Context(), session.Asset{ID: "fs-local", Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	remote := filepath.Join(root, "remote.txt")
	response := production.Dispatcher.Dispatch(t.Context(), ipc.Request{
		Command: "fs_write", Args: json.RawMessage(`{"args":{"sessionId":"` + connected.ID + `","path":` + jsonString(remote) + `,"contentBase64":"` + base64.StdEncoding.EncodeToString([]byte("hello")) + `","backup":true}}`),
	}, production.Environment(""))
	requireProductionNull(t, response)
	response = production.Dispatcher.Dispatch(t.Context(), ipc.Request{Command: "fs_read", Args: json.RawMessage(`{"sessionId":"` + connected.ID + `","path":` + jsonString(remote) + `,"maxBytes":1024}`)}, production.Environment(""))
	var read fsReadDTO
	requireStoreTestResponse(t, response, &read)
	decoded, err := base64.StdEncoding.DecodeString(read.ContentBase64)
	if err != nil || string(decoded) != "hello" || read.Size != 5 {
		t.Fatalf("fs_read = %+v, %q, %v", read, decoded, err)
	}
	response = production.Dispatcher.Dispatch(t.Context(), ipc.Request{Command: "fs_list", Args: json.RawMessage(`{"sessionId":"` + connected.ID + `","path":` + jsonString(root) + `}`)}, production.Environment(""))
	var entries []fsEntryDTO
	requireStoreTestResponse(t, response, &entries)
	if len(entries) != 1 || entries[0].Name != "remote.txt" || entries[0].Mode == "" || entries[0].Mtime == 0 {
		t.Fatalf("fs_list = %+v", entries)
	}
	response = production.Dispatcher.Dispatch(t.Context(), ipc.Request{Command: "fs_checksum", Args: json.RawMessage(`{"sessionId":"` + connected.ID + `","path":` + jsonString(remote) + `,"algo":"sha256"}`)}, production.Environment(""))
	var checksum string
	requireStoreTestResponse(t, response, &checksum)
	if checksum != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Fatalf("checksum = %q", checksum)
	}

	source := filepath.Join(root, "source.bin")
	if err := os.WriteFile(source, []byte("upload"), 0o600); err != nil {
		t.Fatal(err)
	}
	uploaded := filepath.Join(root, "uploaded.bin")
	response = production.Dispatcher.Dispatch(t.Context(), ipc.Request{Command: "fs_upload", Args: json.RawMessage(`{"sessionId":"` + connected.ID + `","localPath":` + jsonString(source) + `,"remotePath":` + jsonString(uploaded) + `}`)}, production.Environment(""))
	var transferred int
	requireStoreTestResponse(t, response, &transferred)
	if transferred != 6 {
		t.Fatalf("upload bytes = %d", transferred)
	}
	downloaded := filepath.Join(root, "downloaded.bin")
	response = production.Dispatcher.Dispatch(t.Context(), ipc.Request{Command: "fs_download", Args: json.RawMessage(`{"sessionId":"` + connected.ID + `","remotePath":` + jsonString(uploaded) + `,"localPath":` + jsonString(downloaded) + `}`)}, production.Environment(""))
	requireStoreTestResponse(t, response, &transferred)
	if data, err := os.ReadFile(downloaded); err != nil || string(data) != "upload" {
		t.Fatalf("downloaded = %q, %v", data, err)
	}
	if events.count(ipc.TopicFSProgress) < 2 {
		t.Fatalf("progress events = %d", events.count(ipc.TopicFSProgress))
	}
	response = production.Dispatcher.Dispatch(t.Context(), ipc.Request{Command: "fs_pack_download", Args: json.RawMessage(`{"sessionId":"` + connected.ID + `","remotePath":` + jsonString(root) + `,"localPath":` + jsonString(filepath.Join(root, "pack.tgz")) + `}`)}, production.Environment(""))
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("local pack capability = %+v", response)
	}
	response = production.Dispatcher.Dispatch(t.Context(), ipc.Request{Command: "fs_delete", Args: json.RawMessage(`{"sessionId":"` + connected.ID + `","path":` + jsonString(remote) + `,"isDir":false}`)}, production.Environment(""))
	requireProductionNull(t, response)
}

func TestProductionFSReadWithoutMaxBytesAppliesDefaultLimit(t *testing.T) {
	events := &fsEventRecorder{}
	connector := session.ConnectorFunc(func(_ context.Context, _ session.Asset, _ uint64) (base.Transport, error) {
		return local.NewWithConfig(local.Config{Shell: "/bin/sh"}), nil
	})
	manager := session.NewManager(session.Config{Connector: connector})
	production, err := NewProductionWithServices(Config{Events: events}, ProductionServices{Sessions: manager})
	if err != nil {
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })
	connected, err := manager.Connect(t.Context(), session.Asset{ID: "fs-read-default", Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	remote := filepath.Join(root, "remote.txt")
	if err := os.WriteFile(remote, []byte("hello without limit"), 0o600); err != nil {
		t.Fatal(err)
	}
	read := func(args string) ipc.Response {
		return production.Dispatcher.Dispatch(t.Context(), ipc.Request{Command: "fs_read", Args: json.RawMessage(args)}, production.Environment(""))
	}
	response := read(`{"sessionId":"` + connected.ID + `","path":` + jsonString(remote) + `}`)
	var result fsReadDTO
	requireStoreTestResponse(t, response, &result)
	decoded, err := base64.StdEncoding.DecodeString(result.ContentBase64)
	if err != nil || string(decoded) != "hello without limit" || result.Size != len("hello without limit") {
		t.Fatalf("fs_read without maxBytes = %+v, %q, %v", result, decoded, err)
	}
	if response = read(`{"sessionId":"` + connected.ID + `","path":` + jsonString(remote) + `,"maxBytes":4}`); response.OK || response.Error == nil {
		t.Fatalf("explicit small limit must fail: %+v", response)
	}
	if response = read(`{"sessionId":"` + connected.ID + `","path":` + jsonString(remote) + `,"maxBytes":-1}`); response.OK || response.Error == nil {
		t.Fatalf("negative maxBytes must fail: %+v", response)
	}
	oversized := filepath.Join(root, "oversized.bin")
	if err := os.WriteFile(oversized, make([]byte, fsReadDefaultMaxBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if response = read(`{"sessionId":"` + connected.ID + `","path":` + jsonString(oversized) + `}`); response.OK || response.Error == nil {
		t.Fatalf("default limit must stay bounded: %+v", response)
	}
}

func TestProductionFSUploadFailureEmitsErrorProgress(t *testing.T) {
	events := &fsEventRecorder{}
	connector := session.ConnectorFunc(func(_ context.Context, _ session.Asset, _ uint64) (base.Transport, error) {
		return local.NewWithConfig(local.Config{Shell: "/bin/sh"}), nil
	})
	manager := session.NewManager(session.Config{Connector: connector})
	production, err := NewProductionWithServices(Config{Events: events}, ProductionServices{Sessions: manager})
	if err != nil {
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })
	connected, err := manager.Connect(t.Context(), session.Asset{ID: "fs-fail", Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "missing.bin")
	response := production.Dispatcher.Dispatch(t.Context(), ipc.Request{Command: "fs_upload", Args: json.RawMessage(`{"sessionId":"` + connected.ID + `","localPath":` + jsonString(missing) + `,"remotePath":` + jsonString(filepath.Join(t.TempDir(), "remote.bin")) + `}`)}, production.Environment(""))
	if response.OK || response.Error == nil {
		t.Fatalf("missing upload source = %+v", response)
	}
	var failures, dones int
	for _, event := range events.events {
		if event.Event != ipc.TopicFSProgress {
			continue
		}
		progress, ok := event.Payload.(fslocal.Progress)
		if !ok {
			t.Fatalf("progress payload = %#v", event.Payload)
		}
		if progress.Done {
			dones++
			if progress.Error == "" {
				t.Fatalf("failure event lacks error: %#v", progress)
			}
		}
		if progress.Error != "" {
			failures++
		}
	}
	if failures != 1 || dones != 1 {
		t.Fatalf("failure events = %d, done events = %d", failures, dones)
	}
}

func jsonString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

type fakeStagedBlobResolver struct {
	dir     string
	userIDs []string
}

func (f *fakeStagedBlobResolver) ResolveStagedPath(rel, userID string) (string, error) {
	f.userIDs = append(f.userIDs, userID)
	id, name, found := strings.Cut(rel, "/")
	if !found || id != "staged-id" || name == "" {
		return "", errors.New("staged blob missing")
	}
	return filepath.Join(f.dir, name), nil
}

// 服务端装配下 fs_upload/fs_download 的 localPath 是 Stage/Reserve 响应里的相对
// id/name, 必须经 StagedBlobResolver 解析为暂存文件; 绝对路径与未知引用一律拒绝。
func TestProductionFSStagedLocalPathResolution(t *testing.T) {
	events := &fsEventRecorder{}
	connector := session.ConnectorFunc(func(_ context.Context, _ session.Asset, _ uint64) (base.Transport, error) {
		return local.NewWithConfig(local.Config{Shell: "/bin/sh"}), nil
	})
	manager := session.NewManager(session.Config{Connector: connector})
	staged := &fakeStagedBlobResolver{dir: t.TempDir()}
	production, err := NewProductionWithServices(Config{Events: events}, ProductionServices{Sessions: manager, stagedBlobs: staged})
	if err != nil {
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })
	connected, err := manager.Connect(t.Context(), session.Asset{ID: "fs-staged", Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	ctx := ipc.WithUserID(t.Context(), "user-1")
	dispatch := func(command, args string) ipc.Response {
		return production.Dispatcher.Dispatch(ctx, ipc.Request{Command: command, Args: json.RawMessage(args)}, production.Environment(""))
	}

	if err := os.WriteFile(filepath.Join(staged.dir, "upload.bin"), []byte("staged-upload"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	remote := filepath.Join(root, "uploaded.bin")
	response := dispatch("fs_upload", `{"sessionId":"`+connected.ID+`","localPath":"staged-id/upload.bin","remotePath":`+jsonString(remote)+`}`)
	var transferred int
	requireStoreTestResponse(t, response, &transferred)
	if transferred != len("staged-upload") {
		t.Fatalf("upload bytes = %d", transferred)
	}
	if data, err := os.ReadFile(remote); err != nil || string(data) != "staged-upload" {
		t.Fatalf("remote content = %q, %v", data, err)
	}
	if len(staged.userIDs) != 1 || staged.userIDs[0] != "user-1" {
		t.Fatalf("resolver userIDs = %v", staged.userIDs)
	}

	response = dispatch("fs_download", `{"sessionId":"`+connected.ID+`","remotePath":`+jsonString(remote)+`,"localPath":"staged-id/download.bin"}`)
	requireStoreTestResponse(t, response, &transferred)
	stagedTarget := filepath.Join(staged.dir, "download.bin")
	if data, err := os.ReadFile(stagedTarget); err != nil || string(data) != "staged-upload" {
		t.Fatalf("staged download target = %q, %v", data, err)
	}

	calls := len(staged.userIDs)
	response = dispatch("fs_upload", `{"sessionId":"`+connected.ID+`","localPath":`+jsonString(filepath.Join(staged.dir, "upload.bin"))+`,"remotePath":`+jsonString(filepath.Join(root, "abs.bin"))+`}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("absolute localPath = %+v, want bad_param", response)
	}
	if len(staged.userIDs) != calls {
		t.Fatal("absolute localPath must not reach the resolver")
	}

	response = dispatch("fs_download", `{"sessionId":"`+connected.ID+`","remotePath":`+jsonString(remote)+`,"localPath":"other-id/download.bin"}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeNotFound {
		t.Fatalf("unknown staged ref = %+v, want not_found", response)
	}
}

type rangeFakeTransport struct {
	fs base.FileSystem
}

func (t *rangeFakeTransport) Kind() string       { return "ssh" }
func (t *rangeFakeTransport) Generation() uint64 { return 1 }
func (t *rangeFakeTransport) Exec(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
	return base.ExecResult{}, base.ErrUnsupported
}
func (t *rangeFakeTransport) Ping(context.Context) (time.Duration, error) { return 0, nil }
func (t *rangeFakeTransport) IsAlive() bool                               { return true }
func (t *rangeFakeTransport) Close() error                                { return nil }
func (t *rangeFakeTransport) FileSystem(context.Context) (base.FileSystem, error) {
	return t.fs, nil
}

func newRangeTestSFTP(t *testing.T) *sshfs.FS {
	t.Helper()
	root := t.TempDir()
	clientSide, serverSide := net.Pipe()
	server, err := sftp.NewServer(serverSide, sftp.WithServerWorkingDirectory(root))
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve() }()
	client, err := sftp.NewClientPipe(clientSide, clientSide)
	if err != nil {
		clientSide.Close()
		serverSide.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		client.Close()
		server.Close()
		clientSide.Close()
		serverSide.Close()
		select {
		case <-serveDone:
		case <-time.After(2 * time.Second):
			t.Error("SFTP test server did not stop")
		}
	})
	return sshfs.New(client, nil)
}

func TestProductionFSReadRange(t *testing.T) {
	events := &fsEventRecorder{}
	filesystem := newRangeTestSFTP(t)
	connector := session.ConnectorFunc(func(_ context.Context, _ session.Asset, _ uint64) (base.Transport, error) {
		return &rangeFakeTransport{fs: filesystem}, nil
	})
	manager := session.NewManager(session.Config{Connector: connector})
	production, err := NewProductionWithServices(Config{Events: events}, ProductionServices{Sessions: manager})
	if err != nil {
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })
	connected, err := manager.Connect(t.Context(), session.Asset{ID: "fs-range", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("0123456789", 100)
	if err := filesystem.WriteFile(t.Context(), "app.log", []byte(content), false); err != nil {
		t.Fatal(err)
	}
	dispatch := func(args string) ipc.Response {
		return production.Dispatcher.Dispatch(t.Context(), ipc.Request{Command: "fs_read_range", Args: json.RawMessage(args)}, production.Environment(""))
	}

	response := dispatch(`{"sessionId":"` + connected.ID + `","path":"app.log","offset":100,"maxBytes":50}`)
	var read fsRangeReadDTO
	requireStoreTestResponse(t, response, &read)
	decoded, err := base64.StdEncoding.DecodeString(read.ContentBase64)
	if err != nil || string(decoded) != content[100:150] {
		t.Fatalf("fs_read_range chunk = %q, %v", decoded, err)
	}
	if read.Offset != 100 || read.Size != 1000 || !read.Truncated {
		t.Fatalf("fs_read_range meta = %+v", read)
	}

	response = dispatch(`{"sessionId":"` + connected.ID + `","path":"app.log","offset":950,"maxBytes":100}`)
	requireStoreTestResponse(t, response, &read)
	decoded, _ = base64.StdEncoding.DecodeString(read.ContentBase64)
	if string(decoded) != content[950:] || read.Truncated {
		t.Fatalf("fs_read_range tail = %q, %+v", decoded, read)
	}

	response = dispatch(`{"sessionId":"` + connected.ID + `","path":"app.log","offset":-1}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("negative offset = %+v, want bad_param", response)
	}
	response = dispatch(`{"sessionId":"` + connected.ID + `","path":"app.log","offset":0,"maxBytes":` + strconv.FormatInt(fsRangeReadMaxBytes+1, 10) + `}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("over-cap maxBytes = %+v, want bad_param", response)
	}
}

func TestProductionFSReadRangeUnsupportedForNonSSH(t *testing.T) {
	events := &fsEventRecorder{}
	connector := session.ConnectorFunc(func(_ context.Context, _ session.Asset, _ uint64) (base.Transport, error) {
		return local.NewWithConfig(local.Config{Shell: "/bin/sh"}), nil
	})
	manager := session.NewManager(session.Config{Connector: connector})
	production, err := NewProductionWithServices(Config{Events: events}, ProductionServices{Sessions: manager})
	if err != nil {
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })
	connected, err := manager.Connect(t.Context(), session.Asset{ID: "fs-range-local", Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	response := production.Dispatcher.Dispatch(t.Context(), ipc.Request{Command: "fs_read_range", Args: json.RawMessage(`{"sessionId":"` + connected.ID + `","path":"/tmp/app.log","offset":0}`)}, production.Environment(""))
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("local fs_read_range = %+v, want unsupported", response)
	}
}
