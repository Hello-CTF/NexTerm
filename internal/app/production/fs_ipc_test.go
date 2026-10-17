//go:build darwin || linux

package production

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/deviceproto"
	fslocal "github.com/Hello-CTF/NexTerm/internal/fs/local"
	sshfs "github.com/Hello-CTF/NexTerm/internal/fs/ssh"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/server"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/device"
	"github.com/Hello-CTF/NexTerm/internal/transport/local"
	transportwinrm "github.com/Hello-CTF/NexTerm/internal/transport/winrm"
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
	packed := filepath.Join(root, "pack.tgz")
	response = production.Dispatcher.Dispatch(t.Context(), ipc.Request{Command: "fs_pack_download", Args: json.RawMessage(`{"sessionId":"` + connected.ID + `","remotePath":` + jsonString(root) + `,"localPath":` + jsonString(packed) + `}`)}, production.Environment(""))
	var packedBytes int
	requireStoreTestResponse(t, response, &packedBytes)
	info, err := os.Stat(packed)
	if err != nil || info.Size() != int64(packedBytes) {
		t.Fatalf("packed archive size = %d, stat = %v", packedBytes, err)
	}
	magic := make([]byte, 2)
	archive, err := os.Open(packed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(archive, magic); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if magic[0] != 0x1f || magic[1] != 0x8b {
		t.Fatalf("packed archive magic = %x, want gzip", magic)
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

func TestProductionFSPathAndSessionErrorsAreDistinct(t *testing.T) {
	connector := session.ConnectorFunc(func(_ context.Context, _ session.Asset, _ uint64) (base.Transport, error) {
		return local.NewWithConfig(local.Config{Shell: "/bin/sh"}), nil
	})
	manager := session.NewManager(session.Config{Connector: connector})
	production, err := NewProductionWithServices(Config{}, ProductionServices{Sessions: manager})
	if err != nil {
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })
	connected, err := manager.Connect(t.Context(), session.Asset{ID: "fs-error-resource", Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	dispatch := func(path string) ipc.Response {
		return production.Dispatcher.Dispatch(t.Context(), ipc.Request{
			Command: "fs_list", Args: json.RawMessage(`{"sessionId":"` + connected.ID + `","path":` + jsonString(path) + `}`),
		}, production.Environment(""))
	}
	requireResource := func(t *testing.T, response ipc.Response, code ipc.Code, resource string) {
		t.Helper()
		if response.OK || response.Error == nil || response.Error.Code != code {
			t.Fatalf("fs_list = %+v, want %s", response, code)
		}
		detail, ok := response.Error.Detail.(map[string]any)
		if !ok || detail["resource"] != resource {
			t.Fatalf("fs_list detail = %#v, want resource %q", response.Error.Detail, resource)
		}
	}

	t.Run("missing path", func(t *testing.T) {
		requireResource(t, dispatch(filepath.Join(root, "missing")), ipc.CodeNotFound, "path")
		if response := dispatch(root); !response.OK || response.Error != nil {
			t.Fatalf("valid path after a path error = %+v", response)
		}
	})
	t.Run("deleted session", func(t *testing.T) {
		if err := manager.Disconnect(connected.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.Session(connected.ID); !errors.Is(err, session.ErrSessionNotFound) {
			t.Fatalf("session after Disconnect = %v, want actual deletion", err)
		}
		requireResource(t, dispatch(root), ipc.CodeNotFound, "session")
	})
}

type rootsFakeFileSystem struct {
	base.FileSystem
	roots base.FileSystemRoots
	err   error
}

func (f rootsFakeFileSystem) Roots(context.Context) (base.FileSystemRoots, error) {
	return f.roots, f.err
}

func TestProductionFSRootsUsesLiveLocalTransport(t *testing.T) {
	connector := session.ConnectorFunc(func(_ context.Context, _ session.Asset, _ uint64) (base.Transport, error) {
		return local.NewWithConfig(local.Config{Shell: "/bin/sh"}), nil
	})
	manager := session.NewManager(session.Config{Connector: connector})
	production, err := NewProductionWithServices(Config{}, ProductionServices{Sessions: manager})
	if err != nil {
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })
	connected, err := manager.Connect(t.Context(), session.Asset{ID: "fs-roots-local", Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	dispatch := func(command, args string) ipc.Response {
		return production.Dispatcher.Dispatch(t.Context(), ipc.Request{
			Command: command, Args: json.RawMessage(args),
		}, production.Environment(""))
	}

	root := t.TempDir()
	response := dispatch("fs_list", `{"sessionId":"`+connected.ID+`","path":`+jsonString(root)+`}`)
	var entries []fsEntryDTO
	requireStoreTestResponse(t, response, &entries)
	if len(entries) != 0 {
		t.Fatalf("fs_list before roots = %+v, want the old empty-directory result", entries)
	}

	response = dispatch("fs_roots", `{"sessionId":"`+connected.ID+`"}`)
	var roots fsRootsDTO
	requireStoreTestResponse(t, response, &roots)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	home, err = filepath.Abs(home)
	if err != nil {
		t.Fatal(err)
	}
	if !roots.Supported || roots.PathStyle != string(base.PathStylePOSIX) || roots.HomePath == nil || *roots.HomePath != home {
		t.Fatalf("local fs_roots = %+v, want POSIX and home %q", roots, home)
	}
	if len(roots.Roots) != 1 || roots.Roots[0].Path != "/" || roots.Roots[0].Label != "/" {
		t.Fatalf("local roots = %+v, want the real POSIX root", roots.Roots)
	}

	if err := manager.Disconnect(connected.ID); err != nil {
		t.Fatal(err)
	}
	response = dispatch("fs_roots", `{"sessionId":"`+connected.ID+`"}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeNotFound {
		t.Fatalf("deleted-session fs_roots = %+v, want not_found", response)
	}
	detail, ok := response.Error.Detail.(map[string]any)
	if !ok || detail["resource"] != "session" {
		t.Fatalf("deleted-session fs_roots detail = %#v, want session", response.Error.Detail)
	}
}

func TestProductionFSRootsDTOAndFallbacks(t *testing.T) {
	windowsRoots := base.FileSystemRoots{
		PathStyle: base.PathStyleWindows,
		Roots: []base.FileRoot{
			{Path: `C:\`, Label: "C:"},
			{Path: `D:\`, Label: "D:"},
		},
		HomePath:  `C:\Users\test`,
		Supported: true,
	}
	for _, testCase := range []struct {
		name       string
		filesystem base.FileSystem
		want       string
	}{
		{
			name:       "windows DTO",
			filesystem: rootsFakeFileSystem{FileSystem: fakeProviderFileSystem{}, roots: windowsRoots},
			want:       `{"pathStyle":"windows","roots":[{"path":"C:\\","label":"C:"},{"path":"D:\\","label":"D:"}],"homePath":"C:\\Users\\test","supported":true}`,
		},
		{
			name: "supported empty roots",
			filesystem: rootsFakeFileSystem{FileSystem: fakeProviderFileSystem{}, roots: base.FileSystemRoots{
				PathStyle: base.PathStyleWindows, Roots: []base.FileRoot{}, Supported: true,
			}},
			want: `{"pathStyle":"windows","roots":[],"homePath":null,"supported":true}`,
		},
		{
			name:       "missing roots capability",
			filesystem: fakeProviderFileSystem{},
			want:       `{"pathStyle":"unknown","roots":[],"homePath":null,"supported":false}`,
		},
		{
			name:       "explicit unsupported",
			filesystem: rootsFakeFileSystem{FileSystem: fakeProviderFileSystem{}, err: base.ErrUnsupported},
			want:       `{"pathStyle":"unknown","roots":[],"homePath":null,"supported":false}`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			connector := session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
				return fakeProviderTransport{kind: "ssh", fs: testCase.filesystem}, nil
			})
			manager := session.NewManager(session.Config{Connector: connector})
			dispatcher := ipc.NewDispatcher()
			if err := registerFSCommands(dispatcher, manager, nil); err != nil {
				t.Fatal(err)
			}
			connected, err := manager.Connect(t.Context(), session.Asset{ID: "fs-roots-" + testCase.name, Kind: session.KindSSH})
			if err != nil {
				t.Fatal(err)
			}
			response := dispatcher.Dispatch(t.Context(), ipc.Request{
				Command: "fs_roots", Args: json.RawMessage(`{"sessionId":"` + connected.ID + `"}`),
			}, ipc.Environment{})
			if !response.OK || string(response.Data) != testCase.want {
				t.Fatalf("fs_roots = %s, error = %+v, want %s", response.Data, response.Error, testCase.want)
			}
		})
	}
}

func TestProductionFSWindowsPathErrorsDoNotPoisonRoots(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		kind         string
		path         string
		err          error
		code         ipc.Code
		reason       string
		message      string
		pathResource bool
	}{
		{name: "missing drive", kind: "local", path: `Z:\`, err: &os.PathError{Op: "list", Path: `Z:\`, Err: os.ErrNotExist}, code: ipc.CodeNotFound, message: "file does not exist", pathResource: true},
		{name: "permission denied", kind: "local", path: `E:\`, err: &os.PathError{Op: "list", Path: `E:\`, Err: os.ErrPermission}, code: ipc.CodeForbidden, reason: "permission_denied", message: "permission denied", pathResource: true},
		{name: "no media", kind: "local", path: `F:\`, err: &os.PathError{Op: "list", Path: `F:\`, Err: base.ErrNoMedia}, code: ipc.CodeIO, reason: "no_media", message: "filesystem media not ready", pathResource: true},
		{name: "ssh no media", kind: "ssh", path: `F:\`, err: fmt.Errorf("SSH list F:\\: %w", base.ErrNoMedia), code: ipc.CodeIO, reason: "no_media", message: "filesystem media not ready", pathResource: true},
		{name: "ssh permission denied", kind: "ssh", path: `E:\`, err: fmt.Errorf("SSH list E:\\: %w", fs.ErrPermission), code: ipc.CodeForbidden, reason: "permission_denied", message: "permission denied", pathResource: true},
		{name: "ssh bridge internal", kind: "ssh", path: `C:\`, err: errors.New("SSH bridge internal for C:\\"), code: ipc.CodeSFTP, message: "SSH bridge internal"},
		{name: "winrm no media", kind: "winrm", path: `F:\`, err: fmt.Errorf("WinRM file operation: device not ready for F:\\: %w", base.ErrNoMedia), code: ipc.CodeIO, reason: "no_media", message: "filesystem media not ready", pathResource: true},
		{name: "winrm permission denied", kind: "winrm", path: `E:\`, err: fmt.Errorf("WinRM file operation: access denied for E:\\: %w", fs.ErrPermission), code: ipc.CodeForbidden, reason: "permission_denied", message: "permission denied", pathResource: true},
		{name: "winrm bridge internal", kind: "winrm", path: `C:\`, err: errors.New("WinRM bridge internal for C:\\"), code: ipc.CodeWinRM, message: "WinRM bridge internal"},
		{name: "POSIX missing path named no media", kind: "ssh", path: "/tmp/no media", err: fmt.Errorf("SFTP list /tmp/no media: %w", fs.ErrNotExist), code: ipc.CodeNotFound, message: "file does not exist", pathResource: true},
		{name: "Windows missing path named access denied", kind: "ssh", path: `D:\access denied`, err: fmt.Errorf("SFTP list D:\\access denied: %w", fs.ErrNotExist), code: ipc.CodeNotFound, message: "file does not exist", pathResource: true},
		{name: "Windows permission path named no media", kind: "ssh", path: `D:\no media`, err: fmt.Errorf("SFTP list D:\\no media: %w", fs.ErrPermission), code: ipc.CodeForbidden, reason: "permission_denied", message: "permission denied", pathResource: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			filesystem := rootsFakeFileSystem{
				FileSystem: fakeProviderFileSystem{err: testCase.err},
				roots: base.FileSystemRoots{
					PathStyle: base.PathStyleWindows,
					Roots:     []base.FileRoot{{Path: `C:\`, Label: "C:"}, {Path: `D:\`, Label: "D:"}},
					Supported: true,
				},
			}
			connector := session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
				return fakeProviderTransport{kind: testCase.kind, fs: filesystem}, nil
			})
			manager := session.NewManager(session.Config{Connector: connector})
			dispatcher := ipc.NewDispatcher()
			if err := registerFSCommands(dispatcher, manager, nil); err != nil {
				t.Fatal(err)
			}
			connected, err := manager.Connect(t.Context(), session.Asset{ID: "fs-roots-error-" + testCase.name, Kind: testCase.kind})
			if err != nil {
				t.Fatal(err)
			}
			response := dispatcher.Dispatch(t.Context(), ipc.Request{
				Command: "fs_list", Args: json.RawMessage(`{"sessionId":"` + connected.ID + `","path":` + jsonString(testCase.path) + `}`),
			}, ipc.Environment{})
			if response.OK || response.Error == nil || response.Error.Code != testCase.code {
				t.Fatalf("fs_list = %+v, want %s", response, testCase.code)
			}
			if !strings.Contains(response.Error.Message, testCase.path) || !strings.Contains(response.Error.Message, testCase.message) {
				t.Fatalf("fs_list diagnostic = %q, want path and %q", response.Error.Message, testCase.message)
			}
			if testCase.pathResource {
				detail, ok := response.Error.Detail.(map[string]any)
				if !ok || detail["resource"] != "path" {
					t.Fatalf("fs_list detail = %#v, want path", response.Error.Detail)
				}
				if testCase.reason != "" && detail["reason"] != testCase.reason {
					t.Fatalf("fs_list reason = %#v, want %q", detail["reason"], testCase.reason)
				}
			} else if response.Error.Detail != nil {
				t.Fatalf("bridge/internal fs_list detail = %#v, want no path classification", response.Error.Detail)
			}

			response = dispatcher.Dispatch(t.Context(), ipc.Request{
				Command: "fs_roots", Args: json.RawMessage(`{"sessionId":"` + connected.ID + `"}`),
			}, ipc.Environment{})
			var roots fsRootsDTO
			requireStoreTestResponse(t, response, &roots)
			if !roots.Supported || len(roots.Roots) != 2 {
				t.Fatalf("fs_roots after one drive error = %+v, want both drives", roots)
			}
		})
	}
}

type productionDeviceFSBridge struct {
	dials   atomic.Int32
	roots   base.FileSystemRoots
	entries []base.FileEntry
	listErr error
}

func (b *productionDeviceFSBridge) dial(_ context.Context, purpose string) (net.Conn, error) {
	if purpose != deviceproto.PurposeFile {
		return nil, fmt.Errorf("unexpected bridge purpose %q", purpose)
	}
	b.dials.Add(1)
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		kind, _, err := deviceproto.ReadFrame(server)
		if err != nil {
			return
		}
		switch kind {
		case deviceproto.FrameRoots:
			_ = deviceproto.WriteMessage(server, deviceproto.FrameRootsReply, b.roots)
		case deviceproto.FrameList:
			if b.listErr != nil {
				_ = deviceproto.WriteError(server, b.listErr)
				return
			}
			_ = deviceproto.WriteMessage(server, deviceproto.FrameListed, deviceproto.ListChunk{Entries: b.entries})
			_ = deviceproto.WriteFrame(server, deviceproto.FrameEnd, nil)
		default:
			_ = deviceproto.WriteError(server, fmt.Errorf("unexpected request frame %d", kind))
		}
	}()
	return client, nil
}

func TestProductionFSDeviceSessionOwnerEnforced(t *testing.T) {
	bridge := &productionDeviceFSBridge{roots: base.FileSystemRoots{
		PathStyle: base.PathStyleWindows,
		Roots:     []base.FileRoot{{Path: `C:\`, Label: "C:"}},
		Supported: true,
	}}
	transport := device.New("dev-1", "digest", bridge.dial, 1, "windows")
	manager := session.NewManager(session.Config{Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
		return transport, nil
	})})
	t.Cleanup(func() { _ = manager.Close() })
	dispatcher := ipc.NewDispatcher()
	if err := registerFSCommands(dispatcher, manager, nil); err != nil {
		t.Fatal(err)
	}
	alice := ipc.WithUserID(t.Context(), "alice")
	bob := ipc.WithUserID(t.Context(), "bob")
	connected, err := manager.Connect(alice, session.Asset{ID: "device/dev-1", Kind: session.KindDevice})
	if err != nil {
		t.Fatal(err)
	}
	dispatch := func(ctx context.Context, command string) ipc.Response {
		return dispatcher.Dispatch(ctx, ipc.Request{
			Command: command,
			Args:    json.RawMessage(`{"sessionId":"` + connected.ID + `","path":` + jsonString(`C:\`) + `}`),
		}, ipc.Environment{})
	}

	for _, command := range []string{"fs_roots", "fs_list"} {
		response := dispatch(bob, command)
		if response.OK || response.Error == nil || response.Error.Code != ipc.CodeForbidden || !errors.Is(response.Error, session.ErrForbidden) {
			t.Errorf("Bob %s = %+v, want ErrForbidden", command, response)
		}
	}
	if got := bridge.dials.Load(); got != 0 {
		t.Errorf("foreign requests caused %d device bridge dials, want 0", got)
	}
	for _, command := range []string{"fs_roots", "fs_list"} {
		if response := dispatch(alice, command); !response.OK || response.Error != nil {
			t.Errorf("owner %s = %+v", command, response)
		}
	}
	if got := bridge.dials.Load(); got != 2 {
		t.Fatalf("owner requests caused %d device bridge dials, want 2", got)
	}
}

func TestProductionFSDevicePathErrorsDoNotPoisonRoots(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		path         string
		err          error
		sentinel     error
		code         ipc.Code
		reason       string
		pathResource bool
	}{
		{name: "no media", path: `F:\`, err: fmt.Errorf("list F:\\: %w", base.ErrNoMedia), sentinel: base.ErrNoMedia, code: ipc.CodeIO, reason: "no_media", pathResource: true},
		{name: "permission denied", path: `E:\`, err: &os.PathError{Op: "list", Path: `E:\`, Err: fs.ErrPermission}, sentinel: fs.ErrPermission, code: ipc.CodeForbidden, reason: "permission_denied", pathResource: true},
		{name: "bridge failure", path: `C:\`, err: errors.New("bridge unavailable"), code: ipc.CodeIO},
		{name: "agent internal failure", path: `C:\`, err: errors.New("agent internal failure"), code: ipc.CodeIO},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			bridge := &productionDeviceFSBridge{
				roots: base.FileSystemRoots{
					PathStyle: base.PathStyleWindows,
					Roots:     []base.FileRoot{{Path: `C:\`, Label: "C:"}, {Path: `D:\`, Label: "D:"}},
					Supported: true,
				},
				listErr: testCase.err,
			}
			transport := device.New("dev-1", "digest", bridge.dial, 1, "windows")
			manager := session.NewManager(session.Config{Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
				return transport, nil
			})})
			t.Cleanup(func() { _ = manager.Close() })
			dispatcher := ipc.NewDispatcher()
			if err := registerFSCommands(dispatcher, manager, nil); err != nil {
				t.Fatal(err)
			}
			connected, err := manager.Connect(t.Context(), session.Asset{ID: "device/dev-1-" + testCase.name, Kind: session.KindDevice})
			if err != nil {
				t.Fatal(err)
			}
			response := dispatcher.Dispatch(t.Context(), ipc.Request{
				Command: "fs_list",
				Args:    json.RawMessage(`{"sessionId":"` + connected.ID + `","path":` + jsonString(testCase.path) + `}`),
			}, ipc.Environment{})
			if response.OK || response.Error == nil || response.Error.Code != testCase.code {
				t.Fatalf("fs_list = %+v, want %s", response, testCase.code)
			}
			if !strings.Contains(response.Error.Message, testCase.err.Error()) {
				t.Fatalf("fs_list diagnostic = %q, want %q", response.Error.Message, testCase.err)
			}
			if testCase.sentinel != nil && !errors.Is(response.Error, testCase.sentinel) {
				t.Fatalf("fs_list error = %v, want errors.Is %v", response.Error, testCase.sentinel)
			}
			if testCase.pathResource {
				detail, ok := response.Error.Detail.(map[string]any)
				if !ok || detail["resource"] != "path" || detail["reason"] != testCase.reason {
					t.Fatalf("fs_list detail = %#v, want path/%s", response.Error.Detail, testCase.reason)
				}
			} else if response.Error.Detail != nil {
				t.Fatalf("bridge/internal fs_list detail = %#v, want no path classification", response.Error.Detail)
			}

			response = dispatcher.Dispatch(t.Context(), ipc.Request{
				Command: "fs_roots", Args: json.RawMessage(`{"sessionId":"` + connected.ID + `"}`),
			}, ipc.Environment{})
			var roots fsRootsDTO
			requireStoreTestResponse(t, response, &roots)
			if !roots.Supported || len(roots.Roots) != 2 {
				t.Fatalf("fs_roots after device path error = %+v, want both drives", roots)
			}
			if got := bridge.dials.Load(); got != 2 {
				t.Fatalf("device bridge dials = %d, want separate list and roots dials", got)
			}
		})
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

// In the server-side assembly, localPath of fs_upload/fs_download is the relative id/name from a Stage/Reserve response
// and must be resolved to a staged file via StagedBlobResolver; absolute paths and unknown references are always refused.
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
	ctx := ipc.WithUserID(t.Context(), "user-1")
	connected, err := manager.Connect(ctx, session.Asset{ID: "fs-staged", Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
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

type extractFakeTransport struct {
	fs base.FileSystem
}

func (t *extractFakeTransport) Kind() string       { return "device" }
func (t *extractFakeTransport) Generation() uint64 { return 1 }
func (t *extractFakeTransport) Exec(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
	return base.ExecResult{}, base.ErrUnsupported
}
func (t *extractFakeTransport) Ping(context.Context) (time.Duration, error) { return 0, nil }
func (t *extractFakeTransport) IsAlive() bool                               { return true }
func (t *extractFakeTransport) Close() error                                { return nil }
func (t *extractFakeTransport) FileSystem(context.Context) (base.FileSystem, error) {
	return t.fs, nil
}

type extractFakeFS struct {
	base.FileSystem
	target string
	err    error
}

func (f *extractFakeFS) Extract(context.Context, string) (string, error) {
	return f.target, f.err
}

func TestProductionFSExtract(t *testing.T) {
	events := &fsEventRecorder{}
	filesystem := &extractFakeFS{target: "/data/bundle"}
	connector := session.ConnectorFunc(func(_ context.Context, _ session.Asset, _ uint64) (base.Transport, error) {
		return &extractFakeTransport{fs: filesystem}, nil
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
	connected, err := manager.Connect(t.Context(), session.Asset{ID: "device/dev-1", Kind: session.KindDevice})
	if err != nil {
		t.Fatal(err)
	}
	dispatch := func() ipc.Response {
		return production.Dispatcher.Dispatch(t.Context(), ipc.Request{Command: "fs_extract", Args: json.RawMessage(`{"sessionId":"` + connected.ID + `","path":"/data/bundle.tar"}`)}, production.Environment(""))
	}

	response := dispatch()
	var target string
	requireStoreTestResponse(t, response, &target)
	if target != "/data/bundle" {
		t.Fatalf("fs_extract = %q, want the extractor's target directory", target)
	}

	filesystem.err = errors.New("extract device archive: remote command exited 1: tar: Error opening archive")
	response = dispatch()
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeIO {
		t.Fatalf("device extract failure = %+v, want the io error code", response)
	}
}

func TestProductionFSExtractUnsupportedForLocal(t *testing.T) {
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
	connected, err := manager.Connect(t.Context(), session.Asset{ID: "fs-extract-local", Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	response := production.Dispatcher.Dispatch(t.Context(), ipc.Request{Command: "fs_extract", Args: json.RawMessage(`{"sessionId":"` + connected.ID + `","path":"/tmp/bundle.tar"}`)}, production.Environment(""))
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("local fs_extract = %+v, want unsupported", response)
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

type productionHTTPActor struct {
	baseURL string
	client  *http.Client
	csrf    string
}

func newProductionHTTPActor(t *testing.T, baseURL, username, password string) *productionHTTPActor {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	actor := &productionHTTPActor{baseURL: baseURL, client: &http.Client{Jar: jar}}
	body, err := json.Marshal(map[string]string{"username": username, "password": password})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+"/auth/login", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := actor.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login %s status = %d", username, response.StatusCode)
	}
	var decoded struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	hasSession := false
	for _, cookie := range response.Cookies() {
		hasSession = hasSession || cookie.Name == "nexterm_session"
	}
	if !hasSession || decoded.CSRFToken == "" {
		t.Fatalf("login %s returned no session cookie or CSRF token", username)
	}
	actor.csrf = decoded.CSRFToken
	return actor
}

func (a *productionHTTPActor) rpc(t *testing.T, command string, args any) (int, ipc.Response) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"cmd": command, "args": args})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, a.baseURL+"/rpc", strings.NewReader(string(payload)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-NexTerm-CSRF", a.csrf)
	response, err := a.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var decoded ipc.Response
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, decoded
}

type productionHTTPBoundaryTransport struct {
	*local.Transport
	mu       sync.Mutex
	commands int
	dials    int
}

func (t *productionHTTPBoundaryTransport) Kind() string { return "device" }

func (t *productionHTTPBoundaryTransport) Exec(_ context.Context, command string, _ base.ExecOptions) (base.ExecResult, error) {
	t.mu.Lock()
	t.commands++
	t.mu.Unlock()
	exitCode := 0
	result := base.ExecResult{ExitCode: &exitCode}
	switch {
	case strings.HasPrefix(command, `docker 'ps' `):
		result.Stdout = "{\"ID\":\"c1\",\"Names\":\"web\",\"Image\":\"nginx:latest\",\"State\":\"running\",\"Status\":\"Up\",\"Ports\":\"\",\"Labels\":\"\"}\n"
	case strings.HasPrefix(command, `docker 'images' `):
		result.Stdout = "{\"ID\":\"i1\",\"Repository\":\"nginx\",\"Tag\":\"latest\",\"Size\":\"1MB\",\"CreatedSince\":\"now\"}\n"
	case strings.HasPrefix(command, `docker 'inspect' `):
		result.Stdout = "{\"Id\":\"c1\",\"State\":{\"Status\":\"running\"}}"
	case strings.HasPrefix(command, `docker 'stats' `):
		result.Stdout = "{\"CPUPerc\":\"0.5%\"}\n"
	case strings.HasPrefix(command, `docker 'pull' `):
		result.Stdout = "pulled\n"
	case strings.HasPrefix(command, `docker 'exec' `):
		result.Stdout = "owner-file\n"
	}
	return result, nil
}

func (t *productionHTTPBoundaryTransport) DialContext(context.Context, string, string) (net.Conn, error) {
	t.mu.Lock()
	t.dials++
	t.mu.Unlock()
	return nil, base.ErrUnsupported
}

func (t *productionHTTPBoundaryTransport) counts() (int, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.commands, t.dials
}

type productionHTTPBoundaryFixture struct {
	owner     *productionHTTPActor
	foreign   *productionHTTPActor
	sessionID string
	transport *productionHTTPBoundaryTransport
	root      string
}

func newProductionHTTPBoundaryFixture(t *testing.T) *productionHTTPBoundaryFixture {
	t.Helper()
	database, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	accounts := account.New(database.GORM())
	owner, err := accounts.CreateUser(t.Context(), "owner", "Owner", "owner-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.CreateUser(t.Context(), "foreign", "Foreign", "foreign-password"); err != nil {
		t.Fatal(err)
	}
	transport := &productionHTTPBoundaryTransport{Transport: local.NewWithConfig(local.Config{Shell: "/bin/sh"})}
	manager := session.NewManager(session.Config{Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
		return transport, nil
	})})
	application, err := NewProductionWithServices(Config{
		Streams: ipc.StreamFactoryFuncs{},
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, ProductionServices{
		Store: database, Sessions: manager, Docker: newProductionDockerService(manager, database),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Shutdown(context.Background()) })
	transportServer, err := server.New(server.Config{
		Options:    server.Options{Listen: "127.0.0.1:0", DataDir: t.TempDir(), Auth: server.AuthOn},
		Dispatcher: application.Dispatcher, Environment: application.Environment(""), Accounts: accounts,
		Channels: server.NewHubAdapter(manager.Hub()), Version: "test", Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(transportServer.Handler())
	t.Cleanup(func() {
		httpServer.Close()
		_ = transportServer.Close()
	})
	connected, err := manager.Connect(ipc.WithUserID(t.Context(), owner.ID), session.Asset{ID: "device/http-owner", Kind: session.KindDevice})
	if err != nil {
		t.Fatal(err)
	}
	return &productionHTTPBoundaryFixture{
		owner:     newProductionHTTPActor(t, httpServer.URL, "owner", "owner-password"),
		foreign:   newProductionHTTPActor(t, httpServer.URL, "foreign", "foreign-password"),
		sessionID: connected.ID, transport: transport, root: t.TempDir(),
	}
}

func requireProductionHTTPRPCOK(t *testing.T, status int, response ipc.Response) {
	t.Helper()
	if status != http.StatusOK || !response.OK {
		t.Fatalf("authorized HTTP RPC = status %d response %+v", status, response)
	}
}

func TestProductionFSAndDockerHTTPAccountBoundaries(t *testing.T) {
	fixture := newProductionHTTPBoundaryFixture(t)
	positive := filepath.Join(fixture.root, "positive.txt")
	status, response := fixture.owner.rpc(t, "fs_write", map[string]any{"args": map[string]any{
		"sessionId": fixture.sessionID, "path": positive, "contentBase64": base64.StdEncoding.EncodeToString([]byte("owner-data")),
	}})
	requireProductionHTTPRPCOK(t, status, response)
	status, response = fixture.owner.rpc(t, "fs_read", map[string]any{"sessionId": fixture.sessionID, "path": positive})
	requireProductionHTTPRPCOK(t, status, response)
	var read fsReadDTO
	if err := json.Unmarshal(response.Data, &read); err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(read.ContentBase64)
	if err != nil || string(decoded) != "owner-data" {
		t.Fatalf("owner fs_read = %q, %v", decoded, err)
	}
	status, response = fixture.owner.rpc(t, "fs_delete", map[string]any{"sessionId": fixture.sessionID, "path": positive})
	requireProductionHTTPRPCOK(t, status, response)
	if _, err := os.Stat(positive); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owner fs_delete left %s: %v", positive, err)
	}
	status, response = fixture.owner.rpc(t, "docker_ps", map[string]any{"sessionId": fixture.sessionID})
	requireProductionHTTPRPCOK(t, status, response)
	status, response = fixture.owner.rpc(t, "docker_action", map[string]any{"args": map[string]any{
		"sessionId": fixture.sessionID, "containerId": "c1", "action": "restart",
	}})
	requireProductionHTTPRPCOK(t, status, response)

	readPath := filepath.Join(fixture.root, "read.txt")
	deletePath := filepath.Join(fixture.root, "delete.txt")
	for _, path := range []string{readPath, deletePath} {
		status, response = fixture.owner.rpc(t, "fs_write", map[string]any{"args": map[string]any{
			"sessionId": fixture.sessionID, "path": path, "contentBase64": base64.StdEncoding.EncodeToString([]byte("owner-data")),
		}})
		requireProductionHTTPRPCOK(t, status, response)
	}
	foreignPath := filepath.Join(fixture.root, "foreign.txt")
	sessionArgs := func() map[string]any { return map[string]any{"sessionId": fixture.sessionID} }
	tests := []struct {
		name    string
		command string
		args    any
		verify  func()
	}{
		{name: "fs read", command: "fs_read", args: map[string]any{"sessionId": fixture.sessionID, "path": readPath}, verify: func() {
			if data, err := os.ReadFile(readPath); err != nil || string(data) != "owner-data" {
				t.Errorf("owner file after foreign read = %q, %v", data, err)
			}
		}},
		{name: "fs write", command: "fs_write", args: map[string]any{"args": map[string]any{
			"sessionId": fixture.sessionID, "path": foreignPath, "contentBase64": base64.StdEncoding.EncodeToString([]byte("foreign-data")),
		}}, verify: func() {
			if _, err := os.Stat(foreignPath); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("foreign write created %s: %v", foreignPath, err)
			}
		}},
		{name: "fs delete", command: "fs_delete", args: map[string]any{"sessionId": fixture.sessionID, "path": deletePath}, verify: func() {
			if data, err := os.ReadFile(deletePath); err != nil || string(data) != "owner-data" {
				t.Errorf("owner file after foreign delete = %q, %v", data, err)
			}
		}},
		{name: "docker overview", command: "docker_overview", args: sessionArgs()},
		{name: "docker ps", command: "docker_ps", args: sessionArgs()},
		{name: "docker logs attach", command: "docker_logs_attach", args: map[string]any{"sessionId": fixture.sessionID, "containerId": "c1", "tail": 1}},
		{name: "docker exec attach", command: "docker_exec_attach", args: map[string]any{"args": map[string]any{
			"sessionId": fixture.sessionID, "containerId": "c1", "cmd": "sh", "cols": 80, "rows": 24,
		}}},
		{name: "docker action", command: "docker_action", args: map[string]any{"args": map[string]any{
			"sessionId": fixture.sessionID, "containerId": "c1", "action": "restart",
		}}},
		{name: "docker images", command: "docker_images", args: sessionArgs()},
		{name: "docker image pull", command: "docker_image_pull", args: map[string]any{"sessionId": fixture.sessionID, "image": "nginx:latest"}},
		{name: "docker image remove", command: "docker_image_remove", args: map[string]any{"sessionId": fixture.sessionID, "image": "nginx:latest"}},
		{name: "docker inspect", command: "docker_inspect", args: map[string]any{"sessionId": fixture.sessionID, "containerId": "c1"}},
		{name: "docker stats", command: "docker_stats", args: sessionArgs()},
		{name: "docker container list dir", command: "docker_container_list_dir", args: map[string]any{
			"sessionId": fixture.sessionID, "containerId": "c1", "path": "/",
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			beforeCommands, beforeDials := fixture.transport.counts()
			status, response := fixture.foreign.rpc(t, test.command, test.args)
			if status != http.StatusOK || response.OK || response.Error == nil || response.Error.Code != ipc.CodeForbidden {
				t.Errorf("foreign HTTP RPC = status %d response %+v, want forbidden", status, response)
			}
			afterCommands, afterDials := fixture.transport.counts()
			if afterCommands != beforeCommands || afterDials != beforeDials {
				t.Errorf("foreign RPC reached Docker backend: commands %d -> %d, dials %d -> %d", beforeCommands, afterCommands, beforeDials, afterDials)
			}
			if test.verify != nil {
				test.verify()
			}
		})
	}
	status, response = fixture.owner.rpc(t, "fs_read", map[string]any{"sessionId": fixture.sessionID, "path": readPath})
	requireProductionHTTPRPCOK(t, status, response)
	status, response = fixture.owner.rpc(t, "docker_ps", map[string]any{"sessionId": fixture.sessionID})
	requireProductionHTTPRPCOK(t, status, response)
}

const winRMChainCreateResponse = `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:a="http://schemas.xmlsoap.org/ws/2004/08/addressing" xmlns:rsp="http://schemas.microsoft.com/wbem/wsman/1/windows/shell"><s:Header><a:Action>http://schemas.xmlsoap.org/ws/2004/09/transfer/CreateResponse</a:Action></s:Header><s:Body><rsp:Shell><rsp:ShellId>fixture-shell</rsp:ShellId></rsp:Shell></s:Body></s:Envelope>`

const winRMChainExecuteResponse = `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:a="http://schemas.xmlsoap.org/ws/2004/08/addressing" xmlns:rsp="http://schemas.microsoft.com/wbem/wsman/1/windows/shell"><s:Header><a:Action>http://schemas.microsoft.com/wbem/wsman/1/windows/shell/CommandResponse</a:Action></s:Header><s:Body><rsp:CommandResponse><rsp:CommandId>fixture-command</rsp:CommandId></rsp:CommandResponse></s:Body></s:Envelope>`

type winRMChainStep struct {
	operation string
	cwd       string
}

type winRMChainFixture struct {
	t      *testing.T
	mu     sync.Mutex
	cwd    string
	stdout string
	steps  []winRMChainStep
}

func (f *winRMChainFixture) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	body, _ := io.ReadAll(request.Body)
	switch {
	case strings.Contains(string(body), "transfer/Create"):
		winRMChainWriteSOAP(w, winRMChainCreateResponse)
	case strings.Contains(string(body), "shell/Command"):
		script, err := decodeWinRMChainScript(string(body))
		if err != nil {
			f.t.Errorf("decode PowerShell: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := f.execute(script); err != nil {
			f.t.Errorf("execute PowerShell: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		winRMChainWriteSOAP(w, winRMChainExecuteResponse)
	case strings.Contains(string(body), "shell/Receive"):
		f.mu.Lock()
		stdout := f.stdout + "\n<<NEXTERM_CWD>>" + f.cwd + "<<EOF>>\r\n"
		f.mu.Unlock()
		response := `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:rsp="http://schemas.microsoft.com/wbem/wsman/1/windows/shell"><s:Body><rsp:ReceiveResponse><rsp:Stream Name="stdout" CommandId="fixture-command">` + base64.StdEncoding.EncodeToString([]byte(stdout)) + `</rsp:Stream><rsp:CommandState CommandId="fixture-command" State="http://schemas.microsoft.com/wbem/wsman/1/windows/shell/CommandState/Done"><rsp:ExitCode>0</rsp:ExitCode></rsp:CommandState></rsp:ReceiveResponse></s:Body></s:Envelope>`
		winRMChainWriteSOAP(w, response)
	default:
		winRMChainWriteSOAP(w, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"/>`)
	}
}

func (f *winRMChainFixture) execute(script string) error {
	cwd, command := winRMChainCommand(script)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.steps = append(f.steps, winRMChainStep{cwd: cwd})
	switch {
	case command == "Set-Location -LiteralPath 'E:\\after'":
		f.steps[len(f.steps)-1].operation = "cd"
		f.stdout = ""
		f.cwd = `E:\after`
	case strings.Contains(command, "Get-PSDrive"):
		f.steps[len(f.steps)-1].operation = "roots"
		f.stdout = `{"windows":true,"supported":true,"status":"ok","home":"","roots":[{"n":"C","r":"C:\\"}]}`
	case strings.Contains(command, "Get-ChildItem"):
		f.steps[len(f.steps)-1].operation = "list"
		f.stdout = "[]"
	case command == "Write-Output (Get-Location).Path":
		f.steps[len(f.steps)-1].operation = "cwd"
		f.stdout = f.cwd
	default:
		return fmt.Errorf("unexpected command %q", command)
	}
	return nil
}

func (f *winRMChainFixture) snapshot() []winRMChainStep {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]winRMChainStep(nil), f.steps...)
}

func winRMChainCommand(script string) (string, string) {
	const setLocation = "Set-Location -LiteralPath "
	start := strings.Index(script, setLocation)
	if start < 0 {
		return "", script
	}
	start += len(setLocation)
	end := strings.Index(script[start:], " -ErrorAction Stop")
	if end < 0 {
		return "", script
	}
	literal := script[start : start+end]
	cwd := strings.TrimSuffix(strings.TrimPrefix(literal, "'"), "'")
	cwd = strings.ReplaceAll(cwd, "''", "'")
	remainder := script[start+end+len(" -ErrorAction Stop"):]
	remainder = strings.TrimPrefix(remainder, "\n")
	if commandEnd := strings.Index(remainder, "\n$__nextermOK"); commandEnd >= 0 {
		remainder = remainder[:commandEnd]
	}
	return cwd, remainder
}

func decodeWinRMChainScript(body string) (string, error) {
	var envelope struct {
		Command string `xml:"Body>CommandLine>Command"`
	}
	if err := xml.Unmarshal([]byte(body), &envelope); err != nil {
		return "", err
	}
	const marker = "powershell.exe -EncodedCommand "
	start := strings.Index(envelope.Command, marker)
	if start < 0 {
		return "", fmt.Errorf("encoded PowerShell command is missing in %q", envelope.Command)
	}
	fields := strings.Fields(envelope.Command[start+len(marker):])
	if len(fields) == 0 {
		return "", errors.New("encoded PowerShell payload is missing")
	}
	data, err := base64.StdEncoding.DecodeString(fields[0])
	if err != nil {
		return "", err
	}
	if len(data)%2 != 0 {
		return "", errors.New("odd UTF-16 command length")
	}
	units := make([]uint16, len(data)/2)
	for index := range units {
		units[index] = binary.LittleEndian.Uint16(data[index*2:])
	}
	return string(utf16.Decode(units)), nil
}

func winRMChainWriteSOAP(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/soap+xml;charset=UTF-8")
	_, _ = io.WriteString(w, body)
}

func TestWinRMExecLineFileOperationsPreserveCWD(t *testing.T) {
	fixture := &winRMChainFixture{t: t, cwd: `C:\`}
	server := httptest.NewServer(fixture)
	t.Cleanup(server.Close)
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	host, portText, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := transportwinrm.New(transportwinrm.Config{Host: host, Port: port, User: "alice", Password: "secret", Auth: transportwinrm.AuthBasic, InitialCWD: `C:\`})
	if err != nil {
		t.Fatal(err)
	}
	manager := session.NewManager(session.Config{Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
		return transport, nil
	})})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, session.Asset{ID: "winrm-cwd-chain", Kind: session.KindWinRM})
	if err != nil {
		t.Fatal(err)
	}
	tab, err := manager.OpenTab(ctx, session.OpenTabOptions{SessionID: connected.ID, ClientID: "client", ChannelID: "channel", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ExecLine(ctx, tab.ID, "client", `Set-Location -LiteralPath 'E:\after'`); err != nil {
		t.Fatal(err)
	}
	filesystem, err := transport.FileSystem(ctx)
	if err != nil {
		t.Fatal(err)
	}
	provider, ok := filesystem.(base.RootsProvider)
	if !ok {
		t.Fatal("WinRM filesystem does not provide roots")
	}
	roots, err := provider.Roots(ctx)
	if err != nil || len(roots.Roots) != 1 || roots.Roots[0].Path != `C:\` {
		t.Fatalf("roots = %+v, %v", roots, err)
	}
	entries, err := filesystem.List(ctx, ".")
	if err != nil || len(entries) != 0 {
		t.Fatalf("list = %+v, %v", entries, err)
	}
	result, err := manager.ExecLine(ctx, tab.ID, "client", "Write-Output (Get-Location).Path")
	if err != nil || strings.TrimSpace(result.Stdout) != `E:\after` || transport.CWD() != `E:\after` {
		t.Fatalf("next ExecLine = %+v, transport cwd = %q, err = %v", result, transport.CWD(), err)
	}
	steps := fixture.snapshot()
	if len(steps) != 4 {
		t.Fatalf("command chain = %+v", steps)
	}
	for index, operation := range []string{"cd", "roots", "list", "cwd"} {
		if steps[index].operation != operation {
			t.Fatalf("command chain = %+v", steps)
		}
	}
	for index, cwd := range []string{`C:\`, `E:\after`, `E:\after`, `E:\after`} {
		if steps[index].cwd != cwd {
			t.Fatalf("injected cwd chain = %+v", steps)
		}
	}
}
