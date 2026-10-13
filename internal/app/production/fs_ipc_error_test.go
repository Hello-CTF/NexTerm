package production

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	fslocal "github.com/Hello-CTF/NexTerm/internal/fs/local"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type fakeProviderFileSystem struct {
	err error
}

func (f fakeProviderFileSystem) List(context.Context, string) ([]base.FileEntry, error) {
	return nil, f.err
}
func (f fakeProviderFileSystem) ReadFile(context.Context, string, int64) ([]byte, error) {
	return nil, f.err
}
func (f fakeProviderFileSystem) WriteFile(context.Context, string, []byte, bool) error {
	return f.err
}
func (f fakeProviderFileSystem) Mkdir(context.Context, string) error { return f.err }
func (f fakeProviderFileSystem) Rename(context.Context, string, string) error {
	return f.err
}
func (f fakeProviderFileSystem) Delete(context.Context, string, bool) error { return f.err }
func (f fakeProviderFileSystem) Chmod(context.Context, string, fs.FileMode) error {
	return f.err
}
func (f fakeProviderFileSystem) Checksum(context.Context, string, string) (string, error) {
	return "", f.err
}
func (f fakeProviderFileSystem) Exists(context.Context, string) (bool, error) {
	return false, f.err
}
func (f fakeProviderFileSystem) Size(context.Context, string) (int64, error) {
	return 0, f.err
}
func (f fakeProviderFileSystem) OpenRead(context.Context, string) (base.RemoteReader, error) {
	return nil, f.err
}
func (f fakeProviderFileSystem) OpenWrite(context.Context, string, bool) (base.RemoteWriter, error) {
	return nil, f.err
}

type fakeProviderTransport struct {
	kind  string
	fs    base.FileSystem
	fsErr error
}

func (t fakeProviderTransport) Kind() string       { return t.kind }
func (t fakeProviderTransport) Generation() uint64 { return 1 }
func (t fakeProviderTransport) IsAlive() bool      { return true }
func (t fakeProviderTransport) Close() error       { return nil }
func (t fakeProviderTransport) Ping(context.Context) (time.Duration, error) {
	return 0, nil
}
func (t fakeProviderTransport) Exec(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
	return base.ExecResult{}, nil
}
func (t fakeProviderTransport) FileSystem(context.Context) (base.FileSystem, error) {
	if t.fsErr != nil {
		return nil, t.fsErr
	}
	return t.fs, nil
}

func TestFSIPCErrorClassifiesProviderFailures(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		kind      string
		assetKind string
		want      ipc.Code
	}{
		{name: "sftp", kind: "ssh", assetKind: session.KindSSH, want: ipc.CodeSFTP},
		{name: "winrm", kind: "winrm", assetKind: session.KindWinRM, want: ipc.CodeWinRM},
		{name: "device", kind: "device", assetKind: session.KindDevice, want: ipc.CodeIO},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			sentinel := errors.New("provider exploded")
			connector := session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
				return fakeProviderTransport{kind: testCase.kind, fs: fakeProviderFileSystem{err: sentinel}}, nil
			})
			manager := session.NewManager(session.Config{Connector: connector})
			dispatcher := ipc.NewDispatcher()
			if err := registerFSCommands(dispatcher, manager, nil); err != nil {
				t.Fatal(err)
			}
			connected, err := manager.Connect(t.Context(), session.Asset{ID: "fs-" + testCase.name, Kind: testCase.assetKind})
			if err != nil {
				t.Fatal(err)
			}
			response := dispatcher.Dispatch(t.Context(), ipc.Request{
				Command: "fs_list", Args: json.RawMessage(`{"sessionId":"` + connected.ID + `","path":"/tmp"}`),
			}, ipc.Environment{})
			if response.OK || response.Error == nil || response.Error.Code != testCase.want {
				t.Fatalf("fs_list = %+v, want %s", response, testCase.want)
			}
			if !strings.Contains(response.Error.Message, "provider exploded") {
				t.Fatalf("underlying detail lost: %q", response.Error.Message)
			}
		})
	}
}

func TestFSIPCErrorPreservesExistingClassification(t *testing.T) {
	connector := session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
		return fakeProviderTransport{kind: "winrm", fs: fakeProviderFileSystem{err: base.ErrUnsupported}}, nil
	})
	manager := session.NewManager(session.Config{Connector: connector})
	dispatcher := ipc.NewDispatcher()
	if err := registerFSCommands(dispatcher, manager, nil); err != nil {
		t.Fatal(err)
	}
	connected, err := manager.Connect(t.Context(), session.Asset{ID: "fs-unsupported", Kind: session.KindWinRM})
	if err != nil {
		t.Fatal(err)
	}
	response := dispatcher.Dispatch(t.Context(), ipc.Request{
		Command: "fs_list", Args: json.RawMessage(`{"sessionId":"` + connected.ID + `","path":"/tmp"}`),
	}, ipc.Environment{})
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("fs_list = %+v, want unsupported", response)
	}
}

func TestFSWriteRejectsInvalidBase64(t *testing.T) {
	connector := session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
		return fakeProviderTransport{kind: "ssh", fs: fakeProviderFileSystem{}}, nil
	})
	manager := session.NewManager(session.Config{Connector: connector})
	dispatcher := ipc.NewDispatcher()
	if err := registerFSCommands(dispatcher, manager, nil); err != nil {
		t.Fatal(err)
	}
	connected, err := manager.Connect(t.Context(), session.Asset{ID: "fs-base64", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	response := dispatcher.Dispatch(t.Context(), ipc.Request{
		Command: "fs_write", Args: json.RawMessage(`{"args":{"sessionId":"` + connected.ID + `","path":"/tmp/x","contentBase64":"!!!"}}`),
	}, ipc.Environment{})
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("fs_write = %+v, want bad_param", response)
	}
	if !strings.HasPrefix(response.Error.Message, "参数错误: 文件内容不是有效的 Base64: ") {
		t.Fatalf("fs_write message = %q", response.Error.Message)
	}
}

func TestFSIPCErrorClassifiesAcquireFailures(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		kind      string
		assetKind string
		fsErr     error
		want      ipc.Code
	}{
		{name: "sftp", kind: "ssh", assetKind: session.KindSSH, fsErr: errors.New("request SFTP subsystem: ssh: rejected"), want: ipc.CodeSFTP},
		{name: "winrm", kind: "winrm", assetKind: session.KindWinRM, fsErr: errors.New("WinRM file operation: access denied"), want: ipc.CodeWinRM},
		{name: "classified", kind: "ssh", assetKind: session.KindSSH, fsErr: base.ErrUnsupported, want: ipc.CodeUnsupported},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			connector := session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
				return fakeProviderTransport{kind: testCase.kind, fsErr: testCase.fsErr}, nil
			})
			manager := session.NewManager(session.Config{Connector: connector})
			dispatcher := ipc.NewDispatcher()
			if err := registerFSCommands(dispatcher, manager, nil); err != nil {
				t.Fatal(err)
			}
			connected, err := manager.Connect(t.Context(), session.Asset{ID: "fs-acquire-" + testCase.name, Kind: testCase.assetKind})
			if err != nil {
				t.Fatal(err)
			}
			response := dispatcher.Dispatch(t.Context(), ipc.Request{
				Command: "fs_list", Args: json.RawMessage(`{"sessionId":"` + connected.ID + `","path":"/tmp"}`),
			}, ipc.Environment{})
			if response.OK || response.Error == nil || response.Error.Code != testCase.want {
				t.Fatalf("fs_list = %+v, want %s", response, testCase.want)
			}
			if testCase.fsErr != base.ErrUnsupported && !strings.Contains(response.Error.Message, testCase.fsErr.Error()) {
				t.Fatalf("underlying detail lost: %q", response.Error.Message)
			}
		})
	}
}

func TestFSIPCErrorAcquirePassesThroughSessionLookup(t *testing.T) {
	manager := session.NewManager(session.Config{Connector: session.ConnectorFunc(
		func(context.Context, session.Asset, uint64) (base.Transport, error) {
			return fakeProviderTransport{kind: "ssh", fs: fakeProviderFileSystem{}}, nil
		})})
	dispatcher := ipc.NewDispatcher()
	if err := registerFSCommands(dispatcher, manager, nil); err != nil {
		t.Fatal(err)
	}
	response := dispatcher.Dispatch(t.Context(), ipc.Request{
		Command: "fs_list", Args: json.RawMessage(`{"sessionId":"missing","path":"/tmp"}`),
	}, ipc.Environment{})
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeNotFound {
		t.Fatalf("fs_list = %+v, want not_found", response)
	}
}

func TestFSIPCErrorMapsNotExistToNotFound(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		kind      string
		assetKind string
	}{
		{name: "ssh", kind: "ssh", assetKind: session.KindSSH},
		{name: "winrm", kind: "winrm", assetKind: session.KindWinRM},
		{name: "device", kind: "device", assetKind: session.KindDevice},
		{name: "local", kind: "local", assetKind: session.KindLocal},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			notExist := fmt.Errorf("list /missing: %w", fs.ErrNotExist)
			connector := session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
				return fakeProviderTransport{kind: testCase.kind, fs: fakeProviderFileSystem{err: notExist}}, nil
			})
			manager := session.NewManager(session.Config{Connector: connector})
			dispatcher := ipc.NewDispatcher()
			if err := registerFSCommands(dispatcher, manager, nil); err != nil {
				t.Fatal(err)
			}
			connected, err := manager.Connect(t.Context(), session.Asset{ID: "fs-notexist-" + testCase.name, Kind: testCase.assetKind})
			if err != nil {
				t.Fatal(err)
			}
			response := dispatcher.Dispatch(t.Context(), ipc.Request{
				Command: "fs_list", Args: json.RawMessage(`{"sessionId":"` + connected.ID + `","path":"/missing"}`),
			}, ipc.Environment{})
			if response.OK || response.Error == nil || response.Error.Code != ipc.CodeNotFound {
				t.Fatalf("fs_list = %+v, want not_found", response)
			}
			if !strings.Contains(response.Error.Message, "list /missing") {
				t.Fatalf("underlying detail lost: %q", response.Error.Message)
			}
		})
	}
}

// singleLevelMkdirFS mimics sftp Mkdir semantics over the local filesystem:
// one level only, and a bare server failure when the directory exists.
type singleLevelMkdirFS struct {
	base.FileSystem
}

func (f singleLevelMkdirFS) Mkdir(_ context.Context, path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("mkdir %s: server failure", path)
	}
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		return fmt.Errorf("mkdir %s: server failure", path)
	}
	return os.Mkdir(path, 0o755)
}

func TestFSMkdirCreatesNestedDirectories(t *testing.T) {
	connector := session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
		return fakeProviderTransport{kind: "ssh", fs: singleLevelMkdirFS{FileSystem: fslocal.New()}}, nil
	})
	manager := session.NewManager(session.Config{Connector: connector})
	dispatcher := ipc.NewDispatcher()
	if err := registerFSCommands(dispatcher, manager, nil); err != nil {
		t.Fatal(err)
	}
	connected, err := manager.Connect(t.Context(), session.Asset{ID: "fs-mkdir", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	mkdir := func(path string) ipc.Response {
		encoded, _ := json.Marshal(path)
		return dispatcher.Dispatch(t.Context(), ipc.Request{
			Command: "fs_mkdir", Args: json.RawMessage(`{"sessionId":"` + connected.ID + `","path":` + string(encoded) + `}`),
		}, ipc.Environment{})
	}
	nested := filepath.Join(t.TempDir(), "a", "b", "c")
	if response := mkdir(nested); !response.OK || response.Error != nil {
		t.Fatalf("nested fs_mkdir = %+v", response)
	}
	if info, err := os.Stat(nested); err != nil || !info.IsDir() {
		t.Fatalf("nested dir stat = %v, %v", info, err)
	}
	if response := mkdir(nested); !response.OK || response.Error != nil {
		t.Fatalf("existing-dir fs_mkdir = %+v, want a no-op", response)
	}
	blocker := filepath.Join(t.TempDir(), "occupied")
	if err := os.WriteFile(blocker, []byte("a file sits where a directory is needed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if response := mkdir(filepath.Join(blocker, "child")); response.OK || response.Error == nil {
		t.Fatalf("fs_mkdir through a file component = %+v, want a failure", response)
	}
	if response := mkdir(blocker); response.OK || response.Error == nil {
		t.Fatalf("fs_mkdir onto a file = %+v, want a failure", response)
	}
}
