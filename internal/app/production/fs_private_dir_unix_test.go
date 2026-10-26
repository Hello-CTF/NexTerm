//go:build unix

package production

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	fslocal "github.com/Hello-CTF/NexTerm/internal/fs/local"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// recursiveMkdirFS hides MkdirPrivate and mimics the device agent: Mkdir is recursive and accepts an existing directory.
type recursiveMkdirFS struct {
	base.FileSystem
	ignoreChmod bool
}

func (f recursiveMkdirFS) Mkdir(_ context.Context, path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	// Pin the agent's default mode so the result does not depend on the test process umask.
	return os.Chmod(path, 0o755)
}

func (f recursiveMkdirFS) Chmod(ctx context.Context, path string, mode fs.FileMode) error {
	if f.ignoreChmod {
		return nil
	}
	return f.FileSystem.Chmod(ctx, path, mode)
}

func privateDirDispatcher(t *testing.T, kind string, filesystem base.FileSystem) func(path string) ipc.Response {
	t.Helper()
	connector := session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
		return fakeProviderTransport{kind: kind, fs: filesystem}, nil
	})
	manager := session.NewManager(session.Config{Connector: connector, OpenAccess: true})
	dispatcher := ipc.NewDispatcher()
	if err := registerFSCommands(dispatcher, manager, nil); err != nil {
		t.Fatal(err)
	}
	connected, err := manager.Connect(t.Context(), session.Asset{ID: "fs-private-" + t.Name(), Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	return func(path string) ipc.Response {
		encoded, _ := json.Marshal(path)
		return dispatcher.Dispatch(t.Context(), ipc.Request{
			Command: "fs_mkdir_private", Args: json.RawMessage(`{"sessionId":"` + connected.ID + `","path":` + string(encoded) + `}`),
		}, ipc.Environment{})
	}
}

func TestFSMkdirPrivateCreatesOwnerOnlyDirectoryAndRefusesReuse(t *testing.T) {
	for _, testCase := range []struct {
		name string
		kind string
		fs   base.FileSystem
	}{
		{name: "native", kind: "local", fs: fslocal.New()},
		{name: "fallback", kind: "device", fs: recursiveMkdirFS{FileSystem: fslocal.New()}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			mkdir := privateDirDispatcher(t, testCase.kind, testCase.fs)
			root := t.TempDir()
			target := filepath.Join(root, "nexterm-paste-x")
			if response := mkdir(target); !response.OK {
				t.Fatalf("fs_mkdir_private = %+v", response)
			}
			if info, err := os.Lstat(target); err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
				t.Fatalf("private dir = %v, %v; want a 0700 directory", info, err)
			}
			if response := mkdir(target); response.OK || response.Error == nil {
				t.Fatalf("second fs_mkdir_private = %+v, want a refusal", response)
			}
			planted := filepath.Join(root, "planted")
			if err := os.Mkdir(planted, 0o777); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(root, "link")
			if err := os.Symlink(planted, link); err != nil {
				t.Fatal(err)
			}
			for _, occupied := range []string{planted, link} {
				if response := mkdir(occupied); response.OK || response.Error == nil {
					t.Fatalf("fs_mkdir_private(%s) = %+v, want a refusal", occupied, response)
				}
			}
			if response := mkdir(filepath.Join(root, "missing", "child")); response.OK {
				t.Fatalf("fs_mkdir_private created a parent: %+v", response)
			}
			if response := mkdir("relative/dir"); response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
				t.Fatalf("relative fs_mkdir_private = %+v, want bad_param", response)
			}
		})
	}
}

func TestFSMkdirPrivateFallbackRefusesUnverifiedMode(t *testing.T) {
	mkdir := privateDirDispatcher(t, "device", recursiveMkdirFS{FileSystem: fslocal.New(), ignoreChmod: true})
	response := mkdir(filepath.Join(t.TempDir(), "loose"))
	if response.OK || response.Error == nil {
		t.Fatalf("fs_mkdir_private with a 0755 result = %+v, want a refusal", response)
	}
}
