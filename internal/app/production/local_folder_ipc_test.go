package production

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/local"
)

func writeLocalFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func dispatchLocalFolderList(t *testing.T, dispatcher *ipc.Dispatcher, root string) (localFolderListDTO, *ipc.Error) {
	t.Helper()
	response := dispatcher.Dispatch(context.Background(), ipc.Request{
		Command: "local_folder_list",
		Args:    json.RawMessage(`{"path":` + localPathJSON(root) + `}`),
	}, ipc.Environment{})
	if response.Error != nil {
		return localFolderListDTO{}, response.Error
	}
	raw, err := json.Marshal(response.Data)
	if err != nil {
		t.Fatal(err)
	}
	var result localFolderListDTO
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result, nil
}

func newLocalFolderDispatcher(t *testing.T) *ipc.Dispatcher {
	t.Helper()
	dispatcher := ipc.NewDispatcher()
	if err := registerLocalFolderCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

func TestLocalFolderListWalksTreeWithForwardSlashRels(t *testing.T) {
	root := t.TempDir()
	writeLocalFile(t, filepath.Join(root, "a.txt"), "a")
	writeLocalFile(t, filepath.Join(root, "sub", "deep", "b.txt"), "bb")
	writeLocalFile(t, filepath.Join(root, "sub", "c.txt"), "ccc")
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "half", "leaf"), 0o755); err != nil {
		t.Fatal(err)
	}

	result, ipcErr := dispatchLocalFolderList(t, newLocalFolderDispatcher(t), root)
	if ipcErr != nil {
		t.Fatal(ipcErr)
	}
	if result.Name != filepath.Base(root) {
		t.Fatalf("name = %q", result.Name)
	}
	if len(result.Files) != 3 {
		t.Fatalf("files = %+v", result.Files)
	}
	byRel := map[string]localFolderFileDTO{}
	for _, f := range result.Files {
		if strings.Contains(f.RelativePath, `\`) {
			t.Fatalf("relativePath 含反斜杠: %q", f.RelativePath)
		}
		byRel[f.RelativePath] = f
	}
	for rel, size := range map[string]int64{"a.txt": 1, "sub/c.txt": 3, "sub/deep/b.txt": 2} {
		entry, ok := byRel[rel]
		if !ok {
			t.Fatalf("缺少 %s: %+v", rel, result.Files)
		}
		if entry.Size != size || entry.MtimeMs == 0 {
			t.Fatalf("%s = %+v", rel, entry)
		}
		wantLocal := filepath.Join(root, filepath.FromSlash(rel))
		if entry.LocalPath != wantLocal {
			t.Fatalf("%s localPath = %q, want %q", rel, entry.LocalPath, wantLocal)
		}
		if !filepath.IsAbs(entry.LocalPath) {
			t.Fatalf("%s localPath 不是绝对路径: %q", rel, entry.LocalPath)
		}
		if runtime.GOOS != "windows" && strings.Contains(entry.LocalPath, `\`) {
			t.Fatalf("非 Windows localPath 含反斜杠: %q", entry.LocalPath)
		}
	}
	if len(result.EmptyDirs) != 2 || result.EmptyDirs[0] != "empty" || result.EmptyDirs[1] != "half/leaf" {
		t.Fatalf("emptyDirs = %+v", result.EmptyDirs)
	}
}

func TestLocalFolderListSkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	writeLocalFile(t, filepath.Join(root, "keep.txt"), "keep")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	writeLocalFile(t, outside, "secret")
	links := []struct{ link, target string }{
		{filepath.Join(root, "link-out.txt"), outside},
		{filepath.Join(root, "link-in.txt"), filepath.Join(root, "keep.txt")},
		{filepath.Join(root, "link-dir"), root},
	}
	created := 0
	for _, l := range links {
		if err := os.Symlink(l.target, l.link); err == nil {
			created++
		}
	}
	if created == 0 {
		t.Skip("当前平台无法创建符号链接")
	}

	result, ipcErr := dispatchLocalFolderList(t, newLocalFolderDispatcher(t), root)
	if ipcErr != nil {
		t.Fatal(ipcErr)
	}
	if len(result.Files) != 1 || result.Files[0].RelativePath != "keep.txt" {
		t.Fatalf("files = %+v", result.Files)
	}
	for _, f := range result.Files {
		if strings.Contains(f.RelativePath, "outside") || strings.Contains(f.RelativePath, "link") {
			t.Fatalf("符号链接未被跳过: %+v", f)
		}
	}
	if len(result.EmptyDirs) != 0 {
		t.Fatalf("emptyDirs = %+v", result.EmptyDirs)
	}
}

func TestLocalFolderListRejectsBadRoots(t *testing.T) {
	dispatcher := newLocalFolderDispatcher(t)
	if _, ipcErr := dispatchLocalFolderList(t, dispatcher, filepath.Join(t.TempDir(), "missing")); ipcErr == nil || ipcErr.Code != ipc.CodeNotFound {
		t.Fatalf("missing root = %+v", ipcErr)
	}
	file := filepath.Join(t.TempDir(), "file.txt")
	writeLocalFile(t, file, "x")
	if _, ipcErr := dispatchLocalFolderList(t, dispatcher, file); ipcErr == nil || ipcErr.Code != ipc.CodeBadParam {
		t.Fatalf("file root = %+v", ipcErr)
	}
	volumeRoot := "/"
	if runtime.GOOS == "windows" {
		volumeRoot = filepath.VolumeName(root(t)) + `\`
	}
	if _, ipcErr := dispatchLocalFolderList(t, dispatcher, volumeRoot); ipcErr == nil || ipcErr.Code != ipc.CodeBadParam {
		t.Fatalf("volume root = %+v", ipcErr)
	}
}

func root(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func TestLocalFolderListEmptyFolder(t *testing.T) {
	root := t.TempDir()
	result, ipcErr := dispatchLocalFolderList(t, newLocalFolderDispatcher(t), root)
	if ipcErr != nil {
		t.Fatal(ipcErr)
	}
	if len(result.Files) != 0 || len(result.EmptyDirs) != 0 {
		t.Fatalf("result = %+v", result)
	}
	if result.Files == nil || result.EmptyDirs == nil {
		t.Fatal("空结果必须编码为 [] 而非 null")
	}
}

func TestLocalFileRead(t *testing.T) {
	dispatcher := newLocalFolderDispatcher(t)
	path := filepath.Join(t.TempDir(), "data.bin")
	writeLocalFile(t, path, "hello")
	dispatch := func(args string) (fsReadDTO, *ipc.Error) {
		response := dispatcher.Dispatch(context.Background(), ipc.Request{
			Command: "local_file_read",
			Args:    json.RawMessage(args),
		}, ipc.Environment{})
		if response.Error != nil {
			return fsReadDTO{}, response.Error
		}
		raw, err := json.Marshal(response.Data)
		if err != nil {
			t.Fatal(err)
		}
		var result fsReadDTO
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		return result, nil
	}

	result, ipcErr := dispatch(`{"path":` + localPathJSON(path) + `}`)
	if ipcErr != nil {
		t.Fatal(ipcErr)
	}
	if result.Size != 5 {
		t.Fatalf("size = %d", result.Size)
	}
	data, err := base64.StdEncoding.DecodeString(result.ContentBase64)
	if err != nil || string(data) != "hello" {
		t.Fatalf("content = %q, %v", data, err)
	}
	if _, ipcErr := dispatch(`{"path":` + localPathJSON(path) + `,"maxBytes":4}`); ipcErr == nil || ipcErr.Code != ipc.CodeBadParam {
		t.Fatalf("over maxBytes = %+v", ipcErr)
	}
	if _, ipcErr := dispatch(`{"path":` + localPathJSON(filepath.Join(t.TempDir(), "missing")) + `}`); ipcErr == nil || ipcErr.Code != ipc.CodeNotFound {
		t.Fatalf("missing = %+v", ipcErr)
	}
	dir := t.TempDir()
	if _, ipcErr := dispatch(`{"path":` + localPathJSON(dir) + `}`); ipcErr == nil || ipcErr.Code != ipc.CodeBadParam {
		t.Fatalf("dir = %+v", ipcErr)
	}
}

func localPathJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func TestLocalFolderCommandsRegisteredOnlyOnDesktop(t *testing.T) {
	build := func(desktop bool) *Production {
		connector := session.ConnectorFunc(func(_ context.Context, _ session.Asset, _ uint64) (base.Transport, error) {
			return local.NewWithConfig(local.Config{Shell: "/bin/sh"}), nil
		})
		manager := session.NewManager(session.Config{Connector: connector})
		production, err := NewProductionWithServices(
			Config{Events: ipc.EmitterFunc(func(context.Context, ipc.Event) error { return nil })},
			ProductionServices{Sessions: manager, desktop: desktop},
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := production.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = production.Shutdown(context.Background()) })
		return production
	}
	desktop := build(true)
	server := build(false)
	root := t.TempDir()
	writeLocalFile(t, filepath.Join(root, "a.txt"), "a")

	dispatch := func(production *Production) *ipc.Response {
		response := production.Dispatcher.Dispatch(t.Context(), ipc.Request{
			Command: "local_folder_list",
			Args:    json.RawMessage(`{"path":` + localPathJSON(root) + `}`),
		}, production.Environment(""))
		return &response
	}
	if response := dispatch(desktop); !response.OK {
		t.Fatalf("desktop local_folder_list = %+v", response.Error)
	}
	if response := dispatch(server); response.OK || response.Error == nil || response.Error.Code != ipc.CodeNotFound {
		t.Fatalf("server local_folder_list = %+v", response)
	}
}
