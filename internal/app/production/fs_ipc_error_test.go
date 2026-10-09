package production

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"

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
